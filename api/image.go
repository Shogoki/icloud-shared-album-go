package main

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	icloudalbum "github.com/Shogoki/icloud-shared-album-go"
	"github.com/gorilla/mux"
)

// The image proxy exists because iCloud's asset URLs are signed and expire
// twenty minutes after they are issued. That makes them impossible to put
// into a static page at build time, which is what forced the gallery to fetch
// the album from the browser on every page load.
//
// /img/{album}/{photoGuid}/{size} is stable and unsigned, so a generator can
// emit it into plain <img> markup once and it keeps working. The proxy looks
// the signed URL up at request time and streams the bytes back.
//
// Note that this cannot be pointed at an arbitrary URL: the only URLs it will
// ever fetch are the ones iCloud itself returned for the requested album.

// How long a proxied image may be reused. Images are addressed by photo GUID
// and size, so the bytes change only if the photo behind that GUID is replaced
// — worth a long cache, but not `immutable`, which would rule out ever picking
// a replacement up.
const imageCacheControl = "public, max-age=2592000"

const (
	sizeThumb = "thumb"
	sizeFull  = "full"
)

// albumRecheckAfter is how old a cached album may be before a photo missing
// from it, or missing its URL, sends the proxy back to iCloud for a fresh
// listing instead of trusting the cached one.
const albumRecheckAfter = time.Minute

// upstreamAttempts is how often an asset fetch is tried before giving up.
const upstreamAttempts = 2

// asset is one derivative the proxy is about to fetch: the signed URL it has
// for it, and what identifies it if that URL turns out to be dead.
type asset struct {
	album, guid string
	derivative  icloudalbum.Derivative
}

// A CDN's default cache level typically decides what is cacheable from the
// file extension in the path, so callers address these as ".../full.jpg" and
// the suffix is stripped here. It is cosmetic — the bytes are whatever iCloud
// stored — but without it the response looks dynamic and is never cached.
// Extensionless paths keep working.
const jpegSuffix = ".jpg"

func (s *server) getImageHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	album, guid, size := vars["album"], vars["guid"], vars["size"]
	size = strings.TrimSuffix(size, jpegSuffix)

	maxEdge, resized := resizedSizes[size]
	if size != sizeThumb && size != sizeFull && !resized {
		sendError(w, http.StatusBadRequest, "Unknown size",
			"Size must be "+sizeThumb+", "+sizeSmall+", "+sizeMedium+" or "+sizeFull)
		return
	}

	response, err := s.albums.get(album)
	if err != nil {
		log.Printf("image %s/%s: resolving album: %v", album, guid, err)
		sendError(w, http.StatusBadGateway, "Failed to fetch album",
			"The album could not be resolved upstream")
		return
	}

	photo, found := findPhoto(response.Photos, guid)
	if !found || !hasAllURLs(photo) {
		// A 404 says the photo does not exist, and only a fresh listing can
		// say that: the cached one may predate the photo, or come from a
		// lookup that iCloud only half answered. A lookup that fails now is
		// an upstream failure, not evidence the photo is gone.
		response, err = s.albums.refresh(album, albumRecheckAfter)
		if err != nil {
			log.Printf("image %s/%s: re-resolving album: %v", album, guid, err)
			sendError(w, http.StatusBadGateway, "Failed to fetch album",
				"The album could not be resolved upstream")
			return
		}
		photo, found = findPhoto(response.Photos, guid)
	}
	if !found {
		sendError(w, http.StatusNotFound, "Unknown photo",
			"No photo with that GUID exists in this album")
		return
	}

	thumb, full, ok := pickDerivatives(photo)
	if !ok {
		sendError(w, http.StatusNotFound, "No derivative", "The photo has no usable derivative")
		return
	}
	if resized {
		s.serveResized(w, r, album, guid, maxEdge, thumb, full)
		return
	}
	derivative := full
	if size == sizeThumb {
		derivative = thumb
	}
	if derivative.URL == nil {
		sendNoURL(w)
		return
	}

	// The checksum identifies the bytes, so it is the natural validator: it
	// changes exactly when the underlying derivative does.
	etag := `"` + derivative.Checksum + `"`
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", imageCacheControl)
	if match := r.Header.Get("If-None-Match"); match == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	s.streamImage(w, r, asset{album, guid, derivative})
}

// streamImage copies the upstream asset to the client, forwarding Range so
// that seeking within a video keeps working.
func (s *server) streamImage(w http.ResponseWriter, r *http.Request, a asset) {
	album, guid := a.album, a.guid
	resp, err := s.getUpstream(r, a, r.Header.Get("Range"))
	if err != nil {
		log.Printf("image %s/%s: fetching asset: %v", album, guid, err)
		sendError(w, http.StatusBadGateway, "Upstream fetch failed", "The image could not be fetched")
		return
	}
	defer resp.Body.Close()

	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "image/jpeg"
	}
	// What goes out from here is cached for a month as this image, so
	// anything that is not one — an error page served with a 200, say — must
	// not be passed through.
	if !isMedia(contentType) {
		log.Printf("image %s/%s: upstream sent %q, not an image", album, guid, contentType)
		sendError(w, http.StatusBadGateway, "Upstream fetch failed", "The image could not be fetched")
		return
	}
	w.Header().Set("Content-Type", contentType)
	if cl := resp.Header.Get("Content-Length"); cl != "" {
		w.Header().Set("Content-Length", cl)
	}
	if cr := resp.Header.Get("Content-Range"); cr != "" {
		w.Header().Set("Content-Range", cr)
	}
	w.Header().Set("Accept-Ranges", "bytes")
	w.WriteHeader(resp.StatusCode)

	if _, err := io.Copy(w, resp.Body); err != nil {
		log.Printf("image %s/%s: streaming to client: %v", album, guid, err)
		// The status line is already sent, so the one way left to say the
		// body is incomplete is not to finish the response. Returning would
		// end it cleanly — for a chunked body, indistinguishable from a
		// whole image — and a cache would keep the truncated bytes for a
		// month. Aborting resets the connection (or HTTP/2 stream) instead.
		// If the client is what went away, that costs nothing.
		panic(http.ErrAbortHandler)
	}
}

// getUpstream fetches an iCloud asset, sending rng as its Range header if set.
// Anything but a 200 or 206 is an error. A fetch that fails outright or that
// iCloud answers with a 5xx is tried again: both are usually momentary, and
// the alternative is a broken image. Most 4xx are not, as they will not
// change — except that a 401 or 403 is what iCloud answers for an expired
// signature, so those are tried once more with a freshly signed URL.
func (s *server) getUpstream(r *http.Request, a asset, rng string) (*http.Response, error) {
	url := *a.derivative.URL
	resigned := false
	var err error
	for attempt := 0; attempt < upstreamAttempts; attempt++ {
		var resp *http.Response
		resp, err = s.getUpstreamOnce(r, url, rng)
		if err != nil {
			if r.Context().Err() != nil {
				return nil, err // the client is gone; nobody to retry for
			}
			continue
		}
		if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusPartialContent {
			return resp, nil
		}
		resp.Body.Close()
		// Deliberately not the URL: it is signed, and this ends up in logs.
		err = fmt.Errorf("upstream returned %s", resp.Status)
		if isSignatureRejection(resp.StatusCode) && !resigned {
			resigned = true
			fresh, ok := s.resign(a)
			if !ok {
				return nil, err
			}
			log.Printf("image %s/%s: signed URL rejected (%s), retrying with a fresh one", a.album, a.guid, resp.Status)
			url = fresh
			attempt-- // a fresh URL is a fresh start, not a retry of the dead one
			continue
		}
		if resp.StatusCode < http.StatusInternalServerError {
			return nil, err
		}
	}
	return nil, err
}

func isSignatureRejection(status int) bool {
	return status == http.StatusUnauthorized || status == http.StatusForbidden
}

// resign looks a's derivative up in a fresh listing of its album and returns
// the URL iCloud signed for it there. ok is false if there is no new URL to
// try: the lookup failed, the derivative is gone, or the listing was already
// fresh and holds the same URL.
//
// The listing comes from refresh with albumRecheckAfter, so however many
// images of an album hit dead URLs at once, they share one iCloud lookup per
// albumRecheckAfter.
func (s *server) resign(a asset) (string, bool) {
	if a.derivative.Checksum == "" {
		return "", false // nothing to recognise the derivative by
	}
	response, err := s.albums.refresh(a.album, albumRecheckAfter)
	if err != nil {
		log.Printf("image %s/%s: re-resolving album for a fresh URL: %v", a.album, a.guid, err)
		return "", false
	}
	photo, found := findPhoto(response.Photos, a.guid)
	if !found {
		return "", false
	}
	for _, d := range photo.Derivatives {
		if d.Checksum == a.derivative.Checksum && d.URL != nil && *d.URL != *a.derivative.URL {
			return *d.URL, true
		}
	}
	return "", false
}

func (s *server) getUpstreamOnce(r *http.Request, url, rng string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, url, nil)
	if err != nil {
		return nil, withoutURL(err)
	}
	if rng != "" {
		req.Header.Set("Range", rng)
	}
	resp, err := s.upstream.Do(req)
	if err != nil {
		return nil, withoutURL(err)
	}
	return resp, nil
}

// isMedia reports whether contentType is something an image URL may serve.
// Video counts: a video's "full" is the video itself.
func isMedia(contentType string) bool {
	ct := strings.ToLower(contentType)
	return strings.HasPrefix(ct, "image/") || strings.HasPrefix(ct, "video/")
}

// hasAllURLs reports whether every derivative of photo came with a signed URL.
// iCloud signs all of them when a lookup succeeds, so a gap means the lookup
// was cut short.
func hasAllURLs(photo icloudalbum.Image) bool {
	for _, d := range photo.Derivatives {
		if d.URL == nil {
			return false
		}
	}
	return true
}

// sendNoURL answers for a derivative iCloud returned no signed URL for. Even
// on a fresh listing that is iCloud failing to sign it, not the image being
// gone, so it is a gateway error rather than a 404.
func sendNoURL(w http.ResponseWriter) {
	sendError(w, http.StatusBadGateway, "No URL", "iCloud returned no URL for the image")
}

func findPhoto(photos []icloudalbum.Image, guid string) (icloudalbum.Image, bool) {
	for _, photo := range photos {
		if photo.PhotoGUID == guid {
			return photo, true
		}
	}
	return icloudalbum.Image{}, false
}
