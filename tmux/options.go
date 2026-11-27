package tmux

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	rtmux "github.com/noamsto/tmux-remux/pkg/tmux"
)

// windowExtraFormats and paneExtraFormats are the user options and format
// variables read beside tmux-remux's base fields, in one list-windows /
// list-panes call each: tmux resolves #{@name} inline, so no per-window
// show-options loop is needed. Free text like @window_task is captured from a
// user prompt; it may contain "|" but tmux-remux separates fields with \x1f,
// which a user does not type.
var windowExtraFormats = []string{
	"@branch", "@issue_id", "@pr_number", "@crew_name", "@pr_state",
	"@pr_check_state", "@pr_mergeable", "@window_task", "@git_root", "@crew_color",
}

var paneExtraFormats = []string{
	"@claude_status", "@agent_screen", "@claude_task", "@crew_role", "start_time",
}

// WindowOptions is lazytmux's per-window enrichment. Every field may be empty:
// a window with no linked issue or PR simply has none.
type WindowOptions struct {
	Session      string
	Window       int
	Branch       string
	IssueID      string
	PRNumber     string
	CrewName     string
	PRState      string
	PRCheckState string
	PRMergeable  string
	Task         string
	GitRoot      string
	CrewColor    string
	Name         string // #{window_name}
	Active       bool   // #{window_active}
}

// Target is the "session:window" join key PaneOptions.Target already carries
// verbatim. Exists so callers outside this package don't hand-duplicate this
// string build a second time.
func (w WindowOptions) Target() string {
	return fmt.Sprintf("%s:%d", w.Session, w.Window)
}

type PaneOptions struct {
	PaneID       string
	Target       string // "session:window"
	ClaudeStatus string
	// AgentScreen is #{@agent_screen}: the screen-scraped agent state for an
	// engine without Claude hooks (pi, codex, cursor), "<state> <epoch> [name=count …]".
	// Empty on a Claude pane, or one whose agent has left the screen.
	AgentScreen string
	ClaudeTask  string
	Command     string // #{pane_current_command}
	Index       int    // #{pane_index}
	Active      bool   // #{pane_active}
	CrewRole    string // #{@crew_role}: a role name on a role-grid pane, "lead" on a grid lead (the worker itself), empty outside a grid
	// ServerPID and ServerStart identify the tmux server that produced this
	// listing. Pane ids (e.g. %307) are unique only within one server
	// incarnation, so these are what a caller needs to tell a live pane from
	// one minted by a since-restarted server reusing the same id.
	ServerPID   string
	ServerStart int64
	PanePID     int // #{pane_pid}: the pane's first process — usually the login shell; 0 when unparseable
}

// CrewRoleLead is the @crew_role the dispatcher's grid contract stamps on the
// worker's own pane.
const CrewRoleLead = "lead"

// IsRolePane reports whether the pane is a role-grid pane (a critic or
// reviewer), as opposed to the worker itself or a pane outside a grid.
func (p PaneOptions) IsRolePane() bool {
	return p.CrewRole != "" && p.CrewRole != CrewRoleLead
}

// WithoutCrew returns a copy of wins with the crew fields cleared: tmux mode
// ignores crew enrichment.
func WithoutCrew(wins []WindowOptions) []WindowOptions {
	if wins == nil {
		return nil
	}
	out := make([]WindowOptions, len(wins))
	for i, w := range wins {
		w.CrewName, w.CrewColor = "", ""
		out[i] = w
	}
	return out
}

func (c *Client) ListWindowOptions() ([]WindowOptions, error) {
	// Run + parse rather than rtmux.Client.ListWindows, which would also read
	// decoration options once per window.
	rc := c.remux().SetExtraFormats(nil, windowExtraFormats, nil)
	out, err := rc.Run(context.Background(), []string{"list-windows", "-a", "-F", rc.WindowFormat()})
	if errors.Is(err, rtmux.ErrNoServer) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return ParseWindowOptions(out), nil
}

func (c *Client) ListPaneOptions() ([]PaneOptions, error) {
	rows, err := c.remux().SetExtraFormats(nil, nil, paneExtraFormats).ListPanes(context.Background())
	if err != nil {
		return nil, err
	}
	res := make([]PaneOptions, 0, len(rows))
	for _, r := range rows {
		res = append(res, paneOptionsFromRow(r))
	}
	return res, nil
}

// ParseWindowOptions parses list-windows output carrying windowExtraFormats.
// A line that does not parse (wrong field count, e.g. a value with a newline)
// is skipped on its own so it cannot drop the rest of the listing.
func ParseWindowOptions(out string) []WindowOptions {
	var res []WindowOptions
	for line := range strings.SplitSeq(out, "\n") {
		rows, err := rtmux.ParseWindowsExtra(line, windowExtraFormats)
		if err != nil || len(rows) != 1 {
			continue
		}
		r := rows[0]
		x := r.Extra
		res = append(res, WindowOptions{
			Session: r.Session, Window: r.Index, Branch: x["@branch"], IssueID: x["@issue_id"],
			PRNumber: x["@pr_number"], CrewName: x["@crew_name"], PRState: x["@pr_state"],
			PRCheckState: x["@pr_check_state"], PRMergeable: x["@pr_mergeable"],
			Task: x["@window_task"], GitRoot: x["@git_root"], CrewColor: x["@crew_color"],
			Name: r.Name, Active: r.Active,
		})
	}
	return res
}

func paneOptionsFromRow(r rtmux.PaneRow) PaneOptions {
	x := r.Extra
	// start_time is parsed leniently: this listing is TmuxSource's ground truth
	// for every pane's capabilities, so a strict parse would turn one
	// unexpandable field into "every pane vanished".
	start, _ := strconv.ParseInt(x["start_time"], 10, 64)
	var serverPID string // empty, as before, when tmux gave none
	if r.ServerPID != 0 {
		serverPID = strconv.Itoa(r.ServerPID)
	}
	return PaneOptions{
		PaneID: r.ID, Target: fmt.Sprintf("%s:%d", r.Session, r.WindowIndex),
		ClaudeStatus: x["@claude_status"], AgentScreen: x["@agent_screen"],
		ClaudeTask: x["@claude_task"], Command: r.Command, Index: r.PaneIndex,
		Active: r.Active, CrewRole: x["@crew_role"],
		ServerPID: serverPID, ServerStart: start, PanePID: r.PID,
	}
}
