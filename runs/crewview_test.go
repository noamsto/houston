package runs

import (
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/noamsto/houston/crewview"
)

func goldenView(t *testing.T) crewview.Overview {
	t.Helper()
	b, err := os.ReadFile("../crewview/testdata/overview.ndjson")
	if err != nil {
		t.Fatal(err)
	}
	v, err := crewview.Decode(b)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func sp(s string) *string { return &s }
func ip(i int64) *int64   { return &i }

func TestCrewRunsFromViewGolden(t *testing.T) {
	v := goldenView(t)
	got := crewRunsFromView(v, nil)
	if n := len(got.local) + len(got.remote); n != 12 {
		t.Fatalf("workers = %d, want 12", n)
	}
	l := got.local

	a := l["feat/a"]
	if a.State != StateRunning || a.Crew.Detail != "execute" || a.Crew.Codename != "otter" ||
		a.Crew.Tier != "standard" || a.Crew.Model != "sonnet" || a.Crew.Title != "Task feat/a" ||
		a.Agent != "claude" || a.Crew.Origin != "local" || a.Crew.Name != "c1" {
		t.Errorf("feat/a = %+v crew %+v", a.Run, a.Crew)
	}
	if want := v.GeneratedAt - 45; a.UpdatedAt != want {
		t.Errorf("UpdatedAt = %d, want %d", a.UpdatedAt, want)
	}

	if b := l["feat/b"]; b.State != StateReview || b.PR == nil || b.PR.Number != "12" || b.PR.URL != "https://github.com/o/r/pull/12" {
		t.Errorf("feat/b = %+v", b.Run)
	}

	c := l["feat/c"]
	if c.State != StateBlocked || c.Question == nil || *c.Question != (Question{Text: "which schema should the migration target?", Via: "crew", Kind: QuestionCrew}) {
		t.Errorf("feat/c = %+v q %+v", c.Run, c.Question)
	}

	d := l["feat/d"]
	if d.State != StateBlocked || d.Question == nil || *d.Question != (Question{Text: crewWatchdogPromptNote, Via: "pane", Kind: QuestionDialog}) || d.Crew.Detail != "" {
		t.Errorf("feat/d = %+v q %+v", d.Run, d.Question)
	}

	e := l["feat/e"]
	if e.State != StateRunning || e.Attention != AttentionStuck || e.AttentionNote != crewWatchdogQuietNote || e.Crew.Detail != "" || e.Question != nil {
		t.Errorf("feat/e = %+v", e.Run)
	}
	if len(e.Crew.Attention) != 1 || e.Crew.Attention[0].Action == nil || e.Crew.Attention[0].Action.Verb != "nudge" {
		t.Errorf("feat/e attention = %+v", e.Crew.Attention)
	}
	if f := l["feat/f"]; f.Attention != AttentionStuck || f.AttentionNote != crewWatchdogRunawayNote {
		t.Errorf("feat/f = %+v", f.Run)
	}

	if g := l["feat/g"]; g.State != StateFailed || g.AttentionNote != crewWatchdogDeadNote || g.Crew.Detail != "" {
		t.Errorf("feat/g = %+v", g.Run)
	}

	// state "" reads idle
	var q, r, p *crewBranch
	for i := range got.remote {
		switch got.remote[i].Branch {
		case "feat/q":
			q = &got.remote[i]
		case "feat/r":
			r = &got.remote[i]
		case "feat/p":
			p = &got.remote[i]
		}
	}
	if q == nil || q.State != StateIdle {
		t.Errorf("feat/q = %+v", q)
	}
	// A remote worker's question is addressed to its local dispatcher: it is
	// routed in scan, so here it keeps its question and its episode start.
	if r == nil || r.Crew.Origin != "remote_worker" || r.Crew.Host != "halo" || r.CrewSession != "" || r.State != StateBlocked || r.Question == nil || r.Question.Via != "crew" || r.Question.Kind != QuestionCrew ||
		r.UpdatedAt == 0 || r.blockedSince != r.UpdatedAt*1000 ||
		len(r.Crew.Attention) != 1 || r.Crew.Attention[0].Action == nil || r.Crew.Attention[0].Action.Verb != "reply" {
		t.Errorf("feat/r = %+v", r)
	}
	// A peer's question belongs to the owning host's dispatcher: never blocked here.
	if p == nil || p.Crew.Origin != "peer" || p.Crew.Host != "mbp" || p.State != StateRunning || p.Question != nil || p.Crew.Detail != "" || p.blockedSince != 0 ||
		len(p.Crew.Attention) != 1 || p.Crew.Attention[0].Action != nil || p.Crew.Attention[0].Command != "" {
		t.Errorf("feat/p = %+v", p)
	}

	// crew-scoped items
	if it := got.crewItems["c2"]; len(it) != 1 || it[0].Action != nil || it[0].Command != "crew stream --crew c2 --status" {
		t.Errorf("c2 items = %+v", it)
	}
	if it := got.crewItems["c1"]; len(it) != 1 || it[0].Kind != "hold_matured" || it[0].Action != nil || it[0].Command != "crew hold due --crew c1" || it[0].Since != v.GeneratedAt-120 {
		t.Errorf("c1 items = %+v", it)
	}
}

func oneWorker(state string, detail *string, age int64) crewview.Overview {
	return crewview.Overview{
		GeneratedAt: 1000000,
		Workers:     []crewview.Worker{{Crew: "c1", Branch: "feat/x", Origin: "local", State: state, Detail: detail, AgeSec: ip(age)}},
	}
}

func TestCrewRunsFromViewBlockedNoDetail(t *testing.T) {
	v := oneWorker("blocked", nil, 0)
	v.Attention = []crewview.Item{{ID: "blocked:c1:feat/x", Crew: "c1", Branch: "feat/x", Origin: "local", Kind: "blocked"}}
	r := crewRunsFromView(v, nil).local["feat/x"]
	if r.State != StateBlocked || r.Question == nil || r.Question.Text != crewBlockedNoDetail {
		t.Errorf("got %+v q %+v", r.Run, r.Question)
	}
}

func TestCrewRunsFromViewUnlistedWatchdogPrefix(t *testing.T) {
	v := oneWorker("blocked", sp("load: host busy"), 0)
	v.Attention = []crewview.Item{{ID: "w", Crew: "c1", Branch: "feat/x", Origin: "local", Kind: "watchdog", Message: "load: host busy"}}
	r := crewRunsFromView(v, nil).local["feat/x"]
	if r.State != StateRunning || r.Attention != "" || r.AttentionNote != "" || r.Crew.Detail != "" || r.Question != nil {
		t.Errorf("got %+v", r.Run)
	}
}

func TestCrewRunsFromViewIdentity(t *testing.T) {
	v := goldenView(t)
	ids := map[string]crewIdentity{
		"feat/a": {crew: "c1", color: "#112233", sessions: 2, engineSession: "S", session: 7, lastActivity: 9, workerSilent: true},
		"feat/b": {crew: "other", color: "#445566", sessions: 5},
	}
	got := crewRunsFromView(v, ids).local
	a := got["feat/a"]
	if a.Crew.Color != "#112233" || a.Crew.Sessions != 2 || a.CrewSession != "S" || a.session != 7 || a.lastActivity != 9 || !a.workerSilent {
		t.Errorf("feat/a = %+v crew %+v", a.Run, a.Crew)
	}
	if b := got["feat/b"]; b.Crew.Color != "" || b.Crew.Sessions != 0 {
		t.Errorf("feat/b took another crew's identity: %+v", b.Crew)
	}
}

// blocked_since is the episode start, so a question already past the silence
// window still shows after a houston restart, and a re-stamp (age_sec reset)
// does not move it.
func TestCrewRunsFromViewBlockedSince(t *testing.T) {
	v := oneWorker("blocked", sp("q?"), 5)
	v.Workers[0].BlockedSince = ip(500_000_000)
	v.Attention = []crewview.Item{{ID: "b", Crew: "c1", Branch: "feat/x", Origin: "local", Kind: "blocked", Message: "q?"}}
	r := crewRunsFromView(v, nil).local["feat/x"]
	if r.State != StateBlocked || r.Question == nil || r.blockedSince != 500_000_000 {
		t.Fatalf("got %+v blockedSince %d", r.Run, r.blockedSince)
	}
	// The same episode, decoded from a fixture whose blocked_since is hours old.
	old := goldenView(t)
	old.GeneratedAt += 6 * 3600
	c := crewRunsFromView(old, nil).local["feat/c"]
	if c.State != StateBlocked || c.Question == nil || c.blockedSince != (goldenView(t).GeneratedAt-300)*1000 {
		t.Errorf("feat/c = %+v blockedSince %d", c.Run, c.blockedSince)
	}
	if routed := routeCrewQuestion(crewBranch{Run: c.Run, blockedSince: c.blockedSince}, true, time.UnixMilli(old.GeneratedAt*1000)); routed.State != StateBlocked {
		t.Errorf("a question past the silence window is %v, want blocked", routed.State)
	}
}

// crew reports a worker its dispatcher replied to as "answered": it is not
// needs-you and carries no question.
func TestCrewRunsFromViewAnsweredIsNotNeedsYou(t *testing.T) {
	v := oneWorker("answered", sp("q?"), 300)
	v.Workers[0].BlockedSince, v.Workers[0].AnsweredAt = ip(700_000_000), ip(900_000_000)
	r := crewRunsFromView(v, nil).local["feat/x"]
	if r.State != StateRunning || r.Question != nil || r.blockedSince != 0 {
		t.Errorf("got %+v blockedSince %d, want running with no question", r.Run, r.blockedSince)
	}
}

func TestCrewRunsFromViewWatchdogPrefixes(t *testing.T) {
	cases := []struct {
		msg  string
		att  Attention
		note string
	}{
		{"quota: quota exhausted", AttentionStuck, crewWatchdogQuotaNote},
		{"turn-stall: token count static", AttentionStuck, crewWatchdogTurnStallNote},
		{"quiet: pane unchanged for 1800s", AttentionStuck, crewWatchdogQuietNote},
		{"stalled: no output for 300s", AttentionStuck, crewWatchdogStalledNote},
		{"runaway: sentinel tokens leaked", AttentionStuck, crewWatchdogRunawayNote},
		{"unread: lead has not read the verdict", AttentionStuck, crewWatchdogUnreadNote},
		{"budget: token budget reached", AttentionStuck, crewWatchdogBudgetNote},
		{"load: 1m load 9 on 8 cores", AttentionNone, ""},
		{"mystery: x", AttentionNone, ""},
		{"quietly waiting", AttentionNone, ""},
		{"", AttentionNone, ""},
	}
	for _, c := range cases {
		t.Run(c.msg, func(t *testing.T) {
			v := oneWorker("blocked", sp(c.msg), 0)
			v.Attention = []crewview.Item{{ID: "w", Crew: "c1", Branch: "feat/x", Origin: "local", Kind: "watchdog", Message: c.msg}}
			r := crewRunsFromView(v, nil).local["feat/x"]
			if r.State != StateRunning || r.Question != nil || r.Attention != c.att || r.AttentionNote != c.note ||
				r.Crew.Detail != "" || r.blockedSince != 0 {
				t.Errorf("got %+v detail %q blockedSince %d", r.Run, r.Crew.Detail, r.blockedSince)
			}
		})
	}
}

func TestCrewRunsFromViewStates(t *testing.T) {
	for state, want := range map[string]State{"done": StateDone, "pr_open": StateReview, "failed": StateFailed, "exited": StateFailed} {
		r := crewRunsFromView(oneWorker(state, sp("gate won't pass"), 0), nil).local["feat/x"]
		if r.State != want || r.Attention != "" || r.AttentionNote != "" || r.Question != nil || r.Crew.Detail != "gate won't pass" {
			t.Errorf("%s: got %+v detail %q, want %q with the detail kept", state, r.Run, r.Crew.Detail, want)
		}
	}
}

// A watchdog's raw text never reaches the Crew tab: its items carry houston's
// wording, the same the activity feed uses. Other kinds keep crew's message.
func TestProjectItemWordsWatchdogItems(t *testing.T) {
	v := goldenView(t)
	byID := map[string]crewview.Item{}
	for _, it := range v.Attention {
		byID[it.ID] = it
	}
	cases := []struct {
		item crewview.Item
		want string
	}{
		{byID["prompt:c1:feat/d"], crewWatchdogPromptNote},
		{byID["stall:c1:feat/e"], crewWatchdogQuietNote},
		{byID["watchdog:c1:feat/f"], crewWatchdogRunawayNote},
		{byID["dead:c1:feat/g"], crewWatchdogDeadNote},
		{viewItem("watchdog", "feat/x", "load: host busy"), "watchdog: load"},
		{viewItem("stall", "feat/x", "no output for a long while"), "watchdog"},
		{viewItem("watchdog", "feat/x", "Weird Prefix: x"), "watchdog"},
		{byID["blocked:c1:feat/c"], "which schema should the migration target?"},
		{byID["failed:c1:feat/h"], "exit 1 after test failure"},
	}
	for _, c := range cases {
		t.Run(c.item.ID, func(t *testing.T) {
			if c.item.ID == "" {
				t.Fatal("golden item missing")
			}
			if got := projectItem(c.item, v.GeneratedAt).Message; got != c.want {
				t.Errorf("Message = %q, want %q", got, c.want)
			}
		})
	}
}

// A blocked row without blocked_since or age_sec has no known episode start:
// it is at least as old as the view.
func TestCrewRunsFromViewBlockedWithUnknownAge(t *testing.T) {
	v := oneWorker("blocked", sp("q?"), 0)
	v.Workers[0].AgeSec = nil
	v.Attention = []crewview.Item{{ID: "b", Crew: "c1", Branch: "feat/x", Origin: "local", Kind: "blocked", Message: "q?"}}
	r := crewRunsFromView(v, nil).local["feat/x"]
	if r.State != StateBlocked || r.Question == nil || r.Question.Text != "q?" {
		t.Errorf("got %+v q %+v, want still blocked with its question", r.Run, r.Question)
	}
	if want := v.GeneratedAt * 1000; r.blockedSince != want {
		t.Errorf("blockedSince = %d, want %d (generated_at)", r.blockedSince, want)
	}
}

// A held crew's snapshot is spliced back in place of its rows with every age
// shifted by the time since the snapshot, so UpdatedAt and Since do not move;
// the inputs are never written through, and a snapshot past crewStaleAfter is
// dropped so the crew reads as the view says.
func TestSpliceHeldRebasesAges(t *testing.T) {
	const gen = int64(10_000)
	w := func(crew, branch string, age *int64) crewview.Worker {
		return crewview.Worker{Crew: crew, Branch: branch, State: "working", AgeSec: age}
	}
	it := func(crew, branch string, age int64) crewview.Item {
		return crewview.Item{ID: crew + ":" + branch, Crew: crew, Branch: branch, Kind: "blocked", AgeSec: age}
	}
	capped := gen - int64(crewStaleAfter/time.Second)
	prevOf := func() map[string]rosterSnap {
		return map[string]rosterSnap{
			"c1": {at: gen - 300, workers: []crewview.Worker{w("c1", "a", ip(10)), w("c1", "n", nil)}, items: []crewview.Item{it("c1", "a", 20), it("c1", "", 30)}},
			"c3": {at: capped - 1, workers: []crewview.Worker{w("c3", "x", ip(5))}},
			"c4": {at: capped, workers: []crewview.Worker{w("c4", "y", ip(7))}},
		}
	}
	viewOf := func() crewview.Overview {
		return crewview.Overview{
			GeneratedAt: gen,
			Workers:     []crewview.Worker{w("c1", "a", ip(1)), w("c2", "b", ip(40))},
			Attention:   []crewview.Item{it("c1", "a", 1), it("c1", "", 2), it("c2", "b", 50)},
		}
	}
	c2Snap := rosterSnap{at: gen, workers: []crewview.Worker{w("c2", "b", ip(40))}, items: []crewview.Item{it("c2", "b", 50)}}

	for _, tc := range []struct {
		name        string
		errs        []string
		wantWorkers []crewview.Worker
		wantItems   []crewview.Item
		wantNext    map[string]rosterSnap
		wantHeld    map[string]bool
	}{
		{
			name:        "one crew's roster fails",
			errs:        []string{"crew c1: workers: timeout", "crew c3: workers: timeout", "crew c4: workers: timeout"},
			wantWorkers: []crewview.Worker{w("c2", "b", ip(40)), w("c1", "a", ip(310)), w("c1", "n", nil), w("c4", "y", ip(7+int64(crewStaleAfter/time.Second)))},
			wantItems:   []crewview.Item{it("c1", "", 2), it("c2", "b", 50), it("c1", "a", 320)},
			wantNext:    map[string]rosterSnap{"c1": prevOf()["c1"], "c2": c2Snap, "c4": prevOf()["c4"]},
			wantHeld:    map[string]bool{"c1": true, "c4": true},
		},
		{
			name:        "the whole roster fails",
			errs:        []string{"roster: crew crews: exit 1"},
			wantWorkers: []crewview.Worker{w("c2", "b", ip(40)), w("c1", "a", ip(310)), w("c1", "n", nil), w("c4", "y", ip(7+int64(crewStaleAfter/time.Second)))},
			wantItems:   []crewview.Item{it("c2", "b", 50), it("c1", "a", 320), it("c1", "", 330)},
			wantNext:    map[string]rosterSnap{"c1": prevOf()["c1"], "c4": prevOf()["c4"]},
			wantHeld:    map[string]bool{"c1": true, "c4": true},
		},
		{
			name:        "no roster error",
			errs:        []string{"peer mbp: unreachable"},
			wantWorkers: viewOf().Workers,
			wantItems:   viewOf().Attention,
			wantNext: map[string]rosterSnap{
				"c1": {at: gen, workers: []crewview.Worker{w("c1", "a", ip(1))}, items: []crewview.Item{it("c1", "a", 1), it("c1", "", 2)}},
				"c2": c2Snap,
			},
			wantHeld: map[string]bool{},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ov, prev := viewOf(), prevOf()
			got, next, heldIDs, _ := spliceHeld(ov, heldCrewsOf(tc.errs), prev)
			if !reflect.DeepEqual(got.Workers, tc.wantWorkers) {
				t.Errorf("workers = %+v\nwant      %+v", got.Workers, tc.wantWorkers)
			}
			if !reflect.DeepEqual(got.Attention, tc.wantItems) {
				t.Errorf("items = %+v\nwant    %+v", got.Attention, tc.wantItems)
			}
			if !reflect.DeepEqual(next, tc.wantNext) {
				t.Errorf("next = %+v\nwant   %+v", next, tc.wantNext)
			}
			if !reflect.DeepEqual(heldIDs, tc.wantHeld) {
				t.Errorf("held = %v, want %v", heldIDs, tc.wantHeld)
			}

			for i := range got.Workers {
				got.Workers[i].State = "scribbled"
				if got.Workers[i].AgeSec != nil && heldIDs[got.Workers[i].Crew] {
					*got.Workers[i].AgeSec = -1
				}
			}
			for i := range got.Attention {
				got.Attention[i].AgeSec = -1
			}
			if !reflect.DeepEqual(ov, viewOf()) {
				t.Error("spliceHeld's result aliases the view")
			}
			if !reflect.DeepEqual(prev, prevOf()) {
				t.Error("spliceHeld's result aliases the previous snapshots")
			}
		})
	}
}
