package runs

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/noamsto/houston/tmux"
)

func TestDeltasFromTmuxJoinsPanesToWindows(t *testing.T) {
	wins := []tmux.WindowOptions{{
		Session: "lazytmux", Window: 2, Branch: "feat/320",
		IssueID: "#320", PRNumber: "565", PRState: "open", PRCheckState: "passing",
		CrewName: "mauve", GitRoot: "/home/n/git/lazytmux",
	}}
	panes := []tmux.PaneOptions{{
		PaneID: "%459", Target: "lazytmux:2", ClaudeStatus: "processing 1788 ",
	}}

	got := deltasFromTmux(wins, panes)
	if len(got) != 1 {
		t.Fatalf("%d deltas, want 1", len(got))
	}
	d := got[0]
	if d.Key != "%459" {
		t.Fatalf("Key = %q, want the pane id", d.Key)
	}
	if d.Run.State != StateRunning {
		t.Errorf("State = %q, want running (from @claude_status)", d.Run.State)
	}
	if d.Run.Branch != "feat/320" {
		t.Errorf("Branch = %q — the pane must inherit its window's enrichment", d.Run.Branch)
	}
	if d.Run.Issue == nil || d.Run.Issue.ID != "#320" {
		t.Errorf("Issue = %+v, want #320", d.Run.Issue)
	}
	if d.Run.PR == nil || d.Run.PR.Number != "565" || d.Run.PR.CheckState != "passing" {
		t.Errorf("PR = %+v", d.Run.PR)
	}
	if d.Run.Crew == nil || d.Run.Crew.Name != "mauve" {
		t.Errorf("Crew = %+v", d.Run.Crew)
	}
	if d.Run.Repo != "lazytmux" {
		t.Errorf("Repo = %q, want the git root's base name", d.Run.Repo)
	}
	if d.Run.Agent != "claude" {
		t.Errorf("Agent = %q, want claude — a non-empty @claude_status makes this an agent run", d.Run.Agent)
	}
	if d.Run.UpdatedAt != 1788 {
		t.Errorf("UpdatedAt = %d, want 1788 — the second field of @claude_status is the epoch", d.Run.UpdatedAt)
	}
}

func TestDeltasFromTmuxOmitsAbsentEnrichment(t *testing.T) {
	got := deltasFromTmux(
		[]tmux.WindowOptions{{Session: "houston", Window: 1, Branch: "main"}},
		[]tmux.PaneOptions{{PaneID: "%1", Target: "houston:1"}},
	)
	d := got[0].Run
	if d.Issue != nil || d.PR != nil || d.Crew != nil {
		t.Fatalf("absent options must stay nil, got issue=%+v pr=%+v crew=%+v", d.Issue, d.PR, d.Crew)
	}
	if d.Branch != "main" {
		t.Errorf("Branch = %q, want main", d.Branch)
	}
}

func TestDeltasFromTmuxSkipsPanesWithNoWindow(t *testing.T) {
	got := deltasFromTmux(nil, []tmux.PaneOptions{{PaneID: "%9", Target: "ghost:1"}})
	if len(got) != 0 {
		t.Fatalf("%d deltas for a pane whose window was not listed, want 0", len(got))
	}
}

func TestDeltasFromTmuxLeavesAgentEmptyWithNoClaudeStatus(t *testing.T) {
	// A shell, a build, htop — the enrichment (branch, worktree) still reaches
	// the layer harmlessly, but it must not turn into a listed run.
	got := deltasFromTmux(
		[]tmux.WindowOptions{{Session: "houston", Window: 1, Branch: "main"}},
		[]tmux.PaneOptions{{PaneID: "%1", Target: "houston:1"}},
	)
	if got[0].Run.Agent != "" {
		t.Fatalf("Agent = %q, want empty — no @claude_status means no agent", got[0].Run.Agent)
	}
	if got[0].Run.Branch != "main" {
		t.Fatalf("Branch = %q, enrichment must still reach a plain pane's layer", got[0].Run.Branch)
	}
}

// fakeLister drives TmuxSource.Run in tests without shelling out to tmux. It
// replays one step per call, repeating the last step once steps run out.
type fakeLister struct {
	steps []fakeStep
	calls int
}

type fakeStep struct {
	wins  []tmux.WindowOptions
	panes []tmux.PaneOptions
	err   error
}

func (f *fakeLister) step() fakeStep {
	i := f.calls
	if i >= len(f.steps) {
		i = len(f.steps) - 1
	}
	f.calls++
	return f.steps[i]
}

func (f *fakeLister) ListWindowOptions() ([]tmux.WindowOptions, error) {
	s := f.step()
	return s.wins, s.err
}

func (f *fakeLister) ListPaneOptions() ([]tmux.PaneOptions, error) {
	// step() must only be called once per tick; ListWindowOptions already
	// advanced the cursor for this tick, so replay the same step here.
	i := f.calls - 1
	if i < 0 {
		i = 0
	}
	if i >= len(f.steps) {
		i = len(f.steps) - 1
	}
	return f.steps[i].panes, f.steps[i].err
}

func TestTmuxSourceRun(t *testing.T) {
	claudePane := tmux.PaneOptions{PaneID: "%1", Target: "houston:1", ClaudeStatus: "processing 1 "}
	claudeWin := tmux.WindowOptions{Session: "houston", Window: 1, Branch: "main"}

	f := &fakeLister{steps: []fakeStep{
		{wins: []tmux.WindowOptions{claudeWin}, panes: []tmux.PaneOptions{claudePane}}, // tick 1: pane present
		{err: errors.New("tmux boom")},                                                 // tick 2: transient error
		{wins: []tmux.WindowOptions{claudeWin}, panes: []tmux.PaneOptions{claudePane}}, // tick 3: still present — proves tick 2 retired nothing
		{wins: nil, panes: nil},                                                        // tick 4: pane genuinely gone
	}}

	src := NewTmuxSource(f, 5*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	out := make(chan Delta, 16)
	done := make(chan error, 1)
	go func() { done <- src.Run(ctx, out) }()

	recv := func(t *testing.T) Delta {
		t.Helper()
		select {
		case d := <-out:
			return d
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for a delta")
			return Delta{}
		}
	}

	// Tick 1: the pane is listed, agent set from its claude_status.
	d := recv(t)
	if d.Gone || d.Key != "%1" || d.Run.Agent != "claude" {
		t.Fatalf("tick 1: got %+v, want a live claude-agent delta for %%1", d)
	}

	// Tick 2 (the error tick) must emit nothing. The discriminator is this
	// second receive: a regression that emits Gone on error would produce
	// update, Gone, update, Gone here, which is why this must check !d.Gone
	// specifically rather than just "a Gone eventually arrives" — the old,
	// weaker assertion could not tell a correct run from that regression,
	// since the tail of both sequences ends in a Gone for %1 either way.
	d = recv(t)
	if d.Gone || d.Key != "%1" {
		t.Fatalf("got %+v, want tick 3's live update for %%1 — the error tick must not have retired it", d)
	}

	// Tick 4: the pane is genuinely gone.
	d = recv(t)
	if !d.Gone || d.Key != "%1" {
		t.Fatalf("got %+v, want tick 4's Gone for %%1", d)
	}

	select {
	case extra := <-out:
		t.Fatalf("unexpected extra delta %+v", extra)
	case <-time.After(50 * time.Millisecond):
	}

	cancel()
	if err := <-done; err != context.Canceled {
		t.Fatalf("Run returned %v, want context.Canceled", err)
	}
}
