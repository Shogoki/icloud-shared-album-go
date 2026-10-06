package main

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	_ "image/png" // iCloud derivatives are JPEG, but decode PNG rather than fail on one
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"

	icloudalbum "github.com/Shogoki/icloud-shared-album-go"
	"golang.org/x/image/draw"
)

// iCloud stores two derivatives per photo: a 342px preview and the original,
// typically 2048px and 0.3–1.3MB. Neither fits most of what a page shows —
// the preview is blurry on a retina screen, and the original is several times
// more bytes than the screen can use. The proxy therefore makes two sizes of
// its own by scaling the original down so its longer edge is at most:
//
//	small   640px — a gallery tile (~230px) on a 2x screen, portrait too
//	medium 1024px — cards, headers on phones, link previews
//
// The resize happens on request. It is cached the same way as the stored
// sizes (long max-age, ETag), so a CDN in front of this serves each one once.
const (
	sizeSmall  = "small"
	sizeMedium = "medium"

	resizeQuality = 80

	// The original is read into memory to decode it. iCloud's originals are
	// a few MB at most; anything larger is not a photo this should touch.
	resizeMaxSourceBytes = 40 << 20
)

// resizedSizes maps each size the proxy produces itself to its longest edge.
var resizedSizes = map[string]int{
	sizeSmall:  640,
	sizeMedium: 1024,
}

// Decoding a 2048px JPEG and resampling it takes tens of MB and a noticeable
// slice of a CPU. A gallery page asks for a dozen at once, so cap how many run
// concurrently; the rest wait their turn rather than exhausting the container.
var resizeSlots = make(chan struct{}, 4)

// scaledSize is the size a derivative of w×h is scaled to for a size whose
// longest edge is maxEdge: aspect ratio kept, never enlarged.
func scaledSize(w, h, maxEdge int) (int, int) {
	if w <= 0 || h <= 0 {
		return w, h
	}
	longest := max(w, h)
	if longest <= maxEdge {
		return w, h
	}
	scale := float64(maxEdge) / float64(longest)
	return max(1, int(float64(w)*scale+0.5)), max(1, int(float64(h)*scale+0.5))
}

// resizedETag ties the validator to both the source bytes and the resize
// parameters, so changing either invalidates every cached copy.
func resizedETag(full icloudalbum.Derivative, maxEdge int) string {
	return fmt.Sprintf(`"%s-m%dq%d"`, full.Checksum, maxEdge, resizeQuality)
}

// serveResized answers a request for a resized size (maxEdge) of a photo whose
// derivatives have already been resolved.
func (s *server) serveResized(w http.ResponseWriter, r *http.Request, album, guid string, maxEdge int, thumb, full icloudalbum.Derivative) {
	if full.URL == nil {
		sendError(w, http.StatusNotFound, "No URL", "The derivative has no resolved URL")
		return
	}

	etag := resizedETag(full, maxEdge)
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", imageCacheControl)
	if match := r.Header.Get("If-None-Match"); match == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	// Already small enough: the original is this size.
	if sw, sh := scaledSize(full.Width, full.Height, maxEdge); sw == full.Width && sh == full.Height && full.Width > 0 {
		s.streamImage(w, r, *full.URL, album, guid)
		return
	}

	resizeSlots <- struct{}{}
	defer func() { <-resizeSlots }()

	body, err := s.fetchAll(r, *full.URL)
	if err != nil {
		log.Printf("image %s/%s: fetching original to resize: %v", album, guid, err)
		sendError(w, http.StatusBadGateway, "Upstream fetch failed", "The image could not be fetched")
		return
	}

	encoded, err := resizeTo(body, maxEdge)
	if err != nil {
		// Not a decodable still image — a video's original, say. The preview
		// is always an image, so a page asking for one still gets a picture.
		log.Printf("image %s/%s: resize failed, serving the preview instead: %v", album, guid, err)
		if thumb.URL == nil {
			sendError(w, http.StatusNotFound, "No URL", "The derivative has no resolved URL")
			return
		}
		w.Header().Set("ETag", `"`+thumb.Checksum+`"`)
		s.streamImage(w, r, *thumb.URL, album, guid)
		return
	}

	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Content-Length", strconv.Itoa(len(encoded)))
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return
	}
	if _, err := w.Write(encoded); err != nil {
		log.Printf("image %s/%s: writing resized image to client: %v", album, guid, err)
	}
}

// fetchAll downloads a whole upstream asset into memory, up to
// resizeMaxSourceBytes.
func (s *server) fetchAll(r *http.Request, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.upstream.Do(req)
	if err != nil {
		return nil, withoutURL(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// Deliberately not the URL: it is signed, and this ends up in logs.
		return nil, fmt.Errorf("upstream returned %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, resizeMaxSourceBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > resizeMaxSourceBytes {
		return nil, fmt.Errorf("original is larger than %d bytes", resizeMaxSourceBytes)
	}
	return body, nil
}

// resizeTo decodes an image, scales it so its longer edge is at most maxEdge
// and encodes it as JPEG.
//
// EXIF orientation is not applied. It does not need to be: iCloud's
// derivatives are stored upright, with the width and height the album reports.
func resizeTo(src []byte, maxEdge int) ([]byte, error) {
	img, _, err := image.Decode(bytes.NewReader(src))
	if err != nil {
		return nil, fmt.Errorf("decoding: %w", err)
	}

	b := img.Bounds()
	w, h := scaledSize(b.Dx(), b.Dy(), maxEdge)
	var out image.Image = img
	if w != b.Dx() || h != b.Dy() {
		dst := image.NewRGBA(image.Rect(0, 0, w, h))
		// CatmullRom is the sharpest of x/image's kernels, and at this size
		// its cost is a few tens of milliseconds.
		draw.CatmullRom.Scale(dst, dst.Bounds(), img, b, draw.Src, nil)
		out = dst
	}

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, out, &jpeg.Options{Quality: resizeQuality}); err != nil {
		return nil, fmt.Errorf("encoding: %w", err)
	}
	return buf.Bytes(), nil
}

// withoutURL strips the request URL from an HTTP client error. Go's
// *url.Error prints as `Get "<url>": <cause>`, and every upstream URL here is
// a signed iCloud asset URL — which must not end up in the log.
func withoutURL(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return fmt.Errorf("%s: %w", ue.Op, ue.Err)
	}
	return err
}
