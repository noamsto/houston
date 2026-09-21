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

func TestBuildWorkspace_JoinsRunStateAndDetail(t *testing.T) {
	wins := []tmux.WindowOptions{
		{Session: "main", Window: 0, Name: "w", IssueID: "#69", PRNumber: "71", PRState: "OPEN", PRCheckState: "failure"},
	}
	panes := []tmux.PaneOptions{
		{PaneID: "%1", Target: "main:0", Command: "claude", Index: 0},
		{PaneID: "%2", Target: "main:0", Command: "fish", Index: 1},
	}
	snap := []runs.Run{{
		ID: "run-1", Agent: "claude-code", State: runs.StateRunning, UpdatedAt: 1234, Stale: true,
		Tmux:     &runs.TmuxRef{Session: "main", Window: 0, PaneID: "%1"},
		Activity: runs.Activity{Tool: "Edit", Hint: "a.go"},
	}}

	ws := buildWorkspace(wins, panes, snap, nil)

	w := ws.Sessions[0].Other[0]
	if w.IssueID != "#69" || w.PRNumber != "71" || w.PRState != "OPEN" || w.PRCheckState != "failure" {
		t.Errorf("issue/PR fields not carried through: %+v", w)
	}
	agent, plain := w.Panes[0], w.Panes[1]
	if agent.AgentType != "claude-code" || agent.State != "running" || agent.UpdatedAt != 1234 || agent.Detail != "Edit · a.go" || !agent.Stale {
		t.Errorf("run not joined onto agent pane: %+v", agent)
	}
	if plain.AgentType != "" || plain.State != "" || plain.Detail != "" || plain.UpdatedAt != 0 || plain.Stale {
		t.Errorf("plain pane carries run fields: %+v", plain)
	}
}

func TestRunDetail(t *testing.T) {
	tests := []struct {
		name string
		run  runs.Run
		want string
	}{
		{"blocked with message", runs.Run{State: runs.StateBlocked, Activity: runs.Activity{Message: "Allow Bash?"}}, "Allow Bash?"},
		{"blocked without message", runs.Run{State: runs.StateBlocked}, "waiting on you"},
		{"tool and hint", runs.Run{State: runs.StateRunning, Activity: runs.Activity{Tool: "Read", Hint: "x.go"}}, "Read · x.go"},
		{"tool only", runs.Run{State: runs.StateRunning, Activity: runs.Activity{Tool: "Read"}}, "Read"},
		{"task", runs.Run{State: runs.StateThinking, Activity: runs.Activity{Task: "fix bug"}}, "fix bug"},
		{"pr with checks", runs.Run{State: runs.StateDone, PR: &runs.PRRef{Number: "7", CheckState: "success"}}, "PR #7 · success"},
		{"pr without checks", runs.Run{State: runs.StateDone, PR: &runs.PRRef{Number: "7"}}, "PR #7"},
		{"pr with none check state", runs.Run{State: runs.StateDone, PR: &runs.PRRef{Number: "7", CheckState: "none"}}, "PR #7"},
		{"blocked beats tool", runs.Run{State: runs.StateBlocked, Activity: runs.Activity{Tool: "Edit", Message: "Allow?"}}, "Allow?"},
		{"tool beats task", runs.Run{State: runs.StateRunning, Activity: runs.Activity{Tool: "Edit", Task: "x"}}, "Edit"},
		{"multi-line free text collapses", runs.Run{State: runs.StateThinking, Activity: runs.Activity{Task: "line one\n  line two"}}, "line one line two"},
		{"falls back to state", runs.Run{State: runs.StateIdle}, "idle"},
	}
	for _, tt := range tests {
		if got := runDetail(tt.run); got != tt.want {
			t.Errorf("%s: runDetail = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestBuildWorkspace_DropsNoneSentinelOptions(t *testing.T) {
	wins := []tmux.WindowOptions{
		{Session: "main", Window: 0, Name: "w", IssueID: "#69", PRNumber: "none", PRState: "none", PRCheckState: "none"},
	}
	panes := []tmux.PaneOptions{{PaneID: "%1", Target: "main:0", Index: 0}}

	w := buildWorkspace(wins, panes, nil, nil).Sessions[0].Other[0]

	if w.IssueID != "#69" {
		t.Errorf("IssueID = %q, want #69", w.IssueID)
	}
	if w.PRNumber != "" || w.PRState != "" || w.PRCheckState != "" {
		t.Errorf("none sentinel leaked: %+v", w)
	}
}
