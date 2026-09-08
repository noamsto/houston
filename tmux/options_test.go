package tmux

import "testing"

func TestParseWindowOptions(t *testing.T) {
	// Real output shape, including the common all-empty-options case.
	out := "lazytmux|2|feat/320-relay|#320|565|mauve|open|passing|MERGEABLE||\n" +
		"houston|1|main||||||||\n"

	got := ParseWindowOptions(out)
	if len(got) != 2 {
		t.Fatalf("%d windows, want 2", len(got))
	}

	w := got[0]
	if w.Session != "lazytmux" || w.Window != 2 {
		t.Errorf("target = %s:%d, want lazytmux:2", w.Session, w.Window)
	}
	if w.Branch != "feat/320-relay" || w.IssueID != "#320" || w.PRNumber != "565" {
		t.Errorf("got branch=%q issue=%q pr=%q", w.Branch, w.IssueID, w.PRNumber)
	}
	if w.CrewName != "mauve" || w.PRState != "open" || w.PRCheckState != "passing" {
		t.Errorf("got crew=%q pr_state=%q checks=%q", w.CrewName, w.PRState, w.PRCheckState)
	}

	bare := got[1]
	if bare.Branch != "main" || bare.IssueID != "" || bare.PRNumber != "" {
		t.Errorf("a window with no enrichment should carry only its branch, got %+v", bare)
	}
}

func TestParseWindowOptionsSkipsMalformedLines(t *testing.T) {
	got := ParseWindowOptions("too|few|fields\n\nhouston|1|main||||||||\n")
	if len(got) != 1 {
		t.Fatalf("%d windows, want 1 — short and empty lines are skipped, not fatal", len(got))
	}
}

func TestParsePaneOptions(t *testing.T) {
	out := "%307|houston:1|processing 1788848628 |and add a ci task\n" +
		"%283|dispatcher:1||\n"

	got := ParsePaneOptions(out)
	if len(got) != 2 {
		t.Fatalf("%d panes, want 2", len(got))
	}
	if got[0].PaneID != "%307" || got[0].ClaudeStatus != "processing 1788848628 " {
		t.Errorf("got %+v", got[0])
	}
	if got[0].ClaudeTask != "and add a ci task" {
		t.Errorf("task = %q", got[0].ClaudeTask)
	}
	if got[1].ClaudeStatus != "" {
		t.Errorf("a pane with no claude status should be empty, got %q", got[1].ClaudeStatus)
	}
}
