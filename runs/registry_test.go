package runs

import (
	"encoding/base64"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
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

func TestSignatureCoversProjectAndRole(t *testing.T) {
	for _, tt := range []struct {
		name string
		a, b Run
	}{
		{"project", Run{Agent: "claude", Project: "a"}, Run{Agent: "claude", Project: "b"}},
		{"role", Run{Agent: "claude", Role: RoleWorker}, Run{Agent: "claude", Role: RoleDispatcher}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := NewRegistry(DefaultOrder)
			sub := r.Subscribe()
			defer r.Unsubscribe(sub)

			r.Apply(Delta{Source: "tmux", Key: "%1", Run: tt.a})
			r.Apply(Delta{Source: "tmux", Key: "%1", Run: tt.b})

			if n := len(sub); n != 2 {
				t.Fatalf("%d broadcasts, want 2 — a %s change must reach a subscriber", n, tt.name)
			}
		})
	}
}

func TestMergeIntoRole(t *testing.T) {
	tests := []struct {
		name     string
		dst, src string
		want     string
	}{
		{"empty src keeps dst", RoleWorker, "", RoleWorker},
		{"worker fills empty", "", RoleWorker, RoleWorker},
		{"dispatcher fills empty", "", RoleDispatcher, RoleDispatcher},
		{"worker never demotes dispatcher", RoleDispatcher, RoleWorker, RoleDispatcher},
		{"dispatcher promotes worker", RoleWorker, RoleDispatcher, RoleDispatcher},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dst := Run{Role: tt.dst, Project: "keep"}
			mergeInto(&dst, Run{Role: tt.src})
			if dst.Role != tt.want {
				t.Errorf("Role = %q, want %q", dst.Role, tt.want)
			}
			if dst.Project != "keep" {
				t.Errorf("Project = %q, an unset src Project must not blank it", dst.Project)
			}
		})
	}
}

func TestDispatcherRoleSurvivesCrewLayer(t *testing.T) {
	r := NewRegistry(DefaultOrder)
	r.Apply(Delta{Source: "tmux", Key: "%1", Run: Run{Agent: "claude", Role: RoleDispatcher}})
	r.Apply(Delta{Source: "crew", Key: "%1", Run: Run{Role: RoleWorker}})

	if got := r.Snapshot()[0].Role; got != RoleDispatcher {
		t.Errorf("Role = %q, want dispatcher — the crew layer's worker must not demote it", got)
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

	const applies = 600
	const minChurn = 20
	stop := make(chan struct{})
	var applyWG, churnWG sync.WaitGroup
	var churned atomic.Int64
	giveUp := time.Now().Add(5 * time.Second)

	for i := 0; i < 50; i++ {
		applyWG.Add(1)
		go func(n int) {
			defer applyWG.Done()
			// Keep applying past the minimum until the churn has cycled, so the
			// two are guaranteed to overlap however the scheduler orders them.
			for turn := 0; turn < applies || (churned.Load() < minChurn && time.Now().Before(giveUp)); turn++ {
				// Vary the payload every call — otherwise dedupe collapses
				// everything after the first send and this stops
				// exercising the fan-out path this test is about.
				r.Apply(Delta{Source: "hooks", Key: "%1", Run: Run{
					Agent: "claude", State: StateRunning,
					Activity: Activity{Message: fmt.Sprintf("%d-%d", n, turn)},
				}})
			}
		}(i)
	}

	churnWG.Add(1)
	go func() {
		defer churnWG.Done()
		for {
			select {
			case <-stop:
				return
			default:
				ch := r.Subscribe()
				r.Unsubscribe(ch)
				churned.Add(1)
			}
		}
	}()

	applyWG.Wait()
	close(stop)
	churnWG.Wait()
	if n := churned.Load(); n < minChurn {
		t.Fatalf("only %d subscribe/unsubscribe cycles completed, want at least %d", n, minChurn)
	}
}

func TestPointerRefsReplaceWholesale(t *testing.T) {
	// Issue and TmuxRef are swapped, not merged. This is safe only while every
	// source that publishes one publishes it complete. Crew and PR are the
	// exceptions: two layers own different fields of each, so they merge
	// field-wise instead — see TestCrewMergesFieldWise and
	// TestPRMergesFieldWiseTmuxKeepsItsFields. If another ref ever starts arriving partial
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

func TestStaleMarkerBroadcastsAndClears(t *testing.T) {
	r := NewRegistry(DefaultOrder)
	sub := r.Subscribe()
	defer r.Unsubscribe(sub)

	r.Apply(Delta{Source: "tmux", Key: "%1", Run: Run{Agent: "claude", State: StateRunning}})
	select {
	case run := <-sub:
		if run.Stale {
			t.Fatalf("first listing already stale: %+v", run)
		}
	case <-time.After(time.Second):
		t.Fatal("no broadcast on first listing")
	}

	r.Apply(Delta{Source: "conn", Key: "%1", Run: Run{Stale: true}})
	select {
	case run := <-sub:
		if !run.Stale {
			t.Fatal("stale marker did not broadcast as Stale")
		}
	case <-time.After(time.Second):
		t.Fatal("no broadcast for the stale marker")
	}
	if got := r.Snapshot(); len(got) != 1 || !got[0].Stale {
		t.Fatalf("Snapshot = %+v, want one stale run", got)
	}

	r.Apply(Delta{Source: "conn", Key: "%1", Gone: true})
	select {
	case run := <-sub:
		if run.Stale {
			t.Fatal("clearing the marker still broadcast Stale")
		}
		if run.Removed {
			t.Fatal("clearing the marker removed the run")
		}
	case <-time.After(time.Second):
		t.Fatal("no broadcast when the marker cleared")
	}
	if got := r.Snapshot(); len(got) != 1 || got[0].Stale {
		t.Fatalf("Snapshot = %+v, want one non-stale run", got)
	}
}

func TestPRMergesFieldWiseTmuxKeepsItsFields(t *testing.T) {
	r := NewRegistry(DefaultOrder)
	r.Apply(Delta{Source: "tmux", Key: "%1", Run: Run{
		Agent: "claude",
		PR:    &PRRef{Number: "9", State: "OPEN", CheckState: "failure", Mergeable: "MERGEABLE"},
	}})
	r.Apply(Delta{Source: "crew", Key: "%1", Run: Run{
		PR: &PRRef{Number: "9", URL: "https://github.com/x/y/pull/9"},
	}})

	got := r.Snapshot()[0].PR
	if got == nil || got.URL != "https://github.com/x/y/pull/9" || got.State != "OPEN" || got.CheckState != "failure" || got.Mergeable != "MERGEABLE" {
		t.Errorf("PR = %+v, want crew's URL with tmux's State/CheckState/Mergeable intact", got)
	}
}

func TestCrewBusFieldsMergeAndReachTheSignature(t *testing.T) {
	r := NewRegistry(DefaultOrder)
	r.Apply(Delta{Source: "tmux", Key: "%1", Run: Run{Agent: "claude", Crew: &CrewRef{Codename: "Ferris"}}})
	r.Apply(Delta{Source: "crew", Key: "%1", Run: Run{Crew: &CrewRef{Name: "c", Title: "fix it", Model: "sonnet", Detail: "review"}}})

	got := r.Snapshot()[0].Crew
	if got.Title != "fix it" || got.Model != "sonnet" || got.Detail != "review" || got.Codename != "Ferris" {
		t.Errorf("Crew = %+v", got)
	}

	base := Run{Agent: "claude", Crew: &CrewRef{Name: "c"}}
	for name, mod := range map[string]func(*CrewRef){
		"title": func(c *CrewRef) { c.Title = "t" }, "model": func(c *CrewRef) { c.Model = "m" }, "detail": func(c *CrewRef) { c.Detail = "d" },
	} {
		changed := Run{Agent: "claude", Crew: &CrewRef{Name: "c"}}
		mod(changed.Crew)
		if runSignature(base) == runSignature(changed) {
			t.Errorf("signature ignores Crew.%s", name)
		}
	}
	withURL := Run{PR: &PRRef{Number: "1", URL: "u"}}
	if runSignature(Run{PR: &PRRef{Number: "1"}}) == runSignature(withURL) {
		t.Error("signature ignores PR.URL")
	}
}

func TestPRURLForADifferentNumberIsNotMergedIntoTmuxsPR(t *testing.T) {
	r := NewRegistry(DefaultOrder)
	r.Apply(Delta{Source: "tmux", Key: "%1", Run: Run{Agent: "claude", PR: &PRRef{Number: "9", State: "OPEN"}}})
	r.Apply(Delta{Source: "crew", Key: "%1", Run: Run{PR: &PRRef{Number: "7", URL: "https://github.com/x/y/pull/7"}}})

	got := r.Snapshot()[0].PR
	if got.URL != "" && got.Number == "9" {
		t.Errorf("PR = %+v, hybrid of tmux #9 and crew's pull/7 URL", got)
	}
}

func TestEndedHooksRunWithoutATmuxLayerStaysListedAsDone(t *testing.T) {
	r := NewRegistry(DefaultOrder)
	r.Apply(Delta{Source: "hooks", Key: "%1", Run: Run{Agent: "claude", State: StateDone, Repo: "houston"}})

	got := r.Snapshot()
	if len(got) != 1 {
		t.Fatalf("%d runs, want 1 — an ended run stays listed so it can age into history", len(got))
	}
	if got[0].State != StateDone {
		t.Errorf("State = %q, want done", got[0].State)
	}
	if got[0].Caps.Terminal {
		t.Errorf("Caps.Terminal = true, want false without a tmux layer")
	}
	if got[0].Question != nil {
		t.Errorf("Question = %+v, want none on a done run", got[0].Question)
	}
}

func TestQuestionOnACrewLayerForcesBlockedOverEndedHooks(t *testing.T) {
	// Chosen behaviour: the registry's Question invariant outranks hooks'
	// done. An ended hook layer does not clear a crew-bus question; only the
	// crew layer answering (or going Gone) does.
	r := NewRegistry(DefaultOrder)
	r.Apply(Delta{Source: "crew", Key: "%1", Run: Run{
		Agent:    "claude",
		State:    StateBlocked,
		Question: &Question{Text: "run tests?", Via: "crew"},
	}})
	r.Apply(Delta{Source: "hooks", Key: "%1", Run: Run{Agent: "claude", State: StateDone}})

	got := r.Snapshot()[0]
	if got.State != StateBlocked {
		t.Fatalf("State = %q, want blocked while the crew question stands", got.State)
	}
}

func TestProjectKeepsTheLowestLayersOpinion(t *testing.T) {
	r := NewRegistry(DefaultOrder)
	r.Apply(Delta{Source: "tmux", Key: "%1", Run: Run{Agent: "claude", Project: "from-git-root"}})
	r.Apply(Delta{Source: "hooks", Key: "%1", Run: Run{Agent: "claude", Project: "from-a-drifted-cwd"}})

	if got := r.Snapshot()[0].Project; got != "from-git-root" {
		t.Errorf("Project = %q, want the tmux layer's — a hook cwd must not override it", got)
	}

	r.Apply(Delta{Source: "hooks", Key: "%2", Run: Run{Agent: "claude", Project: "hook-only"}})
	var hookOnly *Run
	for _, run := range r.Snapshot() {
		if run.ID == "pane-2" {
			hookOnly = &run
		}
	}
	if hookOnly == nil {
		t.Fatal("pane-2 missing from the snapshot")
	}
	if hookOnly.Project != "hook-only" {
		t.Errorf("Project = %q, want the hook's when no lower layer has one", hookOnly.Project)
	}
}

// hooksource.go normalizes a pane away from its key before this ever reaches
// the registry — for a foreign server, and for a session whose own hook state
// says it ended (#120) — so a hook run that once matched %20 and the
// tmux-source run that currently owns %20 must compose as two distinct runs,
// not merge into one. The hooks delta below still names %20 on purpose: caps
// come from layer presence, never from a Tmux ref, and that ref is half of
// what server/runs_terminal.go gates the terminal WS on. The normalization
// itself is assumed here, not exercised — the hooksource tests own that.
func TestForeignHookRunAndItsFormerPaneComposeAsTwoRuns(t *testing.T) {
	r := NewRegistry(DefaultOrder)
	r.Apply(Delta{Source: "hooks", Key: "claude/foreign-sess", Run: Run{Agent: "claude", State: StateBlocked, Tmux: &TmuxRef{Session: "s", PaneID: "%20"}}})
	r.Apply(Delta{Source: "tmux", Key: "%20", Run: Run{Agent: "claude", State: StateIdle}})

	got := r.Snapshot()
	if len(got) != 2 {
		t.Fatalf("%d runs, want 2 — a foreign hook session and the live tmux pane it once matched must not merge", len(got))
	}

	var hookRun, paneRun *Run
	for _, run := range got {
		switch run.ID {
		case idFor("claude/foreign-sess"):
			hookRun = &run
		case idFor("%20"):
			paneRun = &run
		}
	}
	if hookRun == nil || paneRun == nil {
		t.Fatalf("snapshot missing a run: %+v", got)
	}
	if hookRun.Caps.Terminal || hookRun.Caps.Reply || hookRun.Caps.Kill {
		t.Errorf("Caps = %+v, want all false even though the hooks layer still names %%20 — caps derive from layer presence, not the Tmux ref", hookRun.Caps)
	}
	if !paneRun.Caps.Terminal {
		t.Errorf("pane Caps.Terminal = false, want true — the pane's own run still owns it")
	}
}
