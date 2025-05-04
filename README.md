# Go iCloud Album

A Go implementation for fetching and processing iCloud Shared Album data.
Heavily inspired by https://github.com/ghostops/ICloud-Shared-Album

## Installation

```bash
go get github.com/Shogoki/icloud-shared-album-go
```

## Usage

```go
package main

import (
    "fmt"
    icloudalbum "github.com/Shogoki/icloud-shared-album-go"
)

func main() {
    // Create a new client
    client := icloudalbum.NewClient()

    // Get images from the shared album
    // The token is the unique identifier in the shared album URL
    response, err := client.GetImages("your-album-token")
    if err != nil {
        panic(err)
    }

    // Process the images
    for _, photo := range response.Photos {
        fmt.Printf("Photo: %s\n", photo.PhotoGUID)
        for key, derivative := range photo.Derivatives {
            if derivative.URL != nil {
                fmt.Printf("  %s: %s\n", key, *derivative.URL)
            }
        }
    }
}
```

## Features

- Fetches shared album metadata and images
- Handles Apple's 2024 redirect changes
- Processes images in chunks to avoid overwhelming the API
- Provides strongly typed responses
- Enriches images with their respective URLs

## Types

The package provides several types to work with the iCloud Shared Album API:

- `Client`: The main client for interacting with the API
- `Response`: The final processed response containing metadata and photos
- `Image`: Represents a single image with its metadata and derivatives
- `Derivative`: Contains information about different versions of an image
- `Metadata`: Contains album metadata

## Example

See the `example` directory for a complete working example.
