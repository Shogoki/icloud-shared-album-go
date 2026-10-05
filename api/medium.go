package main

import (
	"bytes"
	"fmt"
	"image"
	"image/jpeg"
	_ "image/png" // iCloud derivatives are JPEG, but decode PNG rather than fail on one
	"io"
	"log"
	"net/http"
	"strconv"

	icloudalbum "github.com/Shogoki/icloud-shared-album-go"
	"golang.org/x/image/draw"
)

// iCloud stores two derivatives per photo: a 342px preview and the original,
// typically 2048px and 0.3–1.3MB. Neither fits a 700px homepage card or a
// gallery tile on a retina screen — the preview is blurry there and the
// original is several times more bytes than the screen can show. "medium" is
// the original scaled down so its longer edge is at most mediumMaxEdge.
//
// The resize happens on request. It is cached the same way as the other sizes
// (long max-age, ETag), so a CDN in front of this serves each one once.
const (
	sizeMedium    = "medium"
	mediumMaxEdge = 1024
	mediumQuality = 80

	// The original is read into memory to decode it. iCloud's originals are
	// a few MB at most; anything larger is not a photo this should touch.
	mediumMaxSourceBytes = 40 << 20
)

// Decoding a 2048px JPEG and resampling it takes tens of MB and a noticeable
// slice of a CPU. A gallery page asks for a dozen at once, so cap how many run
// concurrently; the rest wait their turn rather than exhausting the container.
var mediumSlots = make(chan struct{}, 4)

// mediumSize is the size a derivative of w×h is scaled to: longer edge at most
// mediumMaxEdge, aspect ratio kept, never enlarged.
func mediumSize(w, h int) (int, int) {
	if w <= 0 || h <= 0 {
		return w, h
	}
	longest := max(w, h)
	if longest <= mediumMaxEdge {
		return w, h
	}
	scale := float64(mediumMaxEdge) / float64(longest)
	return max(1, int(float64(w)*scale+0.5)), max(1, int(float64(h)*scale+0.5))
}

// mediumETag ties the validator to both the source bytes and the resize
// parameters, so changing either invalidates every cached copy.
func mediumETag(full icloudalbum.Derivative) string {
	return fmt.Sprintf(`"%s-m%dq%d"`, full.Checksum, mediumMaxEdge, mediumQuality)
}

// serveMedium answers a request for the medium size of a photo whose
// derivatives have already been resolved.
func (s *server) serveMedium(w http.ResponseWriter, r *http.Request, album, guid string, thumb, full icloudalbum.Derivative) {
	if full.URL == nil {
		sendError(w, http.StatusNotFound, "No URL", "The derivative has no resolved URL")
		return
	}

	etag := mediumETag(full)
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", imageCacheControl)
	if match := r.Header.Get("If-None-Match"); match == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	// Already small enough: the original is the medium size.
	if mw, mh := mediumSize(full.Width, full.Height); mw == full.Width && mh == full.Height && full.Width > 0 {
		s.streamImage(w, r, *full.URL, album, guid)
		return
	}

	mediumSlots <- struct{}{}
	defer func() { <-mediumSlots }()

	body, err := s.fetchAll(r, *full.URL)
	if err != nil {
		log.Printf("image %s/%s: fetching original for medium: %v", album, guid, err)
		sendError(w, http.StatusBadGateway, "Upstream fetch failed", "The image could not be fetched")
		return
	}

	encoded, err := resizeToMedium(body)
	if err != nil {
		// Not a decodable still image — a video's original, say. The preview
		// is always an image, so a page asking for one still gets a picture.
		log.Printf("image %s/%s: medium resize failed, serving the preview instead: %v", album, guid, err)
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
		log.Printf("image %s/%s: writing medium to client: %v", album, guid, err)
	}
}

// fetchAll downloads a whole upstream asset into memory, up to
// mediumMaxSourceBytes.
func (s *server) fetchAll(r *http.Request, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.upstream.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// Deliberately not the URL: it is signed, and this ends up in logs.
		return nil, fmt.Errorf("upstream returned %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, mediumMaxSourceBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > mediumMaxSourceBytes {
		return nil, fmt.Errorf("original is larger than %d bytes", mediumMaxSourceBytes)
	}
	return body, nil
}

// resizeToMedium decodes an image, scales it to the medium size and encodes it
// as JPEG.
//
// EXIF orientation is not applied. It does not need to be: iCloud's
// derivatives are stored upright, with the width and height the album reports.
func resizeToMedium(src []byte) ([]byte, error) {
	img, _, err := image.Decode(bytes.NewReader(src))
	if err != nil {
		return nil, fmt.Errorf("decoding: %w", err)
	}

	b := img.Bounds()
	w, h := mediumSize(b.Dx(), b.Dy())
	var out image.Image = img
	if w != b.Dx() || h != b.Dy() {
		dst := image.NewRGBA(image.Rect(0, 0, w, h))
		// CatmullRom is the sharpest of x/image's kernels, and at this size
		// its cost is a few tens of milliseconds.
		draw.CatmullRom.Scale(dst, dst.Bounds(), img, b, draw.Src, nil)
		out = dst
	}

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, out, &jpeg.Options{Quality: mediumQuality}); err != nil {
		return nil, fmt.Errorf("encoding: %w", err)
	}
	return buf.Bytes(), nil
}
