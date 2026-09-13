package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	icloudalbum "github.com/Shogoki/icloud-shared-album-go"
	"github.com/gorilla/mux"
)

func ptr(s string) *string { return &s }

func deriv(url string, size int64, w, h int) icloudalbum.Derivative {
	return icloudalbum.Derivative{
		Checksum: url + "-checksum",
		FileSize: size,
		Width:    w,
		Height:   h,
		URL:      ptr(url),
	}
}

func photo(guid string, created time.Time, derivs map[string]icloudalbum.Derivative) icloudalbum.Image {
	return icloudalbum.Image{
		PhotoGUID:   guid,
		DateCreated: created,
		Derivatives: derivs,
	}
}

// twoDerivatives mirrors what iCloud actually returns: a 342px preview and a
// full-size original.
func twoDerivatives(name string) map[string]icloudalbum.Derivative {
	return map[string]icloudalbum.Derivative{
		"342":  deriv(name+"-thumb", 90_000, 342, 257),
		"2048": deriv(name+"-full", 950_000, 2048, 1536),
	}
}

func TestPickDerivatives(t *testing.T) {
	thumb, full, ok := pickDerivatives(photo("a", time.Now(), twoDerivatives("a")))
	if !ok {
		t.Fatal("pickDerivatives reported no derivatives")
	}
	if got := *thumb.URL; got != "a-thumb" {
		t.Errorf("thumbnail = %q, want the smallest derivative", got)
	}
	if got := *full.URL; got != "a-full" {
		t.Errorf("full = %q, want the largest derivative", got)
	}
	if thumb.Width != 342 || full.Width != 2048 {
		t.Errorf("dimensions not carried through: thumb %d, full %d", thumb.Width, full.Width)
	}
}

func TestPickDerivativesNone(t *testing.T) {
	if _, _, ok := pickDerivatives(photo("a", time.Now(), nil)); ok {
		t.Error("pickDerivatives reported a derivative for a photo that has none")
	}
}

// Equal file sizes must not be resolved by map iteration order: the choice
// determines the proxy's ETag, which has to be stable across requests.
func TestPickDerivativesIsDeterministic(t *testing.T) {
	derivs := map[string]icloudalbum.Derivative{
		"a": deriv("a", 1000, 10, 10),
		"b": deriv("b", 1000, 10, 10),
		"c": deriv("c", 1000, 10, 10),
	}
	thumb, full, _ := pickDerivatives(photo("p", time.Now(), derivs))
	for i := 0; i < 50; i++ {
		gotThumb, gotFull, _ := pickDerivatives(photo("p", time.Now(), derivs))
		if *gotThumb.URL != *thumb.URL || *gotFull.URL != *full.URL {
			t.Fatalf("selection varied between calls: %q/%q then %q/%q",
				*thumb.URL, *full.URL, *gotThumb.URL, *gotFull.URL)
		}
	}
}

// Regression test. The handler used to sort the mapped slice with a comparator
// that indexed the *source* slice, which never got reordered — so the
// comparator answered questions about elements other than the ones being
// swapped and the output order was arbitrary rather than chronological.
func TestToImageResponsesSortsByDateCreated(t *testing.T) {
	base := time.Date(2025, 11, 27, 8, 0, 0, 0, time.UTC)
	photos := []icloudalbum.Image{
		photo("third", base.Add(2*time.Hour), twoDerivatives("c")),
		photo("first", base, twoDerivatives("a")),
		photo("fourth", base.Add(72*time.Hour), twoDerivatives("d")),
		photo("second", base.Add(time.Hour), twoDerivatives("b")),
	}

	got := toImageResponses(photos)
	want := []string{"first", "second", "third", "fourth"}
	if len(got) != len(want) {
		t.Fatalf("got %d responses, want %d", len(got), len(want))
	}
	for i, guid := range want {
		if got[i].PhotoGUID != guid {
			t.Errorf("position %d = %q, want %q", i, got[i].PhotoGUID, guid)
		}
	}
}

// Photos taken in the same second are common in a burst; the order still has
// to be identical on every run, because a build step commits it to markup.
func TestToImageResponsesIsStableForEqualTimestamps(t *testing.T) {
	at := time.Date(2025, 11, 27, 8, 0, 0, 0, time.UTC)
	photos := []icloudalbum.Image{
		photo("ccc", at, twoDerivatives("c")),
		photo("aaa", at, twoDerivatives("a")),
		photo("bbb", at, twoDerivatives("b")),
	}
	for i := 0; i < 20; i++ {
		got := toImageResponses(photos)
		for j, guid := range []string{"aaa", "bbb", "ccc"} {
			if got[j].PhotoGUID != guid {
				t.Fatalf("run %d, position %d = %q, want %q", i, j, got[j].PhotoGUID, guid)
			}
		}
	}
}

func TestToImageResponsesCarriesDimensionsAndAssetType(t *testing.T) {
	video := "video"
	p := photo("v", time.Now(), twoDerivatives("v"))
	p.MediaAssetType = &video
	p.Caption = "Am Strand"

	got := toImageResponses([]icloudalbum.Image{p})
	if len(got) != 1 {
		t.Fatalf("got %d responses, want 1", len(got))
	}
	switch {
	case got[0].Width != 2048 || got[0].Height != 1536:
		t.Errorf("full dimensions = %dx%d, want 2048x1536", got[0].Width, got[0].Height)
	case got[0].ThumbWidth != 342 || got[0].ThumbHeight != 257:
		t.Errorf("thumb dimensions = %dx%d, want 342x257", got[0].ThumbWidth, got[0].ThumbHeight)
	case got[0].AssetType != "video":
		t.Errorf("assetType = %q, want video", got[0].AssetType)
	case got[0].Caption != "Am Strand":
		t.Errorf("caption = %q", got[0].Caption)
	}
}

func TestToImageResponsesSkipsPhotosWithoutDerivatives(t *testing.T) {
	photos := []icloudalbum.Image{
		photo("empty", time.Now(), nil),
		photo("ok", time.Now(), twoDerivatives("ok")),
	}
	got := toImageResponses(photos)
	if len(got) != 1 || got[0].PhotoGUID != "ok" {
		t.Errorf("got %+v, want only the photo that has a derivative", got)
	}
}

// newTestServer wires a server whose album lookups are answered from memory.
func newTestServer(t *testing.T, photos []icloudalbum.Image, err error) (*server, *mux.Router) {
	t.Helper()
	srv := &server{
		albums: newAlbumCache(time.Hour, func(string) (*icloudalbum.Response, error) {
			if err != nil {
				return nil, err
			}
			return &icloudalbum.Response{Photos: photos}, nil
		}),
		upstream: http.DefaultClient,
	}
	r := mux.NewRouter()
	r.HandleFunc("/album/{key}", srv.getAlbumHandler).Methods(http.MethodGet)
	r.HandleFunc("/img/{album}/{guid}/{size}", srv.getImageHandler).
		Methods(http.MethodGet, http.MethodHead)
	return srv, r
}

func TestGetAlbumHandler(t *testing.T) {
	_, router := newTestServer(t, []icloudalbum.Image{
		photo("b", time.Unix(200, 0), twoDerivatives("b")),
		photo("a", time.Unix(100, 0), twoDerivatives("a")),
	}, nil)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/album/TOKEN", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "public, max-age=900" {
		t.Errorf("Cache-Control = %q", cc)
	}
	var got []ImageResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(got) != 2 || got[0].PhotoGUID != "a" {
		t.Errorf("got %+v, want the older photo first", got)
	}
}

func TestGetAlbumHandlerEmpty(t *testing.T) {
	_, router := newTestServer(t, nil, nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/album/TOKEN", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}
