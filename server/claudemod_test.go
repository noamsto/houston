package server

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/noamsto/houston/mode"
)

func TestClaudeModDisabledWithoutSocket(t *testing.T) {
	dir := t.TempDir()
	h := newFullServer(t, Config{StatusDir: dir, Mode: mode.Tmux}).Handler()

	if _, err := os.Lstat(filepath.Join(dir, "claude-mod.sock")); !os.IsNotExist(err) {
		t.Fatalf("claude-mod.sock exists without a configured socket: %v", err)
	}
	if rec := modeRequest(t, h, "GET", "/api/debug/claude-mod", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404 — body %s", rec.Code, rec.Body.String())
	}
}

func TestClaudeModListensAndFolds(t *testing.T) {
	// Short path: a unix socket path is limited to about 108 bytes.
	dir, err := os.MkdirTemp("", "cms")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "claude-mod.sock")

	s, err := New(Config{StatusDir: dir, Mode: mode.Tmux, ClaudeModSocket: sock})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	fi, err := os.Lstat(sock)
	if err != nil {
		t.Fatalf("socket: %v", err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Fatalf("socket mode %o, want 600", got)
	}

	c := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", sock)
		},
	}}
	t.Cleanup(c.CloseIdleConnections)
	resp, err := c.Post("http://houston/v1/record", "application/json",
		strings.NewReader(`{"v":1,"sid":"s1","boot":"b1","seq":1,"ts":1,"ev":"turn.start"}`))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("post status %d, want 204", resp.StatusCode)
	}

	rec := modeRequest(t, s.Handler(), "GET", "/api/debug/claude-mod", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200 — body %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type %q, want application/json", ct)
	}
	var got struct {
		Sessions []struct {
			SID   string `json:"sid"`
			State string `json:"state"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v — body %s", err, rec.Body.String())
	}
	if len(got.Sessions) != 1 || got.Sessions[0].SID != "s1" || got.Sessions[0].State != "thinking" {
		t.Fatalf("sessions %+v, want s1 thinking", got.Sessions)
	}

	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if _, err := os.Lstat(sock); !os.IsNotExist(err) {
		t.Fatalf("socket still present after Close: %v", err)
	}
}

func TestClaudeModSocketTakenStillStarts(t *testing.T) {
	// Short path: a unix socket path is limited to about 108 bytes.
	dir, err := os.MkdirTemp("", "cms")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "claude-mod.sock")

	taken, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = taken.Close() })

	s := newFullServer(t, Config{StatusDir: dir, Mode: mode.Tmux, ClaudeModSocket: sock})

	if rec := modeRequest(t, s.Handler(), "GET", "/api/debug/claude-mod", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404 — body %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Lstat(sock); err != nil {
		t.Fatalf("the other listener's socket is gone: %v", err)
	}
}
