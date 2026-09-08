package runs

import (
	"reflect"
	"sync"
	"testing"
	"time"
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

func TestApplyDoesNotRaceSubscribeClose(t *testing.T) {
	// A send on a closed channel panics; select/default does not protect
	// against it — it only guards a full buffer, not a closed one. Apply used
	// to copy the subscriber set under Lock, release it, then send, which
	// left a window for Unsubscribe to close a channel out from under it.
	// This spins concurrent Apply against concurrent Subscribe/Unsubscribe
	// churn to prove that window is real, not theoretical.
	r := NewRegistry(DefaultOrder)

	stop := make(chan struct{})
	var wg sync.WaitGroup

	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					r.Apply(Delta{Source: "hooks", Key: "%1", Run: Run{State: StateRunning}})
				}
			}
		}()
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				ch := r.Subscribe()
				r.Unsubscribe(ch)
			}
		}
	}()

	time.Sleep(200 * time.Millisecond)
	close(stop)
	wg.Wait()
}

func TestPointerRefsReplaceWholesale(t *testing.T) {
	// Nested refs are swapped, not merged. This is safe only while every source
	// that publishes one publishes it complete — tmux owns Issue/PR/Crew, and
	// hooks and tmux both publish a full TmuxRef. If a source ever starts
	// emitting a partial ref, this test is where that shows up.
	r := NewRegistry(DefaultOrder)
	r.Apply(Delta{Source: "tmux", Key: "%1", Run: Run{Issue: &IssueRef{ID: "#1", Title: "rich"}}})
	r.Apply(Delta{Source: "hooks", Key: "%1", Run: Run{Issue: &IssueRef{ID: "#1"}}})

	got := r.Snapshot()[0]
	if got.Issue.Title != "" {
		t.Fatalf("Issue.Title = %q — if refs ever merge field-by-field, update the sources that rely on wholesale replacement", got.Issue.Title)
	}
}

func TestMergeIntoCoversEveryField(t *testing.T) {
	// Reflection, not a list: an enumerated test is correct only until someone
	// adds a field to Run. A dropped field would never reach the API and no
	// other test would notice.
	var src Run
	fillNonZero(reflect.ValueOf(&src).Elem())

	var dst Run
	mergeInto(&dst, src)

	v := reflect.ValueOf(dst)
	tp := v.Type()
	for i := 0; i < v.NumField(); i++ {
		if tp.Field(i).Name == "ID" {
			continue // set by composeLocked from the key, deliberately not merged
		}
		if v.Field(i).IsZero() {
			t.Errorf("mergeInto drops %s — it will never reach the API", tp.Field(i).Name)
		}
	}
}

// fillNonZero sets every field of v, recursively, to a distinctive non-zero value.
func fillNonZero(v reflect.Value) {
	switch v.Kind() {
	case reflect.String:
		v.SetString("x")
	case reflect.Int, reflect.Int64:
		v.SetInt(7)
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Ptr:
		v.Set(reflect.New(v.Type().Elem()))
		fillNonZero(v.Elem())
	case reflect.Slice:
		v.Set(reflect.MakeSlice(v.Type(), 1, 1))
		fillNonZero(v.Index(0))
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			fillNonZero(v.Field(i))
		}
	}
}
