package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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
			time.Sleep(25 * time.Millisecond)
		}
		return false
	}

	if !waitForBody("event: snapshot", time.Second) {
		t.Fatalf("snapshot event never sent\n%s", rec.body())
	}

	// The run's only agent-bearing layer leaves — the registry must report it
	// as removed, not just fall silent.
	reg.Apply(runs.Delta{Source: "hooks", Key: "%1", Gone: true})

	if !waitForBody(`"removed":true`, 2*time.Second) {
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
	s, err := New(Config{StatusDir: dir, AuthEnabled: true})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	req := httptest.NewRequest("GET", "http://127.0.0.1/api/runs", nil)
	req.Host = "127.0.0.1"
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status %d, want 401 — /api/runs must require a token", rec.Code)
	}
}
