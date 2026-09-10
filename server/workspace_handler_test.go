package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/noamsto/houston/runs"
	"github.com/noamsto/houston/tmux"
)

// fakeWorkspaceLister satisfies workspaceLister without shelling out to tmux.
type fakeWorkspaceLister struct {
	wins    []tmux.WindowOptions
	panes   []tmux.PaneOptions
	winErr  error
	paneErr error
}

func (f *fakeWorkspaceLister) ListWindowOptions() ([]tmux.WindowOptions, error) {
	return f.wins, f.winErr
}

func (f *fakeWorkspaceLister) ListPaneOptions() ([]tmux.PaneOptions, error) {
	return f.panes, f.paneErr
}

// fakeRepoClassifier satisfies mainCheckoutChecker with a canned answer per
// root — no real git call, that belongs to workspace_repo_test.go.
type fakeRepoClassifier struct {
	main map[string]bool
}

func (f *fakeRepoClassifier) isMainCheckout(root string) bool { return f.main[root] }

func TestHandleWorkspaceWithoutRegistryIs503(t *testing.T) {
	s := &Server{} // registry never started
	rec := httptest.NewRecorder()
	s.handleWorkspace(rec, httptest.NewRequest("GET", "/api/workspace", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d, want 503", rec.Code)
	}
}

func TestHandleWorkspaceWithoutTmuxOrReposIs503(t *testing.T) {
	reg := runs.NewRegistry(runs.DefaultOrder)

	cases := []struct {
		name string
		s    *Server
	}{
		{"no wsTmux", &Server{runs: reg, wsRepos: &fakeRepoClassifier{}}},
		{"no wsRepos", &Server{runs: reg, wsTmux: &fakeWorkspaceLister{}}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			tc.s.handleWorkspace(rec, httptest.NewRequest("GET", "/api/workspace", nil))

			if rec.Code != http.StatusServiceUnavailable {
				t.Fatalf("status %d, want 503", rec.Code)
			}
		})
	}
}

func TestHandleWorkspaceJoinsRunAndBuckets(t *testing.T) {
	reg := runs.NewRegistry(runs.DefaultOrder)
	reg.Apply(runs.Delta{
		Source: "tmux",
		Key:    "%1",
		Run: runs.Run{
			Agent: "claude",
			Tmux:  &runs.TmuxRef{Session: "houston", Window: 0, PaneID: "%1"},
		},
	})

	s := &Server{
		runs: reg,
		wsTmux: &fakeWorkspaceLister{
			wins: []tmux.WindowOptions{
				{Session: "houston", Window: 0, Name: "win0", Active: true, GitRoot: "/repo/main"},
			},
			panes: []tmux.PaneOptions{
				{PaneID: "%1", Target: "houston:0", Command: "node", Index: 0, Active: true},
			},
		},
		wsRepos: &fakeRepoClassifier{main: map[string]bool{"/repo/main": true}},
	}

	rec := httptest.NewRecorder()
	s.handleWorkspace(rec, httptest.NewRequest("GET", "/api/workspace", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200 — body %s", rec.Code, rec.Body.String())
	}

	var got Workspace
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v — body %s", err, rec.Body.String())
	}

	if len(got.Sessions) != 1 {
		t.Fatalf("got %d sessions, want 1: %+v", len(got.Sessions), got)
	}
	session := got.Sessions[0]
	if len(session.MainCheckout) != 1 || len(session.Worktrees) != 0 || len(session.Other) != 0 {
		t.Fatalf("got session %+v, want exactly one window in main_checkout", session)
	}

	panes := session.MainCheckout[0].Panes
	if len(panes) != 1 {
		t.Fatalf("got %d panes, want 1: %+v", len(panes), panes)
	}
	if !panes[0].Agent || panes[0].RunID != "pane-1" {
		t.Fatalf("got pane %+v, want agent=true run_id=pane-1", panes[0])
	}
}

func TestHandleWorkspaceTmuxErrorIs502(t *testing.T) {
	reg := runs.NewRegistry(runs.DefaultOrder)
	s := &Server{
		runs:    reg,
		wsTmux:  &fakeWorkspaceLister{winErr: errors.New("no server running on socket")},
		wsRepos: &fakeRepoClassifier{},
	}

	rec := httptest.NewRecorder()
	s.handleWorkspace(rec, httptest.NewRequest("GET", "/api/workspace", nil))

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status %d, want 502", rec.Code)
	}
}

func TestWorkspaceRouteIsBehindTheAuthGate(t *testing.T) {
	// A new route registered outside apiMux would reopen the hole closed in #4.
	dir := t.TempDir()
	s, err := New(Config{StatusDir: dir, AuthEnabled: true})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	req := httptest.NewRequest("GET", "http://127.0.0.1/api/workspace", nil)
	req.Host = "127.0.0.1"
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status %d, want 401 — /api/workspace must require a token", rec.Code)
	}
}
