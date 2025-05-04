package icloudalbum

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const chunkSize = 25

// Client represents an iCloud album client
type Client struct {
	httpClient *http.Client
}

// NewClient creates a new iCloud album client
func NewClient() *Client {
	return &Client{
		httpClient: &http.Client{
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

// GetImages retrieves images from an iCloud shared album
func (c *Client) GetImages(token string) (*Response, error) {
	baseURL := getBaseURL(token)
	
	// Handle potential redirects (added in 2024)
	redirectedBaseURL, err := c.getRedirectedBaseURL(baseURL, token)
	if err != nil {
		return nil, fmt.Errorf("getting redirected base URL: %w", err)
	}

	apiResponse, err := c.getAPIResponse(redirectedBaseURL)
	if err != nil {
		return nil, fmt.Errorf("getting API response: %w", err)
	}

	allURLs := make(map[string]string)
	for i := 0; i < len(apiResponse.PhotoGUIDs); i += chunkSize {
		end := i + chunkSize
		if end > len(apiResponse.PhotoGUIDs) {
			end = len(apiResponse.PhotoGUIDs)
		}
		chunk := apiResponse.PhotoGUIDs[i:end]

		urls, err := c.getURLs(redirectedBaseURL, chunk)
		if err != nil {
			return nil, fmt.Errorf("getting URLs for chunk: %w", err)
		}
		for k, v := range urls {
			allURLs[k] = v
		}
	}

	return &Response{
		Metadata: apiResponse.Metadata,
		Photos:   enrichImagesWithURLs(apiResponse, allURLs),
	}, nil
}

const base62CharSet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

func base62ToInt(s string) int {
	result := 0
	for i := 0; i < len(s); i++ {
		result = result*62 + strings.IndexByte(base62CharSet, s[i])
	}
	return result
}

func getBaseURL(token string) string {
	firstChar := token[0]
	var serverPartition int

	if firstChar == 'A' {
		serverPartition = base62ToInt(string(token[1]))
	} else {
		serverPartition = base62ToInt(token[1:3])
	}

	// Remove any part after semicolon if present
	semicolonIdx := strings.Index(token, ";")
	if semicolonIdx >= 0 {
		token = token[:semicolonIdx]
	}

	// Format server partition with leading zero if needed
	partitionStr := fmt.Sprintf("%02d", serverPartition)

	return fmt.Sprintf("https://p%s-sharedstreams.icloud.com/%s/sharedstreams", partitionStr, token)
}

func (c *Client) getRedirectedBaseURL(baseURL, token string) (string, error) {
	req, err := http.NewRequest(http.MethodGet, baseURL, nil)
	if err != nil {
		return "", err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusPermanentRedirect || resp.StatusCode == http.StatusTemporaryRedirect {
		location := resp.Header.Get("Location")
		if location != "" {
			return strings.TrimSuffix(location, "/"), nil
		}
	}

	return baseURL, nil
}

var defaultHeaders = map[string]string{
	"Origin":          "https://www.icloud.com",
	"Accept-Language": "en-US,en;q=0.8",
	"User-Agent":      "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_12_4) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/56.0.2924.87 Safari/537.36",
	"Content-Type":    "text/plain",
	"Accept":          "*/*",
	"Referer":         "https://www.icloud.com/sharedalbum/",
	"Connection":      "keep-alive",
}

type rawAPIResponse struct {
	Photos        []json.RawMessage `json:"photos"`
	StreamName    string           `json:"streamName"`
	UserFirstName string           `json:"userFirstName"`
	UserLastName  string           `json:"userLastName"`
	StreamCtag    string           `json:"streamCtag"`
	ItemsReturned string           `json:"itemsReturned"`
	Locations     interface{}      `json:"locations"`
}

type rawImage struct {
	BatchGUID            string                 `json:"batchGuid"`
	Derivatives         map[string]rawDerivative `json:"derivatives"`
	ContributorLastName  string                 `json:"contributorLastName"`
	BatchDateCreated    string                 `json:"batchDateCreated"`
	DateCreated         string                 `json:"dateCreated"`
	ContributorFirstName string                 `json:"contributorFirstName"`
	PhotoGUID           string                 `json:"photoGuid"`
	ContributorFullName  string                 `json:"contributorFullName"`
	Caption             string                 `json:"caption"`
	Height              string                 `json:"height"`
	Width               string                 `json:"width"`
	MediaAssetType      *string                `json:"mediaAssetType,omitempty"`
}

type rawDerivative struct {
	Checksum string `json:"checksum"`
	FileSize string `json:"fileSize"`
	Width    string `json:"width"`
	Height   string `json:"height"`
	URL      string `json:"url,omitempty"`
}

func parseDate(date string) time.Time {
	t, err := time.Parse(time.RFC3339, date)
	if err != nil {
		return time.Time{}
	}
	return t
}

func (c *Client) getAPIResponse(baseURL string) (*APIResponse, error) {
	url := fmt.Sprintf("%s/webstream", baseURL)
	fmt.Printf("Requesting URL: %s\n", url)

	payload := map[string]interface{}{
		"streamCtag": nil,
	}

	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshaling payload: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(payloadBytes))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	for key, value := range defaultHeaders {
		req.Header.Set(key, value)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("HTTP request failed: %w", err)
	}
	defer resp.Body.Close()

	fmt.Printf("Response Status: %s\n", resp.Status)

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response body: %w", err)
	}

	fmt.Printf("Response Body: %s\n", string(body))

	var raw rawAPIResponse
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("unmarshaling response: %w (body: %s)", err, string(body))
	}

	photos := make(map[string]Image)
	photoGUIDs := make([]string, 0, len(raw.Photos))

	for _, photoData := range raw.Photos {
		var rawPhoto rawImage
		if err := json.Unmarshal(photoData, &rawPhoto); err != nil {
			return nil, fmt.Errorf("unmarshaling photo: %w", err)
		}

		height, _ := strconv.Atoi(rawPhoto.Height)
		width, _ := strconv.Atoi(rawPhoto.Width)

		derivatives := make(map[string]Derivative)
		for key, rawDeriv := range rawPhoto.Derivatives {
			fileSize, _ := strconv.ParseInt(rawDeriv.FileSize, 10, 64)
			width, _ := strconv.Atoi(rawDeriv.Width)
			height, _ := strconv.Atoi(rawDeriv.Height)

			derivatives[key] = Derivative{
				Checksum: rawDeriv.Checksum,
				FileSize: fileSize,
				Width:    width,
				Height:   height,
			}
		}

		photo := Image{
			BatchGUID:           rawPhoto.BatchGUID,
			Derivatives:        derivatives,
			ContributorLastName: rawPhoto.ContributorLastName,
			BatchDateCreated:   parseDate(rawPhoto.BatchDateCreated),
			DateCreated:        parseDate(rawPhoto.DateCreated),
			ContributorFirstName: rawPhoto.ContributorFirstName,
			PhotoGUID:          rawPhoto.PhotoGUID,
			ContributorFullName: rawPhoto.ContributorFullName,
			Caption:            rawPhoto.Caption,
			Height:             height,
			Width:              width,
			MediaAssetType:     rawPhoto.MediaAssetType,
		}

		photos[photo.PhotoGUID] = photo
		photoGUIDs = append(photoGUIDs, photo.PhotoGUID)
	}

	itemsReturned, _ := strconv.Atoi(raw.ItemsReturned)

	return &APIResponse{
		Photos:     photos,
		PhotoGUIDs: photoGUIDs,
		Metadata: Metadata{
			StreamName:    raw.StreamName,
			UserFirstName: raw.UserFirstName,
			UserLastName:  raw.UserLastName,
			StreamCtag:    raw.StreamCtag,
			ItemsReturned: itemsReturned,
			Locations:     raw.Locations,
		},
	}, nil
}

type urlResponse struct {
	Items map[string]struct {
		URLLocation string `json:"url_location"`
		URLPath    string `json:"url_path"`
	} `json:"items"`
}

func (c *Client) getURLs(baseURL string, photoGUIDs []string) (map[string]string, error) {
	url := fmt.Sprintf("%s/webasseturls", baseURL)

	payload := map[string]interface{}{
		"photoGuids": photoGUIDs,
	}

	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshaling payload: %w", err)
	}

	// Convert to string and back to match TypeScript behavior
	payloadStr := string(payloadBytes)

	fmt.Printf("URL Request Payload: %s\n", payloadStr)

	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(payloadStr))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	for key, value := range defaultHeaders {
		req.Header.Set(key, value)
	}

	fmt.Printf("Requesting URLs from: %s\n", url)
	fmt.Printf("Requesting URLs for %d photos\n", len(photoGUIDs))
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("HTTP request failed: %w", err)
	}
	defer resp.Body.Close()

	fmt.Printf("URL Response Status: %s\n", resp.Status)

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response body: %w", err)
	}

	fmt.Printf("URL Response Body: %s\n", string(body))

	var response urlResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("unmarshaling response: %w (body: %s)", err, string(body))
	}

	urls := make(map[string]string)
	for itemID, item := range response.Items {
		url := fmt.Sprintf("https://%s%s", item.URLLocation, item.URLPath)
		urls[itemID] = url
		fmt.Printf("Generated URL for %s: %s\n", itemID, url)
	}

	return urls, nil
}

func enrichImagesWithURLs(apiResp *APIResponse, urls map[string]string) []Image {
	images := make([]Image, 0, len(apiResp.Photos))
	
	for _, photoGUID := range apiResp.PhotoGUIDs {
		if photo, ok := apiResp.Photos[photoGUID]; ok {
			for derivativeKey, derivative := range photo.Derivatives {
				urlKey := fmt.Sprintf("%s-%s", photoGUID, derivativeKey)
				if url, ok := urls[urlKey]; ok {
					derivative.URL = &url
					photo.Derivatives[derivativeKey] = derivative
				}
			}
			images = append(images, photo)
		}
	}

	return images
}
