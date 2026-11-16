package runs

import (
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
	ids, _ := identityFromCrewLog(strings.NewReader(idDispatch + "\n"))
	want := crewIdentity{
		crew: "c1", codename: "otter", color: tmuxColorToHex("colour12"),
		engineSession: "S1", sessions: 1, workerSilent: true, lastActivity: 1000,
	}
	if got := ids["feat/a"]; got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestIdentityBadCodenameLeavesIdentityEmpty(t *testing.T) {
	row := `{"ts":1000,"crew_id":"c1","kind":"dispatch","branch":"feat/a","name":"Bad Name","tmux":"colour12"}`
	ids, _ := identityFromCrewLog(strings.NewReader(row + "\n"))
	got := ids["feat/a"]
	if got.codename != "" || got.color != "" {
		t.Fatalf("codename=%q color=%q, want both empty", got.codename, got.color)
	}
}

func TestIdentityResumeBumpsSessionsAndEngineSession(t *testing.T) {
	ids, _ := identityFromCrewLog(strings.NewReader(idDispatch + "\n" + idResume + "\n"))
	got := ids["feat/a"]
	if got.sessions != 2 || got.engineSession != "S2" || got.lastActivity != 2000 {
		t.Fatalf("got %+v", got)
	}
}

func TestIdentityWorkerStatusOnlyTouchesLiveness(t *testing.T) {
	before, _ := identityFromCrewLog(strings.NewReader(idDispatch + "\n"))
	after, _ := identityFromCrewLog(strings.NewReader(idDispatch + "\n" + idStatus + "\n"))
	b, a := before["feat/a"], after["feat/a"]
	if a.session != 1794838897 || a.lastActivity != 3000 || a.workerSilent {
		t.Fatalf("got %+v", a)
	}
	b.session, b.lastActivity, b.workerSilent = a.session, a.lastActivity, a.workerSilent
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("status changed identity: got %+v, want %+v", a, b)
	}
}

func TestIdentityWatchdogStatusIsSilent(t *testing.T) {
	wd := `{"ts":4000,"crew_id":"c1","from":"worker:feat/a#s5-1","kind":"status","body":{"state":"blocked","detail":"quiet: x","source":"watchdog"}}`
	ids, _ := identityFromCrewLog(strings.NewReader(idDispatch + "\n" + idStatus + "\n" + wd + "\n"))
	if !ids["feat/a"].workerSilent {
		t.Fatalf("watchdog status should leave the worker silent")
	}
}

func TestIdentityRepliesKeepMaxPerBranch(t *testing.T) {
	newer := `{"ts":5000,"crew_id":"c1","from":"dispatcher:c1","to":"worker:feat/a#s1","kind":"msg","body":"go"}`
	older := `{"ts":4000,"crew_id":"c1","from":"dispatcher:c1","to":"worker:feat/a#s1","kind":"msg","body":"hm"}`
	_, replies := identityFromCrewLog(strings.NewReader(newer + "\n" + older + "\n"))
	if replies["feat/a"] != 5000 {
		t.Fatalf("replies = %v, want feat/a=5000", replies)
	}
}

func TestAnsweredByDispatcher(t *testing.T) {
	for _, c := range []struct {
		reply, status int64
		want          bool
	}{{5000, 4, true}, {5000, 5, true}, {5000, 6, false}, {0, 0, false}} {
		if got := answeredByDispatcher(c.reply, c.status); got != c.want {
			t.Errorf("answeredByDispatcher(%d, %d) = %v, want %v", c.reply, c.status, got, c.want)
		}
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
	// replaces: bus fold garbage-line case
	ids, _ := identityFromCrewLog(strings.NewReader("not json at all\n" + idDispatch + "\n"))
	if ids["feat/a"].codename != "otter" {
		t.Fatalf("a torn line stopped later rows being read: %+v", ids)
	}
}

func TestIdentityLastWellFormedDispatchStands(t *testing.T) {
	// replaces: bus fold codename and colour cases
	log := `{"ts":1,"crew_id":"c1","kind":"dispatch","branch":"c","name":"ok","tmux":"#ff0000"}
{"ts":4,"crew_id":"c1","kind":"dispatch","branch":"d","name":"old","tmux":"colour25"}
{"ts":5,"crew_id":"c1","kind":"dispatch","branch":"d","name":"Bad name","tmux":"colour131"}
`
	ids, _ := identityFromCrewLog(strings.NewReader(log))
	if c := ids["c"]; c.codename != "" || c.color != "" {
		t.Errorf("c = %+v, want no identity for a malformed colour", c)
	}
	if d := ids["d"]; d.codename != "old" || d.color != tmuxColorToHex("colour25") {
		t.Errorf("d = %+v, want the last well-formed row to stand", d)
	}
}

func TestIdentityWorkerSilent(t *testing.T) {
	// replaces: bus fold workerSilent table
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
		ids, _ := identityFromCrewLog(strings.NewReader(tc.log))
		if got := ids[b].workerSilent; got != tc.want {
			t.Errorf("%s: workerSilent = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestIdentityRepliesOnlyFromTheDispatcherToTheirBranch(t *testing.T) {
	// replaces: bus fold answered-question cases for another branch and a non-dispatcher sender
	log := `{"ts":3000,"crew_id":"c1","from":"dispatcher:c1","to":"worker:other/branch#s1","kind":"msg","body":"unrelated"}
{"ts":3100,"crew_id":"c1","from":"worker:other#s1","to":"worker:fix/9#s1","kind":"msg","body":"not the dispatcher"}
`
	_, replies := identityFromCrewLog(strings.NewReader(log))
	if replies["fix/9"] != 0 || replies["other/branch"] != 3000 {
		t.Errorf("replies = %v, want only other/branch=3000", replies)
	}
}
