package runs

import (
	"encoding/base64"
	"encoding/json"
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

	if got := firstRun(t, r).State; got != StateRunning {
		t.Fatalf("State = %q, want %q — an empty field must not overwrite a set one", got, StateRunning)
	}
}

func TestCrewBeatsTmuxButNotHooks(t *testing.T) {
	r := NewRegistry(DefaultOrder)
	r.Apply(Delta{Source: "tmux", Key: "%1", Run: Run{State: StateIdle}})
	r.Apply(Delta{Source: "crew", Key: "%1", Run: Run{Agent: "claude", State: StateBlocked}})
	if got := firstRun(t, r).State; got != StateBlocked {
		t.Fatalf("State = %q, want %q", got, StateBlocked)
	}

	r.Apply(Delta{Source: "hooks", Key: "%1", Run: Run{State: StateRunning}})
	if got := firstRun(t, r).State; got != StateRunning {
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

	got := firstRun(t, r)
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

		got := firstRun(t, r)
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

		got := firstRun(t, r)
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
	for i := range 2 {
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

func TestSignatureCoversSessionAndChat(t *testing.T) {
	base := Run{Agent: "claude"}
	for _, tt := range []struct {
		name  string
		after Run
	}{
		{"Session", Run{Agent: "claude", Session: "s"}},
		{"Caps.Chat", Run{Agent: "claude", Caps: Caps{Chat: true}}},
		{"Background", Run{Agent: "claude", Background: []BackgroundTask{{ID: "b1", Kind: "shell"}}}},
	} {
		if runSignature(base) == runSignature(tt.after) {
			t.Errorf("signature ignores %s — a change to it would never reach a subscriber", tt.name)
		}
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

	if got := firstRun(t, r).Role; got != RoleDispatcher {
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

func TestPaneRunID(t *testing.T) {
	if got := PaneRunID("%42"); got != "pane-42" {
		t.Errorf("PaneRunID(%%42) = %q, want pane-42", got)
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

	for i := range 50 {
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
	// Issue is swapped, not merged. This is safe only while every source that
	// publishes one publishes it complete. Crew, PR, and TmuxRef are the
	// exceptions: two layers own different fields of each, so they merge
	// field-wise instead — see TestCrewMergesFieldWise,
	// TestPRMergesFieldWiseTmuxKeepsItsFields, and TestTmuxMergesFieldWise.
	// If another ref ever starts arriving partial from more than one layer,
	// this test is where that shows up.
	r := NewRegistry(DefaultOrder)
	r.Apply(Delta{Source: "tmux", Key: "%1", Run: Run{Issue: &IssueRef{ID: "#1", Title: "rich"}}})
	r.Apply(Delta{Source: "hooks", Key: "%1", Run: Run{Agent: "claude", Issue: &IssueRef{ID: "#1"}}})

	got := firstRun(t, r)
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
		case "CrewSession":
			continue // crew-layer evidence; composeLocked falls back to it only when no layer names a Session
		case "Attention", "AttentionNote":
			continue // computed in composeLocked from the layers, never merged
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

	got := firstRun(t, r).Crew
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

func TestAgentComposesHooksOverCrewOverTmux(t *testing.T) {
	r := NewRegistry(DefaultOrder)
	r.Apply(Delta{Source: "tmux", Key: "%1", Run: Run{Agent: "claude"}})
	r.Apply(Delta{Source: "crew", Key: "%1", Run: Run{Agent: "pi"}})
	r.Apply(Delta{Source: "hooks", Key: "%1", Run: Run{Agent: "pi"}})

	got := r.Snapshot()
	if len(got) != 1 || got[0].Agent != "pi" {
		t.Fatalf("Agent = %+v, want pi — hooks outranks crew and tmux in DefaultOrder", got)
	}
}

func TestAgentFromHooksListsAPaneWithNoTmuxAgent(t *testing.T) {
	// pi panes carry no @claude_status, so TmuxSource never labels them —
	// the hooks layer must still make the run listed with a working terminal.
	r := NewRegistry(DefaultOrder)
	r.Apply(Delta{Source: "tmux", Key: "%1", Run: Run{Agent: ""}})
	r.Apply(Delta{Source: "hooks", Key: "%1", Run: Run{
		Agent: "pi",
		Caps:  Caps{Terminal: true, Reply: true, Kill: true},
	}})

	got := r.Snapshot()
	if len(got) != 1 || got[0].Agent != "pi" {
		t.Fatalf("Agent = %+v, want pi", got)
	}
	if !got[0].Caps.Terminal {
		t.Errorf("Caps.Terminal = false, want true — the tmux layer is present even with no agent label")
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
	default:
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

	got := firstRun(t, r).PR
	if got == nil || got.URL != "https://github.com/x/y/pull/9" || got.State != "OPEN" || got.CheckState != "failure" || got.Mergeable != "MERGEABLE" {
		t.Errorf("PR = %+v, want crew's URL with tmux's State/CheckState/Mergeable intact", got)
	}
}

func TestTmuxMergesFieldWise(t *testing.T) {
	// tmux polls the server pid; hooks sit above it. A legacy hook ref that
	// leaves Server empty must not erase the pid tmux recorded — runPane's
	// send-time guard reads an empty Server as "unknown" and never refuses, so
	// erasing it silently disables the guard.
	r := NewRegistry(DefaultOrder)
	r.Apply(Delta{Source: "tmux", Key: "%5", Run: Run{
		Agent: "claude",
		Tmux:  &TmuxRef{Session: "s", Window: 1, PaneID: "%5", Server: "123"},
	}})
	r.Apply(Delta{Source: "hooks", Key: "%5", Run: Run{
		State: StateRunning,
		Tmux:  &TmuxRef{Session: "s", Window: 1, PaneID: "%5"},
	}})

	got := firstRun(t, r).Tmux
	if got == nil || got.Server != "123" {
		t.Fatalf("Tmux = %+v, want Server 123 preserved from the tmux layer", got)
	}
}

func TestTmuxHigherLayerServerWinsOnSamePane(t *testing.T) {
	r := NewRegistry(DefaultOrder)
	r.Apply(Delta{Source: "tmux", Key: "%5", Run: Run{
		Agent: "claude",
		Tmux:  &TmuxRef{Session: "s", Window: 1, PaneID: "%5", Server: "123"},
	}})
	r.Apply(Delta{Source: "hooks", Key: "%5", Run: Run{
		State: StateRunning,
		Tmux:  &TmuxRef{Session: "s", Window: 1, PaneID: "%5", Server: "456"},
	}})

	if got := firstRun(t, r).Tmux; got == nil || got.Server != "456" {
		t.Fatalf("Tmux = %+v, want the higher layer's explicit Server 456", got)
	}
}

func TestTmuxRefForADifferentPaneWinsWholesale(t *testing.T) {
	// A server pid belongs to the server incarnation that minted the pane.
	// When the layers name different panes, the higher layer's whole ref wins —
	// tmux's pid for %5 must not be grafted onto %9.
	r := NewRegistry(DefaultOrder)
	r.Apply(Delta{Source: "tmux", Key: "%5", Run: Run{
		Agent: "claude",
		Tmux:  &TmuxRef{Session: "s", Window: 1, PaneID: "%5", Server: "123"},
	}})
	r.Apply(Delta{Source: "hooks", Key: "%5", Run: Run{
		State: StateRunning,
		Tmux:  &TmuxRef{Session: "s", Window: 2, PaneID: "%9", Server: "999"},
	}})

	got := firstRun(t, r).Tmux
	if got == nil || got.PaneID != "%9" || got.Server != "999" {
		t.Fatalf("Tmux = %+v, want hooks' %%9/999 wholesale", got)
	}
}

func TestTmuxMergeDoesNotMutateSourceRefs(t *testing.T) {
	tmuxRef := &TmuxRef{Session: "s", Window: 1, PaneID: "%5", Server: "123"}
	hooksRef := &TmuxRef{Session: "s", Window: 1, PaneID: "%5", Server: "456"}

	var dst Run
	mergeInto(&dst, Run{Agent: "claude", Tmux: tmuxRef}) // dst.Tmux aliases tmuxRef
	mergeInto(&dst, Run{State: StateRunning, Tmux: hooksRef})

	if dst.Tmux == nil || dst.Tmux.Server != "456" {
		t.Fatalf("Tmux = %+v, want merged Server 456", dst.Tmux)
	}
	if tmuxRef.Server != "123" || tmuxRef.Session != "s" || tmuxRef.Window != 1 || tmuxRef.PaneID != "%5" {
		t.Errorf("lower layer's ref mutated by the merge: %+v", tmuxRef)
	}
	if hooksRef.Server != "456" {
		t.Errorf("source ref mutated by the merge: %+v", hooksRef)
	}

	// The empty higher-layer Server is the bug case: the merge must still not
	// write through the aliased lower ref.
	tmuxRef2 := &TmuxRef{Session: "s", Window: 1, PaneID: "%5", Server: "123"}
	var dst2 Run
	mergeInto(&dst2, Run{Agent: "claude", Tmux: tmuxRef2})
	mergeInto(&dst2, Run{State: StateRunning, Tmux: &TmuxRef{Session: "s", Window: 1, PaneID: "%5"}})
	if dst2.Tmux.Server != "123" {
		t.Fatalf("Tmux = %+v, want Server 123 preserved", dst2.Tmux)
	}
	if tmuxRef2.Server != "123" {
		t.Errorf("lower layer's ref mutated by the merge: %+v", tmuxRef2)
	}
}

func TestCrewBusFieldsMergeAndReachTheSignature(t *testing.T) {
	r := NewRegistry(DefaultOrder)
	r.Apply(Delta{Source: "tmux", Key: "%1", Run: Run{Agent: "claude", Crew: &CrewRef{Codename: "Ferris"}}})
	r.Apply(Delta{Source: "crew", Key: "%1", Run: Run{Crew: &CrewRef{Name: "c", Title: "fix it", Model: "sonnet", Detail: "review"}}})

	got := firstRun(t, r).Crew
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

	got := firstRun(t, r).PR
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

	got := firstRun(t, r)
	if got.State != StateBlocked {
		t.Fatalf("State = %q, want blocked while the crew question stands", got.State)
	}
}

func TestProjectKeepsTheLowestLayersOpinion(t *testing.T) {
	r := NewRegistry(DefaultOrder)
	r.Apply(Delta{Source: "tmux", Key: "%1", Run: Run{Agent: "claude", Project: "from-git-root"}})
	r.Apply(Delta{Source: "hooks", Key: "%1", Run: Run{Agent: "claude", Project: "from-a-drifted-cwd"}})

	if got := firstRun(t, r).Project; got != "from-git-root" {
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

func TestRunSessionNeverSerialized(t *testing.T) {
	r := NewRegistry(DefaultOrder)
	r.Apply(Delta{Source: "hooks", Key: "%1", Run: Run{Agent: "claude", Session: "sess-abc"}})

	b, err := json.Marshal(firstRun(t, r))
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	for k := range m {
		if strings.EqualFold(k, "session") {
			t.Fatalf("wire payload has key %q, want Session never serialized", k)
		}
	}
}

func TestMergeIntoSessionFirstNonEmptyWins(t *testing.T) {
	dst := Run{Session: "first"}
	mergeInto(&dst, Run{Session: "second"})
	if dst.Session != "first" {
		t.Errorf("Session = %q, want first — first non-empty wins", dst.Session)
	}

	var empty Run
	mergeInto(&empty, Run{Session: "filled"})
	if empty.Session != "filled" {
		t.Errorf("Session = %q, want filled — a src opinion fills an empty dst", empty.Session)
	}
}

func TestRunSignatureIncludesSession(t *testing.T) {
	r := NewRegistry(DefaultOrder)
	sub := r.Subscribe()
	defer r.Unsubscribe(sub)

	r.Apply(Delta{Source: "hooks", Key: "%1", Run: Run{Agent: "claude", Session: "sess-a"}})
	r.Apply(Delta{Source: "hooks", Key: "%1", Run: Run{Agent: "claude", Session: "sess-b"}})

	if n := len(sub); n != 2 {
		t.Fatalf("%d broadcasts, want 2 — a Session flip must reach a subscriber", n)
	}
}

func TestCapsChat(t *testing.T) {
	tests := []struct {
		name  string
		hooks *Run // nil means no hooks layer applied
		tmux  bool
		want  bool
	}{
		{"claude with session", &Run{Agent: "claude", Session: "sess-1"}, false, true},
		{"pi with session", &Run{Agent: "pi", Session: "sess-1"}, false, true},
		{"codex with session", &Run{Agent: "codex", Session: "sess-1"}, false, false},
		{"claude with no session", &Run{Agent: "claude"}, false, false},
		{"tmux-only, no hooks layer", nil, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewRegistry(DefaultOrder)
			if tt.tmux {
				r.Apply(Delta{Source: "tmux", Key: "%1", Run: Run{Agent: "claude"}})
			}
			if tt.hooks != nil {
				r.Apply(Delta{Source: "hooks", Key: "%1", Run: *tt.hooks})
			}

			got := r.Snapshot()
			if len(got) != 1 {
				t.Fatalf("%d runs, want 1", len(got))
			}
			if got[0].Caps.Chat != tt.want {
				t.Errorf("Caps.Chat = %v, want %v", got[0].Caps.Chat, tt.want)
			}
		})
	}
}

func TestCapsChatBroadcastsWhenSessionArrivesLater(t *testing.T) {
	r := NewRegistry(DefaultOrder)
	r.Apply(Delta{Source: "hooks", Key: "%1", Run: Run{Agent: "claude"}})

	sub := r.Subscribe()
	defer r.Unsubscribe(sub)

	r.Apply(Delta{Source: "hooks", Key: "%1", Run: Run{Agent: "claude", Session: "sess-1"}})

	select {
	case run := <-sub:
		if !run.Caps.Chat {
			t.Errorf("Caps.Chat = false, want true once the hooks layer names a session")
		}
	default:
		t.Fatal("no broadcast — a Session arriving must flip Caps.Chat and re-emit")
	}
}

func firstRun(t *testing.T, r *Registry) Run {
	t.Helper()
	snap := r.Snapshot()
	if len(snap) == 0 {
		t.Fatal("registry has no listed runs")
	}
	return snap[0]
}

func TestCrewNamedSessionGrantsChatAndShadowsHistoryCard(t *testing.T) {
	r := NewRegistry(DefaultOrder)
	r.Apply(Delta{Source: "hooks", Key: "claude/s1", Run: Run{Agent: "claude", Session: "s1", State: StateDone}})
	if got := len(r.Snapshot()); got != 1 {
		t.Fatalf("before the crew names it: %d runs, want 1", got)
	}

	ch := r.Subscribe()
	defer r.Unsubscribe(ch)
	r.Apply(Delta{Source: "crew", Key: "%1", Run: Run{Agent: "claude", CrewSession: "s1", State: StateDone}})

	snap := r.Snapshot()
	if len(snap) != 1 || snap[0].ID != "pane-1" || !snap[0].Caps.Chat || snap[0].Session != "s1" {
		t.Fatalf("after: %+v, want the one worker card with Chat for s1", snap)
	}
	var sawRemoval bool
	for len(ch) > 0 {
		if u := <-ch; u.Removed && u.ID == "sess-s1" {
			sawRemoval = true
		}
	}
	if !sawRemoval {
		t.Error("the shadowed history card was never broadcast as removed")
	}

	// Gone crew layer: the history card comes back.
	r.Apply(Delta{Source: "crew", Key: "%1", Gone: true})
	if snap := r.Snapshot(); len(snap) != 1 || snap[0].ID != "sess-s1" {
		t.Fatalf("after the crew layer left: %+v, want the history card back", snap)
	}
}

func TestCrewSessionNeedsAReaderForTheBusEngine(t *testing.T) {
	r := NewRegistry(DefaultOrder)
	r.Apply(Delta{Source: "crew", Key: "%1", Run: Run{Agent: "codex", CrewSession: "s1"}})
	if got := firstRun(t, r); got.Caps.Chat {
		t.Error("a codex bus record granted Chat")
	}
}

func TestCrewSessionGrantsChatForAPiBusRecord(t *testing.T) {
	r := NewRegistry(DefaultOrder)
	r.Apply(Delta{Source: "crew", Key: "%1", Run: Run{Agent: "pi", CrewSession: "s1"}})
	got := firstRun(t, r)
	if !got.Caps.Chat || got.Session != "s1" {
		t.Errorf("pi bus record: chat=%v session=%q, want Chat and s1", got.Caps.Chat, got.Session)
	}
}

func TestShadowFollowsTheSessionTheWorkerCardShows(t *testing.T) {
	r := NewRegistry(DefaultOrder)
	r.Apply(Delta{Source: "hooks", Key: "claude/s1", Run: Run{Agent: "claude", Session: "s1"}})
	r.Apply(Delta{Source: "crew", Key: "%1", Run: Run{Agent: "claude", CrewSession: "s1"}})
	if got := len(r.Snapshot()); got != 1 {
		t.Fatalf("%d runs, want 1", got)
	}

	// /clear: the pane's hooks layer moves to s2, so s1 has no other card.
	r.Apply(Delta{Source: "hooks", Key: "%1", Run: Run{Agent: "claude", Session: "s2"}})
	ids := map[string]bool{}
	for _, run := range r.Snapshot() {
		ids[run.ID] = true
	}
	if !ids["sess-s1"] || !ids["pane-1"] || len(ids) != 2 {
		t.Fatalf("after /clear: %v, want pane-1 and sess-s1", ids)
	}

	// --fresh resume: the crew layer names s3; s1 stays visible, s3 is hidden.
	r.Apply(Delta{Source: "hooks", Key: "claude/s3", Run: Run{Agent: "claude", Session: "s3"}})
	r.Apply(Delta{Source: "hooks", Key: "%1", Run: Run{Agent: "claude", Session: "s3"}})
	r.Apply(Delta{Source: "crew", Key: "%1", Run: Run{Agent: "claude", CrewSession: "s3"}})
	ids = map[string]bool{}
	for _, run := range r.Snapshot() {
		ids[run.ID] = true
	}
	if !ids["sess-s1"] || ids["sess-s3"] {
		t.Fatalf("after fresh resume: %v, want sess-s1 shown and sess-s3 hidden", ids)
	}
}

func TestComposeAttention(t *testing.T) {
	type layer struct {
		source string
		run    Run
	}
	cases := []struct {
		name      string
		layers    []layer
		wantAtt   Attention
		wantNote  string
		wantState State
	}{
		{"hooks blocked is needs-you", []layer{
			{"hooks", Run{Agent: "claude", State: StateBlocked}},
		}, AttentionNeedsYou, "", StateBlocked},
		{"hooks idle alone is none", []layer{
			{"hooks", Run{Agent: "claude", State: StateIdle}},
		}, AttentionNone, "", StateIdle},
		{"crew done under hooks idle is done", []layer{
			{"crew", Run{Agent: "claude", State: StateDone}},
			{"hooks", Run{Agent: "claude", State: StateIdle}},
		}, AttentionDone, "", StateIdle},
		{"crew review under hooks idle is done", []layer{
			{"crew", Run{Agent: "claude", State: StateReview}},
			{"hooks", Run{Agent: "claude", State: StateIdle}},
		}, AttentionDone, "", StateIdle},
		{"crew failed is stuck", []layer{
			{"crew", Run{Agent: "claude", State: StateFailed}},
		}, AttentionStuck, "", StateFailed},
		{"crew stuck opinion survives hooks running", []layer{
			{"crew", Run{Agent: "claude", State: StateRunning, Attention: AttentionStuck, AttentionNote: "x"}},
			{"hooks", Run{Agent: "claude", State: StateRunning}},
		}, AttentionStuck, "x", StateRunning},
		{"tmux failed is stuck under hooks thinking", []layer{
			{"tmux", Run{Agent: "claude", State: StateFailed}},
			{"hooks", Run{Agent: "claude", State: StateThinking}},
		}, AttentionStuck, tmuxFailedNote, StateThinking},
		{"hooks-ended run alone is none", []layer{
			{"hooks", Run{Agent: "claude", State: StateDone}},
		}, AttentionNone, "", StateDone},
		{"crew blocked with a question is needs-you", []layer{
			{"crew", Run{Agent: "claude", State: StateBlocked, Question: &Question{Text: "go?", Via: "crew"}}},
		}, AttentionNeedsYou, "", StateBlocked},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := NewRegistry(DefaultOrder)
			for _, l := range tc.layers {
				r.Apply(Delta{Source: l.source, Key: "%1", Run: l.run})
			}
			got := firstRun(t, r)
			if got.Attention != tc.wantAtt || got.AttentionNote != tc.wantNote {
				t.Errorf("Attention = %q/%q, want %q/%q", got.Attention, got.AttentionNote, tc.wantAtt, tc.wantNote)
			}
			if got.State != tc.wantState {
				t.Errorf("State = %q, want %q", got.State, tc.wantState)
			}
		})
	}
}

func TestAttentionAgreesWithNeedsAttention(t *testing.T) {
	r := NewRegistry(DefaultOrder)
	for i, s := range AllStates() {
		r.Apply(Delta{Source: "hooks", Key: fmt.Sprintf("%%%d", i), Run: Run{Agent: "claude", State: s}})
	}
	r.Apply(Delta{Source: "crew", Key: "%q", Run: Run{Agent: "claude", State: StateBlocked, Question: &Question{Text: "go?", Via: "crew"}}})
	r.Apply(Delta{Source: "hooks", Key: "%q", Run: Run{Agent: "claude", State: StateRunning}})

	rows := r.Snapshot()
	if len(rows) != len(AllStates())+1 {
		t.Fatalf("%d rows, want %d", len(rows), len(AllStates())+1)
	}
	for _, run := range rows {
		if got := run.Attention == AttentionNeedsYou; got != run.State.NeedsAttention() {
			t.Errorf("%s: attention %q disagrees with State %q NeedsAttention()=%v", run.ID, run.Attention, run.State, run.State.NeedsAttention())
		}
	}
}

func TestRunSignatureSeesAttention(t *testing.T) {
	base := Run{Agent: "claude", State: StateRunning}
	stuck := base
	stuck.Attention = AttentionStuck
	noted := stuck
	noted.AttentionNote = "x"

	if runSignature(base) == runSignature(stuck) {
		t.Error("an Attention change must change the signature")
	}
	if runSignature(stuck) == runSignature(noted) {
		t.Error("an AttentionNote change must change the signature")
	}
}

// TestComposeAttentionFromCrewLog feeds the crew layer from a real bus fold
// (deltasFromCrewLog + routeCrewQuestion) rather than hand-built runs, so a
// change to the fold or the router that the composer's inputs depend on shows
// up here.
func TestComposeAttentionFromCrewLog(t *testing.T) {
	const (
		statusTS = int64(1_700_000_000_000)
		branch   = "fix/7"
	)
	busLog := func(status string) string {
		return `{"ts":1000,"crew_id":"c1","kind":"dispatch","branch":"fix/7","engine":"claude"}` + "\n" +
			fmt.Sprintf(`{"ts":%d,"crew_id":"c1","from":"worker:fix/7#s1","kind":"status","body":%s}`, statusTS, status) + "\n"
	}
	soon := time.UnixMilli(statusTS + 1000)

	cases := []struct {
		name      string
		status    string
		live      bool
		hooks     State
		wantAtt   Attention
		wantNote  string
		wantState State
		wantVia   string
	}{
		{"watchdog dead under hooks thinking", `{"state":"failed","detail":"dead: engine process gone","source":"watchdog"}`,
			false, StateThinking, AttentionStuck, crewWatchdogDeadNote, StateThinking, ""},
		{"done under hooks idle", `{"state":"done"}`,
			false, StateIdle, AttentionDone, "", StateIdle, ""},
		{"pr_open under hooks idle", `{"state":"pr_open","pr_url":"https://github.com/x/y/pull/3"}`,
			false, StateIdle, AttentionDone, "", StateIdle, ""},
		{"worker question while the dispatcher is live", `{"state":"blocked","detail":"keep the legacy route?"}`,
			true, StateRunning, AttentionNone, "", StateRunning, ""},
		{"worker question with no dispatcher", `{"state":"blocked","detail":"keep the legacy route?"}`,
			false, StateRunning, AttentionNeedsYou, "", StateBlocked, "crew"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b, ok := deltasFromCrewLog(strings.NewReader(busLog(tc.status)))[branch]
			if !ok {
				t.Fatalf("no fold result for %q", branch)
			}
			r := NewRegistry(DefaultOrder)
			r.Apply(Delta{Source: "crew", Key: "%1", Run: routeCrewQuestion(b, tc.live, soon)})
			r.Apply(Delta{Source: "hooks", Key: "%1", Run: Run{Agent: "claude", State: tc.hooks}})

			got := firstRun(t, r)
			if got.Attention != tc.wantAtt || got.AttentionNote != tc.wantNote {
				t.Errorf("Attention = %q/%q, want %q/%q", got.Attention, got.AttentionNote, tc.wantAtt, tc.wantNote)
			}
			if got.State != tc.wantState {
				t.Errorf("State = %q, want %q", got.State, tc.wantState)
			}
			if tc.wantVia == "" {
				if got.Question != nil {
					t.Errorf("Question = %+v, want none", got.Question)
				}
			} else if got.Question == nil || got.Question.Via != tc.wantVia {
				t.Errorf("Question = %+v, want Via %q", got.Question, tc.wantVia)
			}
		})
	}
}
