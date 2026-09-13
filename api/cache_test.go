package main

import (
	"errors"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	icloudalbum "github.com/Shogoki/icloud-shared-album-go"
)

func TestAlbumCacheReusesAResolvedAlbum(t *testing.T) {
	var calls atomic.Int32
	c := newAlbumCache(time.Hour, func(string) (*icloudalbum.Response, error) {
		calls.Add(1)
		return &icloudalbum.Response{}, nil
	})

	for i := 0; i < 5; i++ {
		if _, err := c.get("TOKEN"); err != nil {
			t.Fatalf("get: %v", err)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("fetched %d times, want 1", got)
	}
}

func TestAlbumCacheKeepsTokensSeparate(t *testing.T) {
	var calls atomic.Int32
	c := newAlbumCache(time.Hour, func(string) (*icloudalbum.Response, error) {
		calls.Add(1)
		return &icloudalbum.Response{}, nil
	})
	c.get("A")
	c.get("B")
	c.get("A")
	if got := calls.Load(); got != 2 {
		t.Errorf("fetched %d times, want one per distinct token", got)
	}
}

// A gallery of fifteen images asks the proxy for fifteen images at once. If
// each of those started its own five-second album resolution, a cold page load
// would hammer iCloud with fifteen identical lookups.
func TestAlbumCacheCollapsesConcurrentFetches(t *testing.T) {
	var calls atomic.Int32
	release := make(chan struct{})
	c := newAlbumCache(time.Hour, func(string) (*icloudalbum.Response, error) {
		calls.Add(1)
		<-release // hold the fetch open so every caller piles up behind it
		return &icloudalbum.Response{}, nil
	})

	var wg sync.WaitGroup
	for i := 0; i < 15; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.get("TOKEN"); err != nil {
				t.Errorf("get: %v", err)
			}
		}()
	}

	// Give the goroutines time to queue up, then let the single fetch finish.
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()

	if got := calls.Load(); got != 1 {
		t.Errorf("fetched %d times, want all callers to share one fetch", got)
	}
}

func TestAlbumCacheRefetchesAfterTTL(t *testing.T) {
	var calls atomic.Int32
	c := newAlbumCache(time.Hour, func(string) (*icloudalbum.Response, error) {
		calls.Add(1)
		return &icloudalbum.Response{}, nil
	})

	now := time.Now()
	c.now = func() time.Time { return now }

	c.get("TOKEN")
	now = now.Add(30 * time.Minute)
	c.get("TOKEN")
	if got := calls.Load(); got != 1 {
		t.Fatalf("fetched %d times within the TTL, want 1", got)
	}

	now = now.Add(31 * time.Minute) // past the hour
	c.get("TOKEN")
	if got := calls.Load(); got != 2 {
		t.Errorf("fetched %d times after the TTL, want 2", got)
	}
}

// A failure must be retried soon, but not on every single request, or an
// iCloud outage turns into a retry storm.
func TestAlbumCacheRetriesFailuresAfterShortTTL(t *testing.T) {
	var calls atomic.Int32
	wantErr := errors.New("icloud is down")
	c := newAlbumCache(time.Hour, func(string) (*icloudalbum.Response, error) {
		calls.Add(1)
		return nil, wantErr
	})

	now := time.Now()
	c.now = func() time.Time { return now }

	if _, err := c.get("TOKEN"); !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want the fetch error", err)
	}
	c.get("TOKEN")
	if got := calls.Load(); got != 1 {
		t.Fatalf("fetched %d times, want the failure to be cached briefly", got)
	}

	now = now.Add(albumErrorTTL + time.Second)
	c.get("TOKEN")
	if got := calls.Load(); got != 2 {
		t.Errorf("fetched %d times, want a retry once the error TTL passed", got)
	}
	// The error must not be pinned for the full album TTL.
	if albumErrorTTL >= defaultAlbumTTL {
		t.Error("errors are cached as long as successes")
	}
}

// Tokens come straight from the request path, so the map would otherwise grow
// without bound.
func TestAlbumCacheSweepsExpiredEntries(t *testing.T) {
	c := newAlbumCache(time.Hour, func(string) (*icloudalbum.Response, error) {
		return &icloudalbum.Response{}, nil
	})
	now := time.Now()
	c.now = func() time.Time { return now }

	for i := 0; i < albumCacheSweepAt; i++ {
		c.get("token-" + strconv.Itoa(i))
	}
	before := len(c.entries)
	now = now.Add(2 * time.Hour) // everything is stale
	c.get("one-more")

	if len(c.entries) >= before {
		t.Errorf("cache held %d entries before and %d after the sweep; expired entries were not dropped",
			before, len(c.entries))
	}
}
