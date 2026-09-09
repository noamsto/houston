package server

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/noamsto/houston/hook"
	"github.com/noamsto/houston/hub"
)

// newTestServer constructs a minimal Server with only the hub wired — no tmux,
// no opencode, no UI. Enough to exercise /api/agents and /api/agents/stream.
func newTestServer(t *testing.T, stateDir string) *Server {
	t.Helper()
	s := &Server{hub: hub.NewWithOptions(stateDir, hub.Options{ClaudeProjectsDir: "-"}, slog.Default())}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = s.hub.Run(ctx) }()
	// let Run set up fsnotify before tests write state files
	time.Sleep(80 * time.Millisecond)
	return s
}

func TestAgentsSnapshotEmpty(t *testing.T) {
	s := newTestServer(t, t.TempDir())
	req := httptest.NewRequest(http.MethodGet, "/api/agents", nil)
	rec := httptest.NewRecorder()
	s.handleAgentsSnapshot(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var got []hub.SessionView
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, rec.Body.String())
	}
	if len(got) != 0 {
		t.Errorf("empty stateDir should yield 0 sessions, got %d", len(got))
	}
}

func TestAgentsSnapshotReflectsHookState(t *testing.T) {
	dir := t.TempDir()
	if err := hook.Write(hook.Path(dir, "demo"), hook.SessionState{
		SessionID: "demo",
		State:     hook.StateWaiting,
		CWD:       "/tmp",
		Turn:      1,
		UpdatedAt: time.Now().Unix(),
	}); err != nil {
		t.Fatalf("seed state: %v", err)
	}

	s := newTestServer(t, dir)

	req := httptest.NewRequest(http.MethodGet, "/api/agents", nil)
	rec := httptest.NewRecorder()
	s.handleAgentsSnapshot(rec, req)

	var got []hub.SessionView
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if len(got) != 1 {
		t.Fatalf("got %d sessions, want 1", len(got))
	}
	if got[0].SessionID != "demo" || got[0].State != hook.StateWaiting {
		t.Errorf("snapshot mismatch: %+v", got[0])
	}
}

// syncRecorder wraps httptest.ResponseRecorder with a mutex so a handler
// writing concurrently with a test goroutine reading the body doesn't race.
// The recorder is unexported (not embedded) so any stray direct
// .Body/.Header() access fails to compile instead of silently working.
type syncRecorder struct {
	mu  sync.Mutex
	rec *httptest.ResponseRecorder
}

func newSyncRecorder() *syncRecorder {
	return &syncRecorder{rec: httptest.NewRecorder()}
}

func (s *syncRecorder) Header() http.Header {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rec.Header()
}

func (s *syncRecorder) Write(b []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rec.Write(b)
}

func (s *syncRecorder) WriteHeader(code int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rec.WriteHeader(code)
}

func (s *syncRecorder) Flush() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rec.Flush()
}

func (s *syncRecorder) body() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rec.Body.String()
}

func (s *syncRecorder) header() http.Header {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rec.Header()
}

func TestAgentsStreamEmitsSnapshotAndUpdate(t *testing.T) {
	dir := t.TempDir()
	s := newTestServer(t, dir)

	rec := newSyncRecorder()

	// Kick the SSE handler off in a goroutine and cancel it when we're done.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/api/agents/stream", nil).WithContext(ctx)

	done := make(chan struct{})
	go func() {
		s.handleAgentsStream(rec, req)
		close(done)
	}()

	// Wait until the snapshot event lands.
	waitForBody := func(substr string, timeout time.Duration) bool {
		deadline := time.Now().Add(timeout)
		for time.Now().Before(deadline) {
			if strings.Contains(rec.body(), substr) {
				return true
			}
			time.Sleep(25 * time.Millisecond)
		}
		return false
	}

	if !waitForBody("event: snapshot", time.Second) {
		t.Fatalf("snapshot event never sent\n%s", rec.body())
	}

	// Write a state file — the hub should broadcast an update.
	if err := hook.Write(hook.Path(dir, "live"), hook.SessionState{
		SessionID: "live",
		State:     hook.StateToolRunning,
		Tool:      "Bash",
		UpdatedAt: time.Now().Unix(),
	}); err != nil {
		t.Fatalf("write: %v", err)
	}

	if !waitForBody("event: update", 2*time.Second) {
		t.Errorf("update event never sent\n%s", rec.body())
	}

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Errorf("handler did not return after cancel")
	}

	// Content-Type sanity.
	if got := rec.header().Get("Content-Type"); got != "text/event-stream" {
		t.Errorf("Content-Type = %q", got)
	}

	// Parse the SSE stream and assert the update payload deserializes cleanly.
	r := bufio.NewReader(strings.NewReader(rec.body()))
	var lastData string
	var lastEvent string
	for {
		line, err := r.ReadString('\n')
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		line = strings.TrimRight(line, "\n")
		switch {
		case strings.HasPrefix(line, "event: "):
			lastEvent = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: ") && lastEvent == "update":
			lastData = strings.TrimPrefix(line, "data: ")
		}
	}
	if lastData != "" {
		var v hub.SessionView
		if err := json.Unmarshal([]byte(lastData), &v); err != nil {
			t.Errorf("update payload not a SessionView: %v\n%s", err, lastData)
		}
	}
}

func TestAgentsSnapshotReturnsServiceUnavailableWhenHubMissing(t *testing.T) {
	s := &Server{} // no hub
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/agents", nil)
	s.handleAgentsSnapshot(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
}
