package server

import (
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
