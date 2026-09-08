package runs

import (
	"testing"
)

func TestPrecedenceHooksBeatTmux(t *testing.T) {
	r := NewRegistry(DefaultOrder)
	r.Apply(Delta{Source: "tmux", Key: "%1", Run: Run{State: StateIdle, Branch: "main"}})
	r.Apply(Delta{Source: "hooks", Key: "%1", Run: Run{State: StateRunning}})

	got := r.Snapshot()
	if len(got) != 1 {
		t.Fatalf("%d runs, want 1 — both deltas key on the same pane", len(got))
	}
	if got[0].State != StateRunning {
		t.Errorf("State = %q, want %q — hooks outrank tmux", got[0].State, StateRunning)
	}
	if got[0].Branch != "main" {
		t.Errorf("Branch = %q, want %q — tmux still supplies fields hooks leaves empty",
			got[0].Branch, "main")
	}
}

func TestLowerPrecedenceCannotBlankAHigherField(t *testing.T) {
	r := NewRegistry(DefaultOrder)
	r.Apply(Delta{Source: "hooks", Key: "%1", Run: Run{State: StateRunning}})
	r.Apply(Delta{Source: "tmux", Key: "%1", Run: Run{State: ""}})

	if got := r.Snapshot()[0].State; got != StateRunning {
		t.Fatalf("State = %q, want %q — an empty field must not overwrite a set one", got, StateRunning)
	}
}

func TestCrewBeatsTmuxButNotHooks(t *testing.T) {
	r := NewRegistry(DefaultOrder)
	r.Apply(Delta{Source: "tmux", Key: "%1", Run: Run{State: StateIdle}})
	r.Apply(Delta{Source: "crew", Key: "%1", Run: Run{State: StateBlocked}})
	if got := r.Snapshot()[0].State; got != StateBlocked {
		t.Fatalf("State = %q, want %q", got, StateBlocked)
	}

	r.Apply(Delta{Source: "hooks", Key: "%1", Run: Run{State: StateRunning}})
	if got := r.Snapshot()[0].State; got != StateRunning {
		t.Fatalf("State = %q, want %q", got, StateRunning)
	}
}

func TestGoneRemovesOnlyThatSourcesLayer(t *testing.T) {
	r := NewRegistry(DefaultOrder)
	r.Apply(Delta{Source: "tmux", Key: "%1", Run: Run{Branch: "main"}})
	r.Apply(Delta{Source: "hooks", Key: "%1", Run: Run{State: StateRunning}})

	r.Apply(Delta{Source: "hooks", Key: "%1", Gone: true})

	got := r.Snapshot()
	if len(got) != 1 {
		t.Fatalf("%d runs, want 1 — the tmux layer still describes this pane", len(got))
	}
	if got[0].Branch != "main" {
		t.Errorf("Branch = %q, want main", got[0].Branch)
	}

	r.Apply(Delta{Source: "tmux", Key: "%1", Gone: true})
	if n := len(r.Snapshot()); n != 0 {
		t.Fatalf("%d runs after the last layer went away, want 0", n)
	}
}

func TestSubscribeReceivesComposedRun(t *testing.T) {
	r := NewRegistry(DefaultOrder)
	sub := r.Subscribe()
	defer r.Unsubscribe(sub)

	r.Apply(Delta{Source: "tmux", Key: "%1", Run: Run{Branch: "main"}})
	r.Apply(Delta{Source: "hooks", Key: "%1", Run: Run{State: StateRunning}})

	var last Run
	for i := 0; i < 2; i++ {
		select {
		case last = <-sub:
		default:
			t.Fatalf("only %d broadcasts received", i)
		}
	}
	if last.State != StateRunning || last.Branch != "main" {
		t.Fatalf("subscriber got %+v, want the composed run, not one layer", last)
	}
}
