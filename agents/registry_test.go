package agents

import (
	"testing"
)

func TestDetectFromCommand(t *testing.T) {
	tests := []struct {
		command string
		want    AgentType
	}{
		{"claude", AgentClaudeCode},
		{"claude-code", AgentClaudeCode},
		{"/nix/store/.../bin/claude", AgentClaudeCode},
		{"amp", AgentAmp},
		{"amp-cli", AgentAmp},
		{"bash", AgentGeneric},
		{"zsh", AgentGeneric},
		{"node", AgentGeneric},
		{"vim", AgentGeneric},
		{"", AgentGeneric},
	}

	for _, tt := range tests {
		t.Run(tt.command, func(t *testing.T) {
			got := detectFromCommand(tt.command)
			if got != tt.want {
				t.Errorf("detectFromCommand(%q) = %v, want %v", tt.command, got, tt.want)
			}
		})
	}
}
