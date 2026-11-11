package main

import (
	"errors"
	"fmt"
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

// refresh exists so the image proxy can double-check a cached album before
// calling a photo nonexistent. It must re-fetch an album older than maxAge,
// but reuse a younger one, or a stream of requests for a bogus GUID would each
// cost an iCloud lookup.
func TestAlbumCacheRefreshRefetchesOnlyOlderEntries(t *testing.T) {
	var calls atomic.Int32
	c := newAlbumCache(time.Hour, func(string) (*icloudalbum.Response, error) {
		calls.Add(1)
		return &icloudalbum.Response{}, nil
	})
	now := time.Now()
	c.now = func() time.Time { return now }

	c.get("TOKEN")
	now = now.Add(30 * time.Second)
	c.refresh("TOKEN", time.Minute)
	if got := calls.Load(); got != 1 {
		t.Fatalf("fetched %d times, want an album younger than maxAge reused", got)
	}

	now = now.Add(time.Minute)
	c.refresh("TOKEN", time.Minute)
	c.refresh("TOKEN", time.Minute)
	if got := calls.Load(); got != 2 {
		t.Errorf("fetched %d times, want exactly one re-fetch of the older album", got)
	}
	// What refresh fetched is an ordinary entry: get reuses it.
	c.get("TOKEN")
	if got := calls.Load(); got != 2 {
		t.Errorf("fetched %d times, want get to reuse the refreshed album", got)
	}
}

func TestCompleteAlbums(t *testing.T) {
	full := &icloudalbum.Response{Photos: []icloudalbum.Image{{PhotoGUID: "a"}}}
	blip := errors.New("connection reset by peer")

	for _, tc := range []struct {
		name      string
		results   []*icloudalbum.Response // nil entries fail with blip
		wantErr   error
		wantCalls int32
	}{
		{"success is passed through", []*icloudalbum.Response{full}, nil, 1},
		{"a failure is retried once", []*icloudalbum.Response{nil, full}, nil, 2},
		{"two failures are an error", []*icloudalbum.Response{nil, nil}, blip, 2},
		// An empty listing is a failed lookup, not a valid album: cached as
		// one, it would 404 every photo of the album for the full TTL.
		{"an empty album is retried", []*icloudalbum.Response{{}, full}, nil, 2},
		{"an empty album twice is an error", []*icloudalbum.Response{{}, {}}, errEmptyAlbum, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			fetch := completeAlbums(func(string) (*icloudalbum.Response, error) {
				r := tc.results[calls.Add(1)-1]
				if r == nil {
					return nil, blip
				}
				return r, nil
			})

			resp, err := fetch("TOKEN")
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("err = %v, want %v", err, tc.wantErr)
			}
			if err == nil && resp != full {
				t.Errorf("resp = %+v, want the album with photos", resp)
			}
			if err != nil && resp != nil {
				t.Errorf("resp = %+v alongside an error", resp)
			}
			if got := calls.Load(); got != tc.wantCalls {
				t.Errorf("fetched %d times, want %d", got, tc.wantCalls)
			}
		})
	}
}

// signedAlbum is an album of one photo whose two URLs expire at the given
// times, the way iCloud states it: Unix seconds in the "e" parameter.
func signedAlbum(thumbExpires, fullExpires time.Time) *icloudalbum.Response {
	url := func(name string, e time.Time) *string {
		return ptr(fmt.Sprintf("https://cvws.icloud-content.com/S/x/%s?v=1&e=%d&s=sig", name, e.Unix()))
	}
	return &icloudalbum.Response{Photos: []icloudalbum.Image{photo("a", time.Unix(100, 0), map[string]icloudalbum.Derivative{
		"342":  {FileSize: 1_000, URL: url("thumb.JPG", thumbExpires)},
		"2048": {FileSize: 900_000, URL: url("full.JPG", fullExpires)},
	})}}
}

// iCloud's URLs were assumed to live three hours and cached for one, but they
// live twenty minutes: for forty minutes of every hour, every image of the
// album was a 401 from iCloud and a 502 from the proxy. The cache must let an
// album go before its first URL expires, whatever the TTL says.
func TestAlbumCacheExpiresBeforeItsURLs(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	var calls atomic.Int32
	c := newAlbumCache(time.Hour, func(string) (*icloudalbum.Response, error) {
		calls.Add(1)
		return signedAlbum(now.Add(20*time.Minute), now.Add(25*time.Minute)), nil
	})
	c.now = func() time.Time { return now }

	start := now
	c.get("TOKEN")
	now = start.Add(20*time.Minute - urlExpiryMargin - time.Second)
	c.get("TOKEN")
	if got := calls.Load(); got != 1 {
		t.Fatalf("fetched %d times while every URL had more than the margin left, want 1", got)
	}

	now = start.Add(20*time.Minute - urlExpiryMargin + time.Second)
	c.get("TOKEN")
	if got := calls.Load(); got != 2 {
		t.Errorf("fetched %d times once the earliest URL was within the margin, want 2", got)
	}
}

// URLs that arrive with less than the margin left, or already expired, must
// not make every request an iCloud lookup.
func TestAlbumCacheKeepsNearlyExpiredAlbumsBriefly(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	var calls atomic.Int32
	c := newAlbumCache(time.Hour, func(string) (*icloudalbum.Response, error) {
		calls.Add(1)
		return signedAlbum(now.Add(-time.Minute), now.Add(-time.Minute)), nil
	})
	c.now = func() time.Time { return now }

	start := now
	c.get("TOKEN")
	now = start.Add(albumErrorTTL - time.Second)
	c.get("TOKEN")
	if got := calls.Load(); got != 1 {
		t.Fatalf("fetched %d times within albumErrorTTL, want 1", got)
	}
	now = start.Add(albumErrorTTL + time.Second)
	c.get("TOKEN")
	if got := calls.Load(); got != 2 {
		t.Errorf("fetched %d times after albumErrorTTL, want 2", got)
	}
}

func TestURLExpiry(t *testing.T) {
	early, late := time.Unix(1_800_000_000, 0), time.Unix(1_800_000_600, 0)

	got, ok := urlExpiry(signedAlbum(late, early).Photos)
	if !ok || !got.Equal(early) {
		t.Errorf("urlExpiry = %s, %v; want the earliest, %s", got, ok, early)
	}

	// URLs without an expiry, or no URL at all, state nothing.
	unstated := []icloudalbum.Image{photo("a", time.Unix(100, 0), map[string]icloudalbum.Derivative{
		"342":  {URL: ptr("https://cvws.icloud-content.com/S/x/thumb.JPG?v=1")},
		"2048": {URL: nil},
		"bad":  {URL: ptr("https://cvws.icloud-content.com/S/x/full.JPG?e=soon")},
	})}
	if got, ok := urlExpiry(unstated); ok {
		t.Errorf("urlExpiry = %s, want no expiry", got)
	}
}
