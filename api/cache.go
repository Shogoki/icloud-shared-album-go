package main

import (
	"errors"
	"sync"
	"time"

	icloudalbum "github.com/Shogoki/icloud-shared-album-go"
)

// Resolving one album costs a webstream call plus a webasseturls call per 25
// photos — around five seconds end to end. Both the album endpoint and the
// image proxy need that result, and a page of 15 images asks for 15 images at
// once, so the resolved album is cached and concurrent callers share a single
// in-flight fetch.
//
// The asset URLs inside a resolved album are signed and expire three hours
// after they are issued, which is the hard ceiling on the TTL. An hour leaves
// every URL handed to a browser at least two hours of life.
const (
	defaultAlbumTTL = time.Hour

	// A failed lookup is cached briefly so an iCloud outage does not turn
	// into a retry storm against iCloud, while still recovering quickly.
	albumErrorTTL = 30 * time.Second

	// Albums are keyed by a token supplied in the request path, so the map
	// would otherwise grow without bound. Expired entries are swept when the
	// cache reaches this size.
	albumCacheSweepAt = 512
)

// albumFetcher resolves an album token into its photos. It is a field rather
// than a direct call so tests can supply a stub instead of reaching iCloud.
type albumFetcher func(token string) (*icloudalbum.Response, error)

type albumEntry struct {
	// ready is closed once resp and err are final. Callers that find an
	// in-flight entry wait on it instead of starting a second fetch.
	ready   chan struct{}
	resp    *icloudalbum.Response
	err     error
	fetched time.Time // when the fetch behind resp and err started
	expires time.Time
}

type albumCache struct {
	mu      sync.Mutex
	entries map[string]*albumEntry
	ttl     time.Duration
	fetch   albumFetcher
	now     func() time.Time // overridable in tests
}

func newAlbumCache(ttl time.Duration, fetch albumFetcher) *albumCache {
	return &albumCache{
		entries: make(map[string]*albumEntry),
		ttl:     ttl,
		fetch:   fetch,
		now:     time.Now,
	}
}

// get returns the album for token, fetching it only if no fresh entry exists.
// Concurrent calls for the same token share one fetch; calls for different
// tokens do not block each other.
func (c *albumCache) get(token string) (*icloudalbum.Response, error) {
	return c.load(token, time.Time{})
}

// refresh is get for a caller with reason to doubt the cached album: one
// resolved more than maxAge ago is fetched again instead of reused.
//
// The image proxy asks for this before answering 404 for a photo the cached
// album lacks, since the album may predate the photo or come from a lookup
// iCloud only half answered. maxAge bounds what that costs: requests for a GUID
// that really does not exist trigger at most one lookup per album per maxAge.
func (c *albumCache) refresh(token string, maxAge time.Duration) (*icloudalbum.Response, error) {
	return c.load(token, c.now().Add(-maxAge))
}

// load returns the cached album for token if it is unexpired and was fetched
// no earlier than notBefore, and fetches it otherwise.
func (c *albumCache) load(token string, notBefore time.Time) (*icloudalbum.Response, error) {
	c.mu.Lock()

	if entry, ok := c.entries[token]; ok {
		// An entry whose fetch is still running has a zero expiry and must be
		// waited on rather than treated as stale. It is also as fresh as any
		// caller can ask for, having started after whatever was cached before.
		inFlight := entry.expires.IsZero()
		if inFlight || (entry.expires.After(c.now()) && !entry.fetched.Before(notBefore)) {
			c.mu.Unlock()
			<-entry.ready
			return entry.resp, entry.err
		}
	}

	if len(c.entries) >= albumCacheSweepAt {
		c.sweepLocked()
	}

	entry := &albumEntry{ready: make(chan struct{}), fetched: c.now()}
	c.entries[token] = entry
	c.mu.Unlock()

	resp, err := c.fetch(token)

	c.mu.Lock()
	entry.resp, entry.err = resp, err
	if err != nil {
		entry.expires = c.now().Add(albumErrorTTL)
	} else {
		entry.expires = c.now().Add(c.ttl)
	}
	c.mu.Unlock()

	close(entry.ready)
	return resp, err
}

// errEmptyAlbum stands in for an album that resolved to no photos at all. That
// is far more often a lookup iCloud failed to answer properly than a shared
// album someone emptied, and caching it as a success would turn every photo of
// the album into a 404 for the full TTL.
var errEmptyAlbum = errors.New("iCloud returned no photos for the album")

// completeAlbums wraps fetch so that only an album with photos counts as a
// success. A failed or empty lookup is tried once more — a reset connection or
// a 5xx from iCloud is usually momentary — and if it is still unusable it is an
// error: the cache keeps it for albumErrorTTL only, and the endpoints answer
// 5xx rather than reporting the photos as nonexistent.
func completeAlbums(fetch albumFetcher) albumFetcher {
	once := func(token string) (*icloudalbum.Response, error) {
		resp, err := fetch(token)
		if err != nil {
			return nil, err
		}
		if resp == nil || len(resp.Photos) == 0 {
			return nil, errEmptyAlbum
		}
		return resp, nil
	}
	return func(token string) (*icloudalbum.Response, error) {
		resp, err := once(token)
		if err != nil {
			resp, err = once(token)
		}
		return resp, err
	}
}

// sweepLocked drops entries that have expired. The caller holds c.mu. Entries
// still being fetched have a zero expiry and are always kept.
func (c *albumCache) sweepLocked() {
	now := c.now()
	for token, entry := range c.entries {
		if !entry.expires.IsZero() && entry.expires.Before(now) {
			delete(c.entries, token)
		}
	}
}
