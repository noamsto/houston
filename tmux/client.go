// tmux/client.go
package tmux

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

type Session struct {
	Name         string
	Created      time.Time
	Windows      int
	Attached     bool
	LastActivity time.Time
}

type Window struct {
	Index  int
	Name   string
	Active bool
	Panes  int
}

type Pane struct {
	Session string
	Window  int
	Index   int
}

type PaneInfo struct {
	Index   int
	Active  bool
	Command string
}

func (p Pane) Target() string {
	// If window/pane are default (0), just use session name
	// This lets tmux pick the active window/pane
	if p.Window == 0 && p.Index == 0 {
		return p.Session
	}
	return fmt.Sprintf("%s:%d.%d", p.Session, p.Window, p.Index)
}

// URLTarget returns a URL-safe version of Target() for use in URLs.
// Session names with / are encoded to %2F.
func (p Pane) URLTarget() string {
	target := p.Target()
	return strings.ReplaceAll(target, "/", "%2F")
}

type Client struct {
	tmuxPath string
}

func NewClient() *Client {
	return &Client{tmuxPath: "tmux"}
}

func parseSessionLine(line string) (Session, error) {
	parts := strings.Split(line, "|")
	if len(parts) != 5 {
		return Session{}, fmt.Errorf("invalid session line: %s", line)
	}

	created, _ := strconv.ParseInt(parts[1], 10, 64)
	windows, _ := strconv.Atoi(parts[2])
	attached := parts[3] == "1"
	activity, _ := strconv.ParseInt(parts[4], 10, 64)

	return Session{
		Name:         parts[0],
		Created:      time.Unix(created, 0),
		Windows:      windows,
		Attached:     attached,
		LastActivity: time.Unix(activity, 0),
	}, nil
}

func (c *Client) ListSessions() ([]Session, error) {
	cmd := exec.Command(c.tmuxPath, "list-sessions", "-F",
		"#{session_name}|#{session_created}|#{session_windows}|#{session_attached}|#{session_activity}")

	out, err := cmd.Output()
	if err != nil {
		if strings.Contains(err.Error(), "no server running") {
			return nil, nil
		}
		return nil, err
	}

	var sessions []Session
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		s, err := parseSessionLine(line)
		if err != nil {
			continue
		}
		sessions = append(sessions, s)
	}

	return sessions, nil
}

func (c *Client) ListWindows(session string) ([]Window, error) {
	cmd := exec.Command(c.tmuxPath, "list-windows", "-t", session, "-F",
		"#{window_index}|#{window_name}|#{window_active}|#{window_panes}")

	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	var windows []Window
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		parts := strings.Split(line, "|")
		if len(parts) != 4 {
			continue
		}
		idx, _ := strconv.Atoi(parts[0])
		active := parts[2] == "1"
		panes, _ := strconv.Atoi(parts[3])
		windows = append(windows, Window{
			Index:  idx,
			Name:   parts[1],
			Active: active,
			Panes:  panes,
		})
	}

	return windows, nil
}

func (c *Client) ListPanes(session string, window int) ([]PaneInfo, error) {
	target := fmt.Sprintf("%s:%d", session, window)
	cmd := exec.Command(c.tmuxPath, "list-panes", "-t", target, "-F",
		"#{pane_index}|#{pane_active}|#{pane_current_command}")

	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	var panes []PaneInfo
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		parts := strings.Split(line, "|")
		if len(parts) != 3 {
			continue
		}
		idx, _ := strconv.Atoi(parts[0])
		active := parts[1] == "1"
		panes = append(panes, PaneInfo{
			Index:   idx,
			Active:  active,
			Command: parts[2],
		})
	}

	return panes, nil
}

// CaptureResult holds the captured pane output and detected mode
type CaptureResult struct {
	Output string
	Mode   string // "insert", "normal", or ""
}

func (c *Client) CapturePane(p Pane, lines int) (string, error) {
	result, err := c.CapturePaneWithMode(p, lines)
	if err != nil {
		return "", err
	}
	return result.Output, nil
}

func (c *Client) CapturePaneWithMode(p Pane, lines int) (CaptureResult, error) {
	cmd := exec.Command(c.tmuxPath, "capture-pane",
		"-t", p.Target(),
		"-p",
		"-S", fmt.Sprintf("-%d", lines))

	out, err := cmd.Output()
	if err != nil {
		return CaptureResult{}, fmt.Errorf("capture-pane failed: %w", err)
	}

	raw := string(out)
	mode := detectModeFromOutput(raw)
	filtered := filterStatusBar(raw)

	return CaptureResult{Output: filtered, Mode: mode}, nil
}

// detectModeFromOutput checks for INSERT mode in the last few lines of output.
// The mode indicator appears at the bottom of the terminal.
// If INSERT isn't shown, we're in NORMAL mode (Claude Code only shows -- INSERT --).
func detectModeFromOutput(output string) string {
	lines := strings.Split(output, "\n")
	// Only check last 5 lines where status bar appears
	start := len(lines) - 5
	if start < 0 {
		start = 0
	}
	bottomLines := strings.Join(lines[start:], "\n")
	if strings.Contains(bottomLines, "-- INSERT --") {
		return "insert"
	}
	return "normal"
}

// filterStatusBar removes Claude Code status bar lines from output.
// The status bar includes horizontal lines, mode indicators, and context info.
func filterStatusBar(output string) string {
	lines := strings.Split(output, "\n")
	var filtered []string

	for _, line := range lines {
		if isStatusBarLine(line) {
			continue
		}
		filtered = append(filtered, line)
	}

	return strings.Join(filtered, "\n")
}

func isStatusBarLine(line string) bool {
	trimmed := strings.TrimSpace(line)

	// Skip empty lines at the end (but keep them in content)
	if trimmed == "" {
		return false
	}

	// Horizontal separator lines (─────)
	if len(trimmed) > 10 && strings.Count(trimmed, "─") > len(trimmed)/2 {
		return true
	}

	// Mode indicators
	if strings.Contains(line, "-- INSERT --") || strings.Contains(line, "-- NORMAL --") {
		return true
	}

	// Status line with model info, context, cost
	if strings.Contains(line, "🤖") || strings.Contains(line, "📊") || strings.Contains(line, "💬") {
		return true
	}

	// Status line with directory/git info
	if strings.Contains(line, "❄") && strings.Contains(line, "📂") {
		return true
	}

	// Note: We keep ">" and ">>>" prompts - users need to see them

	return false
}

func (c *Client) SendKeys(p Pane, keys string, enter bool) error {
	// Use -l for literal text to avoid interpreting special characters
	args := []string{"send-keys", "-t", p.Target(), "-l", keys}
	cmd := exec.Command(c.tmuxPath, args...)
	if err := cmd.Run(); err != nil {
		return err
	}

	// Send Enter separately (not literal)
	if enter {
		cmd = exec.Command(c.tmuxPath, "send-keys", "-t", p.Target(), "Enter")
		return cmd.Run()
	}
	return nil
}

func (c *Client) SendSpecialKey(p Pane, key string) error {
	cmd := exec.Command(c.tmuxPath, "send-keys", "-t", p.Target(), key)
	return cmd.Run()
}

// GetPaneLocation finds the window and pane index for a given pane ID
// Returns window index, pane index, and error
func (c *Client) GetPaneLocation(session string, paneID int) (int, int, error) {
	// List all panes in session with their IDs
	cmd := exec.Command(c.tmuxPath, "list-panes", "-s", "-t", session, "-F",
		"#{pane_id}|#{window_index}|#{pane_index}")

	out, err := cmd.Output()
	if err != nil {
		return 0, 0, err
	}

	target := fmt.Sprintf("%%%d", paneID)
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		parts := strings.Split(line, "|")
		if len(parts) != 3 {
			continue
		}
		if parts[0] == target {
			winIdx, _ := strconv.Atoi(parts[1])
			paneIdx, _ := strconv.Atoi(parts[2])
			return winIdx, paneIdx, nil
		}
	}

	return 0, 0, fmt.Errorf("pane %%%d not found", paneID)
}
