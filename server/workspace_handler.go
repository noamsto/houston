package server

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

// handleWorkspace returns the full tmux tree across every reachable session,
// bucketed by repo (main checkout / worktree / no repo) and joined against
// the run registry so agent panes carry their run_id.
//
//	GET /api/workspace → Workspace
func (s *Server) handleWorkspace(w http.ResponseWriter, _ *http.Request) {
	if s.runs == nil || s.wsTmux == nil || s.wsRepos == nil {
		http.Error(w, "workspace not started", http.StatusServiceUnavailable)
		return
	}

	wins, err := s.wsTmux.ListWindowOptions()
	if err != nil {
		slog.Error("workspace: list window options failed", "error", err)
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	panes, err := s.wsTmux.ListPaneOptions()
	if err != nil {
		slog.Error("workspace: list pane options failed", "error", err)
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	// isMainCheckout shells out to git, so ask it once per distinct root
	// rather than once per window — repoClassifier already caches per root,
	// but there's no reason to make redundant calls into that cache.
	mainCheckouts := make(map[string]bool)
	for _, win := range wins {
		if win.GitRoot == "" {
			continue
		}
		if _, ok := mainCheckouts[win.GitRoot]; ok {
			continue
		}
		mainCheckouts[win.GitRoot] = s.wsRepos.isMainCheckout(win.GitRoot)
	}

	ws := buildWorkspace(wins, panes, s.runs.Snapshot(), mainCheckouts)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(ws)
}
