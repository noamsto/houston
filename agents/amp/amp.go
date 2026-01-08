// Package amp implements the Agent interface for Amp (Sourcegraph's AI coding agent).
package amp

import (
	"github.com/noamsto/houston/agents"
	"github.com/noamsto/houston/parser"
)

// Agent implements agents.Agent for Amp.
type Agent struct {
	threadsDir string // ~/.local/share/amp/threads/
	stateDir   string // ~/.local/state/amp/
}

// New creates a new Amp agent with default paths.
func New() *Agent {
	return &Agent{
		threadsDir: getThreadsDir(),
		stateDir:   getStateDir(),
	}
}

func (a *Agent) Type() agents.AgentType {
	return agents.AgentAmp
}

func (a *Agent) DetectFromOutput(output string) bool {
	return DetectFromOutput(output)
}

func (a *Agent) ParseOutput(output string) agents.AgentState {
	result := ParseOutput(output)
	return agents.AgentState{
		Agent:  agents.AgentAmp,
		Result: result,
	}
}

func (a *Agent) GetStateFromFiles(cwd string) (*agents.AgentState, error) {
	state, err := GetStateFromFiles(a.threadsDir, a.stateDir, cwd)
	if err != nil {
		return nil, err
	}
	return &agents.AgentState{
		Agent:  agents.AgentAmp,
		Result: *state,
	}, nil
}

func (a *Agent) FilterStatusBar(output string) string {
	return FilterStatusBar(output)
}

func (a *Agent) DetectMode(_ string) parser.Mode {
	return parser.ModeUnknown // Amp has no vim modes
}
