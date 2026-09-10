package server

import (
	"testing"

	"github.com/noamsto/houston/runs"
	"github.com/noamsto/houston/tmux"
)

func TestBuildWorkspace_PlainPaneNoRun(t *testing.T) {
	wins := []tmux.WindowOptions{
		{Session: "main", Window: 0, Name: "shell", Active: true},
	}
	panes := []tmux.PaneOptions{
		{PaneID: "%1", Target: "main:0", ClaudeStatus: "", Index: 0},
	}

	ws := buildWorkspace(wins, panes, nil, nil)

	if len(ws.Sessions) != 1 || len(ws.Sessions[0].Other) != 1 || len(ws.Sessions[0].Other[0].Panes) != 1 {
		t.Fatalf("unexpected shape: %+v", ws)
	}
	p := ws.Sessions[0].Other[0].Panes[0]
	if p.Agent {
		t.Errorf("Agent = true, want false for plain pane with no matching run")
	}
	if p.RunID != "" {
		t.Errorf("RunID = %q, want empty", p.RunID)
	}
}

func TestBuildWorkspace_OrdinaryEnrichedAgentPane(t *testing.T) {
	wins := []tmux.WindowOptions{
		{Session: "main", Window: 0, Branch: "feat/x", CrewName: "falcon", Name: "agent", Active: true},
	}
	panes := []tmux.PaneOptions{
		{PaneID: "%2", Target: "main:0", ClaudeStatus: "working", Index: 0},
	}
	snap := []runs.Run{
		{ID: "run-1", Tmux: &runs.TmuxRef{Session: "main", Window: 0, PaneID: "%2"}},
	}

	ws := buildWorkspace(wins, panes, snap, nil)

	w := ws.Sessions[0].Other[0]
	if w.Branch != "feat/x" || w.CrewCodename != "falcon" {
		t.Errorf("window fields not carried through: %+v", w)
	}
	p := w.Panes[0]
	if !p.Agent {
		t.Errorf("Agent = false, want true for pane joined to a run")
	}
	if p.RunID != "run-1" {
		t.Errorf("RunID = %q, want run-1", p.RunID)
	}
}

func TestBuildWorkspace_HookOnlyAgentPane(t *testing.T) {
	// ClaudeStatus is empty (lazytmux never touched this pane), yet the pane
	// id matches a Run the registry composed via HookSource — Agent must
	// still read true, since it's derived from RunID, not ClaudeStatus.
	wins := []tmux.WindowOptions{
		{Session: "main", Window: 0, Name: "hook-agent", Active: true},
	}
	panes := []tmux.PaneOptions{
		{PaneID: "%3", Target: "main:0", ClaudeStatus: "", Index: 0},
	}
	snap := []runs.Run{
		{ID: "run-2", Tmux: &runs.TmuxRef{Session: "main", Window: 0, PaneID: "%3"}},
	}

	ws := buildWorkspace(wins, panes, snap, nil)

	p := ws.Sessions[0].Other[0].Panes[0]
	if !p.Agent {
		t.Errorf("Agent = false, want true for hook-only composed run (ClaudeStatus empty)")
	}
	if p.RunID != "run-2" {
		t.Errorf("RunID = %q, want run-2", p.RunID)
	}
}

func TestBuildWorkspace_ClaudeStatusWithNoJoinedRun(t *testing.T) {
	// Mirror case: lazytmux opinion alone, with no composed run in snap,
	// must not make the pane routable.
	wins := []tmux.WindowOptions{
		{Session: "main", Window: 0, Name: "orphaned", Active: true},
	}
	panes := []tmux.PaneOptions{
		{PaneID: "%4", Target: "main:0", ClaudeStatus: "working", Index: 0},
	}

	ws := buildWorkspace(wins, panes, nil, nil)

	p := ws.Sessions[0].Other[0].Panes[0]
	if p.Agent {
		t.Errorf("Agent = true, want false when no run joined this pane id")
	}
	if p.RunID != "" {
		t.Errorf("RunID = %q, want empty", p.RunID)
	}
}

func TestBuildWorkspace_PaneWithMissingWindowSkipped(t *testing.T) {
	wins := []tmux.WindowOptions{
		{Session: "main", Window: 0, Name: "kept", Active: true},
	}
	panes := []tmux.PaneOptions{
		{PaneID: "%5", Target: "main:0", Index: 0},
		{PaneID: "%6", Target: "main:1", Index: 0}, // window 1 never listed
	}

	ws := buildWorkspace(wins, panes, nil, nil)

	if len(ws.Sessions) != 1 || len(ws.Sessions[0].Other) != 1 {
		t.Fatalf("unexpected session/window shape: %+v", ws)
	}
	panesOut := ws.Sessions[0].Other[0].Panes
	if len(panesOut) != 1 || panesOut[0].ID != "%5" {
		t.Errorf("panes = %+v, want only %%5", panesOut)
	}
}

func TestBuildWorkspace_Ordering(t *testing.T) {
	wins := []tmux.WindowOptions{
		{Session: "zeta", Window: 0, Name: "z0"},
		{Session: "alpha", Window: 1, Name: "a1"},
		{Session: "alpha", Window: 0, Name: "a0"},
	}
	panes := []tmux.PaneOptions{
		{PaneID: "%20", Target: "zeta:0", Index: 0},
		{PaneID: "%21", Target: "alpha:1", Index: 0},
		{PaneID: "%12", Target: "alpha:0", Index: 2},
		{PaneID: "%10", Target: "alpha:0", Index: 0},
		{PaneID: "%11", Target: "alpha:0", Index: 1},
	}

	ws := buildWorkspace(wins, panes, nil, nil)

	if len(ws.Sessions) != 2 || ws.Sessions[0].Name != "alpha" || ws.Sessions[1].Name != "zeta" {
		t.Fatalf("sessions not sorted by name: %+v", ws.Sessions)
	}
	alphaWindows := ws.Sessions[0].Other
	if len(alphaWindows) != 2 || alphaWindows[0].Index != 0 || alphaWindows[1].Index != 1 {
		t.Fatalf("windows not sorted by index: %+v", alphaWindows)
	}
	panesOut := alphaWindows[0].Panes
	if len(panesOut) != 3 || panesOut[0].ID != "%10" || panesOut[1].ID != "%11" || panesOut[2].ID != "%12" {
		t.Fatalf("panes not sorted by index: %+v", panesOut)
	}
}

// TestBuildWorkspace_Bucketing pins the MainCheckout/Worktrees/Other split:
// one session, four windows sharing the session name, each landing in a
// different bucket for a different reason — including the "GitRoot present
// but absent from mainCheckouts" case, which must read the same as an
// explicit false, not as "unknown, skip it."
func TestBuildWorkspace_Bucketing(t *testing.T) {
	wins := []tmux.WindowOptions{
		{Session: "s", Window: 0, Name: "main-checkout", GitRoot: "/repo/main"},
		{Session: "s", Window: 1, Name: "worktree-explicit-false", GitRoot: "/repo/wt1"},
		{Session: "s", Window: 2, Name: "no-repo", GitRoot: ""},
		{Session: "s", Window: 3, Name: "worktree-absent-key", GitRoot: "/repo/wt2"},
	}
	panes := []tmux.PaneOptions{
		{PaneID: "%30", Target: "s:0", Index: 0},
		{PaneID: "%31", Target: "s:1", Index: 0},
		{PaneID: "%32", Target: "s:2", Index: 0},
		{PaneID: "%33", Target: "s:3", Index: 0},
	}
	mainCheckouts := map[string]bool{
		"/repo/main": true,
		"/repo/wt1":  false,
		// "/repo/wt2" intentionally absent — must still bucket as Worktrees.
	}

	ws := buildWorkspace(wins, panes, nil, mainCheckouts)

	if len(ws.Sessions) != 1 {
		t.Fatalf("unexpected session count: %+v", ws.Sessions)
	}
	s := ws.Sessions[0]

	if s.WindowCount != 4 {
		t.Errorf("WindowCount = %d, want 4 regardless of bucket", s.WindowCount)
	}
	if len(s.MainCheckout) != 1 || s.MainCheckout[0].Name != "main-checkout" {
		t.Errorf("MainCheckout = %+v, want just main-checkout", s.MainCheckout)
	}
	if len(s.Worktrees) != 2 {
		t.Fatalf("Worktrees = %+v, want 2 (explicit false + absent key)", s.Worktrees)
	}
	if s.Worktrees[0].Name != "worktree-explicit-false" || s.Worktrees[1].Name != "worktree-absent-key" {
		t.Errorf("Worktrees = %+v, want [worktree-explicit-false, worktree-absent-key] in window-index order", s.Worktrees)
	}
	if len(s.Other) != 1 || s.Other[0].Name != "no-repo" {
		t.Errorf("Other = %+v, want just no-repo", s.Other)
	}
}
