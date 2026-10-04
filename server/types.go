package server

import (
	"github.com/noamsto/houston/opencode"
)

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
