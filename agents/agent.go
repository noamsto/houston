// Package agents provides an abstraction for detecting and parsing AI coding agents.
package agents

import "github.com/noamsto/houston/parser"

// AgentType identifies which AI coding agent is running.
type AgentType string

const (
	AgentClaudeCode AgentType = "claude-code"
	AgentAmp        AgentType = "amp"
	AgentGeneric    AgentType = "generic"
)

// AgentState wraps parser.Result with agent metadata.
type AgentState struct {
	Agent  AgentType
	Result parser.Result
}

// Agent is the interface for AI coding agent implementations.
type Agent interface {
	// Type returns the agent type identifier.
	Type() AgentType

	// DetectFromOutput checks if ANSI-stripped terminal output matches this agent.
	DetectFromOutput(output string) bool

	// ParseOutput extracts state from terminal output.
	ParseOutput(output string) AgentState

	// GetStateFromFiles reads state from agent's file-based storage.
	// cwd is the pane's working directory used to locate relevant files.
	GetStateFromFiles(cwd string) (*AgentState, error)

	// FilterStatusBar removes agent-specific status bar elements from output.
	FilterStatusBar(output string) string

	// ExtractStatusLine extracts the agent's status line with ANSI colors intact.
	ExtractStatusLine(output string) string

	// DetectMode returns the vim-like mode if applicable (insert/normal).
	// Returns empty string for agents without mode support.
	DetectMode(output string) parser.Mode
}
