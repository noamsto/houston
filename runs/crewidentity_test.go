package runs

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const (
	idDispatch = `{"ts":1000,"crew_id":"c1","kind":"dispatch","branch":"feat/a","name":"otter","tmux":"colour12","engine_session":"S1"}`
	idResume   = `{"ts":2000,"crew_id":"c1","kind":"resume","branch":"feat/a","engine_session":"S2"}`
	idStatus   = `{"ts":3000,"crew_id":"c1","from":"worker:feat/a#s1794838897-1624760","kind":"status","body":{"state":"blocked","detail":"q","pr_url":"https://github.com/o/r/pull/9"}}`
)

func TestIdentityFromDispatchRow(t *testing.T) {
	ids := identityFromCrewLog(strings.NewReader(idDispatch + "\n"))
	want := crewIdentity{
		crew: "c1", codename: "otter", color: tmuxColorToHex("colour12"),
		engineSession: "S1", sessions: 1, workerSilent: true, lastActivity: 1000, state: "working",
	}
	if got := ids["feat/a"]; got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestIdentityBadCodenameLeavesIdentityEmpty(t *testing.T) {
	row := `{"ts":1000,"crew_id":"c1","kind":"dispatch","branch":"feat/a","name":"Bad Name","tmux":"colour12"}`
	ids := identityFromCrewLog(strings.NewReader(row + "\n"))
	got := ids["feat/a"]
	if got.codename != "" || got.color != "" {
		t.Fatalf("codename=%q color=%q, want both empty", got.codename, got.color)
	}
}

func TestIdentityResumeBumpsSessionsAndEngineSession(t *testing.T) {
	ids := identityFromCrewLog(strings.NewReader(idDispatch + "\n" + idResume + "\n"))
	got := ids["feat/a"]
	if got.sessions != 2 || got.engineSession != "S2" || got.lastActivity != 2000 {
		t.Fatalf("got %+v", got)
	}
}

func TestIdentityWorkerStatusOnlyTouchesLiveness(t *testing.T) {
	before := identityFromCrewLog(strings.NewReader(idDispatch + "\n"))
	after := identityFromCrewLog(strings.NewReader(idDispatch + "\n" + idStatus + "\n"))
	b, a := before["feat/a"], after["feat/a"]
	if a.session != 1794838897 || a.lastActivity != 3000 || a.workerSilent {
		t.Fatalf("got %+v", a)
	}
	b.session, b.lastActivity, b.workerSilent = a.session, a.lastActivity, a.workerSilent
	b.state, b.detail, b.prURL, b.statusTS, b.blockedSince = a.state, a.detail, a.prURL, a.statusTS, a.blockedSince
	if a.state != "blocked" || a.detail != "q" || a.prURL != "https://github.com/o/r/pull/9" || a.statusTS != 3000 || a.blockedSince != 3000 {
		t.Fatalf("status fields = %+v", a)
	}
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("status changed identity: got %+v, want %+v", a, b)
	}
}

func TestIdentityWatchdogStatusIsSilent(t *testing.T) {
	wd := `{"ts":4000,"crew_id":"c1","from":"worker:feat/a#s5-1","kind":"status","body":{"state":"blocked","detail":"quiet: x","source":"watchdog"}}`
	ids := identityFromCrewLog(strings.NewReader(idDispatch + "\n" + idStatus + "\n" + wd + "\n"))
	if !ids["feat/a"].workerSilent {
		t.Fatalf("watchdog status should leave the worker silent")
	}
}

func TestReadIdentityLogCachesOnSizeAndMtime(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bus.jsonl")
	if err := os.WriteFile(path, []byte(idDispatch+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cache := map[string]identityLog{}
	first, ok := readIdentityLog(path, cache)
	if !ok {
		t.Fatal("first read failed")
	}
	cache[path] = first
	second, ok := readIdentityLog(path, cache)
	if !ok || !reflect.DeepEqual(first, second) {
		t.Fatalf("second read differs: %+v vs %+v", first, second)
	}
}

func TestIdentitySurvivesGarbageLines(t *testing.T) {
	ids := identityFromCrewLog(strings.NewReader("not json at all\n" + idDispatch + "\n"))
	if ids["feat/a"].codename != "otter" {
		t.Fatalf("a torn line stopped later rows being read: %+v", ids)
	}
}

func TestIdentityLastWellFormedDispatchStands(t *testing.T) {
	log := `{"ts":1,"crew_id":"c1","kind":"dispatch","branch":"c","name":"ok","tmux":"#ff0000"}
{"ts":4,"crew_id":"c1","kind":"dispatch","branch":"d","name":"old","tmux":"colour25"}
{"ts":5,"crew_id":"c1","kind":"dispatch","branch":"d","name":"Bad name","tmux":"colour131"}
`
	ids := identityFromCrewLog(strings.NewReader(log))
	if c := ids["c"]; c.codename != "" || c.color != "" {
		t.Errorf("c = %+v, want no identity for a malformed colour", c)
	}
	if d := ids["d"]; d.codename != "old" || d.color != tmuxColorToHex("colour25") {
		t.Errorf("d = %+v, want the last well-formed row to stand", d)
	}
}

func TestIdentityWorkerSilent(t *testing.T) {
	const b = "feat/303"
	release := `{"kind":"release","branch":"feat/303","ts":1300}` + "\n"
	claim := `{"kind":"claim","branch":"feat/303","ts":1400}` + "\n"
	tests := []struct {
		name string
		log  string
		want bool
	}{
		{"dispatch only", dispatchRow(1000, b), true},
		{"dispatch then worker status", dispatchRow(1000, b) + statusRow(1100, b, "working"), false},
		{"worker status then watchdog", dispatchRow(1000, b) + statusRow(1100, b, "working") +
			watchdogRow(1200, b, "stalled: no output for 300s"), true},
		{"watchdog then worker status", watchdogRow(1000, b, "stalled: no output for 300s") +
			statusRow(1100, b, "working"), false},
		{"worker status then release and claim rows", dispatchRow(1000, b) + statusRow(1100, b, "working") +
			release + claim, false},
		{"finished, released, redispatched, watchdog", statusRow(1000, b, "pr_open") + statusRow(1100, b, "done") +
			release + claim + dispatchRow(1500, b) + watchdogRow(1600, b, "stalled: no output for 300s"), true},
	}
	for _, tc := range tests {
		ids := identityFromCrewLog(strings.NewReader(tc.log))
		if got := ids[b].workerSilent; got != tc.want {
			t.Errorf("%s: workerSilent = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestIdentityStatusFold(t *testing.T) {
	const b = "feat/a"
	row := func(ts int64, state, detail string) string {
		return fmt.Sprintf(`{"ts":%d,"crew_id":"c1","from":"worker:%s#s1","kind":"status","body":{"state":%q,"detail":%q}}`+"\n", ts, b, state, detail)
	}
	wd := func(ts int64, state, detail string) string {
		return fmt.Sprintf(`{"ts":%d,"crew_id":"c1","from":"worker:%s#s1","kind":"status","body":{"state":%q,"detail":%q,"source":"watchdog"}}`+"\n", ts, b, state, detail)
	}
	restampRow := func(ts int64, detail string) string {
		return fmt.Sprintf(`{"ts":%d,"crew_id":"c1","from":"worker:%s#s1","kind":"status","body":{"state":"blocked","detail":%q,"restamp":true}}`+"\n", ts, b, detail)
	}
	reapRow := func(ts int64) string { return fmt.Sprintf(`{"ts":%d,"kind":"reap","branch":"feat/a"}`+"\n", ts) }
	releaseRow := func(ts int64) string { return fmt.Sprintf(`{"ts":%d,"kind":"release","branch":"feat/a"}`+"\n", ts) }
	dispatch := `{"ts":1000,"crew_id":"c1","kind":"dispatch","branch":"feat/a","title":"T","tier":"standard","engine":"claude","model":"sonnet"}` + "\n"
	reply := `{"ts":%d,"crew_id":"c1","from":"dispatcher:c1","to":"worker:feat/a#s1","kind":"msg","body":"go on"}` + "\n"
	tests := []struct {
		name string
		log  string
		want func(crewIdentity) string // empty when it holds
	}{
		{"dispatch sets title and resets state", row(900, "done", "x") + dispatch, func(i crewIdentity) string {
			if i.title != "T" || i.tier != "standard" || i.engine != "claude" || i.model != "sonnet" || i.state != "working" || i.detail != "" || i.statusTS != 0 {
				return fmt.Sprintf("%+v", i)
			}
			return ""
		}},
		{"resume resets state", dispatch + row(1100, "pr_open", "") + `{"ts":1200,"crew_id":"c1","kind":"resume","branch":"feat/a"}` + "\n", func(i crewIdentity) string {
			if i.state != "working" || i.prURL != "" || i.title != "T" {
				return fmt.Sprintf("%+v", i)
			}
			return ""
		}},
		{"blockedSince is kept while blocked", dispatch + row(1100, "blocked", "q1") + row(1200, "blocked", "q1"), func(i crewIdentity) string {
			if i.state != "blocked" || i.detail != "q1" || i.blockedSince != 1100 {
				return fmt.Sprintf("%+v", i)
			}
			return ""
		}},
		{"blockedSince clears on leaving blocked", dispatch + row(1100, "blocked", "q") + row(1200, "working", ""), func(i crewIdentity) string {
			if i.state != "working" || i.blockedSince != 0 {
				return fmt.Sprintf("%+v", i)
			}
			return ""
		}},
		{"dispatcher reply answers a blocked worker", dispatch + row(1100, "blocked", "q") + fmt.Sprintf(reply, 1200), func(i crewIdentity) string {
			if i.state != "answered" || i.statusTS != 1200 {
				return fmt.Sprintf("%+v", i)
			}
			return ""
		}},
		{"dispatcher message to a working worker changes nothing", dispatch + row(1100, "working", "") + fmt.Sprintf(reply, 1200), func(i crewIdentity) string {
			if i.state != "working" {
				return fmt.Sprintf("%+v", i)
			}
			return ""
		}},
		{"reap row on a finished worker", dispatch + row(1100, "done", "") + reapRow(1300), func(i crewIdentity) string {
			if i.state != "reaped" || i.statusTS != 1300 {
				return fmt.Sprintf("%+v", i)
			}
			return ""
		}},
		{"reap row on a failed worker", dispatch + row(1100, "failed", "") + reapRow(1300), func(i crewIdentity) string {
			if i.state != "reaped" {
				return fmt.Sprintf("%+v", i)
			}
			return ""
		}},
		{"reap row on an exited worker", dispatch + row(1100, "exited", "") + reapRow(1300), func(i crewIdentity) string {
			if i.state != "reaped" {
				return fmt.Sprintf("%+v", i)
			}
			return ""
		}},
		{"reap row on a working worker is ignored", dispatch + row(1100, "working", "") + reapRow(1300), func(i crewIdentity) string {
			if i.state != "working" || i.statusTS != 1100 {
				return fmt.Sprintf("%+v", i)
			}
			return ""
		}},
		{"release keeps a done worker done", dispatch + row(1100, "done", "") + releaseRow(1300), func(i crewIdentity) string {
			if i.state != "done" || i.statusTS != 1100 {
				return fmt.Sprintf("%+v", i)
			}
			return ""
		}},
		{"release keeps a failed worker failed", dispatch + row(1100, "failed", "") + releaseRow(1300), func(i crewIdentity) string {
			if i.state != "failed" {
				return fmt.Sprintf("%+v", i)
			}
			return ""
		}},
		{"new question without a working row resets blockedSince", dispatch + row(1100, "blocked", "q1") + row(1200, "blocked", "q2"), func(i crewIdentity) string {
			if i.state != "blocked" || i.detail != "q2" || i.blockedSince != 1200 {
				return fmt.Sprintf("%+v", i)
			}
			return ""
		}},
		{"repeated question keeps blockedSince", dispatch + row(1100, "blocked", "q1") + row(1200, "blocked", "q1"), func(i crewIdentity) string {
			if i.blockedSince != 1100 {
				return fmt.Sprintf("%+v", i)
			}
			return ""
		}},
		{"restamp keeps blockedSince", dispatch + row(1100, "blocked", "q1") + restampRow(1200, "q2"), func(i crewIdentity) string {
			if i.blockedSince != 1100 || i.detail != "q2" {
				return fmt.Sprintf("%+v", i)
			}
			return ""
		}},
		{"watchdog row between questions does not count", dispatch + row(1100, "blocked", "q1") + wd(1150, "blocked", "quiet: x") + row(1200, "blocked", "q1"), func(i crewIdentity) string {
			if i.blockedSince != 1100 {
				return fmt.Sprintf("%+v", i)
			}
			return ""
		}},
		{"reaped status", dispatch + row(1100, "reaped", ""), func(i crewIdentity) string {
			if i.state != "reaped" {
				return fmt.Sprintf("%+v", i)
			}
			return ""
		}},
		{"watchdog prompt", dispatch + row(1100, "working", "") + wd(1200, "blocked", "prompt: dialog"), func(i crewIdentity) string {
			if i.state != "working" || i.watchdog != (watchdogItem{"prompt", "prompt: dialog"}) {
				return fmt.Sprintf("%+v", i)
			}
			return ""
		}},
		{"watchdog quiet", dispatch + wd(1200, "blocked", "quiet: x"), func(i crewIdentity) string {
			if i.watchdog != (watchdogItem{"watchdog", "quiet: x"}) {
				return fmt.Sprintf("%+v", i)
			}
			return ""
		}},
		{"watchdog dead fails the worker", dispatch + row(1100, "working", "") + wd(1200, "failed", "dead: engine exited"), func(i crewIdentity) string {
			if i.state != "failed" || i.statusTS != 1200 || i.watchdog.kind != "dead" {
				return fmt.Sprintf("%+v", i)
			}
			return ""
		}},
		{"watchdog unlisted prefix claims nothing", dispatch + wd(1200, "blocked", "load: high"), func(i crewIdentity) string {
			if i.watchdog != (watchdogItem{}) || i.state != "working" {
				return fmt.Sprintf("%+v", i)
			}
			return ""
		}},
		{"worker status clears the watchdog item", dispatch + wd(1200, "blocked", "quiet: x") + row(1300, "working", ""), func(i crewIdentity) string {
			if i.watchdog != (watchdogItem{}) {
				return fmt.Sprintf("%+v", i)
			}
			return ""
		}},
	}
	for _, tc := range tests {
		if bad := tc.want(identityFromCrewLog(strings.NewReader(tc.log))[b]); bad != "" {
			t.Errorf("%s: got %s", tc.name, bad)
		}
	}
}
