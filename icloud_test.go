package icloudalbum

import (
	"strings"
	"testing"
	"time"
)

func TestBase62ToInt(t *testing.T) {
	tests := []struct {
		in   string
		want int
	}{
		{"0", 0},
		{"9", 9},
		{"A", 10},
		{"Z", 35},
		{"a", 36},
		{"z", 61},
		{"2R", 2*62 + 27},
		{"10", 62},
	}
	for _, tt := range tests {
		if got := base62ToInt(tt.in); got != tt.want {
			t.Errorf("base62ToInt(%q) = %d, want %d", tt.in, got, tt.want)
		}
	}
}

func TestGetBaseURL(t *testing.T) {
	tests := []struct {
		name  string
		token string
		want  string
	}{
		{
			// Two-character partition: "2R" is 151, which is the partition
			// these album URLs actually resolve to.
			name:  "two character partition",
			token: "B2R5MbK9V8BScyX",
			want:  "https://p151-sharedstreams.icloud.com/B2R5MbK9V8BScyX/sharedstreams",
		},
		{
			// Tokens starting with A carry a single-character partition.
			name:  "single character partition is zero padded",
			token: "A5abcdef",
			want:  "https://p05-sharedstreams.icloud.com/A5abcdef/sharedstreams",
		},
		{
			// Everything from the semicolon on is a fragment of the sharing
			// URL, not part of the token.
			name:  "semicolon suffix is stripped",
			token: "B2R5MbK9V8BScyX;extra",
			want:  "https://p151-sharedstreams.icloud.com/B2R5MbK9V8BScyX/sharedstreams",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := getBaseURL(tt.token); got != tt.want {
				t.Errorf("getBaseURL(%q)\n got %q\nwant %q", tt.token, got, tt.want)
			}
		})
	}
}

func TestParseDate(t *testing.T) {
	got := parseDate("2025-11-27T07:42:30Z")
	want := time.Date(2025, 11, 27, 7, 42, 30, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("parseDate = %v, want %v", got, want)
	}
	if got := parseDate("not a date"); !got.IsZero() {
		t.Errorf("parseDate of an unparseable value = %v, want the zero time", got)
	}
}

// A client must not write to the caller's stdout unless the caller asks for it:
// the trace carries signed asset URLs, which do not belong in a production log.
func TestLogfIsOptIn(t *testing.T) {
	c := NewClient()
	if c.Logf != nil {
		t.Fatal("NewClient set Logf; tracing must be off by default")
	}
	c.logf("this must not panic and must go nowhere: %s", "value")

	var sb strings.Builder
	c.Logf = func(format string, args ...any) {
		sb.WriteString(format)
	}
	c.logf("traced %s", "line")
	if sb.String() != "traced %s" {
		t.Errorf("Logf received %q, want the format string to be forwarded", sb.String())
	}
}
