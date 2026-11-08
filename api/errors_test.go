package main

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	icloudalbum "github.com/Shogoki/icloud-shared-album-go"
	"github.com/gorilla/mux"
)

// These tests cover how the image proxy fails. Its successes are cached for a
// month, so a failure that looks like a success, or a temporary failure that
// looks permanent, becomes a broken image that outlives the problem by weeks.

// imageServer wires the image route around an album fetcher and an upstream.
// The fetcher goes through completeAlbums, as in main().
func imageServer(t *testing.T, fetch albumFetcher, upstream *http.Client) (*server, *mux.Router) {
	t.Helper()
	srv := &server{albums: newAlbumCache(time.Hour, completeAlbums(fetch)), upstream: upstream}
	r := mux.NewRouter()
	r.HandleFunc("/img/{album}/{guid}/{size}", srv.getImageHandler).
		Methods(http.MethodGet, http.MethodHead)
	return srv, r
}

// stubPhoto is a photo whose two derivatives point at base.
func stubPhoto(guid, base string) icloudalbum.Image {
	return photo(guid, time.Unix(100, 0), map[string]icloudalbum.Derivative{
		"342":  {Checksum: guid + "-thumb", FileSize: 1_000, Width: 342, Height: 257, URL: ptr(base + "/thumb.jpg")},
		"2048": {Checksum: guid + "-full", FileSize: 900_000, Width: 2048, Height: 1536, URL: ptr(base + "/full.jpg")},
	})
}

func albumOf(photos ...icloudalbum.Image) albumFetcher {
	return func(string) (*icloudalbum.Response, error) {
		return &icloudalbum.Response{Photos: photos}, nil
	}
}

func get(router http.Handler, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

// Without an explicit Cache-Control on an error, Cloudflare stamped its default
// max-age=14400 on a 404 and a reader saw a broken image for hours. Every error
// must say no-store, including ones sent after the success headers were set.
func TestImageProxyErrorsAreNotCacheable(t *testing.T) {
	forbidden := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer forbidden.Close()

	for _, tc := range []struct {
		name  string
		fetch albumFetcher
		path  string
		want  int
	}{
		{"unknown size", albumOf(stubPhoto("GUID-1", forbidden.URL)), "/img/ALBUM/GUID-1/original.jpg", http.StatusBadRequest},
		{"unknown photo", albumOf(stubPhoto("GUID-1", forbidden.URL)), "/img/ALBUM/NOPE/medium.jpg", http.StatusNotFound},
		{"album lookup fails", func(string) (*icloudalbum.Response, error) {
			return nil, errors.New("icloud is down")
		}, "/img/ALBUM/GUID-1/medium.jpg", http.StatusBadGateway},
		{"asset fetch fails", albumOf(stubPhoto("GUID-1", forbidden.URL)), "/img/ALBUM/GUID-1/full.jpg", http.StatusBadGateway},
		{"original fetch for a resize fails", albumOf(stubPhoto("GUID-1", forbidden.URL)), "/img/ALBUM/GUID-1/medium.jpg", http.StatusBadGateway},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, router := imageServer(t, tc.fetch, forbidden.Client())
			rec := get(router, tc.path)

			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d", rec.Code, tc.want)
			}
			if got := rec.Header().Get("Cache-Control"); got != errorCacheControl {
				t.Errorf("Cache-Control = %q, want %q", got, errorCacheControl)
			}
			if got := rec.Header().Get("ETag"); got != "" {
				t.Errorf("ETag = %q on an error; a cache could revalidate the error as the image", got)
			}
		})
	}
}

// A photo missing from a cached album may simply be newer than the cache entry,
// or the lookup behind it may have been cut short. Before answering 404 the
// proxy must ask iCloud again.
func TestImageProxyRechecksAlbumBeforeNotFound(t *testing.T) {
	stub := newUpstreamStub(t, "JPEGBYTES")
	var calls atomic.Int32
	srv, router := imageServer(t, func(string) (*icloudalbum.Response, error) {
		photos := []icloudalbum.Image{stubPhoto("GUID-1", stub.server.URL)}
		if calls.Add(1) > 1 {
			photos = append(photos, stubPhoto("GUID-2", stub.server.URL))
		}
		return &icloudalbum.Response{Photos: photos}, nil
	}, stub.server.Client())
	now := time.Now()
	srv.albums.now = func() time.Time { return now }

	if rec := get(router, "/img/ALBUM/GUID-1/full.jpg"); rec.Code != http.StatusOK {
		t.Fatalf("known photo: status = %d, want 200", rec.Code)
	}

	now = now.Add(albumRecheckAfter + time.Second)
	rec := get(router, "/img/ALBUM/GUID-2/full.jpg")
	if rec.Code != http.StatusOK {
		t.Errorf("photo added after the album was cached: status = %d, want 200", rec.Code)
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("album fetched %d times, want one re-check", got)
	}
}

// A GUID that really does not exist still gets its 404 — and requests for it
// must not each cost an iCloud lookup.
func TestImageProxyRecheckIsRateLimited(t *testing.T) {
	stub := newUpstreamStub(t, "JPEGBYTES")
	var calls atomic.Int32
	srv, router := imageServer(t, func(string) (*icloudalbum.Response, error) {
		calls.Add(1)
		return &icloudalbum.Response{Photos: []icloudalbum.Image{stubPhoto("GUID-1", stub.server.URL)}}, nil
	}, stub.server.Client())
	now := time.Now()
	srv.albums.now = func() time.Time { return now }

	get(router, "/img/ALBUM/GUID-1/full.jpg")
	now = now.Add(albumRecheckAfter + time.Second)
	for i := 0; i < 10; i++ {
		if rec := get(router, "/img/ALBUM/NOPE/full.jpg"); rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", rec.Code)
		}
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("album fetched %d times, want one re-check per albumRecheckAfter", got)
	}
}

// If the re-check itself fails, nobody knows whether the photo exists. That is
// an upstream failure, and must not be reported as "no such photo".
func TestImageProxyFailedRecheckIsNotNotFound(t *testing.T) {
	stub := newUpstreamStub(t, "JPEGBYTES")
	var calls atomic.Int32
	srv, router := imageServer(t, func(string) (*icloudalbum.Response, error) {
		if calls.Add(1) > 1 {
			return nil, errors.New("connection reset by peer")
		}
		return &icloudalbum.Response{Photos: []icloudalbum.Image{stubPhoto("GUID-1", stub.server.URL)}}, nil
	}, stub.server.Client())
	now := time.Now()
	srv.albums.now = func() time.Time { return now }

	get(router, "/img/ALBUM/GUID-1/full.jpg")
	now = now.Add(albumRecheckAfter + time.Second)
	rec := get(router, "/img/ALBUM/GUID-2/medium.jpg")
	if rec.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); got != errorCacheControl {
		t.Errorf("Cache-Control = %q, want %q", got, errorCacheControl)
	}
}

// iCloud signs every derivative of a lookup that succeeds, so a derivative
// without a URL is a lookup cut short. That warrants a fresh listing, and if
// that does not help either, a gateway error — never "not found".
func TestImageProxyMissingURLIsGatewayError(t *testing.T) {
	stub := newUpstreamStub(t, "JPEGBYTES")
	unsigned := stubPhoto("GUID-1", stub.server.URL)
	full := unsigned.Derivatives["2048"]
	full.URL = nil
	unsigned.Derivatives = map[string]icloudalbum.Derivative{"342": unsigned.Derivatives["342"], "2048": full}

	var calls atomic.Int32
	srv, router := imageServer(t, func(string) (*icloudalbum.Response, error) {
		calls.Add(1)
		return &icloudalbum.Response{Photos: []icloudalbum.Image{unsigned}}, nil
	}, stub.server.Client())
	now := time.Now()
	srv.albums.now = func() time.Time { return now }

	get(router, "/img/ALBUM/GUID-1/thumb.jpg")
	now = now.Add(albumRecheckAfter + time.Second)
	for _, size := range []string{"full", "medium"} {
		rec := get(router, "/img/ALBUM/GUID-1/"+size+".jpg")
		if rec.Code != http.StatusBadGateway {
			t.Errorf("%s: status = %d, want 502", size, rec.Code)
		}
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("album fetched %d times, want one re-check", got)
	}
}

// Whatever comes back with a 2xx is cached for a month as an image, so an
// upstream answer that is not one must not be passed through.
func TestImageProxyRejectsNonImageUpstream(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte("<html>Service Unavailable</html>"))
	}))
	defer upstream.Close()

	_, router := imageServer(t, albumOf(stubPhoto("GUID-1", upstream.URL)), upstream.Client())
	rec := get(router, "/img/ALBUM/GUID-1/full.jpg")
	if rec.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); got != errorCacheControl {
		t.Errorf("Cache-Control = %q, want %q", got, errorCacheControl)
	}
}

// A 5xx from iCloud is usually momentary and worth one more try; a 4xx will
// not change and is not.
func TestImageProxyRetriesUpstreamServerErrors(t *testing.T) {
	for _, tc := range []struct {
		name      string
		first     int
		want      int
		wantTries int32
	}{
		{"5xx is retried", http.StatusServiceUnavailable, http.StatusOK, 2},
		{"4xx is not", http.StatusForbidden, http.StatusBadGateway, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var tries atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tries.Add(1) == 1 {
					w.WriteHeader(tc.first)
					return
				}
				w.Header().Set("Content-Type", "image/jpeg")
				w.Write([]byte("JPEGBYTES"))
			}))
			defer upstream.Close()

			_, router := imageServer(t, albumOf(stubPhoto("GUID-1", upstream.URL)), upstream.Client())
			if rec := get(router, "/img/ALBUM/GUID-1/full.jpg"); rec.Code != tc.want {
				t.Errorf("status = %d, want %d", rec.Code, tc.want)
			}
			if got := tries.Load(); got != tc.wantTries {
				t.Errorf("upstream tried %d times, want %d", got, tc.wantTries)
			}
		})
	}
}

// If iCloud drops the connection mid-image, the status line has already gone
// out as 200. Ending the response normally would hand the client — and the CDN
// caching it for a month — a truncated image that looks complete. The
// response must be cut off instead, so the client sees an error.
func TestImageProxyAbortsATruncatedStream(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// No Content-Length: the body is chunked, and only the terminating
		// chunk tells a client that it is complete.
		w.Header().Set("Content-Type", "image/jpeg")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("PARTIAL"))
		w.(http.Flusher).Flush()
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			return
		}
		conn.Close()
	}))
	defer upstream.Close()

	_, router := imageServer(t, albumOf(stubPhoto("GUID-1", upstream.URL)), upstream.Client())
	proxy := httptest.NewServer(router)
	defer proxy.Close()

	resp, err := http.Get(proxy.URL + "/img/ALBUM/GUID-1/full.jpg")
	if err != nil {
		return // cut off before the headers: also not a cacheable image
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err == nil {
		t.Errorf("read a complete %d response with body %q; a truncated image must not end cleanly",
			resp.StatusCode, body)
	}
}
