# Build context is this `api/` directory. The API used to be built from the
# repository root because go.mod carried a `replace` pointing at the parent
# module; it now depends on the published github.com/Shogoki/icloud-shared-album-go
# release instead, so the parent tree is no longer needed in the build.

# ---- build stage ----------------------------------------------------------
FROM golang:1.25-alpine AS build

WORKDIR /src

# Download dependencies first so this layer caches independently of source edits.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Static binary — the runtime image only needs CA certificates for the outbound
# HTTPS calls to iCloud.
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" \
    -o /out/icloud-api-go .

# ---- runtime stage --------------------------------------------------------
FROM alpine:3

RUN apk add --no-cache ca-certificates \
    && adduser -D -u 10001 app

COPY --from=build /out/icloud-api-go /usr/local/bin/icloud-api-go

USER app
WORKDIR /app

# Listen on 80 so a containerised reverse proxy can reach the app directly over
# a shared Docker network, with no host port mapping. Binding a low port works
# as a non-root user because Docker defaults to
# net.ipv4.ip_unprivileged_port_start=0.
EXPOSE 80
ENV PORT=80

# Hits /health, not /: the router only serves /album/{key} and /health, so /
# returns 404 and would keep the container permanently unhealthy. /health is
# also dependency-free, so an iCloud outage does not trigger restarts.
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD wget -qO- "http://127.0.0.1:${PORT}/health" >/dev/null 2>&1 || exit 1

ENTRYPOINT ["icloud-api-go"]
