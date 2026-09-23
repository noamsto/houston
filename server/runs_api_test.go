package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/noamsto/houston/hook"
	"github.com/noamsto/houston/hub"
	"github.com/noamsto/houston/runs"
)

func TestRunsSnapshotReturnsComposedRuns(t *testing.T) {
	reg := runs.NewRegistry(runs.DefaultOrder)
	reg.Apply(runs.Delta{Source: "tmux", Key: "%1", Run: runs.Run{Branch: "main"}})
	reg.Apply(runs.Delta{Source: "hooks", Key: "%1", Run: runs.Run{Agent: "claude", State: runs.StateRunning}})

	s := &Server{runs: reg}
	rec := httptest.NewRecorder()
	s.handleRunsSnapshot(rec, httptest.NewRequest("GET", "/api/runs", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200", rec.Code)
	}
	var got []runs.Run
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v — body %s", err, rec.Body.String())
	}
	if len(got) != 1 || got[0].State != runs.StateRunning || got[0].Branch != "main" {
		t.Fatalf("got %+v, want one composed run", got)
	}
}

// TestRunsSnapshotShowsAPiAgentFromAHookEnvelope wires the real hook →
// hub → HookSource → Registry pipeline (server.go's New does the same) over a
// pi session_start envelope, and checks the resulting /api/runs body carries
// the engine hookyard reported rather than the hardcoded "claude".
func TestRunsSnapshotShowsAPiAgentFromAHookEnvelope(t *testing.T) {
	// No tmux to talk to: the pane key path is not under test here.
	t.Setenv("TMUX_PANE", "")
	t.Setenv("TMUX", "")

	stateDir := t.TempDir()
	payload := []byte(`{
		"engine": "pi",
		"canonical_event": "session_start",
		"native_event": "session_start",
		"session_id": "pi-s1",
		"cwd": "/w",
		"native": {
			"cwd": "/w",
			"hook_event_name": "session_start",
			"session_id": "pi-s1",
			"session_file": "/s/pi.jsonl",
			"reason": "startup"
		}
	}`)
	if err := hook.Dispatch("", stateDir, bytes.NewReader(payload)); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	discard := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := hub.NewWithOptions(stateDir, hub.Options{ClaudeProjectsDir: "-"}, discard)

	ctx, cancel := context.WithCancel(context.Background())
	hubDone := make(chan struct{})
	go func() {
		defer close(hubDone)
		_ = h.Run(ctx)
	}()

	reg := runs.NewRegistry(runs.DefaultOrder)
	deltas := make(chan runs.Delta, 16)
	pumpDone := make(chan struct{})
	go func() {
		defer close(pumpDone)
		for d := range deltas {
			reg.Apply(d)
		}
	}()

	src := runs.NewHookSource(h, nil)
	srcDone := make(chan struct{})
	go func() {
		defer close(srcDone)
		_ = src.Run(ctx, deltas)
	}()

	t.Cleanup(func() {
		cancel()
		waitClosed := func(what string, ch <-chan struct{}) {
			select {
			case <-ch:
			case <-time.After(5 * time.Second):
				t.Errorf("%s did not stop after cancel", what)
			}
		}
		waitClosed("hub", hubDone)
		waitClosed("hook source", srcDone)
		close(deltas)
		waitClosed("delta pump", pumpDone)
	})

	s := &Server{runs: reg}
	deadline := time.Now().Add(5 * time.Second)
	var body []byte
	for time.Now().Before(deadline) {
		rec := httptest.NewRecorder()
		s.handleRunsSnapshot(rec, httptest.NewRequest("GET", "/api/runs", nil))
		body = rec.Body.Bytes()

		var got []runs.Run
		if err := json.Unmarshal(body, &got); err == nil && len(got) == 1 {
			if got[0].Agent != "pi" {
				t.Fatalf("Agent = %q, want pi; body=%s", got[0].Agent, body)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no run appeared within deadline; last body=%s", body)
}

func TestRunsSnapshotWithoutRegistryIs503(t *testing.T) {
	s := &Server{} // registry never started
	rec := httptest.NewRecorder()
	s.handleRunsSnapshot(rec, httptest.NewRequest("GET", "/api/runs", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d, want 503", rec.Code)
	}
}

func TestRunsStreamDeliversARemoval(t *testing.T) {
	reg := runs.NewRegistry(runs.DefaultOrder)
	reg.Apply(runs.Delta{Source: "hooks", Key: "%1", Run: runs.Run{Agent: "claude", State: runs.StateRunning}})

	s := &Server{runs: reg}
	rec := newSyncRecorder()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/api/runs/stream", nil).WithContext(ctx)

	done := make(chan struct{})
	go func() {
		s.handleRunsStream(rec, req)
		close(done)
	}()

	waitForBody := func(substr string, timeout time.Duration) bool {
		deadline := time.Now().Add(timeout)
		for time.Now().Before(deadline) {
			if strings.Contains(rec.body(), substr) {
				return true
			}
			time.Sleep(time.Millisecond)
		}
		return false
	}

	if !waitForBody("event: snapshot", 5*time.Second) {
		t.Fatalf("snapshot event never sent\n%s", rec.body())
	}

	// The run's only agent-bearing layer leaves — the registry must report it
	// as removed, not just fall silent.
	reg.Apply(runs.Delta{Source: "hooks", Key: "%1", Gone: true})

	if !waitForBody(`"removed":true`, 5*time.Second) {
		t.Fatalf("removal never reached the client\n%s", rec.body())
	}

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Errorf("handler did not return after cancel")
	}
}

func TestRunsRoutesAreBehindTheAuthGate(t *testing.T) {
	// A new route registered outside apiMux would reopen the hole closed in #4.
	dir := t.TempDir()
	s := newFullServer(t, Config{StatusDir: dir, AuthEnabled: true})

	req := httptest.NewRequest("GET", "http://127.0.0.1/api/runs", nil)
	req.Host = "127.0.0.1"
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status %d, want 401 — /api/runs must require a token", rec.Code)
	}
}
