package runs

import (
	"testing"

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
