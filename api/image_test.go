package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	icloudalbum "github.com/Shogoki/icloud-shared-album-go"
	"github.com/gorilla/mux"
)

// upstreamStub stands in for iCloud's asset CDN and records what was asked of
// it, so a test can tell which derivative the proxy resolved.
type upstreamStub struct {
	server   *httptest.Server
	requests []*http.Request
	body     string
}

func newUpstreamStub(t *testing.T, body string) *upstreamStub {
	t.Helper()
	stub := &upstreamStub{body: body}
	stub.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stub.requests = append(stub.requests, r.Clone(r.Context()))
		if rng := r.Header.Get("Range"); rng != "" {
			w.Header().Set("Content-Range", "bytes 0-3/"+"100")
			w.WriteHeader(http.StatusPartialContent)
			w.Write([]byte(stub.body[:4]))
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write([]byte(stub.body))
	}))
	t.Cleanup(stub.server.Close)
	return stub
}

// proxyServer builds a server whose album contains one photo with a thumb and
// a full derivative, both pointing at the stub upstream.
func proxyServer(t *testing.T, stub *upstreamStub) *mux.Router {
	t.Helper()
	p := photo("GUID-1", time.Unix(100, 0), map[string]icloudalbum.Derivative{
		"342": {
			Checksum: "thumb-sum", FileSize: 1_000, Width: 342, Height: 257,
			URL: ptr(stub.server.URL + "/thumb.jpg"),
		},
		"2048": {
			Checksum: "full-sum", FileSize: 900_000, Width: 2048, Height: 1536,
			URL: ptr(stub.server.URL + "/full.jpg"),
		},
	})
	srv := &server{
		albums: newAlbumCache(time.Hour, func(string) (*icloudalbum.Response, error) {
			return &icloudalbum.Response{Photos: []icloudalbum.Image{p}}, nil
		}),
		upstream: stub.server.Client(),
	}
	r := mux.NewRouter()
	r.HandleFunc("/img/{album}/{guid}/{size}", srv.getImageHandler).
		Methods(http.MethodGet, http.MethodHead)
	return r
}

func TestImageProxyServesRequestedSize(t *testing.T) {
	for _, tc := range []struct {
		size     string
		wantPath string
		wantETag string
	}{
		{"thumb", "/thumb.jpg", `"thumb-sum"`},
		{"full", "/full.jpg", `"full-sum"`},
	} {
		t.Run(tc.size, func(t *testing.T) {
			stub := newUpstreamStub(t, "JPEGBYTES")
			router := proxyServer(t, stub)

			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/img/ALBUM/GUID-1/"+tc.size, nil))

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			if len(stub.requests) != 1 {
				t.Fatalf("upstream saw %d requests, want 1", len(stub.requests))
			}
			if got := stub.requests[0].URL.Path; got != tc.wantPath {
				t.Errorf("fetched %q, want %q", got, tc.wantPath)
			}
			if got := rec.Header().Get("ETag"); got != tc.wantETag {
				t.Errorf("ETag = %q, want %q", got, tc.wantETag)
			}
			if got := rec.Body.String(); got != "JPEGBYTES" {
				t.Errorf("body = %q", got)
			}
			if got := rec.Header().Get("Cache-Control"); got != imageCacheControl {
				t.Errorf("Cache-Control = %q, want %q", got, imageCacheControl)
			}
			if got := rec.Header().Get("Content-Type"); got != "image/jpeg" {
				t.Errorf("Content-Type = %q", got)
			}
		})
	}
}

// A matching validator must be answered without touching iCloud at all — that
// is the whole point of putting a long cache in front of these URLs.
func TestImageProxyHonoursIfNoneMatch(t *testing.T) {
	stub := newUpstreamStub(t, "JPEGBYTES")
	router := proxyServer(t, stub)

	req := httptest.NewRequest(http.MethodGet, "/img/ALBUM/GUID-1/full", nil)
	req.Header.Set("If-None-Match", `"full-sum"`)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotModified {
		t.Errorf("status = %d, want 304", rec.Code)
	}
	if len(stub.requests) != 0 {
		t.Errorf("upstream was contacted %d times for a 304", len(stub.requests))
	}
	if rec.Body.Len() != 0 {
		t.Errorf("304 carried a %d byte body", rec.Body.Len())
	}
}

// Range has to reach iCloud for video seeking to work.
func TestImageProxyForwardsRange(t *testing.T) {
	stub := newUpstreamStub(t, "JPEGBYTES")
	router := proxyServer(t, stub)

	req := httptest.NewRequest(http.MethodGet, "/img/ALBUM/GUID-1/full", nil)
	req.Header.Set("Range", "bytes=0-3")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusPartialContent {
		t.Fatalf("status = %d, want 206", rec.Code)
	}
	if got := stub.requests[0].Header.Get("Range"); got != "bytes=0-3" {
		t.Errorf("upstream Range = %q, want the client's", got)
	}
	if got := rec.Header().Get("Content-Range"); got == "" {
		t.Error("Content-Range was not passed back")
	}
	if got := rec.Body.String(); got != "JPEG" {
		t.Errorf("body = %q, want the requested range", got)
	}
}

func TestImageProxyRejectsBadInput(t *testing.T) {
	for _, tc := range []struct {
		name string
		path string
		want int
	}{
		{"unknown size", "/img/ALBUM/GUID-1/original", http.StatusBadRequest},
		{"unknown photo", "/img/ALBUM/NOPE/full", http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stub := newUpstreamStub(t, "JPEGBYTES")
			router := proxyServer(t, stub)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))
			if rec.Code != tc.want {
				t.Errorf("status = %d, want %d", rec.Code, tc.want)
			}
			if len(stub.requests) != 0 {
				t.Error("upstream was contacted for a request that should have been rejected")
			}
		})
	}
}

// An upstream failure must not surface as a 200 with a broken body, and must
// not leak the signed URL into the response.
func TestImageProxyUpstreamFailure(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer upstream.Close()

	p := photo("GUID-1", time.Unix(100, 0), map[string]icloudalbum.Derivative{
		"2048": {Checksum: "full-sum", FileSize: 900_000, URL: ptr(upstream.URL + "/full.jpg?sig=secret")},
	})
	srv := &server{
		albums: newAlbumCache(time.Hour, func(string) (*icloudalbum.Response, error) {
			return &icloudalbum.Response{Photos: []icloudalbum.Image{p}}, nil
		}),
		upstream: upstream.Client(),
	}
	r := mux.NewRouter()
	r.HandleFunc("/img/{album}/{guid}/{size}", srv.getImageHandler).Methods(http.MethodGet)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/img/ALBUM/GUID-1/full", nil))

	if rec.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "sig=secret") {
		t.Error("the signed upstream URL leaked into the error response")
	}
}
