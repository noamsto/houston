// tmux/client.go
package tmux

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// cmdTimeout is the maximum time any single tmux command is allowed to run.
const cmdTimeout = 5 * time.Second

type Session struct {
	Name         string    `json:"name"`
	Created      time.Time `json:"created"`
	Windows      int       `json:"windows"`
	Attached     bool      `json:"attached"`
	LastActivity time.Time `json:"last_activity"`
}

type Window struct {
	Index        int       `json:"index"`
	Name         string    `json:"name"`
	Active       bool      `json:"active"`
	Panes        int       `json:"panes"`
	LastActivity time.Time `json:"last_activity"` // window_activity timestamp
	Path         string    `json:"path"`          // pane_current_path from active pane
	Branch       string    `json:"branch"`        // git branch name derived from Path
}

type Pane struct {
	Session string `json:"session"`
	Window  int    `json:"window"`
	Index   int    `json:"index"`
}

type PaneInfo struct {
	Index   int    `json:"index"`
	Active  bool   `json:"active"`
	Command string `json:"command"`
	Path    string `json:"path"`  // pane_current_path
	Title   string `json:"title"` // pane_title (can be set with nerd fonts)
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

// output runs a tmux command with a timeout and returns its stdout.
func (c *Client) output(args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), cmdTimeout)
	defer cancel()
	return exec.CommandContext(ctx, c.tmuxPath, args...).Output()
}

// run runs a tmux command with a timeout, discarding output.
func (c *Client) run(args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), cmdTimeout)
	defer cancel()
	return exec.CommandContext(ctx, c.tmuxPath, args...).Run()
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
	out, err := c.output("list-sessions", "-F",
		"#{session_name}|#{session_created}|#{session_windows}|#{session_attached}|#{session_activity}")
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
	out, err := c.output("list-windows", "-t", session, "-F",
		"#{window_index}|#{window_name}|#{window_active}|#{window_panes}|#{window_activity}|#{pane_current_path}")
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
	out, err := c.output("list-panes", "-t", target, "-F",
		"#{pane_index}|#{pane_active}|#{pane_current_command}|#{pane_current_path}|#{pane_title}")
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
	Output     string `json:"output"`
	CursorX    int    `json:"cursor_x"`     // 0-indexed cursor column in visible area
	CursorY    int    `json:"cursor_y"`     // 0-indexed cursor row in visible area
	PaneWidth  int    `json:"pane_width"`   // visible columns in the pane
	PaneHeight int    `json:"pane_height"`  // visible rows in the pane
	Mode       string `json:"mode"`         // "insert", "normal", or ""
	StatusLine string `json:"status_line"`  // Full status line with ANSI colors intact
}

func (c *Client) CapturePane(p Pane, lines int) (string, error) {
	result, err := c.CapturePaneWithMode(p, lines)
	if err != nil {
		return "", err
	}
	return result.Output, nil
}

func (c *Client) CapturePaneWithMode(p Pane, lines int) (CaptureResult, error) {
	out, err := c.output("capture-pane",
		"-t", p.Target(),
		"-p",
		"-e", // Include ANSI escape sequences (colors)
		"-N", // Preserve trailing blank lines (deterministic line count)
		"-S", fmt.Sprintf("-%d", lines))
	if err != nil {
		return CaptureResult{}, fmt.Errorf("capture-pane failed: %w", err)
	}

	raw := string(out)
	// Convert ESC symbol (␛, U+241B) to actual ESC character (\x1b) for ANSI processing
	raw = strings.ReplaceAll(raw, "␛", "\x1b")

	// Get cursor position and pane dimensions so seeds can sync xterm.js cursor
	var cursorX, cursorY, paneWidth, paneHeight int
	if cursorOut, err := c.output("display-message", "-t", p.Target(), "-p", "#{cursor_x},#{cursor_y},#{pane_width},#{pane_height}"); err == nil {
		parts := strings.Split(strings.TrimSpace(string(cursorOut)), ",")
		if len(parts) == 4 {
			cursorX, _ = strconv.Atoi(parts[0])
			cursorY, _ = strconv.Atoi(parts[1])
			paneWidth, _ = strconv.Atoi(parts[2])
			paneHeight, _ = strconv.Atoi(parts[3])
		}
	}

	// Return raw output - agent-specific filtering done by caller
	return CaptureResult{
		Output:     raw,
		CursorX:    cursorX,
		CursorY:    cursorY,
		PaneWidth:  paneWidth,
		PaneHeight: paneHeight,
		Mode:       "", // Agent-specific; set by caller
		StatusLine: "", // Agent-specific; set by caller
	}, nil
}





// GetPaneID returns the tmux pane ID (e.g. "%42") for a given pane target.
func (c *Client) GetPaneID(p Pane) (string, error) {
	out, err := c.output("display-message", "-t", p.Target(), "-p", "#{pane_id}")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// SendRawKeys sends literal text to a pane using hex encoding (-H).
// This safely passes any byte including control characters.
func (c *Client) SendRawKeys(p Pane, text string) error {
	var hexBytes strings.Builder
	for i := 0; i < len(text); i++ {
		if i > 0 {
			hexBytes.WriteByte(' ')
		}
		fmt.Fprintf(&hexBytes, "%02x", text[i])
	}
	return c.run("send-keys", "-t", p.Target(), "-H", hexBytes.String())
}

func (c *Client) SendKeys(p Pane, keys string, enter bool) error {
	// Use -l for literal text to avoid interpreting special characters
	if err := c.run("send-keys", "-t", p.Target(), "-l", keys); err != nil {
		return err
	}

	// Send Enter separately (not literal)
	if enter {
		return c.run("send-keys", "-t", p.Target(), "Enter")
	}
	return nil
}

func (c *Client) SendSpecialKey(p Pane, key string) error {
	return c.run("send-keys", "-t", p.Target(), key)
}

// GetPaneLocation finds the window and pane index for a given pane ID
// Returns window index, pane index, and error
func (c *Client) GetPaneLocation(session string, paneID int) (int, int, error) {
	out, err := c.output("list-panes", "-s", "-t", session, "-F",
		"#{pane_id}|#{window_index}|#{pane_index}")
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
	return c.run("kill-pane", "-t", p.Target())
}

// RespawnPane kills the current process and respawns the pane
func (c *Client) RespawnPane(p Pane) error {
	return c.run("respawn-pane", "-k", "-t", p.Target())
}

// KillWindow closes a window
func (c *Client) KillWindow(session string, window int) error {
	target := fmt.Sprintf("%s:%d", session, window)
	return c.run("kill-window", "-t", target)
}

// ResizePane resizes a pane by the given adjustment in lines/columns.
// direction: "U" (up), "D" (down), "L" (left), "R" (right)
// adjustment: number of lines/columns to resize by (default 5)
func (c *Client) ResizePane(p Pane, direction string, adjustment int) error {
	if adjustment <= 0 {
		adjustment = 5
	}
	return c.run("resize-pane", "-t", p.Target(), "-"+direction, strconv.Itoa(adjustment))
}

// ZoomPane toggles zoom on a pane (maximizes/restores).
func (c *Client) ZoomPane(p Pane) error {
	return c.run("resize-pane", "-t", p.Target(), "-Z")
}

// IsZoomed returns whether the pane's window is currently in zoomed state.
func (c *Client) IsZoomed(p Pane) (bool, error) {
	out, err := c.output("display-message", "-t", p.Target(), "-p", "#{window_zoomed_flag}")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(string(out)) == "1", nil
}

// WindowPaneCount returns the number of panes in the pane's window.
func (c *Client) WindowPaneCount(p Pane) (int, error) {
	out, err := c.output("display-message", "-t", p.Target(), "-p", "#{window_panes}")
	if err != nil {
		return 0, err
	}
	n, _ := strconv.Atoi(strings.TrimSpace(string(out)))
	return n, nil
}

// ForceRedraw sends SIGWINCH to the pane's foreground process by resizing
// to the current dimensions. This forces TUI apps to redraw.
func (c *Client) ForceRedraw(p Pane) error {
	w, h, err := c.GetPaneSize(p)
	if err != nil {
		return err
	}
	return c.run("resize-pane", "-t", p.Target(), "-x", strconv.Itoa(w), "-y", strconv.Itoa(h))
}

// GetPaneSize returns the width and height of a pane.
func (c *Client) GetPaneSize(p Pane) (width, height int, err error) {
	out, err := c.output("display-message", "-t", p.Target(), "-p", "#{pane_width}x#{pane_height}")
	if err != nil {
		return 0, 0, err
	}
	parts := strings.Split(strings.TrimSpace(string(out)), "x")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("unexpected output format: %s", string(out))
	}
	width, _ = strconv.Atoi(parts[0])
	height, _ = strconv.Atoi(parts[1])
	return width, height, nil
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

// GetWindowSize returns the width and height of a tmux window.
func (c *Client) GetWindowSize(session string, window int) (width, height int, err error) {
	target := fmt.Sprintf("%s:%d", session, window)
	out, err := c.output("display-message", "-t", target, "-p", "#{window_width}x#{window_height}")
	if err != nil {
		return 0, 0, err
	}
	parts := strings.Split(strings.TrimSpace(string(out)), "x")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("unexpected output format: %s", string(out))
	}
	width, _ = strconv.Atoi(parts[0])
	height, _ = strconv.Atoi(parts[1])
	return width, height, nil
}

// ResizeWindow sets the absolute width and height of a tmux window.
// This works even when resize-pane is capped by the window dimensions.
func (c *Client) ResizeWindow(session string, window int, cols, rows int) error {
	target := fmt.Sprintf("%s:%d", session, window)
	return c.run("resize-window", "-t", target, "-x", strconv.Itoa(cols), "-y", strconv.Itoa(rows))
}
