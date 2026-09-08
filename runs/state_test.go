package runs

import (
	"testing"

	"github.com/noamsto/houston/hook"
)

func TestFromHookState(t *testing.T) {
	tests := map[hook.State]State{
		hook.StateThinking:    StateThinking,
		hook.StateStarting:    StateThinking,
		hook.StateToolRunning: StateRunning,
		hook.StateWaiting:     StateBlocked,
		hook.StatePermission:  StateBlocked,
		hook.StateCompacting:  StateCompacting,
		hook.StateEnded:       StateDone,
	}
	for in, want := range tests {
		if got := FromHookState(in); got != want {
			t.Errorf("FromHookState(%q) = %q, want %q", in, got, want)
		}
	}
	if got := FromHookState(hook.State("nonsense")); got != StateIdle {
		t.Errorf("unknown hook state = %q, want %q", got, StateIdle)
	}
}

func TestFromCrewState(t *testing.T) {
	tests := map[string]State{
		"working": StateRunning,
		"blocked": StateBlocked,
		"pr_open": StateReview,
		"done":    StateDone,
		"failed":  StateFailed,
		"exited":  StateFailed,
	}
	for in, want := range tests {
		if got := FromCrewState(in); got != want {
			t.Errorf("FromCrewState(%q) = %q, want %q", in, got, want)
		}
	}
	if got := FromCrewState(""); got != StateIdle {
		t.Errorf("empty crew state = %q, want %q", got, StateIdle)
	}
}

func TestFromClaudeStatus(t *testing.T) {
	// The value is lazytmux's "@claude_status", whose first field is the state.
	tests := map[string]State{
		"processing 1788848628 ":  StateRunning,
		"waiting 1788848628 1":    StateBlocked,
		"denied 1788848628 ":      StateBlocked,
		"compacting 1788848628 ":  StateCompacting,
		"error 1788848628 ":       StateFailed,
		"done 1788848628 ":        StateDone,
		"idle 1788846509 ":        StateIdle,
		"interrupted 1788846509 ": StateIdle,
		"":                        StateIdle,
	}
	for in, want := range tests {
		if got := FromClaudeStatus(in); got != want {
			t.Errorf("FromClaudeStatus(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBlockedIsTheOnlyNeedsYouState(t *testing.T) {
	// Exactly one state means "a human is required". The badge, the sort order
	// and (later) push notifications all key off this, so nothing else may
	// claim it.
	needsYou := 0
	for _, s := range AllStates() {
		if s.NeedsAttention() {
			needsYou++
		}
	}
	if needsYou != 1 {
		t.Fatalf("%d states report NeedsAttention, want exactly 1 (blocked)", needsYou)
	}
	if !StateBlocked.NeedsAttention() {
		t.Fatal("blocked does not report NeedsAttention")
	}
}
