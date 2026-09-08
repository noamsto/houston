package runs

import (
	"testing"

	"github.com/noamsto/houston/hook"
	"github.com/noamsto/houston/hub"
)

func TestRunFromSessionViewKeysOnPane(t *testing.T) {
	key, r := runFromSessionView(hub.SessionView{
		SessionID:   "abc-123",
		TmuxSession: "houston",
		TmuxWindow:  "1",
		TmuxPane:    "%307",
		State:       hook.StateToolRunning,
		Tool:        "Bash",
	})

	if key != "%307" {
		t.Fatalf("key = %q, want the tmux pane id — that is what every source correlates on", key)
	}
	if r.State != StateRunning {
		t.Errorf("State = %q, want %q", r.State, StateRunning)
	}
	if r.Tmux == nil || r.Tmux.PaneID != "%307" || r.Tmux.Session != "houston" || r.Tmux.Window != 1 {
		t.Errorf("TmuxRef = %+v, want session houston window 1 pane %%307", r.Tmux)
	}
	if r.Agent != "claude" {
		t.Errorf("Agent = %q, want claude", r.Agent)
	}
	if !r.Caps.Terminal || !r.Caps.Reply {
		t.Errorf("Caps = %+v, want terminal and reply for a pane-backed run", r.Caps)
	}
}

func TestRunFromSessionViewFallsBackToSessionID(t *testing.T) {
	key, r := runFromSessionView(hub.SessionView{SessionID: "abc-123"})

	if key != "claude/abc-123" {
		t.Fatalf("key = %q, want claude/abc-123 when there is no pane", key)
	}
	if r.Tmux != nil {
		t.Errorf("TmuxRef = %+v, want nil without a pane", r.Tmux)
	}
	if r.Caps.Terminal {
		t.Error("Caps.Terminal is true without a pane — there is nothing to attach to")
	}
}

func TestRunFromSessionViewCarriesQuestionWhenBlocked(t *testing.T) {
	_, r := runFromSessionView(hub.SessionView{
		SessionID:   "abc",
		TmuxPane:    "%1",
		State:       hook.StatePermission,
		LastMessage: "Allow Bash(rm -rf)?",
	})
	if r.State != StateBlocked {
		t.Fatalf("State = %q, want blocked", r.State)
	}
	if r.Question == nil || r.Question.Text != "Allow Bash(rm -rf)?" {
		t.Fatalf("Question = %+v, want the permission prompt", r.Question)
	}
	if r.Question.Via != "pane" {
		t.Errorf("Question.Via = %q, want pane", r.Question.Via)
	}
}
