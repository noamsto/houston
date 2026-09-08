package tmux

import (
	"strconv"
	"strings"
)

// optSep separates fields below. tmux emits option values verbatim, and free
// text like @window_task is captured from a user prompt — it can contain "|"
// (e.g. "run x | grep y"), which would shift every later field. \x1f (ASCII
// unit separator) is not something a user types, so it round-trips safely.
const optSep = "\x1f"

// windowOptionsFormat harvests every lazytmux window user-option in one call.
// tmux resolves #{@name} inline, so no per-window show-options loop is needed.
const windowOptionsFormat = "#{session_name}" + optSep + "#{window_index}" + optSep + "#{@branch}" + optSep + "#{@issue_id}" + optSep +
	"#{@pr_number}" + optSep + "#{@crew_name}" + optSep + "#{@pr_state}" + optSep + "#{@pr_check_state}" + optSep + "#{@pr_mergeable}" + optSep +
	"#{@window_task}" + optSep + "#{@git_root}"

const paneOptionsFormat = "#{pane_id}" + optSep + "#{session_name}:#{window_index}" + optSep + "#{@claude_status}" + optSep + "#{@claude_task}"

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
}

type PaneOptions struct {
	PaneID       string
	Target       string // "session:window"
	ClaudeStatus string
	ClaudeTask   string
}

func (c *Client) ListWindowOptions() ([]WindowOptions, error) {
	out, err := c.output("list-windows", "-a", "-F", windowOptionsFormat)
	if err != nil {
		return nil, err
	}
	return ParseWindowOptions(string(out)), nil
}

func (c *Client) ListPaneOptions() ([]PaneOptions, error) {
	out, err := c.output("list-panes", "-a", "-F", paneOptionsFormat)
	if err != nil {
		return nil, err
	}
	return ParsePaneOptions(string(out)), nil
}

func ParseWindowOptions(out string) []WindowOptions {
	var res []WindowOptions
	for _, line := range strings.Split(out, "\n") {
		f := strings.Split(line, optSep)
		if len(f) != 11 {
			continue
		}
		idx, err := strconv.Atoi(f[1])
		if err != nil {
			continue
		}
		res = append(res, WindowOptions{
			Session: f[0], Window: idx, Branch: f[2], IssueID: f[3],
			PRNumber: f[4], CrewName: f[5], PRState: f[6], PRCheckState: f[7],
			PRMergeable: f[8], Task: f[9], GitRoot: f[10],
		})
	}
	return res
}

func ParsePaneOptions(out string) []PaneOptions {
	var res []PaneOptions
	for _, line := range strings.Split(out, "\n") {
		f := strings.Split(line, optSep)
		if len(f) != 4 {
			continue
		}
		res = append(res, PaneOptions{
			PaneID: f[0], Target: f[1], ClaudeStatus: f[2], ClaudeTask: f[3],
		})
	}
	return res
}
