package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadOrCreateTokenGeneratesAndPersists(t *testing.T) {
	dir := t.TempDir()

	first, err := LoadOrCreateToken(dir)
	if err != nil {
		t.Fatalf("LoadOrCreateToken: %v", err)
	}
	if len(first) != 64 {
		t.Fatalf("token length %d, want 64 hex chars", len(first))
	}
	if strings.TrimSpace(first) != first {
		t.Fatalf("token has surrounding whitespace: %q", first)
	}

	second, err := LoadOrCreateToken(dir)
	if err != nil {
		t.Fatalf("second LoadOrCreateToken: %v", err)
	}
	if second != first {
		t.Fatalf("token not persisted: %q then %q", first, second)
	}
}

func TestLoadOrCreateTokenFileIsPrivate(t *testing.T) {
	dir := t.TempDir()
	if _, err := LoadOrCreateToken(dir); err != nil {
		t.Fatalf("LoadOrCreateToken: %v", err)
	}

	info, err := os.Stat(filepath.Join(dir, "token"))
	if err != nil {
		t.Fatalf("stat token: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("token file mode %o, want 600", perm)
	}
}

func TestLoadOrCreateTokenToleratesTrailingNewline(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "token"), []byte("deadbeef\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := LoadOrCreateToken(dir)
	if err != nil {
		t.Fatalf("LoadOrCreateToken: %v", err)
	}
	if got != "deadbeef" {
		t.Fatalf("got %q, want %q — an editor-added newline must not change the token", got, "deadbeef")
	}
}

func TestTokenMatches(t *testing.T) {
	if !tokenMatches("abc123", "abc123") {
		t.Error("identical tokens did not match")
	}
	if tokenMatches("abc123", "abc124") {
		t.Error("different tokens matched")
	}
	if tokenMatches("abc123", "") {
		t.Error("empty presented token matched")
	}
	if tokenMatches("", "abc123") {
		t.Error("empty expected token matched — that would disable auth")
	}
}

func reqWithOrigin(host, origin string) *http.Request {
	r := httptest.NewRequest("GET", "http://"+host+"/api/runs", nil)
	r.Host = host
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	return r
}

func TestOriginAllowed(t *testing.T) {
	allowed := []string{"http://localhost:5173"}

	tests := []struct {
		name   string
		host   string
		origin string
		want   bool
	}{
		{"no origin is a non-browser client", "halo:9090", "", true},
		{"same origin", "halo:9090", "http://halo:9090", true},
		{"same origin over https", "halo:9090", "https://halo:9090", true},
		{"explicitly allowlisted", "halo:9090", "http://localhost:5173", true},
		{"different host", "halo:9090", "http://evil.example", false},
		{"different port on same host", "halo:9090", "http://halo:9091", false},
		{"port-less origin against ported host", "halo:9090", "http://halo", false},
		{"null origin from a sandboxed frame", "halo:9090", "null", false},
		{"prefix of an allowed origin", "halo:9090", "http://localhost:51739", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := originAllowed(reqWithOrigin(tc.host, tc.origin), allowed); got != tc.want {
				t.Fatalf("originAllowed(host=%q, origin=%q) = %v, want %v",
					tc.host, tc.origin, got, tc.want)
			}
		})
	}
}
