package runs

import (
	"encoding/base64"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestPrecedenceHooksBeatTmux(t *testing.T) {
	r := NewRegistry(DefaultOrder)
	r.Apply(Delta{Source: "tmux", Key: "%1", Run: Run{State: StateIdle, Branch: "main"}})
	r.Apply(Delta{Source: "hooks", Key: "%1", Run: Run{Agent: "claude", State: StateRunning}})

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
	r.Apply(Delta{Source: "hooks", Key: "%1", Run: Run{Agent: "claude", State: StateRunning}})
	r.Apply(Delta{Source: "tmux", Key: "%1", Run: Run{State: ""}})

	if got := r.Snapshot()[0].State; got != StateRunning {
		t.Fatalf("State = %q, want %q — an empty field must not overwrite a set one", got, StateRunning)
	}
}

func TestCrewBeatsTmuxButNotHooks(t *testing.T) {
	r := NewRegistry(DefaultOrder)
	r.Apply(Delta{Source: "tmux", Key: "%1", Run: Run{State: StateIdle}})
	r.Apply(Delta{Source: "crew", Key: "%1", Run: Run{Agent: "claude", State: StateBlocked}})
	if got := r.Snapshot()[0].State; got != StateBlocked {
		t.Fatalf("State = %q, want %q", got, StateBlocked)
	}

	r.Apply(Delta{Source: "hooks", Key: "%1", Run: Run{State: StateRunning}})
	if got := r.Snapshot()[0].State; got != StateRunning {
		t.Fatalf("State = %q, want %q", got, StateRunning)
	}
}

func TestQuestionForcesBlockedOverHooksRunning(t *testing.T) {
	// hooksource.go keys on the same pane id as the crew layer and sits above
	// it in DefaultOrder, so a worker blocked on `crew await` is, to Claude's
	// hooks, inside a running Bash tool. Without the composeLocked invariant,
	// hooks' State would win by precedence and the crew Question would survive
	// pointing at a run the UI reports as running.
	r := NewRegistry(DefaultOrder)
	r.Apply(Delta{Source: "tmux", Key: "%1", Run: Run{Agent: "claude", State: StateRunning}})
	r.Apply(Delta{Source: "crew", Key: "%1", Run: Run{
		State:    StateBlocked,
		Question: &Question{Text: "run tests?", Via: "crew"},
	}})
	r.Apply(Delta{Source: "hooks", Key: "%1", Run: Run{
		State:    StateRunning,
		Activity: Activity{Tool: "Bash"},
	}})

	got := r.Snapshot()[0]
	if got.State != StateBlocked {
		t.Fatalf("State = %q, want %q — a surviving Question must force blocked even though hooks composed running", got.State, StateBlocked)
	}
	if got.Question == nil || got.Question.Text != "run tests?" {
		t.Fatalf("Question = %+v, want the crew layer's question intact", got.Question)
	}
}

func TestAnsweredCrewLayerStopsForcingBlocked(t *testing.T) {
	// R5: once the crew bus has answered a question, the crew layer publishes
	// no opinion (State: "", Question: nil) rather than remembering the old
	// blocked state. The badge must actually go dark then, not just stop being
	// refreshed.
	t.Run("crew layer retracts its question, running wins", func(t *testing.T) {
		r := NewRegistry(DefaultOrder)
		r.Apply(Delta{Source: "tmux", Key: "%1", Run: Run{Agent: "claude", State: StateRunning}})
		r.Apply(Delta{Source: "hooks", Key: "%1", Run: Run{State: StateRunning}})
		r.Apply(Delta{Source: "crew", Key: "%1", Run: Run{State: "", Question: nil}})

		got := r.Snapshot()[0]
		if got.State != StateRunning {
			t.Fatalf("State = %q, want %q — an answered question must stop forcing blocked", got.State, StateRunning)
		}
		if got.Question != nil {
			t.Fatalf("Question = %+v, want nil", got.Question)
		}
	})

	t.Run("crew layer still carries its question, blocked wins", func(t *testing.T) {
		r := NewRegistry(DefaultOrder)
		r.Apply(Delta{Source: "tmux", Key: "%1", Run: Run{Agent: "claude", State: StateRunning}})
		r.Apply(Delta{Source: "hooks", Key: "%1", Run: Run{State: StateRunning}})
		r.Apply(Delta{Source: "crew", Key: "%1", Run: Run{
			State:    StateBlocked,
			Question: &Question{Text: "still waiting", Via: "crew"},
		}})

		got := r.Snapshot()[0]
		if got.State != StateBlocked {
			t.Fatalf("State = %q, want %q — the badge must stay lit while the question stands", got.State, StateBlocked)
		}
		if got.Question == nil {
			t.Fatal("Question = nil, want the crew layer's question")
		}
	})
}

func TestGoneRemovesOnlyThatSourcesLayer(t *testing.T) {
	r := NewRegistry(DefaultOrder)
	// tmux carries its own Agent here — a real tmux layer does, whenever the
	// pane's @claude_status is non-empty — so the run stays listed once hooks
	// leaves and this test still exercises "the tmux layer survives".
	r.Apply(Delta{Source: "tmux", Key: "%1", Run: Run{Agent: "claude", Branch: "main"}})
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

func TestSnapshotOmitsUnlistedRuns(t *testing.T) {
	r := NewRegistry(DefaultOrder)
	r.Apply(Delta{Source: "tmux", Key: "%1", Run: Run{Branch: "main"}}) // no agent — a plain pane

	if n := len(r.Snapshot()); n != 0 {
		t.Fatalf("%d runs, want 0 — a pane with no agent is a workspace pane, not a run", n)
	}
}

func TestRemovalFiresOnlyOnTheListedToUnlistedEdge(t *testing.T) {
	r := NewRegistry(DefaultOrder)
	sub := r.Subscribe()
	defer r.Unsubscribe(sub)

	r.Apply(Delta{Source: "tmux", Key: "%1", Run: Run{Branch: "main"}}) // not listed — no broadcast at all
	select {
	case run := <-sub:
		t.Fatalf("got a broadcast for a never-listed pane: %+v", run)
	default:
	}

	r.Apply(Delta{Source: "hooks", Key: "%1", Run: Run{Agent: "claude", State: StateRunning}}) // now listed
	select {
	case run := <-sub:
		if run.Removed {
			t.Fatalf("got a removal on first listing, want the composed run")
		}
	default:
		t.Fatal("no broadcast for the first listed update")
	}

	// The agent-bearing layer leaves; the tmux layer (no agent) survives. This
	// is the wedge case: the key stays live in r.layers, but Agent drops to ""
	// and the run must be reported gone.
	r.Apply(Delta{Source: "hooks", Key: "%1", Gone: true})
	select {
	case run := <-sub:
		if !run.Removed {
			t.Fatalf("got %+v, want a removal — the run lost its only agent-bearing layer", run)
		}
		if run.ID == "" {
			t.Fatal("removal payload has no ID")
		}
	default:
		t.Fatal("no removal broadcast on the listed -> unlisted edge")
	}

	if n := len(r.Snapshot()); n != 0 {
		t.Fatalf("%d runs after the agent-bearing layer left, want 0 (Snapshot must use the same listed() predicate)", n)
	}
}

func TestSubscribeReceivesComposedRun(t *testing.T) {
	r := NewRegistry(DefaultOrder)
	sub := r.Subscribe()
	defer r.Unsubscribe(sub)

	r.Apply(Delta{Source: "tmux", Key: "%1", Run: Run{Agent: "claude", Branch: "main"}})
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

func TestApplyDedupesUnchangedUpdates(t *testing.T) {
	r := NewRegistry(DefaultOrder)
	sub := r.Subscribe()
	defer r.Unsubscribe(sub)

	d := Delta{Source: "hooks", Key: "%1", Run: Run{Agent: "claude", State: StateRunning}}
	r.Apply(d)
	r.Apply(d) // identical — must not re-broadcast

	if n := len(sub); n != 1 {
		t.Fatalf("%d buffered broadcasts, want 1 — an unchanged re-apply must be deduped", n)
	}
}

func TestSignatureCoversCrewCodename(t *testing.T) {
	r := NewRegistry(DefaultOrder)
	sub := r.Subscribe()
	defer r.Unsubscribe(sub)

	r.Apply(Delta{Source: "tmux", Key: "%1", Run: Run{Agent: "claude", Crew: &CrewRef{Codename: "a"}}})
	r.Apply(Delta{Source: "tmux", Key: "%1", Run: Run{Agent: "claude", Crew: &CrewRef{Codename: "b"}}})

	if n := len(sub); n != 2 {
		t.Fatalf("%d broadcasts, want 2 — a Crew.Codename change must reach a subscriber", n)
	}
}

func TestSignatureCoversCrewColor(t *testing.T) {
	r := NewRegistry(DefaultOrder)
	sub := r.Subscribe()
	defer r.Unsubscribe(sub)

	r.Apply(Delta{Source: "tmux", Key: "%1", Run: Run{Agent: "claude", Crew: &CrewRef{Color: "#111111"}}})
	r.Apply(Delta{Source: "tmux", Key: "%1", Run: Run{Agent: "claude", Crew: &CrewRef{Color: "#222222"}}})

	if n := len(sub); n != 2 {
		t.Fatalf("%d broadcasts, want 2 — a Crew.Color change must reach a subscriber", n)
	}
}

func TestSignatureCoversQuestionVia(t *testing.T) {
	r := NewRegistry(DefaultOrder)
	sub := r.Subscribe()
	defer r.Unsubscribe(sub)

	r.Apply(Delta{Source: "hooks", Key: "%1", Run: Run{Agent: "claude", Question: &Question{Text: "q", Via: "pane"}}})
	r.Apply(Delta{Source: "hooks", Key: "%1", Run: Run{Agent: "claude", Question: &Question{Text: "q", Via: "crew"}}})

	if n := len(sub); n != 2 {
		t.Fatalf("%d broadcasts, want 2 — a Question.Via change must reach a subscriber", n)
	}
}

func TestDirtyReflectsADroppedUpdate(t *testing.T) {
	r := NewRegistry(DefaultOrder)
	ch := r.Subscribe()
	defer r.Unsubscribe(ch)

	if r.Dirty(ch) {
		t.Fatal("Dirty before any drop, want false")
	}

	// Fill the channel buffer, then force one more distinct broadcast so the
	// send hits the default branch and flags a drop. Each apply must produce a
	// different signature or dedupe would swallow it before it ever reaches
	// the channel.
	for i := 0; i < cap(ch)+1; i++ {
		r.Apply(Delta{Source: "hooks", Key: "%1", Run: Run{
			Agent: "claude", State: StateRunning,
			Activity: Activity{Message: fmt.Sprintf("m%d", i)},
		}})
	}

	if !r.Dirty(ch) {
		t.Fatal("Dirty after a drop, want true")
	}
	if r.Dirty(ch) {
		t.Fatal("Dirty must clear after being read")
	}
}

func TestIdForIsURLPathSafe(t *testing.T) {
	busDir := "/repo/.git/crew"
	cases := []struct{ key, want string }{
		{"%307", "pane-307"},
		{"claude/abc-123", "sess-abc-123"},
		{"crew/" + busDir + "/fix/412", "crew-" + base64.RawURLEncoding.EncodeToString([]byte(busDir+"/fix/412"))},
	}
	for _, c := range cases {
		got := idFor(c.key)
		if got != c.want {
			t.Errorf("idFor(%q) = %q, want %q", c.key, got, c.want)
		}
		if strings.ContainsAny(got, "/%") {
			t.Errorf("idFor(%q) = %q, not URL-path-safe", c.key, got)
		}
	}
}

func TestApplyDoesNotRaceSubscribeClose(t *testing.T) {
	// A send on a closed channel panics, and select/default does not protect
	// against it — that only guards a full buffer, not a closed one. Spinning
	// Apply against Subscribe/Unsubscribe churn is what makes the window
	// reachable; a sequential test cannot reach it at all.
	r := NewRegistry(DefaultOrder)

	stop := make(chan struct{})
	var wg sync.WaitGroup

	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for turn := 0; ; turn++ {
				select {
				case <-stop:
					return
				default:
					// Vary the payload every call — otherwise dedupe collapses
					// everything after the first send and this stops
					// exercising the fan-out path this test is about.
					r.Apply(Delta{Source: "hooks", Key: "%1", Run: Run{
						Agent: "claude", State: StateRunning,
						Activity: Activity{Message: fmt.Sprintf("%d-%d", n, turn)},
					}})
				}
			}
		}(i)
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
	// that publishes one publishes it complete — tmux owns Issue/PR, and hooks
	// and tmux both publish a full TmuxRef. Crew is the one exception: two
	// layers now own different fields of it, so it merges field-wise instead —
	// see TestCrewMergesFieldWise. If another ref ever starts arriving partial
	// from more than one layer, this test is where that shows up.
	r := NewRegistry(DefaultOrder)
	r.Apply(Delta{Source: "tmux", Key: "%1", Run: Run{Issue: &IssueRef{ID: "#1", Title: "rich"}}})
	r.Apply(Delta{Source: "hooks", Key: "%1", Run: Run{Agent: "claude", Issue: &IssueRef{ID: "#1"}}})

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
		switch tp.Field(i).Name {
		case "ID":
			continue // set by composeLocked from the key, deliberately not merged
		case "Removed":
			continue // set only by the registry when broadcasting a removal, never by a source
		case "Caps":
			continue // derived in composeLocked from layer presence, not merged
		}
		if v.Field(i).IsZero() {
			t.Errorf("mergeInto drops %s — it will never reach the API", tp.Field(i).Name)
		}
	}
}

func TestCrewMergesFieldWise(t *testing.T) {
	r := NewRegistry(DefaultOrder)
	// tmux owns Codename/Color; the crew bus owns Name/Tier. Wholesale
	// replacement would let whichever layer merges last erase the other's half.
	r.Apply(Delta{Source: "tmux", Key: "%1", Run: Run{
		Agent: "claude",
		Crew:  &CrewRef{Codename: "Ferris", Color: "#ff0000"},
	}})
	r.Apply(Delta{Source: "crew", Key: "%1", Run: Run{
		Crew: &CrewRef{Name: "crab-crew", Tier: "lead"},
	}})

	got := r.Snapshot()[0].Crew
	if got == nil {
		t.Fatal("Crew = nil, want both layers' fields merged")
	}
	if got.Codename != "Ferris" || got.Color != "#ff0000" || got.Name != "crab-crew" || got.Tier != "lead" {
		t.Errorf("Crew = %+v, want tmux's Codename/Color and crew's Name/Tier all present", got)
	}
}

func TestCapsTerminalGoesFalseAfterTmuxLayerGoesGone(t *testing.T) {
	r := NewRegistry(DefaultOrder)
	r.Apply(Delta{Source: "tmux", Key: "%1", Run: Run{Agent: "claude"}})
	// Mirrors what hooksource.go sets today for a pane-backed session.
	r.Apply(Delta{Source: "hooks", Key: "%1", Run: Run{
		Agent: "claude",
		Caps:  Caps{Terminal: true, Reply: true, Kill: true},
	}})

	if got := r.Snapshot(); len(got) != 1 || !got[0].Caps.Terminal {
		t.Fatalf("Caps.Terminal = %+v, want true while the tmux layer is present", got)
	}

	r.Apply(Delta{Source: "tmux", Key: "%1", Gone: true})

	got := r.Snapshot()
	if len(got) != 1 {
		t.Fatalf("%d runs after the tmux layer left, want 1 — the hooks layer survives", len(got))
	}
	if got[0].Caps.Terminal || got[0].Caps.Kill {
		t.Errorf("Caps = %+v, want Terminal and Kill false once the tmux layer is gone", got[0].Caps)
	}
}

func TestCapsReplyRequiresLayerNotJustFlag(t *testing.T) {
	r := NewRegistry(DefaultOrder)
	// Matches what crewsource.go publishes today: no Caps set on the delta.
	// Key shape matches I2's zero-or-≥2-candidates case ("crew/" + busDir +
	// "/" + branch) — no source emits "branch/" any more.
	r.Apply(Delta{Source: "crew", Key: "crew//repo/.git/crew/fix/1", Run: Run{Agent: "claude", State: StateBlocked}})

	got := r.Snapshot()
	if len(got) != 1 {
		t.Fatalf("%d runs, want 1", len(got))
	}
	if !got[0].Caps.Reply {
		t.Error("Caps.Reply = false, want true — a crew layer can take a crew reply with no pane")
	}
	if got[0].Caps.Terminal || got[0].Caps.Kill {
		t.Errorf("Caps = %+v, want Terminal and Kill false with no tmux layer", got[0].Caps)
	}
}

func TestCapsTransitionIsBroadcast(t *testing.T) {
	r := NewRegistry(DefaultOrder)
	sub := r.Subscribe()
	defer r.Unsubscribe(sub)

	r.Apply(Delta{Source: "tmux", Key: "%1", Run: Run{Agent: "claude"}})
	r.Apply(Delta{Source: "hooks", Key: "%1", Run: Run{
		Agent: "claude",
		Caps:  Caps{Terminal: true, Reply: true, Kill: true},
	}})

	// Drain whatever setup broadcasts fired (house style: non-blocking
	// select/default, see TestRemovalFiresOnlyOnTheListedToUnlistedEdge).
drain:
	for {
		select {
		case <-sub:
		default:
			break drain
		}
	}

	r.Apply(Delta{Source: "tmux", Key: "%1", Gone: true})

	select {
	case run := <-sub:
		if run.Caps.Terminal {
			t.Fatalf("got %+v, want Caps.Terminal false once the tmux layer is gone", run)
		}
	default:
		t.Fatal("no broadcast when the tmux layer went gone — Caps must be part of the change signature")
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
