package server

import (
	"github.com/noamsto/houston/agents"
	"github.com/noamsto/houston/opencode"
	"github.com/noamsto/houston/parser"
	"github.com/noamsto/houston/tmux"
)

// WindowWithStatus combines window info with its parse result
type WindowWithStatus struct {
	Window         tmux.Window      `json:"window"`
	Pane           tmux.Pane        `json:"pane"`
	ParseResult    parser.Result    `json:"parse_result"`
	Preview        []string         `json:"preview"`
	NeedsAttention bool             `json:"needs_attention"`
	Branch         string           `json:"branch"`
	Process        string           `json:"process"`
	AgentType      agents.AgentType `json:"agent_type"`
}

// SessionWithWindows holds a session and all its windows with status
type SessionWithWindows struct {
	Session        tmux.Session       `json:"session"`
	Windows        []WindowWithStatus `json:"windows"`
	AttentionCount int                `json:"attention_count"`
	HasWorking     bool               `json:"has_working"`
}

// SessionsData holds data for the sessions list
type SessionsData struct {
	NeedsAttention []SessionWithWindows `json:"needs_attention"`
	Active         []SessionWithWindows `json:"active"`
	Idle           []SessionWithWindows `json:"idle"`
}

// AgentStripItem represents one agent in the strip bar
type AgentStripItem struct {
	Session   string           `json:"session"`
	Window    int              `json:"window"`
	Pane      int              `json:"pane"`
	Name      string           `json:"name"`
	Indicator string           `json:"indicator"`
	AgentType agents.AgentType `json:"agent_type"`
	Active    bool             `json:"active"`
}

// PaneData holds data for the pane view
type PaneData struct {
	Pane        tmux.Pane        `json:"pane"`
	Output      string           `json:"output"`
	ParseResult parser.Result    `json:"parse_result"`
	Windows     []tmux.Window    `json:"windows"`
	Panes       []tmux.PaneInfo  `json:"panes"`
	PaneWidth   int              `json:"pane_width"`
	PaneHeight  int              `json:"pane_height"`
	Suggestion  string           `json:"suggestion"`
	StripItems  []AgentStripItem `json:"strip_items"`
}

// OpenCodeSession represents an OpenCode session for display.
type OpenCodeSession struct {
	State          opencode.SessionState `json:"state"`
	NeedsAttention bool                  `json:"needs_attention"`
	IsWorking      bool                  `json:"is_working"`
	Preview        []string              `json:"preview"`
}

// OpenCodeData holds OpenCode sessions for display.
type OpenCodeData struct {
	NeedsAttention []OpenCodeSession  `json:"needs_attention"`
	Active         []OpenCodeSession  `json:"active"`
	Idle           []OpenCodeSession  `json:"idle"`
	Servers        []*opencode.Server `json:"servers"`
}
