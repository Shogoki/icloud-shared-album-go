# iCloud Shared Album Go REST API

A REST API server built in Go that provides easy access to iCloud Shared Albums. This API mirrors the functionality of the Deno TypeScript API and provides a simple HTTP interface for fetching shared album photos.

## Features

- ✅ **REST API** endpoint for fetching album photos
- ✅ **CORS enabled** for web application integration  
- ✅ **Simplified response format** with caption, URLs, and asset type
- ✅ **Automatic sorting** by date created
- ✅ **Docker support** with multi-stage builds
- ✅ **Production ready** with proper error handling

## Quick Start

### Local Development

```bash
# Run the API server locally
go run main.go

# Or use the Makefile
make start
make dev
```

The API will be available at `http://localhost:8000`

### Docker

```bash
# Build the Docker image
make docker-build

# Run with Docker
make docker-run

# Or with docker-compose
docker-compose up --build
```

## API Endpoints

### GET /album/:key

Fetches photos from an iCloud shared album.

**Parameters:**
- `key` (path parameter): The album token from the iCloud shared album URL

**Response:**
```json
[
  {
    "caption": "Photo caption",
    "fullImageUrl": "https://cvws.icloud-content.com/.../full-image.JPG",
    "thumbnailUrl": "https://cvws.icloud-content.com/.../thumbnail.JPG", 
    "assetType": "image"
  }
]
```

**Status Codes:**
- `200 OK`: Photos found and returned
- `404 Not Found`: No photos found in the album
- `400 Bad Request`: Missing or invalid album key
- `500 Internal Server Error`: Server error during processing

**Example:**
```bash
curl "http://localhost:8000/album/B19Gtec4X8nCmDH"
```

## Configuration

### Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `PORT` | `8000` | Port number for the API server |

### CORS Configuration

The API is configured to allow requests from:
- `http://localhost:1313`
- `https://travel.igl-web.de` 
- `https://traveldev.igl-web.de`

## Response Format

The API returns a simplified format compared to the full iCloud API response:

- **`caption`**: Photo caption/description
- **`fullImageUrl`**: URL to the full-size image  
- **`thumbnailUrl`**: URL to the thumbnail image
- **`assetType`**: Either "image" or "video"

Photos are automatically sorted by date created (ascending).

## Development

### Build Commands

```bash
# Local development
make start          # Run the server
make dev           # Run in development mode

# Building
make build         # Build binary
make clean         # Clean up binary

# Docker
make docker-build  # Build Docker image
make docker-run    # Run Docker container
```

### Project Structure

```
api/
├── main.go              # Main API server code
├── go.mod              # Go module dependencies
├── Makefile           # Build and development commands
├── Dockerfile         # Docker image configuration
├── docker-compose.yml # Docker Compose configuration
├── .dockerignore      # Docker build exclusions
└── README.md          # This file
```

### Dependencies

- **Gorilla Mux**: HTTP router for RESTful routes
- **rs/cors**: CORS middleware for cross-origin requests
- **icloud-shared-album-go**: Core library for iCloud album access

## Deployment

### Docker Production

The included Dockerfile uses multi-stage builds for optimal production images:

1. **Builder stage**: Downloads dependencies and compiles the Go binary
2. **Runtime stage**: Minimal Alpine Linux image with just the binary and CA certificates

```bash
# Build and run production container
docker build -t shogoki/icloud-api-go -f Dockerfile ..
docker run -p 8000:80 -e PORT=80 shogoki/icloud-api-go
```

### Docker Compose

For deployment with external networks (e.g., reverse proxy):

```bash
docker-compose up -d
```

This connects the API to the `webproxy` external network for integration with reverse proxy setups.

## Error Handling

The API provides proper HTTP status codes and JSON error responses:

```json
{
  "error": "Failed to fetch album",
  "message": "Detailed error description"
}
```

## Security

- **CORS**: Configured for specific allowed origins
- **No sensitive data exposure**: Only returns processed photo URLs and metadata
- **Minimal attack surface**: Stateless API with no data persistence

## Troubleshooting

### Common Issues

1. **Port already in use**: Change the `PORT` environment variable
2. **CORS errors**: Add your domain to the allowed origins list in `main.go`
3. **Album not found**: Verify the album token is correct and the album is accessible

### Logging

The API provides detailed console logging for:
- Request processing
- Album fetching progress  
- URL enrichment status
- Error conditions

## License

This project uses the same license as the parent icloud-shared-album-go module.
