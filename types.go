package icloudalbum

import "time"

// Derivative represents a single image derivative with its properties
type Derivative struct {
	Checksum string  `json:"checksum"`
	FileSize int64   `json:"fileSize"`
	Width    int     `json:"width"`
	Height   int     `json:"height"`
	URL      *string `json:"url,omitempty"`
}

// Image represents a single image in the album with its metadata
type Image struct {
	BatchGUID           string                `json:"batchGuid"`
	Derivatives        map[string]Derivative `json:"derivatives"`
	ContributorLastName string               `json:"contributorLastName"`
	BatchDateCreated   time.Time            `json:"batchDateCreated"`
	DateCreated        time.Time            `json:"dateCreated"`
	ContributorFirstName string             `json:"contributorFirstName"`
	PhotoGUID          string               `json:"photoGuid"`
	ContributorFullName string              `json:"contributorFullName"`
	Caption            string               `json:"caption"`
	Height             int                  `json:"height"`
	Width              int                  `json:"width"`
	MediaAssetType     *string             `json:"mediaAssetType,omitempty"`
}

// Metadata contains album metadata
type Metadata struct {
	StreamName     string      `json:"streamName"`
	UserFirstName  string      `json:"userFirstName"`
	UserLastName   string      `json:"userLastName"`
	StreamCtag     string      `json:"streamCtag"`
	ItemsReturned  int         `json:"itemsReturned"`
	Locations      interface{} `json:"locations"`
}

// APIResponse represents the raw response from the iCloud API
type APIResponse struct {
	Photos     map[string]Image `json:"photos"`
	PhotoGUIDs []string        `json:"photoGuids"`
	Metadata   Metadata        `json:"metadata"`
}

// Response represents the final processed response
type Response struct {
	Metadata Metadata `json:"metadata"`
	Photos   []Image  `json:"photos"`
}
