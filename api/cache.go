package main

import (
	"errors"
	"net/url"
	"strconv"
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
// The asset URLs inside a resolved album are signed, and each one states when
// it stops working in its "e" parameter (Unix seconds). That has been observed
// at twenty minutes after issue — not the three hours once assumed here, which
// left the cache serving dead URLs for most of every hour. An album is
// therefore cached until its earliest URL is urlExpiryMargin from expiring,
// and the TTL is only a ceiling for albums whose URLs state no expiry.
const (
	defaultAlbumTTL = 15 * time.Minute

	// The least life any URL handed out still has: enough for a browser that
	// just received the album to load what it lists.
	urlExpiryMargin = 5 * time.Minute

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
		entry.expires = c.expiryFor(resp)
	}
	c.mu.Unlock()

	close(entry.ready)
	return resp, err
}

// expiryFor is when a freshly resolved album stops being reused: after the
// TTL, or once its earliest URL is within urlExpiryMargin of expiring,
// whichever is sooner. It is never sooner than albumErrorTTL, so an album
// whose URLs arrive nearly expired is re-fetched promptly but not on every
// request.
func (c *albumCache) expiryFor(resp *icloudalbum.Response) time.Time {
	now := c.now()
	expires := now.Add(c.ttl)
	if urlsExpire, ok := urlExpiry(resp.Photos); ok {
		if usable := urlsExpire.Add(-urlExpiryMargin); usable.Before(expires) {
			expires = usable
		}
	}
	if floor := now.Add(albumErrorTTL); expires.Before(floor) {
		expires = floor
	}
	return expires
}

// urlExpiry returns when the first signed URL among photos expires, read from
// the URLs' "e" parameter. ok is false if no URL states an expiry.
func urlExpiry(photos []icloudalbum.Image) (earliest time.Time, ok bool) {
	for _, photo := range photos {
		for _, d := range photo.Derivatives {
			if d.URL == nil {
				continue
			}
			u, err := url.Parse(*d.URL)
			if err != nil {
				continue
			}
			e, err := strconv.ParseInt(u.Query().Get("e"), 10, 64)
			if err != nil || e <= 0 {
				continue
			}
			if t := time.Unix(e, 0); !ok || t.Before(earliest) {
				earliest, ok = t, true
			}
		}
	}
	return earliest, ok
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
