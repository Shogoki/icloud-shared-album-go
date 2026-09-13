package main

import (
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
	c.mu.Lock()

	if entry, ok := c.entries[token]; ok {
		// An entry whose fetch is still running has a zero expiry and must be
		// waited on rather than treated as stale.
		if entry.expires.IsZero() || entry.expires.After(c.now()) {
			c.mu.Unlock()
			<-entry.ready
			return entry.resp, entry.err
		}
	}

	if len(c.entries) >= albumCacheSweepAt {
		c.sweepLocked()
	}

	entry := &albumEntry{ready: make(chan struct{})}
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
