package main

import (
	"io"
	"log"
	"net/http"
	"strings"

	icloudalbum "github.com/Shogoki/icloud-shared-album-go"
	"github.com/gorilla/mux"
)

// The image proxy exists because iCloud's asset URLs are signed and expire
// about three hours after they are issued. That makes them impossible to put
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

	if size != sizeThumb && size != sizeFull {
		sendError(w, http.StatusBadRequest, "Unknown size",
			"Size must be "+sizeThumb+" or "+sizeFull)
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
	derivative := full
	if size == sizeThumb {
		derivative = thumb
	}
	if derivative.URL == nil {
		sendError(w, http.StatusNotFound, "No URL", "The derivative has no resolved URL")
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

	s.streamImage(w, r, *derivative.URL, album, guid)
}

// streamImage copies the upstream asset to the client, forwarding Range so
// that seeking within a video keeps working.
func (s *server) streamImage(w http.ResponseWriter, r *http.Request, url, album, guid string) {
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, url, nil)
	if err != nil {
		log.Printf("image %s/%s: building request: %v", album, guid, err)
		sendError(w, http.StatusInternalServerError, "Request failed", "Could not build the upstream request")
		return
	}
	if rng := r.Header.Get("Range"); rng != "" {
		req.Header.Set("Range", rng)
	}

	resp, err := s.upstream.Do(req)
	if err != nil {
		log.Printf("image %s/%s: fetching asset: %v", album, guid, err)
		sendError(w, http.StatusBadGateway, "Upstream fetch failed", "The image could not be fetched")
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		log.Printf("image %s/%s: upstream returned %s", album, guid, resp.Status)
		sendError(w, http.StatusBadGateway, "Upstream fetch failed", "The image could not be fetched")
		return
	}

	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "image/jpeg"
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

	// A copy failure here is almost always the client going away mid-image;
	// the status line is already sent, so there is nothing to do but note it.
	if _, err := io.Copy(w, resp.Body); err != nil {
		log.Printf("image %s/%s: streaming to client: %v", album, guid, err)
	}
}

func findPhoto(photos []icloudalbum.Image, guid string) (icloudalbum.Image, bool) {
	for _, photo := range photos {
		if photo.PhotoGUID == guid {
			return photo, true
		}
	}
	return icloudalbum.Image{}, false
}
