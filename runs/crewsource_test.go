package runs

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/noamsto/houston/tmux"
)

const crewFixture = `{"ts":1000,"crew_id":"c1","kind":"dispatch","branch":"fix/412","title":"fix the ws drop","tier":"standard","engine":"claude","model":"sonnet","session":"houston"}
{"ts":1100,"crew_id":"c1","from":"worker:fix/412#s1","kind":"status","body":{"state":"working"}}
{"ts":1200,"crew_id":"c1","from":"worker:fix/412#s1","kind":"status","body":{"state":"blocked","detail":"Keep the legacy route?"}}
{"ts":1300,"crew_id":"c1","from":"worker:feat/413#s2","kind":"status","body":{"state":"pr_open","pr_url":"https://github.com/x/y/pull/9"}}
not json at all
`

func TestDeltasFromCrewLogTakesLatestStatePerBranch(t *testing.T) {
	got := deltasFromCrewLog(strings.NewReader(crewFixture))

	r, ok := got["fix/412"]
	if !ok {
		t.Fatalf("no entry for fix/412, got keys %v", keysOf(got))
	}
	if r.State != StateBlocked {
		t.Errorf("State = %q, want blocked — the latest status wins", r.State)
	}
	if r.Question == nil || r.Question.Text != "Keep the legacy route?" {
		t.Errorf("Question = %+v, want the blocking detail", r.Question)
	}
	if r.Question != nil && r.Question.Via != "crew" {
		t.Errorf("Question.Via = %q, want crew — it is answered with `crew reply`", r.Question.Via)
	}
}

func TestDeltasFromCrewLogCarriesDispatchMetadata(t *testing.T) {
	got := deltasFromCrewLog(strings.NewReader(crewFixture))
	r := got["fix/412"]
	if r.Crew == nil || r.Crew.Name != "c1" || r.Crew.Tier != "standard" {
		t.Fatalf("Crew = %+v, want name c1 tier standard", r.Crew)
	}
	if r.Branch != "fix/412" {
		t.Errorf("Branch = %q", r.Branch)
	}
	if r.Agent != "claude" {
		t.Errorf("Agent = %q, want claude — without it a crew run has no agent and is never listed", r.Agent)
	}
}

func TestDeltasFromCrewLogSurvivesGarbageLines(t *testing.T) {
	got := deltasFromCrewLog(strings.NewReader(crewFixture))
	if _, ok := got["feat/413"]; !ok {
		t.Fatal("a malformed line stopped later records being read")
	}
	if got["feat/413"].State != StateReview {
		t.Errorf("feat/413 State = %q, want review", got["feat/413"].State)
	}
}

func TestScanFindsBusFromInsideAWorktree(t *testing.T) {
	// Worktree-per-branch is this project's mandated topology, and it is the
	// case that was broken: <worktree>/.git is a file, so the bus lives under
	// the main checkout's git dir, not the worktree's.
	main := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"commit", "-q", "--allow-empty", "-m", "seed"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = main
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("git unavailable: %v %s", err, out)
		}
	}

	wt := filepath.Join(t.TempDir(), "wt")
	cmd := exec.Command("git", "worktree", "add", "-q", "-b", "feat/x", wt)
	cmd.Dir = main
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("git worktree unavailable: %v %s", err, out)
	}

	busDir := filepath.Join(main, ".git", "crew")
	if err := os.MkdirAll(busDir, 0o755); err != nil {
		t.Fatal(err)
	}
	line := `{"ts":1000,"crew_id":"c1","from":"worker:feat/x#s1","kind":"status","body":{"state":"blocked","detail":"Keep the alias?"}}` + "\n"
	if err := os.WriteFile(filepath.Join(busDir, "events.jsonl"), []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}

	s := &CrewSource{crewDirs: map[string]string{}}
	got := s.scanRoots([]string{wt})

	byBranch, ok := got[busDir]
	if !ok {
		t.Fatalf("no bus keyed on the main checkout's crew dir %q — buses %v", busDir, busKeysOf(got))
	}
	r, ok := byBranch["feat/x"]
	if !ok {
		t.Fatalf("scan found nothing from inside a worktree — keys %v", keysOf(byBranch))
	}
	if r.State != StateBlocked || r.Question == nil {
		t.Fatalf("got %+v, want a blocked run carrying its question", r)
	}
}

func TestTickCrewDeltasEvictsFinishedRunsAfterGrace(t *testing.T) {
	now := time.Now()
	const key = "crew/" + testBus + "/fix/412"
	current := map[string]Run{
		key: {Branch: "fix/412", State: StateDone, UpdatedAt: now.Add(-30 * time.Minute).Unix()},
	}
	seen := map[string]bool{key: true}

	deltas, newSeen := tickCrewDeltas(current, seen, now)

	var gone []Delta
	for _, d := range deltas {
		if d.Gone {
			gone = append(gone, d)
		}
	}
	if len(gone) != 1 {
		t.Fatalf("got %d Gone deltas, want exactly 1: %+v", len(gone), deltas)
	}
	if gone[0].Key != key {
		t.Errorf("Gone delta Key = %q, want %q verbatim — Registry.Apply matches on the key it was given", gone[0].Key, key)
	}
	if gone[0].Source != "crew" {
		t.Errorf("Gone delta Source = %q, want crew — Registry.Apply deletes r.layers[key][Source], so an empty Source silently no-ops eviction", gone[0].Source)
	}
	if newSeen[key] {
		t.Errorf("%s still present in the new seen set, want evicted", key)
	}
}

func TestTickCrewDeltasKeepsFreshlyFinishedRuns(t *testing.T) {
	now := time.Now()
	current := map[string]Run{
		"%307": {Branch: "fix/412", State: StateDone, UpdatedAt: now.Add(-1 * time.Minute).Unix()},
	}

	deltas, newSeen := tickCrewDeltas(current, map[string]bool{}, now)

	if len(deltas) != 1 || deltas[0].Gone {
		t.Fatalf("deltas = %+v, want one non-Gone delta for a freshly finished run", deltas)
	}
	if !newSeen["%307"] {
		t.Error("the pane key is missing from the new seen set, want present")
	}
}

func TestTickCrewDeltasNeverEvictsAQuietBlockedRun(t *testing.T) {
	now := time.Now()
	const key = "crew/" + testBus + "/fix/412"
	current := map[string]Run{
		key: {Branch: "fix/412", State: StateBlocked, UpdatedAt: now.Add(-24 * time.Hour).Unix()},
	}

	deltas, newSeen := tickCrewDeltas(current, map[string]bool{}, now)

	if len(deltas) != 1 || deltas[0].Gone {
		t.Fatalf("deltas = %+v, want one non-Gone delta — a quiet blocked run must never be evicted", deltas)
	}
	if !newSeen[key] {
		t.Errorf("%s missing from the new seen set, want present", key)
	}
}

func TestTickCrewDeltasKeepsADispatchedRunWithNoStatusYet(t *testing.T) {
	now := time.Now()
	const key = "crew/" + testBus + "/fix/412"
	current := map[string]Run{
		key: {Branch: "fix/412"},
	}

	deltas, newSeen := tickCrewDeltas(current, map[string]bool{}, now)

	if len(deltas) != 1 || deltas[0].Gone {
		t.Fatalf("deltas = %+v, want one non-Gone delta — a dispatch-only run has nothing to evict", deltas)
	}
	if !newSeen[key] {
		t.Errorf("%s missing from the new seen set, want present", key)
	}
}

func keysOf(m map[string]Run) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func busKeysOf(m map[string]map[string]Run) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestDeltasFromCrewLogParsesMsgRecords(t *testing.T) {
	// Before the Body/json.RawMessage fix, a msg record's string body failed
	// to unmarshal into the old struct-typed Body, which failed the whole
	// line's json.Unmarshal — dropping fields ordinary to any record, like
	// tier, along with it. This msg record carries a tier the dispatch record
	// did not; seeing it stick is proof the line was actually parsed, not
	// skipped.
	const fixture = `{"ts":1000,"crew_id":"c1","kind":"dispatch","branch":"fix/4","engine":"claude"}
{"ts":1100,"crew_id":"c1","from":"worker:fix/4#s1","kind":"msg","tier":"deep","body":"{\"ok\":true}"}
`
	got := deltasFromCrewLog(strings.NewReader(fixture))
	r, ok := got["fix/4"]
	if !ok {
		t.Fatalf("no entry for fix/4, got keys %v", keysOf(got))
	}
	if r.Crew == nil || r.Crew.Tier != "deep" {
		t.Fatalf("Crew = %+v, want Tier deep from the msg record", r.Crew)
	}
}

func TestDeltasFromCrewLogBlockedAlwaysCarriesAQuestion(t *testing.T) {
	t.Run("with detail", func(t *testing.T) {
		const fixture = `{"ts":1000,"crew_id":"c1","kind":"dispatch","branch":"fix/5","engine":"claude"}
{"ts":1100,"crew_id":"c1","from":"worker:fix/5#s1","kind":"status","body":{"state":"blocked","detail":"Keep the alias?"}}
`
		got := deltasFromCrewLog(strings.NewReader(fixture))
		r := got["fix/5"]
		if r.State != StateBlocked {
			t.Fatalf("State = %q, want blocked", r.State)
		}
		if r.Question == nil || r.Question.Via != "crew" || r.Question.Text != "Keep the alias?" {
			t.Fatalf("Question = %+v, want the detail text with Via crew", r.Question)
		}
	})

	t.Run("without detail", func(t *testing.T) {
		const fixture = `{"ts":1000,"crew_id":"c1","kind":"dispatch","branch":"fix/6","engine":"claude"}
{"ts":1100,"crew_id":"c1","from":"worker:fix/6#s1","kind":"status","body":{"state":"blocked"}}
`
		got := deltasFromCrewLog(strings.NewReader(fixture))
		r := got["fix/6"]
		if r.State != StateBlocked {
			t.Fatalf("State = %q, want blocked", r.State)
		}
		if r.Question == nil || r.Question.Via != "crew" || r.Question.Text != crewBlockedNoDetail {
			t.Fatalf("Question = %+v, want the synthesised text with Via crew", r.Question)
		}
	})
}

func TestDeltasFromCrewLogRetiresAnAnsweredQuestion(t *testing.T) {
	const blocked = `{"ts":1000,"crew_id":"c1","kind":"dispatch","branch":"fix/9","engine":"claude"}
{"ts":2000,"crew_id":"c1","from":"worker:fix/9#s1","kind":"status","body":{"state":"blocked","detail":"Keep the alias?"}}
`

	t.Run("answered", func(t *testing.T) {
		fixture := blocked + `{"ts":3000,"crew_id":"c1","from":"dispatcher:c1","to":"worker:fix/9#s1","kind":"msg","body":"keep it"}
`
		got := deltasFromCrewLog(strings.NewReader(fixture))
		r, ok := got["fix/9"]
		if !ok {
			t.Fatalf("no entry for fix/9, got keys %v", keysOf(got))
		}
		if r.State != "" {
			t.Errorf("State = %q, want \"\" — the question was answered", r.State)
		}
		if r.Question != nil {
			t.Errorf("Question = %+v, want nil — the question was answered", r.Question)
		}
		if r.Branch != "fix/9" || r.Crew == nil || r.Crew.Name != "c1" || r.Agent != "claude" {
			t.Errorf("Branch/Crew/Agent must survive retirement: Branch=%q Crew=%+v Agent=%q", r.Branch, r.Crew, r.Agent)
		}
	})

	t.Run("unanswered", func(t *testing.T) {
		got := deltasFromCrewLog(strings.NewReader(blocked))
		r := got["fix/9"]
		if r.State != StateBlocked || r.Question == nil {
			t.Fatalf("got %+v, want still blocked with a question — no reply exists", r)
		}
	})

	t.Run("reply older than the block", func(t *testing.T) {
		const fixture = `{"ts":1000,"crew_id":"c1","kind":"dispatch","branch":"fix/9","engine":"claude"}
{"ts":1500,"crew_id":"c1","from":"dispatcher:c1","to":"worker:fix/9#s1","kind":"msg","body":"old news"}
{"ts":2000,"crew_id":"c1","from":"worker:fix/9#s1","kind":"status","body":{"state":"blocked","detail":"Keep the alias?"}}
`
		got := deltasFromCrewLog(strings.NewReader(fixture))
		r := got["fix/9"]
		if r.State != StateBlocked || r.Question == nil {
			t.Fatalf("got %+v, want still blocked — the reply predates the block", r)
		}
	})

	t.Run("reply addressed to a different branch", func(t *testing.T) {
		fixture := blocked + `{"ts":3000,"crew_id":"c1","from":"dispatcher:c1","to":"worker:other/branch#s1","kind":"msg","body":"unrelated"}
`
		got := deltasFromCrewLog(strings.NewReader(fixture))
		r := got["fix/9"]
		if r.State != StateBlocked || r.Question == nil {
			t.Fatalf("got %+v, want still blocked — the reply was addressed elsewhere", r)
		}
	})

	t.Run("reply from a non-dispatcher sender", func(t *testing.T) {
		fixture := blocked + `{"ts":3000,"crew_id":"c1","from":"worker:other#s1","to":"worker:fix/9#s1","kind":"msg","body":"not the dispatcher"}
`
		got := deltasFromCrewLog(strings.NewReader(fixture))
		r := got["fix/9"]
		if r.State != StateBlocked || r.Question == nil {
			t.Fatalf("got %+v, want still blocked — only a dispatcher reply retires the question", r)
		}
	})
}

// testBus stands in for a bus directory in tests that never touch git.
const testBus = "/repo/.git/crew"

// testBusOf is the busOf a join test uses in place of CrewSource.crewDir:
// two worktrees of one repo share a bus, /other has its own, and anything
// else is unknown.
func testBusOf(root string) string {
	switch root {
	case "/wt/a", "/wt/b":
		return testBus
	case "/other":
		return "/other/.git/crew"
	}
	return ""
}

func agentPane(id, target string) tmux.PaneOptions {
	return tmux.PaneOptions{PaneID: id, Target: target, ClaudeStatus: "processing 1 "}
}

func TestResolvePane(t *testing.T) {
	win := func(window int, branch, root string) tmux.WindowOptions {
		return tmux.WindowOptions{Session: "h", Window: window, Branch: branch, GitRoot: root}
	}

	tests := []struct {
		name     string
		wins     []tmux.WindowOptions
		panes    []tmux.PaneOptions
		wantPane string
		wantN    int
	}{
		{
			name:     "one candidate joins",
			wins:     []tmux.WindowOptions{win(1, "fix/412", "/wt/a")},
			panes:    []tmux.PaneOptions{agentPane("%307", "h:1")},
			wantPane: "%307",
			wantN:    1,
		},
		{
			name:  "no window on that branch",
			wins:  []tmux.WindowOptions{win(1, "main", "/wt/a")},
			panes: []tmux.PaneOptions{agentPane("%307", "h:1")},
			wantN: 0,
		},
		{
			name:  "two candidates are ambiguous",
			wins:  []tmux.WindowOptions{win(1, "fix/412", "/wt/a"), win(2, "fix/412", "/wt/b")},
			panes: []tmux.PaneOptions{agentPane("%307", "h:1"), agentPane("%308", "h:2")},
			wantN: 2,
		},
		{
			// Without the @claude_status clause the crew layer's Agent would
			// promote this shell into a listed run.
			name:  "the only pane is a shell",
			wins:  []tmux.WindowOptions{win(1, "fix/412", "/wt/a")},
			panes: []tmux.PaneOptions{{PaneID: "%9", Target: "h:1"}},
			wantN: 0,
		},
		{
			name:  "another bus holds the same branch name",
			wins:  []tmux.WindowOptions{win(1, "fix/412", "/other")},
			panes: []tmux.PaneOptions{agentPane("%307", "h:1")},
			wantN: 0,
		},
		{
			name:  "an unknown root disqualifies its window",
			wins:  []tmux.WindowOptions{win(1, "fix/412", "/not/a/repo")},
			panes: []tmux.PaneOptions{agentPane("%307", "h:1")},
			wantN: 0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pane, n := resolvePane(testBus, "fix/412", tc.wins, tc.panes, testBusOf)
			if n != tc.wantN {
				t.Errorf("candidates = %d, want %d", n, tc.wantN)
			}
			if pane != tc.wantPane {
				t.Errorf("paneID = %q, want %q", pane, tc.wantPane)
			}
		})
	}
}

func TestResolvePaneKeepsTwoBusesApart(t *testing.T) {
	wins := []tmux.WindowOptions{
		{Session: "h", Window: 1, Branch: "main", GitRoot: "/wt/a"},
		{Session: "h", Window: 2, Branch: "main", GitRoot: "/other"},
	}
	panes := []tmux.PaneOptions{agentPane("%1", "h:1"), agentPane("%2", "h:2")}

	if pane, n := resolvePane(testBus, "main", wins, panes, testBusOf); pane != "%1" || n != 1 {
		t.Errorf("bus A: got (%q, %d), want (%%1, 1) — the other bus's window must not count", pane, n)
	}
	if pane, n := resolvePane("/other/.git/crew", "main", wins, panes, testBusOf); pane != "%2" || n != 1 {
		t.Errorf("bus B: got (%q, %d), want (%%2, 1)", pane, n)
	}
}

// writeBus creates a bus directory holding one dispatch record per branch.
func writeBus(t *testing.T, branches ...string) string {
	t.Helper()
	dir := t.TempDir()
	var b strings.Builder
	for _, br := range branches {
		fmt.Fprintf(&b, `{"ts":1000,"crew_id":"c1","kind":"dispatch","branch":%q,"engine":"claude"}`+"\n", br)
	}
	if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// crewScanner builds a CrewSource whose bus lookup is pre-seeded, so scan()
// exercises the join without shelling out to git.
func crewScanner(dirs map[string]string, wins []tmux.WindowOptions, panes []tmux.PaneOptions) *CrewSource {
	return &CrewSource{
		client:   &fakeLister{steps: []fakeStep{{wins: wins, panes: panes}}},
		crewDirs: dirs,
	}
}

func TestScanPublishesUnderTheJoinedPaneKey(t *testing.T) {
	bus := writeBus(t, "fix/412")
	s := crewScanner(
		map[string]string{"/wt/a": bus},
		[]tmux.WindowOptions{{Session: "h", Window: 1, Branch: "fix/412", GitRoot: "/wt/a"}},
		[]tmux.PaneOptions{agentPane("%307", "h:1")},
	)

	got, ok := s.scan()
	if !ok {
		t.Fatal("scan reported failure")
	}
	r, found := got["%307"]
	if !found {
		t.Fatalf("no run under the pane key — keys %v", keysOf(got))
	}
	if r.Worktree != "/wt/a" {
		t.Errorf("Worktree = %q, want /wt/a — this string becomes the reply command's working directory", r.Worktree)
	}
}

func TestScanFallsBackWhenNoPaneJoins(t *testing.T) {
	t.Run("the only pane is a shell", func(t *testing.T) {
		bus := writeBus(t, "fix/412")
		s := crewScanner(
			map[string]string{"/wt/a": bus},
			[]tmux.WindowOptions{{Session: "h", Window: 1, Branch: "fix/412", GitRoot: "/wt/a"}},
			[]tmux.PaneOptions{{PaneID: "%9", Target: "h:1"}},
		)

		got, _ := s.scan()
		want := "crew/" + bus + "/fix/412"
		if _, found := got[want]; !found {
			t.Fatalf("no run under %q — keys %v", want, keysOf(got))
		}
		if _, found := got["%9"]; found {
			t.Error("the crew layer landed on a shell pane, which its Agent would promote into a listed run")
		}
		if got[want].Worktree != "/wt/a" {
			t.Errorf("Worktree = %q, want /wt/a — an unjoined branch still has a window", got[want].Worktree)
		}
	})

	t.Run("no window on that branch", func(t *testing.T) {
		bus := writeBus(t, "fix/412")
		s := crewScanner(
			map[string]string{"/wt/a": bus},
			[]tmux.WindowOptions{{Session: "h", Window: 1, Branch: "main", GitRoot: "/wt/a"}},
			[]tmux.PaneOptions{agentPane("%1", "h:1")},
		)

		got, _ := s.scan()
		want := "crew/" + bus + "/fix/412"
		r, found := got[want]
		if !found {
			t.Fatalf("no run under %q — keys %v", want, keysOf(got))
		}
		// Deliberately not /wt/a: the root set comes only from live windows,
		// so falling back to one would hand this branch another worker's
		// checkout, and WORKER_TASK.md there outranks CREW_ID.
		if r.Worktree != "" {
			t.Errorf("Worktree = %q, want \"\" — no window carries this branch", r.Worktree)
		}
	})
}

func TestScanKeepsTwoBusesApart(t *testing.T) {
	busA := writeBus(t, "main")
	busB := writeBus(t, "main")
	s := crewScanner(
		map[string]string{"/wt/a": busA, "/other": busB},
		[]tmux.WindowOptions{
			{Session: "h", Window: 1, Branch: "main", GitRoot: "/wt/a"},
			{Session: "h", Window: 2, Branch: "main", GitRoot: "/other"},
		},
		nil,
	)

	got, _ := s.scan()
	keyA, keyB := "crew/"+busA+"/main", "crew/"+busB+"/main"
	if len(got) != 2 {
		t.Fatalf("%d runs, want 2 — two repos holding a main record must not collide: keys %v", len(got), keysOf(got))
	}
	if got[keyA].Worktree != "/wt/a" || got[keyB].Worktree != "/other" {
		t.Fatalf("worktrees crossed buses: %q and %q", got[keyA].Worktree, got[keyB].Worktree)
	}

	reg := NewRegistry(DefaultOrder)
	for key, r := range got {
		reg.Apply(Delta{Source: "crew", Key: key, Run: r})
	}
	snap := reg.Snapshot()
	if len(snap) != 2 {
		t.Fatalf("Snapshot has %d runs, want 2: %+v", len(snap), snap)
	}
	if snap[0].ID == snap[1].ID {
		t.Errorf("both runs share the id %q", snap[0].ID)
	}
}

// errPaneLister answers the window query but fails the pane query, which is
// the half of the skip-the-tick rule the pane query added.
type errPaneLister struct{}

func (errPaneLister) ListWindowOptions() ([]tmux.WindowOptions, error) {
	return []tmux.WindowOptions{{Session: "h", Window: 1, Branch: "fix/412", GitRoot: "/wt/a"}}, nil
}

func (errPaneLister) ListPaneOptions() ([]tmux.PaneOptions, error) {
	return nil, errors.New("tmux boom")
}

func TestScanSkipsTheTickOnAPaneQueryError(t *testing.T) {
	s := &CrewSource{client: errPaneLister{}, crewDirs: map[string]string{"/wt/a": writeBus(t, "fix/412")}}
	if got, ok := s.scan(); ok {
		t.Fatalf("scan reported success with %v — a tmux error must skip the tick, not retire every branch", keysOf(got))
	}
}

func TestTickCrewDeltasEmitsTheNewKeyBeforeTheOldGone(t *testing.T) {
	oldKey := "crew/" + testBus + "/fix/412"
	current := map[string]Run{"%307": {Branch: "fix/412", State: StateBlocked, UpdatedAt: time.Now().Unix()}}

	deltas, newSeen := tickCrewDeltas(current, map[string]bool{oldKey: true}, time.Now())

	if len(deltas) != 2 {
		t.Fatalf("deltas = %+v, want the new key's update and the old key's Gone", deltas)
	}
	if deltas[0].Gone || deltas[0].Key != "%307" {
		t.Fatalf("first delta = %+v, want the new key's update — a subscriber must never briefly hold neither key", deltas[0])
	}
	if !deltas[1].Gone || deltas[1].Key != oldKey {
		t.Fatalf("second delta = %+v, want Gone for %q", deltas[1], oldKey)
	}
	if newSeen[oldKey] {
		t.Error("the abandoned key is still in the new seen set")
	}
}

func TestKeyFlipBroadcastsTheNewRunThenRemovesTheOldID(t *testing.T) {
	oldKey := "crew/" + testBus + "/fix/412"
	blocked := Run{Agent: "claude", Branch: "fix/412", State: StateBlocked,
		Question: &Question{Text: "Keep the alias?", Via: "crew"}}

	reg := NewRegistry(DefaultOrder)
	reg.Apply(Delta{Source: "crew", Key: oldKey, Run: blocked})

	sub := reg.Subscribe()
	defer reg.Unsubscribe(sub)

	// The pane opened: scan() now resolves the same branch to %307.
	deltas, _ := tickCrewDeltas(map[string]Run{"%307": blocked}, map[string]bool{oldKey: true}, time.Now())
	for _, d := range deltas {
		reg.Apply(d)
	}

	first := <-sub
	if first.ID != idFor("%307") || first.Removed {
		t.Fatalf("first broadcast = %+v, want the new key's run", first)
	}
	second := <-sub
	if second.ID != idFor(oldKey) || !second.Removed {
		t.Fatalf("second broadcast = %+v, want Removed for the old id %q", second, idFor(oldKey))
	}
}
