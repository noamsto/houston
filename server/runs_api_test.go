package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/noamsto/houston/runs"
)

func TestRunsSnapshotReturnsComposedRuns(t *testing.T) {
	reg := runs.NewRegistry(runs.DefaultOrder)
	reg.Apply(runs.Delta{Source: "tmux", Key: "%1", Run: runs.Run{Branch: "main"}})
	reg.Apply(runs.Delta{Source: "hooks", Key: "%1", Run: runs.Run{State: runs.StateRunning}})

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
