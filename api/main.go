package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	icloudalbum "github.com/Shogoki/icloud-shared-album-go"
	"github.com/gorilla/mux"
	"github.com/rs/cors"
)

// server holds what the handlers share: the album cache in front of iCloud and
// the HTTP client the image proxy streams through.
type server struct {
	albums   *albumCache
	upstream *http.Client
}

func main() {
	port := 8000
	if raw := os.Getenv("PORT"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			log.Fatalf("invalid PORT %q", raw)
		}
		port = n
	}

	client := icloudalbum.NewClient()
	// The library's protocol trace is opt-in and stays off here: it prints
	// every signed asset URL, which does not belong in a production log.
	srv := &server{
		albums:   newAlbumCache(albumTTL(), client.GetImages),
		upstream: &http.Client{Timeout: 60 * time.Second},
	}

	addr := ":" + strconv.Itoa(port)
	log.Printf("listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, srv.routes(originAllowed(configuredOrigins()))))
}

// routes builds the request handler. It is separate from main so tests can
// exercise the real wiring — in particular which routes the CORS middleware
// does and does not wrap, which is load-bearing rather than cosmetic.
func (s *server) routes(allowOrigin func(string) bool) http.Handler {
	r := mux.NewRouter()

	// CORS goes on /album only. It is read with fetch(), so a browser needs
	// the header; /img is loaded as <img src>, which is not subject to CORS at
	// all. That distinction matters for more than tidiness: the CORS
	// middleware stamps "Vary: Origin" on whatever it wraps, and a CDN will
	// not cache a response that varies on anything but Accept-Encoding. With
	// the middleware around the whole router, every proxied image came back
	// cf-cache-status: DYNAMIC and was re-fetched from iCloud on every view.
	albumCORS := cors.New(cors.Options{
		AllowOriginFunc: allowOrigin,
		AllowedMethods:  []string{http.MethodGet, http.MethodHead, http.MethodOptions},
		AllowedHeaders:  []string{"*"},
	})
	r.Handle("/album/{key}", albumCORS.Handler(http.HandlerFunc(s.getAlbumHandler))).
		Methods(http.MethodGet, http.MethodOptions)

	// HEAD is served too so a cache can revalidate an image without a body.
	r.HandleFunc("/img/{album}/{guid}/{size}", s.getImageHandler).
		Methods(http.MethodGet, http.MethodHead)
	// Liveness probe for the container healthcheck and the reverse proxy.
	r.HandleFunc("/health", healthHandler).Methods(http.MethodGet)

	return r
}

// albumTTL reads ALBUM_CACHE_TTL_SECONDS, which must stay below the roughly
// three-hour life of iCloud's signed URLs so the album endpoint never hands
// out a URL that is about to stop working.
func albumTTL() time.Duration {
	raw := os.Getenv("ALBUM_CACHE_TTL_SECONDS")
	if raw == "" {
		return defaultAlbumTTL
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		log.Printf("ignoring invalid ALBUM_CACHE_TTL_SECONDS %q", raw)
		return defaultAlbumTTL
	}
	ttl := time.Duration(n) * time.Second
	if ttl > 2*time.Hour {
		log.Printf("ALBUM_CACHE_TTL_SECONDS %d is too close to the signed-URL expiry; using %s", n, defaultAlbumTTL)
		return defaultAlbumTTL
	}
	return ttl
}

// configuredOrigins reads the deployment's browser origins from
// CORS_ALLOWED_ORIGINS, a comma-separated list. The real origins are supplied
// at runtime so they stay out of this public repo and out of the published
// image.
func configuredOrigins() []string {
	raw := os.Getenv("CORS_ALLOWED_ORIGINS")
	origins := make([]string, 0, strings.Count(raw, ",")+1)
	for _, origin := range strings.Split(raw, ",") {
		if origin = strings.TrimSpace(origin); origin != "" {
			origins = append(origins, origin)
		}
	}
	if len(origins) > 0 {
		log.Printf("CORS: allowing %d configured origin(s)", len(origins))
	}
	return origins
}

// originAllowed permits the configured origins plus any loopback origin.
//
// Loopback is always allowed because the alternative is worse in practice: a
// deployment that sets CORS_ALLOWED_ORIGINS to its production site silently
// breaks every local `hugo server`, and the failure surfaces only as a
// "Failed to fetch" in the browser console. Nothing here is protected by the
// origin check — the albums are public and the API is read-only — so the check
// only governs which sites may spend this server's bandwidth, and a
// developer's own machine is not a concern there.
func originAllowed(configured []string) func(string) bool {
	allowed := make(map[string]struct{}, len(configured))
	for _, origin := range configured {
		allowed[origin] = struct{}{}
	}

	return func(origin string) bool {
		if _, ok := allowed[origin]; ok {
			return true
		}
		return isLoopbackOrigin(origin)
	}
}

func isLoopbackOrigin(origin string) bool {
	host, ok := strings.CutPrefix(origin, "http://")
	if !ok {
		return false
	}
	// Strip the port, which may be anything: Hugo's default is 1313 but it
	// moves whenever that port is already taken.
	if i := strings.LastIndex(host, ":"); i != -1 {
		host = host[:i]
	}
	return host == "localhost" || host == "127.0.0.1" || host == "[::1]"
}

// healthHandler backs the container healthcheck. It must stay dependency-free:
// the album endpoint talks to iCloud, and an outage there should not make the
// container look unhealthy and get restarted.
func healthHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}
