// Package generic provides a fallback agent for non-agent panes.
package generic

import (
	"github.com/noamsto/houston/agents"
	"github.com/noamsto/houston/parser"
)

// Agent is a no-op fallback for panes without a detected AI agent.
type Agent struct{}

// New creates a new generic agent.
func New() *Agent {
	return &Agent{}
}

func (a *Agent) Type() agents.AgentType {
	return agents.AgentGeneric
}

func (a *Agent) DetectFromOutput(_ string) bool {
	return false // Never auto-detect; only used as fallback
}

func (a *Agent) ParseOutput(_ string) agents.AgentState {
	return agents.AgentState{
		Agent:  agents.AgentGeneric,
		Result: parser.Result{Type: parser.TypeIdle},
	}
}

func (a *Agent) GetStateFromFiles(_ string) (*agents.AgentState, error) {
	state := agents.AgentState{
		Agent:  agents.AgentGeneric,
		Result: parser.Result{Type: parser.TypeIdle},
	}
	return &state, nil
}

func (a *Agent) FilterStatusBar(output string) string {
	return output // No filtering for generic
}

func (a *Agent) DetectMode(_ string) parser.Mode {
	return parser.ModeUnknown
}
