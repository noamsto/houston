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
	Output     string `json:"output"`
	Mode       string `json:"mode"`        // "insert", "normal", or ""
	StatusLine string `json:"status_line"` // Full status line with ANSI colors intact
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
		"-e", // Include ANSI escape sequences (colors)
		"-S", fmt.Sprintf("-%d", lines))

	out, err := cmd.Output()
	if err != nil {
		return CaptureResult{}, fmt.Errorf("capture-pane failed: %w", err)
	}

	raw := string(out)
	// Convert ESC symbol (␛, U+241B) to actual ESC character (\x1b) for ANSI processing
	raw = strings.ReplaceAll(raw, "␛", "\x1b")

	// Return raw output - agent-specific filtering done by caller
	return CaptureResult{
		Output:     raw,
		Mode:       "", // Agent-specific; set by caller
		StatusLine: "", // Agent-specific; set by caller
	}, nil
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

// ResizePane resizes a pane by the given adjustment in lines/columns.
// direction: "U" (up), "D" (down), "L" (left), "R" (right)
// adjustment: number of lines/columns to resize by (default 5)
func (c *Client) ResizePane(p Pane, direction string, adjustment int) error {
	if adjustment <= 0 {
		adjustment = 5
	}
	flag := "-" + direction
	cmd := exec.Command(c.tmuxPath, "resize-pane", "-t", p.Target(), flag, strconv.Itoa(adjustment))
	return cmd.Run()
}

// ZoomPane toggles zoom on a pane (maximizes/restores).
func (c *Client) ZoomPane(p Pane) error {
	cmd := exec.Command(c.tmuxPath, "resize-pane", "-t", p.Target(), "-Z")
	return cmd.Run()
}

// GetPaneSize returns the width and height of a pane.
func (c *Client) GetPaneSize(p Pane) (width, height int, err error) {
	cmd := exec.Command(c.tmuxPath, "display-message", "-t", p.Target(), "-p", "#{pane_width}x#{pane_height}")
	out, err := cmd.Output()
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
