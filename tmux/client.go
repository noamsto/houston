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
	Index        int
	Name         string
	Active       bool
	Panes        int
	LastActivity time.Time // window_activity timestamp
	Path         string    // pane_current_path from active pane
	Branch       string    // git branch name derived from Path
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
	Path    string // pane_current_path
	Title   string // pane_title (can be set with nerd fonts)
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
		"#{window_index}|#{window_name}|#{window_active}|#{window_panes}|#{window_activity}|#{pane_current_path}")

	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	var windows []Window
	var firstPath string // Use first window path to get worktrees

	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		parts := strings.Split(line, "|")
		if len(parts) < 6 {
			continue
		}
		idx, _ := strconv.Atoi(parts[0])
		active := parts[2] == "1"
		panes, _ := strconv.Atoi(parts[3])
		var lastActivity time.Time
		if len(parts) >= 5 {
			activityTs, _ := strconv.ParseInt(parts[4], 10, 64)
			lastActivity = time.Unix(activityTs, 0)
		}
		path := ""
		if len(parts) >= 6 {
			path = parts[5]
			if firstPath == "" {
				firstPath = path
			}
		}
		windows = append(windows, Window{
			Index:        idx,
			Name:         parts[1],
			Active:       active,
			Panes:        panes,
			LastActivity: lastActivity,
			Path:         path,
		})
	}

	// Get worktrees and populate branch names
	if firstPath != "" {
		worktrees, _ := GetWorktrees(firstPath)
		for i := range windows {
			windows[i].Branch = GetBranchForPath(windows[i].Path, worktrees)
		}
	}

	return windows, nil
}

func (c *Client) ListPanes(session string, window int) ([]PaneInfo, error) {
	target := fmt.Sprintf("%s:%d", session, window)
	cmd := exec.Command(c.tmuxPath, "list-panes", "-t", target, "-F",
		"#{pane_index}|#{pane_active}|#{pane_current_command}|#{pane_current_path}|#{pane_title}")

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
		if len(parts) < 3 {
			continue
		}
		idx, _ := strconv.Atoi(parts[0])
		active := parts[1] == "1"
		path := ""
		if len(parts) >= 4 {
			path = parts[3]
		}
		title := ""
		if len(parts) >= 5 {
			title = parts[4]
		}
		panes = append(panes, PaneInfo{
			Index:   idx,
			Active:  active,
			Command: parts[2],
			Path:    path,
			Title:   title,
		})
	}

	return panes, nil
}

// CaptureResult holds the captured pane output and detected mode
type CaptureResult struct {
	Output     string
	Mode       string // "insert", "normal", or ""
	StatusLine string // Full status line with ANSI colors intact
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
	statusLine := extractStatusLine(raw)
	filtered := filterStatusBar(raw)

	return CaptureResult{
		Output:     filtered,
		Mode:       mode,
		StatusLine: statusLine,
	}, nil
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

// extractStatusLine finds Claude's status bar line with ANSI colors intact.
// Returns the full status line including ANSI escape sequences for frontend display.
func extractStatusLine(output string) string {
	lines := strings.Split(output, "\n")

	// Check last 20 lines for status bar (separator might be further up)
	start := len(lines) - 20
	if start < 0 {
		start = 0
	}

	// Strategy: Find the LAST horizontal separator (after user input block)
	// and return first non-empty line after it
	// Structure:
	//   ───── (separator 1)
	//   > user input line 1
	//     user input line 2 (indented continuation - multiline)
	//     (blank lines)
	//   ───── (separator 2) <- we want the LAST one
	//   (blank lines)
	//   status line <- this is what we return
	//   -- INSERT --
	//
	// By finding the LAST separator, we automatically skip over all user input
	// regardless of how many lines it spans

	lastSeparatorIdx := -1

	// Find the last separator in the bottom section
	for i := start; i < len(lines); i++ {
		trimmed := strings.TrimSpace(lines[i])

		// Check if this is a horizontal separator line
		// Line may have ANSI codes, so just check for sufficient dashes
		dashCount := strings.Count(trimmed, "─")
		isSeparator := dashCount >= 20

		if isSeparator {
			lastSeparatorIdx = i
		}
	}

	// If we found a separator, get the first non-empty line after it
	if lastSeparatorIdx >= 0 {
		for j := lastSeparatorIdx + 1; j < len(lines); j++ {
			line := lines[j]
			trimmed := strings.TrimSpace(line)

			// Skip empty lines
			if trimmed == "" {
				continue
			}

			// Skip mode indicator line (we want status, not mode)
			// Mode line can be just "-- INSERT --" or "-- INSERT -- ⏸ plan mode on..."
			if strings.HasPrefix(trimmed, "-- INSERT --") {
				continue
			}

			// This is our status line - return with ANSI intact but trim whitespace
			return strings.TrimSpace(line)
		}
	}

	return ""
}

// filterStatusBar removes Claude Code status bar lines from output.
// The status bar includes horizontal lines, mode indicators, and context info.
// Only filters if output looks like Claude Code (has characteristic markers).
func filterStatusBar(output string) string {
	// Only filter if this looks like Claude Code output
	if !LooksLikeClaudeOutput(output) {
		return output
	}

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

// LooksLikeClaudeOutput checks if output appears to be from Claude Code
func LooksLikeClaudeOutput(output string) bool {
	// Claude Code has characteristic status bar elements
	claudeMarkers := []string{
		"-- INSERT --",
		"-- NORMAL --",
		"🤖",  // Model indicator
		"📊",  // Stats
		"💬",  // Messages
	}
	for _, marker := range claudeMarkers {
		if strings.Contains(output, marker) {
			return true
		}
	}

	// Also check for Claude conversation patterns
	// These appear in the output itself, not just status bar
	conversationMarkers := []string{
		"Claude:",           // Claude's responses
		"Human:",            // User messages in transcript
		">>>",               // Claude Code prompt
		"Do you want to",    // Common Claude question pattern
		"Would you like",    // Common Claude question pattern
		"(Recommended)",     // Choice recommendation
		"[Y/n]",             // Yes/no prompt
		"[y/N]",             // Yes/no prompt
		"Select an option",  // Choice prompt
	}
	for _, marker := range conversationMarkers {
		if strings.Contains(output, marker) {
			return true
		}
	}

	return false
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

// KillPane closes a pane
func (c *Client) KillPane(p Pane) error {
	cmd := exec.Command(c.tmuxPath, "kill-pane", "-t", p.Target())
	return cmd.Run()
}

// RespawnPane kills the current process and respawns the pane
func (c *Client) RespawnPane(p Pane) error {
	// -k flag kills the current process first
	cmd := exec.Command(c.tmuxPath, "respawn-pane", "-k", "-t", p.Target())
	return cmd.Run()
}

// KillWindow closes a window
func (c *Client) KillWindow(session string, window int) error {
	target := fmt.Sprintf("%s:%d", session, window)
	cmd := exec.Command(c.tmuxPath, "kill-window", "-t", target)
	return cmd.Run()
}

// Worktree represents a git worktree with its path and branch
type Worktree struct {
	Path   string
	Branch string
}

// GetWorktrees returns all git worktrees for a repository.
// The path should be any directory within the git repo.
// Returns a map of absolute path -> branch name.
func GetWorktrees(path string) (map[string]string, error) {
	if path == "" {
		return nil, nil
	}

	cmd := exec.Command("git", "-C", path, "worktree", "list", "--porcelain")
	out, err := cmd.Output()
	if err != nil {
		// Not a git repo or no worktrees
		return nil, nil
	}

	result := make(map[string]string)
	var currentPath string

	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "worktree ") {
			currentPath = strings.TrimPrefix(line, "worktree ")
		} else if strings.HasPrefix(line, "branch ") {
			branch := strings.TrimPrefix(line, "branch refs/heads/")
			if currentPath != "" {
				result[currentPath] = branch
			}
		}
	}

	return result, nil
}

// GetBranchForPath returns the git branch for a specific path.
// First tries worktree matching, then falls back to git branch command.
func GetBranchForPath(path string, worktrees map[string]string) string {
	if path == "" {
		return ""
	}

	// Try exact match first
	if branch, ok := worktrees[path]; ok {
		return branch
	}

	// Try to find if path is under a worktree
	for wtPath, branch := range worktrees {
		if strings.HasPrefix(path, wtPath+"/") || path == wtPath {
			return branch
		}
	}

	// Fallback: run git branch --show-current
	cmd := exec.Command("git", "-C", path, "branch", "--show-current")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}

	return strings.TrimSpace(string(out))
}
