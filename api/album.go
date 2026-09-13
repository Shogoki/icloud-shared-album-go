package main

import (
	"encoding/json"
	"log"
	"net/http"
	"sort"

	icloudalbum "github.com/Shogoki/icloud-shared-album-go"
	"github.com/gorilla/mux"
)

// ImageResponse is one photo as the album endpoint reports it.
//
// PhotoGUID, the dimensions and AssetType exist so a static site generator can
// build markup ahead of time: the GUID addresses the image proxy, whose URLs do
// not expire, and the dimensions let a page reserve the right box before the
// image arrives. FullImageURL and ThumbnailURL remain the signed iCloud URLs
// and stop working roughly three hours after this response is produced.
type ImageResponse struct {
	PhotoGUID    string `json:"photoGuid"`
	Caption      string `json:"caption"`
	FullImageURL string `json:"fullImageUrl"`
	ThumbnailURL string `json:"thumbnailUrl"`
	AssetType    string `json:"assetType"`
	Width        int    `json:"width"`
	Height       int    `json:"height"`
	ThumbWidth   int    `json:"thumbWidth"`
	ThumbHeight  int    `json:"thumbHeight"`
}

// ErrorResponse represents error response structure
type ErrorResponse struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

// pickDerivatives returns a photo's smallest and largest derivative. iCloud
// ships two per photo — a 342px preview and the full-size original — but the
// set is not guaranteed, so both are chosen by file size rather than by key.
//
// Keys are visited in sorted order so that equal file sizes resolve the same
// way on every call. Map iteration order would otherwise make the choice, and
// with it the proxy's ETag, vary between requests.
func pickDerivatives(photo icloudalbum.Image) (thumb, full icloudalbum.Derivative, ok bool) {
	keys := make([]string, 0, len(photo.Derivatives))
	for key := range photo.Derivatives {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	for i, key := range keys {
		d := photo.Derivatives[key]
		if i == 0 {
			thumb, full = d, d
			continue
		}
		if d.FileSize < thumb.FileSize {
			thumb = d
		}
		if d.FileSize > full.FileSize {
			full = d
		}
	}
	return thumb, full, len(keys) > 0
}

// sortPhotos orders photos oldest first, the order an album reads in.
//
// The GUID tiebreak keeps the result stable for photos sharing a timestamp,
// which matters because a build step turns this order into committed markup;
// without it, identical data would produce a different file on every run.
func sortPhotos(photos []icloudalbum.Image) []icloudalbum.Image {
	sorted := make([]icloudalbum.Image, len(photos))
	copy(sorted, photos)
	sort.SliceStable(sorted, func(i, j int) bool {
		if !sorted[i].DateCreated.Equal(sorted[j].DateCreated) {
			return sorted[i].DateCreated.Before(sorted[j].DateCreated)
		}
		return sorted[i].PhotoGUID < sorted[j].PhotoGUID
	})
	return sorted
}

// toImageResponses maps resolved photos into the wire format, oldest first.
func toImageResponses(photos []icloudalbum.Image) []ImageResponse {
	sorted := sortPhotos(photos)
	out := make([]ImageResponse, 0, len(sorted))

	for _, photo := range sorted {
		thumb, full, ok := pickDerivatives(photo)
		if !ok {
			// No derivative means nothing to show; skip rather than emit an
			// entry whose URLs are both empty.
			continue
		}

		assetType := "image"
		if photo.MediaAssetType != nil && *photo.MediaAssetType == "video" {
			assetType = "video"
		}

		out = append(out, ImageResponse{
			PhotoGUID:    photo.PhotoGUID,
			Caption:      photo.Caption,
			FullImageURL: derefURL(full.URL),
			ThumbnailURL: derefURL(thumb.URL),
			AssetType:    assetType,
			Width:        full.Width,
			Height:       full.Height,
			ThumbWidth:   thumb.Width,
			ThumbHeight:  thumb.Height,
		})
	}

	return out
}

func derefURL(u *string) string {
	if u == nil {
		return ""
	}
	return *u
}

func (s *server) getAlbumHandler(w http.ResponseWriter, r *http.Request) {
	key := mux.Vars(r)["key"]
	if key == "" {
		sendError(w, http.StatusBadRequest, "Missing album key", "Album key is required")
		return
	}

	response, err := s.albums.get(key)
	if err != nil {
		log.Printf("album %s: %v", key, err)
		sendError(w, http.StatusInternalServerError, "Failed to fetch album", err.Error())
		return
	}

	if len(response.Photos) == 0 {
		sendError(w, http.StatusNotFound, "Album is empty", "No photos were returned for this album")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	// The signed URLs in this payload outlive the response by about three
	// hours, so a short shared cache is safe and spares iCloud the repeat.
	w.Header().Set("Cache-Control", "public, max-age=900")
	if err := json.NewEncoder(w).Encode(toImageResponses(response.Photos)); err != nil {
		log.Printf("album %s: encoding response: %v", key, err)
	}
}

func sendError(w http.ResponseWriter, statusCode int, error string, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(ErrorResponse{Error: error, Message: message})
}
