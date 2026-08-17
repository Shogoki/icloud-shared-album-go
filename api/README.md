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

### GET /health

Liveness probe used by the container healthcheck. Always returns `200` with
`{"status":"ok"}` and never calls iCloud, so an upstream outage does not mark
the container unhealthy.

```bash
curl "http://localhost:8000/health"
```

## Configuration

### Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `PORT` | `8000` | Port number for the API server |
| `CORS_ALLOWED_ORIGINS` | `http://localhost:1313` | Comma-separated list of browser origins allowed to call the API |

See `.env.example` for a template.

### CORS Configuration

Allowed origins come from `CORS_ALLOWED_ORIGINS` at runtime rather than being
compiled in, because this repository — and the container image built from it —
are public. The production origins live only in the Dokploy application's
environment.

```bash
CORS_ALLOWED_ORIGINS=https://example.com,https://dev.example.com
```

When the variable is unset the API allows `http://localhost:1313` only. It
fails closed: an unconfigured deployment rejects browser callers rather than
accepting every origin.

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
├── docker-compose.yml # Local development only (production runs on Dokploy)
├── .env.example       # Template for local environment variables
├── .dockerignore      # Docker build exclusions
└── README.md          # This file
```

### Dependencies

- **Gorilla Mux**: HTTP router for RESTful routes
- **rs/cors**: CORS middleware for cross-origin requests
- **icloud-shared-album-go**: Core library for iCloud album access

## Deployment

Deployment is continuous: pushing to `main` builds the image, publishes it to
GHCR and asks Dokploy to redeploy. See `.github/workflows/deploy.yml`.

```
push to main ──▶ go vet / go test ──▶ build image ──▶ ghcr.io ──▶ Dokploy pulls
```

Images are published as `ghcr.io/shogoki/icloud-shared-album-go-api`, tagged
`latest` (what Dokploy pulls) plus immutable `sha-<commit>` and semver tags for
rollbacks. Tagging a release `v*` runs the same pipeline and adds version tags.

### Configuration split

This repository is public, so nothing environment-specific is committed:

| Value | Lives in |
|---|---|
| `DOKPLOY_URL`, `DOKPLOY_API_KEY`, `DOKPLOY_APP_ID` | GitHub Actions secrets |
| API domain | Dokploy application domain |
| `CORS_ALLOWED_ORIGINS`, `PORT` | Dokploy application environment |

The domain and origins are needed only at runtime, never at build time, so they
appear in neither the workflow logs nor the published image.

Until the three secrets are set the deploy step skips with a message and the
image build still runs.

### Docker Production

The Dockerfile uses a multi-stage build; the build context is this directory.

1. **Builder stage**: Downloads dependencies and compiles a static Go binary
2. **Runtime stage**: Minimal Alpine image with the binary, CA certificates and
   a non-root user

```bash
docker build -t icloud-api-go:dev .
docker run --rm -p 8000:80 -e PORT=80 icloud-api-go:dev
```

The image declares a `HEALTHCHECK` against `/health`.

### Docker Compose

`docker-compose.yml` is for local development only — production runs on Dokploy,
which pulls the published image directly.

```bash
cp .env.example .env
docker-compose up --build
```

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
