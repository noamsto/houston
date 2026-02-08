package views

import (
	"github.com/noamsto/houston/agents"
	"github.com/noamsto/houston/opencode"
	"github.com/noamsto/houston/parser"
	"github.com/noamsto/houston/tmux"
)

// WindowWithStatus combines window info with its parse result
type WindowWithStatus struct {
	Window         tmux.Window
	Pane           tmux.Pane // The pane being monitored
	ParseResult    parser.Result
	Preview        []string // Last 2-3 lines for preview
	NeedsAttention bool
	Branch         string           // Git branch name (from worktree or git command)
	Process        string           // Running process (pane_current_command)
	AgentType      agents.AgentType // Type of agent running (claude-code, amp, generic)
}

// SessionWithWindows holds a session and all its windows with status
type SessionWithWindows struct {
	Session        tmux.Session
	Windows        []WindowWithStatus
	AttentionCount int  // Number of windows needing attention
	HasWorking     bool // At least one window is working
}

// SessionsData holds data for the sessions list
type SessionsData struct {
	NeedsAttention []SessionWithWindows // Sessions with windows needing attention
	Active         []SessionWithWindows // Sessions with working windows
	Idle           []SessionWithWindows // Sessions with all idle windows
}

// PaneData holds data for the pane view
type PaneData struct {
	Pane        tmux.Pane
	Output      string
	ParseResult parser.Result
	Windows     []tmux.Window
	Panes       []tmux.PaneInfo
	PaneWidth   int // columns
	PaneHeight  int // rows
	Suggestion  string // Initial prompt suggestion for Claude Code
}

// OpenCodeSession represents an OpenCode session for display.
type OpenCodeSession struct {
	State          opencode.SessionState
	NeedsAttention bool
	IsWorking      bool
	Preview        []string // Recent message excerpts
}

// OpenCodeData holds OpenCode sessions for display.
type OpenCodeData struct {
	NeedsAttention []OpenCodeSession
	Active         []OpenCodeSession
	Idle           []OpenCodeSession
	Servers        []*opencode.Server
}
