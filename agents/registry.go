package agents

import (
	"strings"
	"sync"
	"time"

	"github.com/noamsto/houston/internal/ansi"
)

const detectionTTL = 15 * time.Second

type cachedDetection struct {
	agentType AgentType
	expiresAt time.Time
}

// Registry manages agent detection and caching.
type Registry struct {
	agents  []Agent
	cache   map[string]cachedDetection
	cacheMu sync.RWMutex
}

// NewRegistry creates a registry with the given agents.
// Agents are checked in order during detection.
func NewRegistry(agents ...Agent) *Registry {
	return &Registry{
		agents: agents,
		cache:  make(map[string]cachedDetection),
	}
}

// Detect identifies which agent (if any) is running in a pane.
// paneID is used for caching, command is from tmux pane_current_command,
// output is raw terminal output (ANSI will be stripped internally).
func (r *Registry) Detect(paneID, command, output string) Agent {
	// Check cache first
	r.cacheMu.RLock()
	if cached, ok := r.cache[paneID]; ok && time.Now().Before(cached.expiresAt) {
		r.cacheMu.RUnlock()
		return r.getAgent(cached.agentType)
	}
	r.cacheMu.RUnlock()

	// Try command-based detection first (cheapest)
	if agentType := detectFromCommand(command); agentType != AgentGeneric {
		r.cacheResult(paneID, agentType)
		return r.getAgent(agentType)
	}

	// Fall back to output-based detection
	stripped := ansi.Strip(output)
	for _, a := range r.agents {
		if a.DetectFromOutput(stripped) {
			r.cacheResult(paneID, a.Type())
			return a
		}
	}

	r.cacheResult(paneID, AgentGeneric)
	return r.getAgent(AgentGeneric)
}

// InvalidateCache removes a pane from the cache.
func (r *Registry) InvalidateCache(paneID string) {
	r.cacheMu.Lock()
	delete(r.cache, paneID)
	r.cacheMu.Unlock()
}

// GetAgent returns the agent implementation for a type.
func (r *Registry) GetAgent(agentType AgentType) Agent {
	return r.getAgent(agentType)
}

func (r *Registry) getAgent(agentType AgentType) Agent {
	for _, a := range r.agents {
		if a.Type() == agentType {
			return a
		}
	}
	// Return last agent (should be generic fallback)
	if len(r.agents) > 0 {
		return r.agents[len(r.agents)-1]
	}
	return nil
}

func (r *Registry) cacheResult(paneID string, agentType AgentType) {
	r.cacheMu.Lock()
	r.cache[paneID] = cachedDetection{
		agentType: agentType,
		expiresAt: time.Now().Add(detectionTTL),
	}
	r.cacheMu.Unlock()
}

// detectFromCommand checks tmux pane_current_command for agent patterns.
func detectFromCommand(command string) AgentType {
	cmd := strings.ToLower(command)
	switch {
	case strings.Contains(cmd, "claude"):
		return AgentClaudeCode
	case strings.Contains(cmd, "amp"):
		return AgentAmp
	default:
		return AgentGeneric
	}
}
