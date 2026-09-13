# iCloud Shared Album Go REST API

A REST API server built in Go that provides easy access to iCloud Shared Albums. This API mirrors the functionality of the Deno TypeScript API and provides a simple HTTP interface for fetching shared album photos.

## Features

- ✅ **Album endpoint** returning a shared album's photos as JSON
- ✅ **Image proxy** with stable, non-expiring URLs a static site can embed
- ✅ **Album caching** so a page of images costs one iCloud lookup, not one per image
- ✅ **CORS** for web application integration, loopback always allowed for local development
- ✅ **Chronological ordering** by date created
- ✅ **Docker support** with multi-stage builds

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
    "photoGuid": "CF778672-3802-48BD-BE8C-9314656F065C",
    "caption": "Photo caption",
    "fullImageUrl": "https://cvws.icloud-content.com/.../full-image.JPG",
    "thumbnailUrl": "https://cvws.icloud-content.com/.../thumbnail.JPG",
    "assetType": "image",
    "width": 2049,
    "height": 1536,
    "thumbWidth": 342,
    "thumbHeight": 257
  }
]
```

> **`fullImageUrl` and `thumbnailUrl` expire.** They are signed by iCloud and
> stop working roughly three hours after the response is produced, so they
> cannot be baked into a static page. Use `photoGuid` with the image proxy
> below for anything that outlives that window.

Responses carry `Cache-Control: public, max-age=900`.

**Status Codes:**
- `200 OK`: Photos found and returned
- `404 Not Found`: No photos found in the album
- `400 Bad Request`: Missing or invalid album key
- `500 Internal Server Error`: Server error during processing

**Example:**
```bash
curl "http://localhost:8000/album/B19Gtec4X8nCmDH"
```

### GET /img/:album/:photoGuid/:size

Streams one image. `size` is `thumb` (iCloud's ~342px preview) or `full` (the
original).

Unlike the signed URLs above, **this URL never expires** — which is what makes
it usable from static markup generated ahead of time:

```html
<img src="https://api.example.com/img/B2R5.../CF778672-.../thumb"
     width="342" height="257" loading="lazy" alt="…">
```

The proxy resolves the current signed URL server-side and streams the bytes
back. It can only ever fetch URLs iCloud itself returned for the requested
album, so it is not a general-purpose fetcher.

Responses carry `Cache-Control: public, max-age=2592000` and an `ETag` taken
from the derivative checksum, so a matching `If-None-Match` is answered with a
`304` without touching iCloud. `Range` is forwarded upstream so seeking within
a video works.

**Status Codes:**
- `200 OK` / `206 Partial Content`: image streamed
- `304 Not Modified`: the client's `ETag` still matches
- `400 Bad Request`: `size` is neither `thumb` nor `full`
- `404 Not Found`: no such photo in that album, or it has no usable derivative
- `502 Bad Gateway`: the album or the image could not be fetched from iCloud

```bash
curl "http://localhost:8000/img/B19Gtec4X8nCmDH/<photoGuid>/thumb" -o photo.jpg
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
| `CORS_ALLOWED_ORIGINS` | *(none)* | Comma-separated list of browser origins allowed to call the API. Loopback origins are always allowed on top of these. |
| `ALBUM_CACHE_TTL_SECONDS` | `3600` | How long a resolved album is reused. Capped at 2h, because the signed URLs it holds expire after about 3h. |

See `.env.example` for a template.

### CORS Configuration

Allowed origins come from `CORS_ALLOWED_ORIGINS` at runtime rather than being
compiled in, because this repository — and the container image built from it —
are public. The production origins live only in the Dokploy application's
environment.

```bash
CORS_ALLOWED_ORIGINS=https://example.com,https://dev.example.com
```

Loopback origins (`http://localhost:*`, `http://127.0.0.1:*`, `http://[::1]:*`)
are **always** allowed, on top of whatever is configured. Without that, setting
`CORS_ALLOWED_ORIGINS` to the production site silently breaks every local
`hugo server`, and the only symptom is a `Failed to fetch` in the browser
console. Nothing here is protected by the origin check — the albums are public
and the API is read-only — so it governs only which sites may spend this
server's bandwidth.

Any non-loopback origin that is not configured is rejected.

## Response Format

The API returns a simplified format compared to the full iCloud API response:

- **`photoGuid`**: Stable identifier; addresses the image proxy
- **`caption`**: Photo caption/description
- **`fullImageUrl`**: Signed URL to the full-size image — **expires after ~3h**
- **`thumbnailUrl`**: Signed URL to the thumbnail — **expires after ~3h**
- **`assetType`**: Either "image" or "video"
- **`width`** / **`height`**: Full-size dimensions
- **`thumbWidth`** / **`thumbHeight`**: Thumbnail dimensions

Photos are sorted by date created, ascending, with a `photoGuid` tiebreak so
photos sharing a timestamp keep a stable order between calls — a generator that
commits this order to markup would otherwise see spurious diffs.

iCloud ships two derivatives per photo (a ~342px preview and the original), but
that is not guaranteed, so the thumbnail and full image are chosen as the
smallest and largest by file size.

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
├── main.go              # Wiring: config, router, CORS, health
├── album.go             # /album/{key} and the photo → JSON mapping
├── image.go             # /img/{album}/{guid}/{size} proxy
├── cache.go             # Album cache shared by both endpoints
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

- **CORS**: Configured for specific allowed origins, plus loopback
- **No sensitive data exposure**: Only returns processed photo URLs and metadata
- **Minimal attack surface**: Stateless API with no data persistence
- **Proxy is not an open fetcher**: `/img` can only reach URLs iCloud returned
  for the requested album; it never takes a URL from the caller
- **Signed URLs stay out of errors and logs**: upstream failures report a
  generic message rather than echoing the URL

## Troubleshooting

### Common Issues

1. **Port already in use**: Change the `PORT` environment variable
2. **CORS errors**: Add your domain to `CORS_ALLOWED_ORIGINS` in the deployment's environment. Local development needs no configuration — loopback is always allowed.
3. **Album not found**: Verify the album token is correct and the album is accessible

### Logging

The API logs startup, CORS configuration and errors. The library's verbose
protocol trace is **off**: it prints every signed asset URL, and those do not
belong in a production log. Turn it on while debugging by setting `Logf` on the
client in `main.go`:

```go
client := icloudalbum.NewClient()
client.Logf = log.Printf
```

## License

This project uses the same license as the parent icloud-shared-album-go module.
