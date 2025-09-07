package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"sort"
	"strconv"

	"github.com/gorilla/mux"
	"github.com/rs/cors"
	icloudalbum "github.com/Shogoki/icloud-shared-album-go"
)

// ImageResponse represents the simplified photo response structure
type ImageResponse struct {
	Caption      string `json:"caption"`
	FullImageURL string `json:"fullImageUrl"`
	ThumbnailURL string `json:"thumbnailUrl"`
	AssetType    string `json:"assetType"`
}

// ErrorResponse represents error response structure
type ErrorResponse struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

func main() {
	// Get port from environment or default to 8000
	portStr := os.Getenv("PORT")
	if portStr == "" {
		portStr = "8000"
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		log.Fatal("Invalid PORT environment variable:", err)
	}

	// Create router
	r := mux.NewRouter()

	// Add album endpoint
	r.HandleFunc("/album/{key}", getAlbumHandler).Methods("GET")

	// Setup CORS - matching the origins from the TypeScript version
	c := cors.New(cors.Options{
		AllowedOrigins: []string{
			"http://localhost:1313",
			"https://travel.igl-web.de", 
			"https://traveldev.igl-web.de",
		},
		AllowedMethods: []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders: []string{"*"},
	})

	// Apply CORS middleware
	handler := c.Handler(r)

	fmt.Printf("Listening on: localhost:%d\n", port)
	log.Fatal(http.ListenAndServe(fmt.Sprintf(":%d", port), handler))
}

func getAlbumHandler(w http.ResponseWriter, r *http.Request) {
	// Extract the key from URL parameters
	vars := mux.Vars(r)
	key := vars["key"]

	if key == "" {
		log.Printf("DEBUG: Missing album key in request")
		sendError(w, http.StatusBadRequest, "Missing album key", "Album key is required")
		return
	}

	log.Printf("DEBUG: Requesting album with key: %s", key)

	// Create iCloud client and fetch images
	client := icloudalbum.NewClient()
	log.Printf("DEBUG: Created iCloud client, calling GetImages...")

	response, err := client.GetImages(key)
	if err != nil {
		log.Printf("DEBUG: GetImages returned ERROR: %v", err)
		sendError(w, http.StatusInternalServerError, "Failed to fetch album", err.Error())
		return
	}

	log.Printf("DEBUG: GetImages completed successfully")
	log.Printf("DEBUG: Response metadata - StreamName: %s, UserFirstName: %s, ItemsReturned: %d",
		response.Metadata.StreamName, response.Metadata.UserFirstName, response.Metadata.ItemsReturned)

	// Check if no photos were found
	if response.Photos == nil {
		log.Printf("DEBUG: Response.Photos is nil")
		w.WriteHeader(http.StatusNotFound)
		return
	}

	if len(response.Photos) == 0 {
		log.Printf("DEBUG: Response.Photos is empty (length 0)")
		w.WriteHeader(http.StatusNotFound)
		return
	}

	log.Printf("DEBUG: Found %d photos in response", len(response.Photos))

	// Convert photos to ImageResponse format
	imageResponses := make([]ImageResponse, 0, len(response.Photos))
	
	for _, photo := range response.Photos {
		// Find the full size image (largest file size)
		var fullImage *icloudalbum.Derivative
		for _, derivative := range photo.Derivatives {
			if fullImage == nil || derivative.FileSize > fullImage.FileSize {
				fullImage = &derivative
			}
		}

		// Find the thumbnail (smallest file size)
		var thumbnail *icloudalbum.Derivative
		for _, derivative := range photo.Derivatives {
			if thumbnail == nil || derivative.FileSize < thumbnail.FileSize {
				thumbnail = &derivative
			}
		}

		// Determine asset type
		assetType := "image"
		if photo.MediaAssetType != nil && *photo.MediaAssetType == "video" {
			assetType = "video"
		}

		// Get URLs, defaulting to empty string if not available
		fullImageURL := ""
		if fullImage != nil && fullImage.URL != nil {
			fullImageURL = *fullImage.URL
		}

		thumbnailURL := ""
		if thumbnail != nil && thumbnail.URL != nil {
			thumbnailURL = *thumbnail.URL
		}

		imageResponse := ImageResponse{
			Caption:      photo.Caption,
			FullImageURL: fullImageURL,
			ThumbnailURL: thumbnailURL,
			AssetType:    assetType,
		}

		imageResponses = append(imageResponses, imageResponse)
	}

	// Sort by date created (ascending, like the TypeScript version)
	sort.Slice(imageResponses, func(i, j int) bool {
		return response.Photos[i].DateCreated.Before(response.Photos[j].DateCreated)
	})

	// Send JSON response
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(imageResponses); err != nil {
		log.Printf("Error encoding JSON response: %v", err)
		sendError(w, http.StatusInternalServerError, "Failed to encode response", err.Error())
		return
	}

	log.Printf("Successfully served %d photos for album key: %s", len(imageResponses), key)
}

func sendError(w http.ResponseWriter, statusCode int, error string, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	
	errorResponse := ErrorResponse{
		Error:   error,
		Message: message,
	}
	
	json.NewEncoder(w).Encode(errorResponse)
}
