package tmux

import (
	"strings"
	"testing"
)

func TestParseWindowOptions(t *testing.T) {
	// Real output shape, including the common all-empty-options case.
	out := "lazytmux\x1f2\x1ffeat/320-relay\x1f#320\x1f565\x1fmauve\x1fopen\x1fpassing\x1fMERGEABLE\x1f\x1f\x1fcolour168\x1frelay\x1f1\n" +
		"houston\x1f1\x1fmain\x1f\x1f\x1f\x1f\x1f\x1f\x1f\x1f\x1f\x1fhouston\x1f0\n"

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
	if w.CrewColor != "colour168" {
		t.Errorf("CrewColor = %q, want colour168", w.CrewColor)
	}
	if w.Name != "relay" || !w.Active {
		t.Errorf("got name=%q active=%v, want relay/true", w.Name, w.Active)
	}

	bare := got[1]
	if bare.Branch != "main" || bare.IssueID != "" || bare.PRNumber != "" {
		t.Errorf("a window with no enrichment should carry only its branch, got %+v", bare)
	}
	if bare.Name != "houston" || bare.Active {
		t.Errorf("got name=%q active=%v, want houston/false", bare.Name, bare.Active)
	}
}

func TestParseWindowOptionsSkipsMalformedLines(t *testing.T) {
	got := ParseWindowOptions("too\x1ffew\x1ffields\n\nhouston\x1f1\x1fmain\x1f\x1f\x1f\x1f\x1f\x1f\x1f\x1f\x1f\x1fhouston\x1f0\n")
	if len(got) != 1 {
		t.Fatalf("%d windows, want 1 — short and empty lines are skipped, not fatal", len(got))
	}
}

func TestParseWindowOptionsSurvivesPipeInFreeText(t *testing.T) {
	// @window_task is free text captured from a user prompt, so it can contain
	// anything. With a "|" delimiter this shifted GitRoot into garbage and
	// silently dropped the real value.
	out := "houston\x1f1\x1fmain\x1f\x1f\x1f\x1f\x1f\x1f\x1frun x | grep fail\x1f/home/n/git/houston\x1f\x1fhouston\x1f0\n"

	got := ParseWindowOptions(out)
	if len(got) != 1 {
		t.Fatalf("%d windows, want 1", len(got))
	}
	if got[0].Task != "run x | grep fail" {
		t.Errorf("Task = %q, want the pipe preserved inside the field", got[0].Task)
	}
	if got[0].GitRoot != "/home/n/git/houston" {
		t.Errorf("GitRoot = %q — a pipe in an earlier field shifted it", got[0].GitRoot)
	}
}

func TestParsePaneOptions(t *testing.T) {
	out := "%307\x1fhouston:1\x1fprocessing 1788848628 \x1f\x1fand add a ci task\x1fnode\x1f0\x1f1\x1f\x1f1966\x1f1790086864\x1f4521\n" +
		"%283\x1fdispatcher:1\x1f\x1f\x1f\x1fbash\x1f1\x1f0\x1f\x1f1966\x1f1790086864\x1f4522\n" +
		"%284\x1fdispatcher:1\x1fwaiting\x1f\x1f\x1fnode\x1f2\x1f0\x1fspec-critic\x1f1966\x1f1790086864\x1f4523\n" +
		"%290\x1fhouston:2\x1f\x1fprocessing 1790144005\x1f\x1fpi\x1f0\x1f0\x1f\x1f1966\x1f1790086864\x1f4524\n"

	got := ParsePaneOptions(out)
	if len(got) != 4 {
		t.Fatalf("%d panes, want 4", len(got))
	}
	if got[0].PaneID != "%307" || got[0].ClaudeStatus != "processing 1788848628 " {
		t.Errorf("got %+v", got[0])
	}
	if got[0].AgentScreen != "" {
		t.Errorf("AgentScreen = %q, want empty on a claude pane", got[0].AgentScreen)
	}
	if got[0].ClaudeTask != "and add a ci task" {
		t.Errorf("task = %q", got[0].ClaudeTask)
	}
	if got[0].Command != "node" || got[0].Index != 0 || !got[0].Active {
		t.Errorf("got command=%q index=%d active=%v, want node/0/true", got[0].Command, got[0].Index, got[0].Active)
	}
	if got[0].CrewRole != "" {
		t.Errorf("lead pane CrewRole = %q, want empty", got[0].CrewRole)
	}
	if got[0].ServerPID != "1966" || got[0].ServerStart != 1790086864 {
		t.Errorf("ServerPID = %q ServerStart = %d, want 1966/1790086864", got[0].ServerPID, got[0].ServerStart)
	}
	if got[0].PanePID != 4521 {
		t.Errorf("PanePID = %d, want 4521", got[0].PanePID)
	}
	if got[1].ClaudeStatus != "" {
		t.Errorf("a pane with no claude status should be empty, got %q", got[1].ClaudeStatus)
	}
	if got[1].Command != "bash" || got[1].Index != 1 || got[1].Active {
		t.Errorf("got command=%q index=%d active=%v, want bash/1/false", got[1].Command, got[1].Index, got[1].Active)
	}
	if got[1].PanePID != 4522 {
		t.Errorf("PanePID = %d, want 4522", got[1].PanePID)
	}
	if got[2].CrewRole != "spec-critic" {
		t.Errorf("role pane CrewRole = %q, want spec-critic", got[2].CrewRole)
	}
	if got[2].PanePID != 4523 {
		t.Errorf("PanePID = %d, want 4523", got[2].PanePID)
	}
	if got[3].AgentScreen != "processing 1790144005" || got[3].ClaudeStatus != "" {
		t.Errorf("pi pane = %+v, want agent_screen set and no claude status", got[3])
	}
	if got[3].PanePID != 4524 {
		t.Errorf("PanePID = %d, want 4524", got[3].PanePID)
	}
}

func TestParsePaneOptionsSkipsShortLines(t *testing.T) {
	// A line missing crew_role/pid/start_time (pre-identity format), one
	// missing the agent_screen field, and a pre-pane_pid 11-field line
	// (previously the full format) must all be skipped now that the format
	// requires 12 fields.
	out := "%307\x1fhouston:1\x1f\x1f\x1fnode\x1f0\x1f1\n" +
		"%283\x1fdispatcher:1\x1f\x1f\x1fbash\x1f1\x1f0\x1f1966\x1f1790086864\n" +
		"%284\x1fdispatcher:1\x1f\x1f\x1f\x1fbash\x1f1\x1f0\x1f\x1f1966\x1f1790086864\n"

	got := ParsePaneOptions(out)
	if len(got) != 0 {
		t.Fatalf("%d panes, want 0 — all three lines are short of the current 12-field format", len(got))
	}
}

func TestParsePaneOptionsToleratesNonNumericStartTime(t *testing.T) {
	out := "%307\x1fhouston:1\x1f\x1f\x1f\x1fnode\x1f0\x1f1\x1f\x1f1966\x1fsometime\x1f4525\n" +
		"%308\x1fhouston:1\x1f\x1f\x1f\x1fnode\x1f0\x1f1\x1f\x1f1966\x1f1790086864\x1fnotanumber\n"

	got := ParsePaneOptions(out)
	if len(got) != 2 {
		t.Fatalf("%d panes, want 2 — an unexpandable start_time or pane_pid must not drop the pane", len(got))
	}
	if got[0].ServerPID != "1966" || got[0].ServerStart != 0 {
		t.Errorf("ServerPID = %q ServerStart = %d, want 1966/0", got[0].ServerPID, got[0].ServerStart)
	}
	if got[0].PanePID != 4525 {
		t.Errorf("PanePID = %d, want 4525", got[0].PanePID)
	}
	if got[1].PanePID != 0 {
		t.Errorf("PanePID = %d, want 0 for a non-numeric pane_pid", got[1].PanePID)
	}
}

// The foreign-pane guard in runs/hooksource.go reads the server identity off
// these listed lines, so dropping either field from the format would silently
// disable it — the parser alone cannot notice, since it only ever sees
// hand-built fixtures.
func TestPaneOptionsFormatCarriesTheServerIdentity(t *testing.T) {
	if n := strings.Count(paneOptionsFormat, optSep); n != 11 {
		t.Errorf("%d separators, want 11 — ParsePaneOptions requires 12 fields", n)
	}
	for _, f := range []string{"#{pid}", "#{start_time}", "#{pane_pid}"} {
		if !strings.Contains(paneOptionsFormat, f) {
			t.Errorf("format lost %s: the pane listing no longer identifies its server", f)
		}
	}
}

// The screen-scraped engines (pi, codex, cursor) carry no @claude_status, so
// the pane listing must expose @agent_screen for a crew record to join them.
func TestPaneOptionsFormatCarriesAgentScreen(t *testing.T) {
	if !strings.Contains(paneOptionsFormat, "#{@agent_screen}") {
		t.Error("format lost #{@agent_screen}: pi/codex/cursor panes would never join a crew record")
	}
}
