package main

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	icloudalbum "github.com/Shogoki/icloud-shared-album-go"
	"github.com/gorilla/mux"
)

// testJPEG returns a w×h JPEG with a horizontal gradient, so a resize that
// mixed up width and height would be visible in the decoded bounds.
func testJPEG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for x := 0; x < w; x++ {
		c := color.RGBA{uint8(x * 255 / w), 80, 160, 255}
		for y := 0; y < h; y++ {
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// mediumServer is proxyServer with a full derivative of the given size whose
// bytes are fullBody, served by a stub upstream.
func mediumServer(t *testing.T, fullW, fullH int, fullBody string) (*mux.Router, *upstreamStub) {
	t.Helper()
	stub := newUpstreamStub(t, fullBody)
	p := photo("GUID-1", time.Unix(100, 0), map[string]icloudalbum.Derivative{
		"342": {
			Checksum: "thumb-sum", FileSize: 1_000, Width: 342, Height: 257,
			URL: ptr(stub.server.URL + "/thumb.jpg"),
		},
		"full": {
			Checksum: "full-sum", FileSize: 900_000, Width: fullW, Height: fullH,
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
	return r, stub
}

func TestScaledSize(t *testing.T) {
	for _, tc := range []struct{ w, h, wantW, wantH int }{
		{2048, 1536, 1024, 768}, // landscape
		{1536, 2048, 768, 1024}, // portrait
		{2049, 1537, 1024, 768}, // odd sizes round, not truncate
		{4032, 3024, 1024, 768}, // camera original
		{1024, 683, 1024, 683},  // exactly the limit: unchanged
		{800, 600, 800, 600},    // smaller: never enlarged
		{0, 0, 0, 0},            // unknown dimensions pass through
		{5000, 10, 1024, 2},     // extreme panorama keeps a visible height
	} {
		gotW, gotH := scaledSize(tc.w, tc.h, 1024)
		if gotW != tc.wantW || gotH != tc.wantH {
			t.Errorf("scaledSize(%d, %d, 1024) = %d×%d, want %d×%d", tc.w, tc.h, gotW, gotH, tc.wantW, tc.wantH)
		}
	}
}

func TestMediumResizesTheOriginal(t *testing.T) {
	router, stub := mediumServer(t, 2048, 1536, string(testJPEG(t, 2048, 1536)))

	for _, path := range []string{"/img/ALBUM/GUID-1/medium.jpg", "/img/ALBUM/GUID-1/medium"} {
		t.Run(path, func(t *testing.T) {
			stub.requests = nil
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			if len(stub.requests) != 1 || stub.requests[0].URL.Path != "/full.jpg" {
				t.Fatalf("upstream saw %d requests, want one for /full.jpg", len(stub.requests))
			}
			img, format, err := image.Decode(bytes.NewReader(rec.Body.Bytes()))
			if err != nil {
				t.Fatalf("response is not an image: %v", err)
			}
			if format != "jpeg" {
				t.Errorf("format = %q, want jpeg", format)
			}
			if b := img.Bounds(); b.Dx() != 1024 || b.Dy() != 768 {
				t.Errorf("decoded size = %d×%d, want 1024×768", b.Dx(), b.Dy())
			}
			if got := rec.Header().Get("Content-Type"); got != "image/jpeg" {
				t.Errorf("Content-Type = %q", got)
			}
			if got := rec.Header().Get("ETag"); got != `"full-sum-m1024q80"` {
				t.Errorf("ETag = %q", got)
			}
			if got := rec.Header().Get("Cache-Control"); got != imageCacheControl {
				t.Errorf("Cache-Control = %q, want %q", got, imageCacheControl)
			}
			if rec.Header().Get("Content-Length") == "" {
				t.Error("Content-Length missing")
			}
		})
	}
}

// A portrait original must come out portrait — width and height not swapped.
func TestMediumKeepsPortraitOrientation(t *testing.T) {
	router, _ := mediumServer(t, 1536, 2048, string(testJPEG(t, 1536, 2048)))

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/img/ALBUM/GUID-1/medium.jpg", nil))

	img, _, err := image.Decode(bytes.NewReader(rec.Body.Bytes()))
	if err != nil {
		t.Fatalf("response is not an image: %v", err)
	}
	if b := img.Bounds(); b.Dx() != 768 || b.Dy() != 1024 {
		t.Errorf("decoded size = %d×%d, want 768×1024", b.Dx(), b.Dy())
	}
}

// The validator check must come before the expensive part.
func TestMediumHonoursIfNoneMatch(t *testing.T) {
	router, stub := mediumServer(t, 2048, 1536, string(testJPEG(t, 2048, 1536)))

	req := httptest.NewRequest(http.MethodGet, "/img/ALBUM/GUID-1/medium.jpg", nil)
	req.Header.Set("If-None-Match", `"full-sum-m1024q80"`)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotModified {
		t.Errorf("status = %d, want 304", rec.Code)
	}
	if len(stub.requests) != 0 {
		t.Errorf("upstream was contacted %d times for a 304", len(stub.requests))
	}
}

// HEAD answers with the headers of the real response and no body.
func TestMediumHead(t *testing.T) {
	router, _ := mediumServer(t, 2048, 1536, string(testJPEG(t, 2048, 1536)))

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodHead, "/img/ALBUM/GUID-1/medium.jpg", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("HEAD carried a %d byte body", rec.Body.Len())
	}
	if rec.Header().Get("Content-Length") == "" || rec.Header().Get("ETag") == "" {
		t.Error("HEAD is missing Content-Length or ETag")
	}
}

// An original that already fits is streamed as-is, not re-encoded.
func TestMediumPassesSmallOriginalsThrough(t *testing.T) {
	router, stub := mediumServer(t, 800, 600, "SMALLJPEG")

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/img/ALBUM/GUID-1/medium.jpg", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Body.String(); got != "SMALLJPEG" {
		t.Errorf("body = %q, want the original bytes untouched", got)
	}
	if got := stub.requests[0].URL.Path; got != "/full.jpg" {
		t.Errorf("fetched %q, want /full.jpg", got)
	}
}

// A video's "original" is not a still image. The page asked for a picture, so
// it gets the preview rather than an error or a broken image.
func TestMediumFallsBackToThePreview(t *testing.T) {
	router, stub := mediumServer(t, 1920, 1080, "not an image at all")

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/img/ALBUM/GUID-1/medium.jpg", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if n := len(stub.requests); n != 2 || stub.requests[1].URL.Path != "/thumb.jpg" {
		t.Fatalf("upstream saw %d requests, want the original then /thumb.jpg", n)
	}
	if got := rec.Header().Get("ETag"); got != `"thumb-sum"` {
		t.Errorf("ETag = %q, want the preview's, since that is what was sent", got)
	}
}

func TestMediumUpstreamFailure(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer upstream.Close()

	p := photo("GUID-1", time.Unix(100, 0), map[string]icloudalbum.Derivative{
		"full": {Checksum: "full-sum", FileSize: 900_000, Width: 2048, Height: 1536,
			URL: ptr(upstream.URL + "/full.jpg?sig=secret")},
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
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/img/ALBUM/GUID-1/medium.jpg", nil))

	if rec.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", rec.Code)
	}
	if bytes.Contains(rec.Body.Bytes(), []byte("sig=secret")) {
		t.Error("the signed upstream URL leaked into the error response")
	}
}

// The album endpoint advertises the medium dimensions so a static page can
// reserve the right box without fetching the image.
func TestAlbumReportsMediumDimensions(t *testing.T) {
	photos := []icloudalbum.Image{
		photo("LANDSCAPE", time.Unix(100, 0), map[string]icloudalbum.Derivative{
			"342":  {FileSize: 1_000, Width: 342, Height: 257},
			"full": {FileSize: 900_000, Width: 2048, Height: 1536},
		}),
		photo("SMALL", time.Unix(200, 0), map[string]icloudalbum.Derivative{
			"342":  {FileSize: 1_000, Width: 342, Height: 257},
			"full": {FileSize: 90_000, Width: 640, Height: 480},
		}),
	}
	out := toImageResponses(photos)
	if len(out) != 2 {
		t.Fatalf("got %d photos", len(out))
	}
	if out[0].MediumWidth != 1024 || out[0].MediumHeight != 768 {
		t.Errorf("landscape medium = %d×%d, want 1024×768", out[0].MediumWidth, out[0].MediumHeight)
	}
	if out[1].MediumWidth != 640 || out[1].MediumHeight != 480 {
		t.Errorf("small medium = %d×%d, want the original 640×480", out[1].MediumWidth, out[1].MediumHeight)
	}
	if out[0].SmallWidth != 640 || out[0].SmallHeight != 480 {
		t.Errorf("landscape small = %d×%d, want 640×480", out[0].SmallWidth, out[0].SmallHeight)
	}
	if out[1].SmallWidth != 640 || out[1].SmallHeight != 480 {
		t.Errorf("640×480 original small = %d×%d, want it unchanged", out[1].SmallWidth, out[1].SmallHeight)
	}
}

// small is the same machinery at 640px; it gets its own ETag so the two sizes
// can never be served for each other from a cache.
func TestSmallResizesTheOriginal(t *testing.T) {
	router, _ := mediumServer(t, 2048, 1536, string(testJPEG(t, 2048, 1536)))

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/img/ALBUM/GUID-1/small.jpg", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	img, _, err := image.Decode(bytes.NewReader(rec.Body.Bytes()))
	if err != nil {
		t.Fatalf("response is not an image: %v", err)
	}
	if b := img.Bounds(); b.Dx() != 640 || b.Dy() != 480 {
		t.Errorf("decoded size = %d×%d, want 640×480", b.Dx(), b.Dy())
	}
	if got := rec.Header().Get("ETag"); got != `"full-sum-m640q80"` {
		t.Errorf("ETag = %q, want the small-size validator", got)
	}
}

// Every upstream URL is a signed iCloud URL. Go's client errors print the URL
// they failed on, so they must be stripped before they reach the log.
func TestWithoutURLStripsTheSignedURL(t *testing.T) {
	err := &url.Error{Op: "Get", URL: "https://cvws.icloud-content.com/x.JPG?sig=secret", Err: errors.New("context canceled")}
	got := withoutURL(err).Error()
	if strings.Contains(got, "sig=secret") || strings.Contains(got, "icloud-content") {
		t.Errorf("withoutURL kept the URL: %q", got)
	}
	if !strings.Contains(got, "context canceled") {
		t.Errorf("withoutURL dropped the cause: %q", got)
	}
}

// End to end: an upstream that cannot be reached must not put its signed URL
// into the server log, on either the streaming or the resizing path.
func TestUpstreamErrorsDoNotLogSignedURLs(t *testing.T) {
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	deadURL := dead.URL
	dead.Close() // connection refused from here on

	p := photo("GUID-1", time.Unix(100, 0), map[string]icloudalbum.Derivative{
		"342":  {Checksum: "t", FileSize: 1_000, Width: 342, Height: 257, URL: ptr(deadURL + "/thumb.jpg?sig=secret")},
		"full": {Checksum: "f", FileSize: 900_000, Width: 2048, Height: 1536, URL: ptr(deadURL + "/full.jpg?sig=secret")},
	})
	srv := &server{
		albums: newAlbumCache(time.Hour, func(string) (*icloudalbum.Response, error) {
			return &icloudalbum.Response{Photos: []icloudalbum.Image{p}}, nil
		}),
		upstream: &http.Client{Timeout: 2 * time.Second},
	}
	r := mux.NewRouter()
	r.HandleFunc("/img/{album}/{guid}/{size}", srv.getImageHandler).Methods(http.MethodGet)

	var logs bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(prev) })

	for _, size := range []string{"full", "thumb", "small", "medium"} {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/img/ALBUM/GUID-1/"+size+".jpg", nil))
		if rec.Code != http.StatusBadGateway {
			t.Errorf("%s: status = %d, want 502", size, rec.Code)
		}
	}
	if logs.Len() == 0 {
		t.Fatal("expected the failures to be logged")
	}
	if strings.Contains(logs.String(), "sig=secret") {
		t.Errorf("signed URL leaked into the log:\n%s", logs.String())
	}
}
