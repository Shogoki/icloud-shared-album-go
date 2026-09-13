package main

import (
	"os"
	"testing"
	"time"
)

func TestConfiguredOrigins(t *testing.T) {
	tests := []struct {
		name string
		env  string
		want []string
	}{
		{"unset", "", nil},
		{"single", "https://example.com", []string{"https://example.com"}},
		{"list is trimmed", " https://a.example , https://b.example ",
			[]string{"https://a.example", "https://b.example"}},
		{"empty entries are dropped", "https://a.example,,", []string{"https://a.example"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("CORS_ALLOWED_ORIGINS", tt.env)
			got := configuredOrigins()
			if len(got) != len(tt.want) {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("origin %d = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestOriginAllowed(t *testing.T) {
	allowed := originAllowed([]string{"https://travel.example.com"})

	tests := []struct {
		origin string
		want   bool
	}{
		{"https://travel.example.com", true},
		{"https://not-configured.example.com", false},
		// Loopback is always allowed so a local `hugo server` can reach a
		// deployment whose configured origin is the production site.
		{"http://localhost:1313", true},
		{"http://localhost:1414", true},
		{"http://127.0.0.1:1313", true},
		{"http://localhost", true},
		// https loopback is not a thing Hugo serves, and matching it would
		// widen the rule for no benefit.
		{"https://localhost:1313", false},
		// Must not match a hostname that merely starts with the loopback name.
		{"http://localhost.evil.example.com", false},
		{"http://127.0.0.1.evil.example.com", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := allowed(tt.origin); got != tt.want {
			t.Errorf("originAllowed(%q) = %v, want %v", tt.origin, got, tt.want)
		}
	}
}

func TestAlbumTTL(t *testing.T) {
	tests := []struct {
		name string
		env  string
		want time.Duration
	}{
		{"unset falls back to the default", "", defaultAlbumTTL},
		{"valid value is used", "300", 5 * time.Minute},
		{"garbage falls back", "not-a-number", defaultAlbumTTL},
		{"zero falls back", "0", defaultAlbumTTL},
		{"negative falls back", "-60", defaultAlbumTTL},
		// The signed URLs this cache hands out live about three hours, so a
		// TTL near that would serve URLs that expire in the reader's browser.
		{"too close to the signed-URL expiry falls back", "10800", defaultAlbumTTL},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.env == "" {
				os.Unsetenv("ALBUM_CACHE_TTL_SECONDS")
			} else {
				t.Setenv("ALBUM_CACHE_TTL_SECONDS", tt.env)
			}
			if got := albumTTL(); got != tt.want {
				t.Errorf("albumTTL() = %s, want %s", got, tt.want)
			}
		})
	}
}
