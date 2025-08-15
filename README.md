# Go iCloud Shared Album

A Go implementation for fetching and processing iCloud Shared Album data.
Heavily inspired by https://github.com/ghostops/ICloud-Shared-Album

This repository contains:
- **Go Module**: Core library for iCloud Shared Album access (`icloud.go`, `types.go`)
- **REST API**: HTTP API server for easy web integration (`./api/`)
- **Example**: Command-line usage example (`./example/`)

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

## REST API

A complete REST API server is available in the `./api/` directory, providing HTTP endpoints for easy web integration.

**Quick Start:**
```bash
cd api
go run main.go
# API available at http://localhost:8000
```

**Example Request:**
```bash
curl "http://localhost:8000/album/B19Gtec4X8nCmDH"
```

**Docker Support:**
```bash
cd api
make docker-build
make docker-run
```

📖 **[See complete API documentation →](./api/README.md)**

## Example

See the `example` directory for a complete working example of the Go module usage.
