// Package claude implements the Agent interface for Claude Code.
package claude

import (
	"github.com/noamsto/houston/agents"
	"github.com/noamsto/houston/parser"
)

// Agent implements agents.Agent for Claude Code.
type Agent struct{}

// New creates a new Claude Code agent.
func New() *Agent {
	return &Agent{}
}

func (a *Agent) Type() agents.AgentType {
	return agents.AgentClaudeCode
}

func (a *Agent) DetectFromOutput(output string) bool {
	return DetectFromOutput(output)
}

func (a *Agent) ParseOutput(output string) agents.AgentState {
	result := parser.Parse(output)
	return agents.AgentState{
		Agent:  agents.AgentClaudeCode,
		Result: result,
	}
}

func (a *Agent) GetStateFromFiles(cwd string) (*agents.AgentState, error) {
	state, err := GetStateFromFiles(cwd)
	if err != nil {
		return nil, err
	}
	return &agents.AgentState{
		Agent:  agents.AgentClaudeCode,
		Result: *state,
	}, nil
}

func (a *Agent) FilterStatusBar(output string) string {
	return FilterStatusBar(output)
}

func (a *Agent) DetectMode(output string) parser.Mode {
	return DetectMode(output)
}
