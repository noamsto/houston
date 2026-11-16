package runs

import (
	"os"
	"testing"

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
	got := crewRunsFromView(v, nil, nil, blockedMemory{})
	if n := len(got.local) + len(got.remote); n != 12 {
		t.Fatalf("workers = %d, want 12", n)
	}
	l := got.local

	// replaces: bus fold working-status case
	a := l["feat/a"]
	if a.State != StateRunning || a.Crew.Detail != "execute" || a.Crew.Codename != "otter" ||
		a.Crew.Tier != "standard" || a.Crew.Model != "sonnet" || a.Crew.Title != "Task feat/a" ||
		a.Agent != "claude" || a.Crew.Origin != "local" || a.Crew.Name != "c1" {
		t.Errorf("feat/a = %+v crew %+v", a.Run, a.Crew)
	}
	if want := v.GeneratedAt - 45; a.UpdatedAt != want {
		t.Errorf("UpdatedAt = %d, want %d", a.UpdatedAt, want)
	}

	// replaces: pr_open / PR number case
	if b := l["feat/b"]; b.State != StateReview || b.PR == nil || b.PR.Number != "12" || b.PR.URL != "https://github.com/o/r/pull/12" {
		t.Errorf("feat/b = %+v", b.Run)
	}

	// replaces: blocked-with-detail question case
	c := l["feat/c"]
	if c.State != StateBlocked || c.Question == nil || *c.Question != (Question{Text: "which schema should the migration target?", Via: "crew", Kind: QuestionCrew}) {
		t.Errorf("feat/c = %+v q %+v", c.Run, c.Question)
	}

	// replaces: watchdog prompt case
	d := l["feat/d"]
	if d.State != StateBlocked || d.Question == nil || *d.Question != (Question{Text: crewWatchdogPromptNote, Via: "pane", Kind: QuestionDialog}) || d.Crew.Detail != "" {
		t.Errorf("feat/d = %+v q %+v", d.Run, d.Question)
	}

	// replaces: watchdog quiet / runaway stuck cases
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

	// replaces: watchdog dead case
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
	// replaces: blocked status without detail
	v := oneWorker("blocked", nil, 0)
	v.Attention = []crewview.Item{{ID: "blocked:c1:feat/x", Crew: "c1", Branch: "feat/x", Origin: "local", Kind: "blocked"}}
	r := crewRunsFromView(v, nil, nil, blockedMemory{}).local["feat/x"]
	if r.State != StateBlocked || r.Question == nil || r.Question.Text != crewBlockedNoDetail {
		t.Errorf("got %+v q %+v", r.Run, r.Question)
	}
}

func TestCrewRunsFromViewUnlistedWatchdogPrefix(t *testing.T) {
	// replaces: watchdog load: prefix claims nothing
	v := oneWorker("blocked", sp("load: host busy"), 0)
	v.Attention = []crewview.Item{{ID: "w", Crew: "c1", Branch: "feat/x", Origin: "local", Kind: "watchdog", Message: "load: host busy"}}
	r := crewRunsFromView(v, nil, nil, blockedMemory{}).local["feat/x"]
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
	got := crewRunsFromView(v, ids, nil, blockedMemory{}).local
	a := got["feat/a"]
	if a.Crew.Color != "#112233" || a.Crew.Sessions != 2 || a.CrewSession != "S" || a.session != 7 || a.lastActivity != 9 || !a.workerSilent {
		t.Errorf("feat/a = %+v crew %+v", a.Run, a.Crew)
	}
	if b := got["feat/b"]; b.Crew.Color != "" || b.Crew.Sessions != 0 {
		t.Errorf("feat/b took another crew's identity: %+v", b.Crew)
	}
}

func TestCrewRunsFromViewBlockedSince(t *testing.T) {
	const g = 1000000
	mk := func(gen int64, state string, age int64) crewview.Overview {
		v := oneWorker(state, sp("q?"), age)
		v.GeneratedAt = gen
		if state == "blocked" {
			v.Attention = []crewview.Item{{ID: "b", Crew: "c1", Branch: "feat/x", Origin: "local", Kind: "blocked", Message: "q?"}}
		}
		return v
	}
	mem := blockedMemory{}
	r := crewRunsFromView(mk(g, "blocked", 300), nil, nil, mem).local["feat/x"]
	if r.blockedSince != (g-300)*1000 {
		t.Fatalf("view1 blockedSince = %d", r.blockedSince)
	}
	r = crewRunsFromView(mk(g+300, "blocked", 0), nil, nil, mem).local["feat/x"]
	if r.blockedSince != (g-300)*1000 {
		t.Fatalf("view2 blockedSince = %d", r.blockedSince)
	}
	crewRunsFromView(mk(g+600, "working", 5), nil, nil, mem)
	if len(mem) != 0 {
		t.Fatalf("mem = %v", mem)
	}
}

func TestCrewRunsFromViewReplyShim(t *testing.T) {
	// replaces: dispatcher reply clears a blocked question
	const g = 1000000
	v := oneWorker("blocked", sp("q?"), 300)
	v.GeneratedAt = g
	v.Attention = []crewview.Item{{ID: "b", Crew: "c1", Branch: "feat/x", Origin: "local", Kind: "blocked"}}
	mem := blockedMemory{}
	r := crewRunsFromView(v, nil, map[string]int64{"feat/x": (g-300)*1000 + 1}, mem).local["feat/x"]
	if r.State != "" || r.Question != nil || r.Crew.Detail != "" || len(mem) != 0 {
		t.Errorf("got %+v mem %v", r.Run, mem)
	}
}

func TestCrewRunsFromViewWatchdogPrefixes(t *testing.T) {
	// replaces: bus fold watchdog prefix table
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
			mem := blockedMemory{}
			r := crewRunsFromView(v, nil, nil, mem).local["feat/x"]
			if r.State != StateRunning || r.Question != nil || r.Attention != c.att || r.AttentionNote != c.note ||
				r.Crew.Detail != "" || r.blockedSince != 0 || len(mem) != 0 {
				t.Errorf("got %+v detail %q blockedSince %d mem %v", r.Run, r.Crew.Detail, r.blockedSince, mem)
			}
		})
	}
}

func TestCrewRunsFromViewStates(t *testing.T) {
	// replaces: bus fold done/pr_open and worker-failed/exited cases
	for state, want := range map[string]State{"done": StateDone, "pr_open": StateReview, "failed": StateFailed, "exited": StateFailed} {
		r := crewRunsFromView(oneWorker(state, sp("gate won't pass"), 0), nil, nil, blockedMemory{}).local["feat/x"]
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

// A row with no age_sec has no known status time: an old dispatcher reply
// cannot be said to answer it, and its episode is at least as old as the view.
func TestCrewRunsFromViewBlockedWithUnknownAge(t *testing.T) {
	v := oneWorker("blocked", sp("q?"), 0)
	v.Workers[0].AgeSec = nil
	v.Attention = []crewview.Item{{ID: "b", Crew: "c1", Branch: "feat/x", Origin: "local", Kind: "blocked", Message: "q?"}}
	mem := blockedMemory{}
	r := crewRunsFromView(v, nil, map[string]int64{"feat/x": 5_000}, mem).local["feat/x"]
	if r.State != StateBlocked || r.Question == nil || r.Question.Text != "q?" {
		t.Errorf("got %+v q %+v, want still blocked with its question", r.Run, r.Question)
	}
	if want := v.GeneratedAt * 1000; r.blockedSince != want || mem[crewWorkerKey{"c1", "feat/x"}] != want {
		t.Errorf("blockedSince = %d mem %v, want %d (generated_at)", r.blockedSince, mem, want)
	}
}

// The dispatcher answering a remote worker writes its reply on this bus: the
// reply clears the question as it does for a local worker.
func TestCrewRunsFromViewReplyShimClearsARemoteWorker(t *testing.T) {
	const g = 1000000
	v := oneWorker("blocked", sp("q?"), 300)
	v.GeneratedAt = g
	v.Workers[0].Origin, v.Workers[0].Host = crewview.OriginRemote, "halo"
	v.Attention = []crewview.Item{{ID: "b", Crew: "c1", Branch: "feat/x", Origin: crewview.OriginRemote, Kind: "blocked"}}
	mem := blockedMemory{}
	got := crewRunsFromView(v, nil, map[string]int64{"feat/x": (g-300)*1000 + 1}, mem).remote
	if len(got) != 1 || got[0].State != "" || got[0].Question != nil || len(mem) != 0 {
		t.Errorf("got %+v mem %v", got, mem)
	}
}
