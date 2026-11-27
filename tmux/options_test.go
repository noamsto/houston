package tmux

import (
	"slices"
	"strings"
	"testing"

	rtmux "github.com/noamsto/tmux-remux/pkg/tmux"
)

func TestWithoutCrew(t *testing.T) {
	in := []WindowOptions{{Session: "s", Window: 1, Branch: "main", CrewName: "ash", CrewColor: "colour99"}}
	got := WithoutCrew(in)
	if got[0].CrewName != "" || got[0].CrewColor != "" {
		t.Errorf("crew fields = %q/%q, want cleared", got[0].CrewName, got[0].CrewColor)
	}
	if got[0].Session != "s" || got[0].Branch != "main" {
		t.Errorf("other fields changed: %+v", got[0])
	}
	if in[0].CrewName != "ash" || in[0].CrewColor != "colour99" {
		t.Errorf("input mutated: %+v", in[0])
	}
	if WithoutCrew(nil) != nil {
		t.Error("nil in must give nil out")
	}
}

// winLine builds a list-windows line in tmux-remux's superset format plus
// windowExtraFormats: session, index, name, active, then the ten options in
// windowExtraFormats order.
func winLine(session, index, name, active string, opts ...string) string {
	f := []string{session, index, name, "layout", "@1", "0", active, "1", "0", "/tmp"}
	return strings.Join(append(f, opts...), "\x1f")
}

// paneRow builds a PaneRow as tmux-remux's ListPanes returns it with
// paneExtraFormats.
func paneRow(id, session string, win, idx int, cmd string, active bool, status, screen, task, role, start string, panePID int) rtmux.PaneRow {
	return rtmux.PaneRow{
		ID: id, Session: session, WindowIndex: win, PaneIndex: idx, Command: cmd, Active: active,
		ServerPID: 1966, PID: panePID,
		Extra: map[string]string{
			"@claude_status": status, "@agent_screen": screen, "@claude_task": task,
			"@crew_role": role, "start_time": start,
		},
	}
}

func TestParseWindowOptions(t *testing.T) {
	// Real output shape, including the common all-empty-options case.
	out := winLine("lazytmux", "2", "relay", "1", "feat/320-relay", "#320", "565", "mauve", "open", "passing", "MERGEABLE", "", "", "colour168") + "\n" +
		winLine("houston", "1", "houston", "0", "main", "", "", "", "", "", "", "", "", "") + "\n"

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
	got := ParseWindowOptions("too\x1ffew\x1ffields\n\n" + winLine("houston", "1", "houston", "0", "main", "", "", "", "", "", "", "", "", "") + "\n")
	if len(got) != 1 {
		t.Fatalf("%d windows, want 1 — short and empty lines are skipped, not fatal", len(got))
	}
}

func TestParseWindowOptionsSurvivesPipeInFreeText(t *testing.T) {
	// @window_task is free text captured from a user prompt, so it can contain
	// anything. With a "|" delimiter this shifted GitRoot into garbage and
	// silently dropped the real value.
	out := winLine("houston", "1", "houston", "0", "main", "", "", "", "", "", "", "run x | grep fail", "/home/n/git/houston", "") + "\n"

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

func TestPaneOptionsFromRow(t *testing.T) {
	got := []PaneOptions{
		paneOptionsFromRow(paneRow("%307", "houston", 1, 0, "node", true, "processing 1788848628 ", "", "and add a ci task", "", "1790086864", 4521)),
		paneOptionsFromRow(paneRow("%283", "dispatcher", 1, 1, "bash", false, "", "", "", "", "1790086864", 4522)),
		paneOptionsFromRow(paneRow("%284", "dispatcher", 1, 2, "node", false, "waiting", "", "", "spec-critic", "1790086864", 4523)),
		paneOptionsFromRow(paneRow("%290", "houston", 2, 0, "pi", false, "", "processing 1790144005", "", "", "1790086864", 4524)),
	}
	if got[0].PaneID != "%307" || got[0].ClaudeStatus != "processing 1788848628 " || got[0].Target != "houston:1" {
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
		t.Errorf("non-grid pane CrewRole = %q, want empty", got[0].CrewRole)
	}
	if got[0].ServerPID != "1966" || got[0].ServerStart != 1790086864 {
		t.Errorf("ServerPID = %q ServerStart = %d, want 1966/1790086864", got[0].ServerPID, got[0].ServerStart)
	}
	if got[0].PanePID != 4521 {
		t.Errorf("PanePID = %d, want 4521", got[0].PanePID)
	}
	if got[1].ClaudeStatus != "" || got[1].Command != "bash" || got[1].Index != 1 || got[1].Active || got[1].PanePID != 4522 {
		t.Errorf("got %+v", got[1])
	}
	if got[2].CrewRole != "spec-critic" || got[2].PanePID != 4523 {
		t.Errorf("role pane = %+v, want spec-critic/4523", got[2])
	}
	if got[3].AgentScreen != "processing 1790144005" || got[3].ClaudeStatus != "" || got[3].Target != "houston:2" || got[3].PanePID != 4524 {
		t.Errorf("pi pane = %+v, want agent_screen set and no claude status", got[3])
	}
}

func TestPaneOptionsFromRowToleratesNonNumericStartTime(t *testing.T) {
	got := paneOptionsFromRow(paneRow("%307", "houston", 1, 0, "node", true, "", "", "", "", "sometime", 4525))
	if got.ServerPID != "1966" || got.ServerStart != 0 {
		t.Errorf("ServerPID = %q ServerStart = %d, want 1966/0 — an unexpandable start_time must not drop the pane", got.ServerPID, got.ServerStart)
	}
	if got.PanePID != 4525 {
		t.Errorf("PanePID = %d, want 4525", got.PanePID)
	}
}

// The foreign-pane guard in runs/hooksource.go reads the server identity off
// these listed rows, so dropping start_time from the extras would silently
// disable it. ServerPID comes from tmux-remux's base format.
func TestPaneExtraFormatsCarryTheServerIdentity(t *testing.T) {
	if !slices.Contains(paneExtraFormats, "start_time") {
		t.Error("extras lost start_time: the pane listing no longer identifies its server")
	}
}

// The screen-scraped engines (pi, codex, cursor) carry no @claude_status, so
// the pane listing must expose @agent_screen for a crew record to join them.
func TestPaneExtraFormatsCarryAgentScreen(t *testing.T) {
	if !slices.Contains(paneExtraFormats, "@agent_screen") {
		t.Error("extras lost @agent_screen: pi/codex/cursor panes would never join a crew record")
	}
}

func TestIsRolePane(t *testing.T) {
	for _, tc := range []struct {
		role string
		want bool
	}{
		{"", false},
		{CrewRoleLead, false},
		{"spec-critic", true},
		{"plan-critic", true},
		{"reviewer", true},
		{"refuter", true},
	} {
		if got := (PaneOptions{CrewRole: tc.role}).IsRolePane(); got != tc.want {
			t.Errorf("IsRolePane(%q) = %v, want %v", tc.role, got, tc.want)
		}
	}
}
