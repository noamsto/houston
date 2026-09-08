package runs

import (
	"strings"

	"github.com/noamsto/houston/hook"
)

// State is houston's single vocabulary for what a run is doing. Three upstream
// vocabularies map into it — Claude Code hooks, the dispatcher crew bus, and
// lazytmux's @claude_status — and they disagree with each other, so this is the
// only place the reconciliation lives.
type State string

const (
	StateThinking   State = "thinking"
	StateRunning    State = "running"
	StateBlocked    State = "blocked"
	StateCompacting State = "compacting"
	StateReview     State = "review"
	StateDone       State = "done"
	StateFailed     State = "failed"
	StateIdle       State = "idle"
)

// AllStates lists every state, for exhaustiveness tests.
func AllStates() []State {
	return []State{
		StateThinking, StateRunning, StateBlocked, StateCompacting,
		StateReview, StateDone, StateFailed, StateIdle,
	}
}

// NeedsAttention reports whether a human is required. Exactly one state says
// yes: it drives the badge, the sort order and later the push notification, so
// nothing else may claim it.
func (s State) NeedsAttention() bool { return s == StateBlocked }

func FromHookState(s hook.State) State {
	switch s {
	case hook.StateThinking, hook.StateStarting:
		return StateThinking
	case hook.StateToolRunning:
		return StateRunning
	case hook.StateWaiting, hook.StatePermission:
		return StateBlocked
	case hook.StateCompacting:
		return StateCompacting
	case hook.StateEnded:
		return StateDone
	default:
		return StateIdle
	}
}

func FromCrewState(s string) State {
	switch s {
	case "working":
		return StateRunning
	case "blocked":
		return StateBlocked
	case "pr_open":
		return StateReview
	case "done":
		return StateDone
	case "failed", "exited":
		return StateFailed
	default:
		return StateIdle
	}
}

// FromClaudeStatus reads lazytmux's @claude_status pane option, whose format is
// "<state> <epoch> <unseen>". Only the first field is a state.
func FromClaudeStatus(v string) State {
	word, _, _ := strings.Cut(strings.TrimSpace(v), " ")
	switch word {
	case "processing":
		return StateRunning
	case "waiting", "denied":
		return StateBlocked
	case "compacting":
		return StateCompacting
	case "error":
		return StateFailed
	case "done":
		return StateDone
	default:
		// Covers "idle", "interrupted" and the empty string. Interrupting is
		// something the user did deliberately, so it must not raise a badge.
		return StateIdle
	}
}
