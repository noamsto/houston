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

	"github.com/noamsto/houston/crewfeed"
	"github.com/noamsto/houston/hook"
	"github.com/noamsto/houston/hub"
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

func TestDeltasFromCrewLogTracksSessionEpoch(t *testing.T) {
	log := `{"ts":1000,"crew_id":"c1","from":"worker:fix/412#s10-1","kind":"status","body":{"state":"working"}}
{"ts":1100,"crew_id":"c1","from":"worker:fix/412#s20-2","kind":"status","body":{"state":"working"}}
{"ts":1200,"crew_id":"c1","from":"worker:b","kind":"status","body":{"state":"working"}}
`
	got := deltasFromCrewLog(strings.NewReader(log))

	if got["fix/412"].session != 20 {
		t.Errorf("fix/412 session = %d, want 20 — the latest status wins, same as State/UpdatedAt", got["fix/412"].session)
	}
	if got["b"].session != 0 {
		t.Errorf("b session = %d, want 0 — a bare worker id carries no session", got["b"].session)
	}
}

func TestSessionEpoch(t *testing.T) {
	tests := []struct {
		from string
		want int64
	}{
		{"worker:fix/412#s1788-42", 1788},
		{"worker:fix/412#s1", 1},
		{"worker:fix/412", 0},
		{"worker:a#sx-1", 0},
		{"worker:a#s-1", 0},
		{"worker:a#1788-42", 0},
		{"worker:a#s0-1", 0},
		{"role:fix/412:plan-critic", 0},
		{"dispatcher:c", 0},
		{"worker:feat/x#y#s99-1", 99},
	}
	for _, tc := range tests {
		if got := sessionEpoch(tc.from); got != tc.want {
			t.Errorf("sessionEpoch(%q) = %d, want %d", tc.from, got, tc.want)
		}
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

func TestScanRootsSeesAppendAfterCachedParse(t *testing.T) {
	busDir := t.TempDir()
	path := filepath.Join(busDir, "events.jsonl")
	working := `{"ts":1000,"crew_id":"c1","from":"worker:feat/x#s1","kind":"status","body":{"state":"working"}}` + "\n"
	if err := os.WriteFile(path, []byte(working), 0o644); err != nil {
		t.Fatal(err)
	}
	s := &CrewSource{crewDirs: map[string]string{"root": busDir}}
	if got := s.scanRoots([]string{"root"})[busDir]["feat/x"].State; got != StateRunning {
		t.Fatalf("first scan State = %q, want running", got)
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	blocked := `{"ts":1100,"crew_id":"c1","from":"worker:feat/x#s1","kind":"status","body":{"state":"blocked","detail":"Keep the alias?"}}` + "\n"
	if _, err := f.WriteString(blocked); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	if got := s.scanRoots([]string{"root"})[busDir]["feat/x"].State; got != StateBlocked {
		t.Fatalf("after append State = %q, want blocked — the cached parse went stale", got)
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

func TestTickCrewDeltasKeepsAStaleReviewRun(t *testing.T) {
	now := time.Now()
	const key = "crew/" + testBus + "/fix/412"
	current := map[string]Run{
		key: {Branch: "fix/412", State: StateReview, UpdatedAt: now.Add(-7 * 24 * time.Hour).Unix()},
	}

	deltas, newSeen := tickCrewDeltas(current, map[string]bool{}, now)

	if len(deltas) != 1 || deltas[0].Gone {
		t.Fatalf("deltas = %+v, want one non-Gone delta — a stale review run is UI history, not evicted from the registry", deltas)
	}
	if !newSeen[key] {
		t.Errorf("%s missing from the new seen set, want present so it stays visible under All", key)
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

func keysOf[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func busKeysOf[V any](m map[string]map[string]V) []string {
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

	t.Run("watchdog informational source", func(t *testing.T) {
		const fixture = `{"ts":1000,"crew_id":"c1","kind":"dispatch","branch":"fix/7","engine":"claude"}
{"ts":1100,"crew_id":"c1","from":"worker:fix/7#s1","kind":"status","body":{"state":"blocked","detail":"quiet: pane unchanged for 1800s","source":"watchdog"}}
`
		got := deltasFromCrewLog(strings.NewReader(fixture))
		r := got["fix/7"]
		if r.State == StateBlocked {
			t.Fatalf("State = %q, want not needs-attention — a watchdog check-in asks nobody anything", r.State)
		}
		if r.Question != nil {
			t.Fatalf("Question = %+v, want nil", r.Question)
		}
		if r.Crew.Detail != "" {
			t.Errorf("Detail = %q, want cleared — a watchdog note is not a current phase", r.Crew.Detail)
		}
	})
}

func TestDeltasFromCrewLogWatchdogDetailDoesNotLeakOntoLaterStatus(t *testing.T) {
	const blocked = `{"ts":1000,"crew_id":"c1","kind":"dispatch","branch":"fix/8","engine":"claude"}
{"ts":1100,"crew_id":"c1","from":"worker:fix/8#s1","kind":"status","body":{"state":"blocked","detail":"quiet: cleared — awaited 300s, no reply (cycle 3 of 24)","source":"watchdog"}}
`

	t.Run("followed by another watchdog status", func(t *testing.T) {
		fixture := blocked + `{"ts":1200,"crew_id":"c1","from":"worker:fix/8#s1","kind":"status","body":{"state":"working","detail":"quiet: cleared","source":"watchdog"}}
`
		got := deltasFromCrewLog(strings.NewReader(fixture))
		r := got["fix/8"]
		if r.Crew.Detail != "" {
			t.Errorf("Detail = %q, want cleared — watchdog detail must not leak", r.Crew.Detail)
		}
		if r.Question != nil {
			t.Errorf("Question = %+v, want nil — no longer blocked", r.Question)
		}
	})

	t.Run("followed by the worker's own status", func(t *testing.T) {
		fixture := blocked + `{"ts":1200,"crew_id":"c1","from":"worker:fix/8#s1","kind":"status","body":{"state":"working","detail":"running tests"}}
`
		got := deltasFromCrewLog(strings.NewReader(fixture))
		r := got["fix/8"]
		if r.Crew.Detail != "running tests" {
			t.Errorf("Detail = %q, want the worker's own detail", r.Crew.Detail)
		}
		if r.Question != nil {
			t.Errorf("Question = %+v, want nil — no longer blocked", r.Question)
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

// agentPaneAt is agentPane with a controllable activity epoch (the second
// field of lazytmux's @claude_status), for exercising resolvePane's
// terminal-state timestamp rule. epoch 0 reproduces an unknown/missing epoch.
func agentPaneAt(id, target string, epoch int64) tmux.PaneOptions {
	status := "idle"
	if epoch != 0 {
		status = fmt.Sprintf("idle %d ", epoch)
	}
	return tmux.PaneOptions{PaneID: id, Target: target, ClaudeStatus: status}
}

func rolePane(id, target, role string) tmux.PaneOptions {
	return tmux.PaneOptions{PaneID: id, Target: target, ClaudeStatus: "waiting", CrewRole: role}
}

// piPane is an agent pane from the hooks-less side: agent-detect stamps
// @agent_screen ("<state> <epoch> [name=count ...]") instead of
// @claude_status, so the crew layer must recognize it as an agent pane too.
func piPane(id, target, state string, epoch int64) tmux.PaneOptions {
	return tmux.PaneOptions{PaneID: id, Target: target, AgentScreen: fmt.Sprintf("%s %d", state, epoch)}
}

// startAt is a procStartFunc that reports ts for every pid, for tests that
// don't care which pane's process was probed.
func startAt(ts int64) procStartFunc {
	return func(panePID int) int64 { return ts }
}

// startsByPID is a procStartFunc keyed by pane pid, for tests distinguishing
// which pane's process was probed.
func startsByPID(m map[int]int64) procStartFunc {
	return func(panePID int) int64 { return m[panePID] }
}

// noProbe fails the test if resolvePane's identity probe is ever called — for
// non-terminal cases, where it must not be reached.
func noProbe(t *testing.T) procStartFunc {
	return func(panePID int) int64 {
		t.Fatalf("procStart called with pid %d, want no probe on a non-terminal record", panePID)
		return 0
	}
}

// fakeHookSessions is a hookSessions whose Snapshot is pre-seeded, so tests
// exercise the crew join's engine-session gate without a real hub.
type fakeHookSessions struct {
	views []hub.SessionView
}

func (f fakeHookSessions) Snapshot() []hub.SessionView { return f.views }

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
			// A role grid puts the lead and its critic panes in the same
			// window; the lead here carries no @crew_role (the dispatcher's
			// real @crew_role=lead shape is TestResolvePaneJoinsTheGridLead).
			name: "role-grid panes don't make the join ambiguous",
			wins: []tmux.WindowOptions{win(1, "fix/412", "/wt/a")},
			panes: []tmux.PaneOptions{
				agentPane("%40", "h:1"),
				rolePane("%41", "h:1", "spec-critic"),
				rolePane("%42", "h:1", "plan-critic"),
			},
			wantPane: "%40",
			wantN:    1,
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
			pane, n := resolvePane(testBus, "fix/412", "", 0, 0, "", nil, tc.wins, tc.panes, testBusOf, noProbe(t))
			if n != tc.wantN {
				t.Errorf("candidates = %d, want %d", n, tc.wantN)
			}
			if pane != tc.wantPane {
				t.Errorf("paneID = %q, want %q", pane, tc.wantPane)
			}
		})
	}
}

func TestResolvePaneJoinsTheGridLead(t *testing.T) {
	// A role grid's lead pane carries @crew_role=lead: it is the worker, so it
	// joins, while the critic panes in the same window still don't count.
	win := tmux.WindowOptions{Session: "h", Window: 1, Branch: "fix/412", GitRoot: "/wt/a"}
	lead := func(status string) tmux.PaneOptions {
		return tmux.PaneOptions{PaneID: "%40", Target: "h:1", ClaudeStatus: status, CrewRole: "lead"}
	}
	critics := []tmux.PaneOptions{rolePane("%41", "h:1", "spec-critic"), rolePane("%42", "h:1", "plan-critic")}

	t.Run("non-terminal", func(t *testing.T) {
		panes := append([]tmux.PaneOptions{lead("processing 1 ")}, critics...)
		pane, n := resolvePane(testBus, "fix/412", StateRunning, 0, 0, "", nil, []tmux.WindowOptions{win}, panes, testBusOf, noProbe(t))
		if n != 1 || pane != "%40" {
			t.Errorf("got (%q, %d), want (%%40, 1)", pane, n)
		}
	})

	t.Run("terminal", func(t *testing.T) {
		panes := append([]tmux.PaneOptions{lead("done 113 1")}, critics...)
		pane, n := resolvePane(testBus, "fix/412", StateDone, 100, 50, "", nil, []tmux.WindowOptions{win}, panes, testBusOf, startAt(52))
		if n != 1 || pane != "%40" {
			t.Errorf("got (%q, %d), want (%%40, 1)", pane, n)
		}
	})
}

func TestResolvePaneKeepsTwoBusesApart(t *testing.T) {
	wins := []tmux.WindowOptions{
		{Session: "h", Window: 1, Branch: "main", GitRoot: "/wt/a"},
		{Session: "h", Window: 2, Branch: "main", GitRoot: "/other"},
	}
	panes := []tmux.PaneOptions{agentPane("%1", "h:1"), agentPane("%2", "h:2")}

	if pane, n := resolvePane(testBus, "main", "", 0, 0, "", nil, wins, panes, testBusOf, noProbe(t)); pane != "%1" || n != 1 {
		t.Errorf("bus A: got (%q, %d), want (%%1, 1) — the other bus's window must not count", pane, n)
	}
	if pane, n := resolvePane("/other/.git/crew", "main", "", 0, 0, "", nil, wins, panes, testBusOf, noProbe(t)); pane != "%2" || n != 1 {
		t.Errorf("bus B: got (%q, %d), want (%%2, 1)", pane, n)
	}
}

func TestResolvePaneTerminalStateSameSessionJoins(t *testing.T) {
	// The worker's own pane sits idle in its window after it finished — the
	// normal state until `crew reap` reclaims it. Pane epoch <= the terminal
	// record's timestamp means nothing touched the pane since: same session.
	win := tmux.WindowOptions{Session: "h", Window: 1, Branch: "fix/412", GitRoot: "/wt/a"}
	panes := []tmux.PaneOptions{agentPaneAt("%307", "h:1", 100)}

	for _, state := range []State{StateDone, StateFailed} {
		pane, n := resolvePane(testBus, "fix/412", state, 100, 50, "", nil, []tmux.WindowOptions{win}, panes, testBusOf, startAt(60))
		if n != 1 || pane != "%307" {
			t.Errorf("state=%s: got (%q, %d), want (%%307, 1) — epoch <= record ts is the same session", state, pane, n)
		}
	}
}

func TestResolvePaneTerminalStateWithinGraceJoins(t *testing.T) {
	// The worker posts its terminal bus status and THEN ends its final turn,
	// so its own Stop hook stamps the pane's epoch seconds after the record
	// (observed +4s and +13s live). Inside the grace window it is still that
	// same finished session — join it, or the run splits into a history card
	// plus an anonymous live-pane card.
	win := tmux.WindowOptions{Session: "h", Window: 1, Branch: "fix/412", GitRoot: "/wt/a"}
	panes := []tmux.PaneOptions{agentPaneAt("%307", "h:1", 113)}

	for _, state := range []State{StateDone, StateFailed} {
		pane, n := resolvePane(testBus, "fix/412", state, 100, 50, "", nil, []tmux.WindowOptions{win}, panes, testBusOf, startAt(60))
		if n != 1 || pane != "%307" {
			t.Errorf("state=%s: got (%q, %d), want (%%307, 1) — a pane stamped just after the record is the same session", state, pane, n)
		}
	}
}

func TestResolvePaneTerminalStateNewOccupantDoesNotJoin(t *testing.T) {
	// Issue #132's scenario: a new session took the pane after the old one
	// finished, and its first activity stamp lands beyond the grace window —
	// don't join the stale record onto it. The epoch is fixed, not derived from
	// terminalJoinGrace, so a too-large grace window fails this test.
	win := tmux.WindowOptions{Session: "h", Window: 1, Branch: "fix/412", GitRoot: "/wt/a"}
	panes := []tmux.PaneOptions{agentPaneAt("%307", "h:1", 3700)}

	for _, state := range []State{StateDone, StateFailed} {
		pane, n := resolvePane(testBus, "fix/412", state, 100, 50, "", nil, []tmux.WindowOptions{win}, panes, testBusOf, startAt(60))
		if n != 0 || pane != "" {
			t.Errorf("state=%s: got (%q, %d), want (\"\", 0) — a newer pane epoch means a new occupant", state, pane, n)
		}
	}
}

func TestResolvePaneTerminalStateNewOccupantWithinGraceDoesNotJoin(t *testing.T) {
	// The grace window exists because the finished worker's own Stop hook
	// stamps the pane a few seconds after its bus record. It must not let a
	// genuinely new occupant in: an actively working pane (processing) within
	// the window is still issue #132's new session, not the finished worker.
	win := tmux.WindowOptions{Session: "h", Window: 1, Branch: "fix/412", GitRoot: "/wt/a"}
	panes := []tmux.PaneOptions{{PaneID: "%307", Target: "h:1", ClaudeStatus: "processing 130 "}}

	for _, state := range []State{StateDone, StateFailed} {
		pane, n := resolvePane(testBus, "fix/412", state, 100, 50, "", nil, []tmux.WindowOptions{win}, panes, testBusOf, startAt(60))
		if n != 0 || pane != "" {
			t.Errorf("state=%s: got (%q, %d), want (\"\", 0) — an actively working pane is a new occupant even inside grace", state, pane, n)
		}
	}
}

func TestResolvePaneTerminalStateIdleNewOccupantWithinGraceDoesNotJoin(t *testing.T) {
	// #158 — the finished pane's idle stamp and a new session's SessionStart
	// idle look the same; the engine's start time doesn't. The pane went idle
	// within grace (200), but its foreground engine started at 150, after the
	// record was written at 100 — too late to be the session that wrote it.
	win := tmux.WindowOptions{Session: "h", Window: 1, Branch: "fix/412", GitRoot: "/wt/a"}
	panes := []tmux.PaneOptions{agentPaneAt("%307", "h:1", 200)}

	for _, state := range []State{StateDone, StateFailed} {
		pane, n := resolvePane(testBus, "fix/412", state, 100, 50, "", nil, []tmux.WindowOptions{win}, panes, testBusOf, startAt(150))
		if n != 0 || pane != "" {
			t.Errorf("state=%s: got (%q, %d), want (\"\", 0) — an engine that started after the record is a new session", state, pane, n)
		}
	}
}

func TestResolvePaneJoinsPiPane(t *testing.T) {
	// A pi worker's pane carries @agent_screen and no @claude_status; the join
	// must recognize it as an agent pane, or the crew record splits into a
	// crew-only history card beside an anonymous live-pane card.
	win := tmux.WindowOptions{Session: "h", Window: 1, Branch: "fix/412", GitRoot: "/wt/a"}
	panes := []tmux.PaneOptions{piPane("%307", "h:1", "processing", 100)}

	pane, n := resolvePane(testBus, "fix/412", StateRunning, 0, 0, "", nil, []tmux.WindowOptions{win}, panes, testBusOf, noProbe(t))
	if n != 1 || pane != "%307" {
		t.Errorf("got (%q, %d), want (%%307, 1) — a pi pane is an agent pane", pane, n)
	}
}

func TestResolvePanePiPaneTerminalIdentity(t *testing.T) {
	win := tmux.WindowOptions{Session: "h", Window: 1, Branch: "fix/412", GitRoot: "/wt/a"}

	// An at-rest pi session (agent-detect's idle) within grace is the finished
	// worker's own pane.
	t.Run("idle within grace joins", func(t *testing.T) {
		panes := []tmux.PaneOptions{piPane("%307", "h:1", "idle", 113)}
		pane, n := resolvePane(testBus, "fix/412", StateDone, 100, 50, "", nil, []tmux.WindowOptions{win}, panes, testBusOf, startAt(60))
		if n != 1 || pane != "%307" {
			t.Errorf("got (%q, %d), want (%%307, 1)", pane, n)
		}
	})

	// A working pi session within grace is a new occupant — rule 2, same as
	// the claude processing case.
	t.Run("processing within grace does not join", func(t *testing.T) {
		panes := []tmux.PaneOptions{piPane("%307", "h:1", "processing", 130)}
		pane, n := resolvePane(testBus, "fix/412", StateDone, 100, 50, "", nil, []tmux.WindowOptions{win}, panes, testBusOf, startAt(60))
		if n != 0 || pane != "" {
			t.Errorf("got (%q, %d), want (\"\", 0) — an actively working pi pane is a new occupant", pane, n)
		}
	})

	// A pi pane whose @agent_screen has no parseable epoch fails closed on a
	// terminal record.
	t.Run("unknown epoch does not join", func(t *testing.T) {
		panes := []tmux.PaneOptions{{PaneID: "%307", Target: "h:1", AgentScreen: "idle"}}
		pane, n := resolvePane(testBus, "fix/412", StateDone, 100, 50, "", nil, []tmux.WindowOptions{win}, panes, testBusOf, startAt(60))
		if n != 0 || pane != "" {
			t.Errorf("got (%q, %d), want (\"\", 0) — unknown @agent_screen epoch must fail closed", pane, n)
		}
	})
}

func TestResolvePaneTerminalStateUnknownEpochDoesNotJoin(t *testing.T) {
	// An unknown pane epoch (0) on a terminal record fails closed — identity
	// can't be confirmed either way.
	win := tmux.WindowOptions{Session: "h", Window: 1, Branch: "fix/412", GitRoot: "/wt/a"}
	panes := []tmux.PaneOptions{agentPaneAt("%307", "h:1", 0)}

	for _, state := range []State{StateDone, StateFailed} {
		pane, n := resolvePane(testBus, "fix/412", state, 100, 50, "", nil, []tmux.WindowOptions{win}, panes, testBusOf, startAt(60))
		if n != 0 || pane != "" {
			t.Errorf("state=%s: got (%q, %d), want (\"\", 0) — unknown epoch must fail closed", state, pane, n)
		}
	}
}

func TestResolvePaneJoinsNonTerminal(t *testing.T) {
	win := tmux.WindowOptions{Session: "h", Window: 1, Branch: "fix/412", GitRoot: "/wt/a"}
	panes := []tmux.PaneOptions{agentPane("%307", "h:1")}

	// Non-terminal or zero state → join still works, regardless of epoch.
	for _, state := range []State{StateRunning, StateBlocked, StateReview, State("")} {
		pane, n := resolvePane(testBus, "fix/412", state, 0, 0, "", nil, []tmux.WindowOptions{win}, panes, testBusOf, noProbe(t))
		if n != 1 {
			t.Errorf("state=%q: candidates = %d, want 1 — non-terminal must join", state, n)
		}
		if pane != "%307" {
			t.Errorf("state=%q: paneID = %q, want %%307", state, pane)
		}
	}
}

func TestResolvePaneZeroStateJoins(t *testing.T) {
	// Zero (empty) state means dispatch-only branch with no status yet —
	// join must proceed exactly as before.
	win := tmux.WindowOptions{Session: "h", Window: 1, Branch: "main", GitRoot: "/wt/a"}
	panes := []tmux.PaneOptions{agentPane("%1", "h:1")}

	pane, n := resolvePane(testBus, "main", "", 0, 0, "", nil, []tmux.WindowOptions{win}, panes, testBusOf, noProbe(t))
	if n != 1 || pane != "%1" {
		t.Errorf("got (%q, %d), want (%%1, 1) — zero state on one branch must still join", pane, n)
	}
}

func TestResolvePaneTerminalSessionIdentity(t *testing.T) {
	win := tmux.WindowOptions{Session: "h", Window: 1, Branch: "fix/412", GitRoot: "/wt/a"}
	panes := []tmux.PaneOptions{agentPaneAt("%307", "h:1", 100)}

	tests := []struct {
		name    string
		start   int64
		session int64
		want    bool
	}{
		{"starts after the epoch", 60, 50, true},
		{"starts at the record", 100, 50, true},
		{"starts after the record", 101, 50, false},
		{"starts inside the slack before the epoch", 25, 50, true},
		{"starts before the epoch minus slack", 19, 50, false},
		{"unknown start", 0, 50, false},
		{"unknown session", 60, 0, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for _, state := range []State{StateDone, StateFailed} {
				pane, n := resolvePane(testBus, "fix/412", state, 100, tc.session, "", nil, []tmux.WindowOptions{win}, panes, testBusOf, startAt(tc.start))
				got := n == 1 && pane == "%307"
				if got != tc.want {
					t.Errorf("state=%s start=%d session=%d: joined=%v, want %v", state, tc.start, tc.session, got, tc.want)
				}
			}
		})
	}
}

func TestResolvePaneTerminalEngineSessionIdentity(t *testing.T) {
	// #162: a /clear or /resume inside the same finished engine process starts
	// a new hook session id but leaves the process start time unchanged. When
	// the bus row names its engine session and the pane's hook session is
	// known, they must match; either side unknown falls back to the process
	// rule exactly.
	win := tmux.WindowOptions{Session: "h", Window: 1, Branch: "fix/412", GitRoot: "/wt/a"}
	panes := []tmux.PaneOptions{agentPaneAt("%307", "h:1", 100)}

	tests := []struct {
		name          string
		engineSession string
		paneSessions  map[string]string
		start         int64
		want          bool
	}{
		{"matching session joins", "sid-1", map[string]string{"%307": "sid-1"}, 60, true},
		{"cleared session does not join", "sid-1", map[string]string{"%307": "sid-2"}, 60, false},
		{"missing engine_session keeps process rule (joins)", "", map[string]string{"%307": "sid-2"}, 60, true},
		{"missing engine_session keeps process rule (rejects)", "", map[string]string{"%307": "sid-2"}, 150, false},
		{"unknown pane session keeps process rule (joins)", "sid-1", nil, 60, true},
		{"unknown pane session keeps process rule (rejects)", "sid-1", nil, 150, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for _, state := range []State{StateDone, StateFailed} {
				pane, n := resolvePane(testBus, "fix/412", state, 100, 50, tc.engineSession, tc.paneSessions, []tmux.WindowOptions{win}, panes, testBusOf, startAt(tc.start))
				got := n == 1 && pane == "%307"
				if got != tc.want {
					t.Errorf("state=%s: joined=%v, want %v", state, got, tc.want)
				}
			}
		})
	}
}

func TestResolvePaneTerminalPiNewOccupantDoesNotJoin(t *testing.T) {
	win := tmux.WindowOptions{Session: "h", Window: 1, Branch: "fix/412", GitRoot: "/wt/a"}
	panes := []tmux.PaneOptions{piPane("%307", "h:1", "idle", 113)}

	pane, n := resolvePane(testBus, "fix/412", StateDone, 100, 50, "", nil, []tmux.WindowOptions{win}, panes, testBusOf, startAt(150))
	if n != 0 || pane != "" {
		t.Errorf("got (%q, %d), want (\"\", 0) — a pi engine that started after the record is a new occupant", pane, n)
	}
}

func TestResolvePaneTerminalSiblingNewOccupantDoesNotMakeItAmbiguous(t *testing.T) {
	// The finished worker's own pane (%40, pid 1) sits idle alongside a brand
	// new occupant (%41, pid 2) that also went idle within grace. Only the
	// worker's engine started inside the session, so the join is unambiguous.
	win := tmux.WindowOptions{Session: "h", Window: 1, Branch: "fix/412", GitRoot: "/wt/a"}
	panes := []tmux.PaneOptions{
		{PaneID: "%40", Target: "h:1", ClaudeStatus: "idle 100 ", PanePID: 1},
		{PaneID: "%41", Target: "h:1", ClaudeStatus: "idle 150 ", PanePID: 2},
	}

	pane, n := resolvePane(testBus, "fix/412", StateDone, 100, 50, "", nil, []tmux.WindowOptions{win}, panes, testBusOf, startsByPID(map[int]int64{1: 60, 2: 150}))
	if n != 1 || pane != "%40" {
		t.Errorf("got (%q, %d), want (%%40, 1) — only the finished worker's own pane passes identity", pane, n)
	}
}

func TestScanRejectsStaleJoin(t *testing.T) {
	// Bus with a done status record: the worker ended, and — issue #132's
	// scenario — a new session has since taken over its pane (activity epoch
	// newer than the done record's timestamp).
	bus := t.TempDir()
	rec := `{"ts":1000,"crew_id":"c1","from":"worker:fix/412#s1","kind":"dispatch","branch":"fix/412","engine":"claude"}
{"ts":2000,"crew_id":"c1","from":"worker:fix/412#s1","kind":"status","body":{"state":"done"}}
`
	if err := os.WriteFile(filepath.Join(bus, "events.jsonl"), []byte(rec), 0o644); err != nil {
		t.Fatal(err)
	}

	s := crewScanner(
		map[string]string{"/wt/a": bus},
		[]tmux.WindowOptions{{Session: "h", Window: 1, Branch: "fix/412", GitRoot: "/wt/a"}},
		[]tmux.PaneOptions{agentPaneAt("%307", "h:1", 999)},
	)

	got, ok := s.scan()
	if !ok {
		t.Fatal("scan reported failure")
	}

	// The stale bus record must NOT join to the new occupant's pane.
	if r, found := got["%307"]; found {
		t.Errorf("run joined to pane %%307: %+v — a newer pane epoch means a new occupant", r)
	}

	// The run must appear under the crew/<bus>/<branch> fallback key.
	wantKey := "crew/" + bus + "/fix/412"
	r, found := got[wantKey]
	if !found {
		t.Fatalf("no run under %q — keys: %v", wantKey, keysOf(got))
	}
	if r.State != StateDone {
		t.Errorf("State = %s, want done", r.State)
	}
}

func TestScanJoinsTerminalRecordToItsOwnIdlePane(t *testing.T) {
	// Issue #132's regression case: the worker finished and its own pane
	// still sits idle in its window, untouched since — the normal state
	// until `crew reap` reclaims it. The run must still join to that pane,
	// not split into a stale history card plus an anonymous live-pane card.
	bus := t.TempDir()
	rec := `{"ts":1000,"crew_id":"c1","from":"worker:fix/412#s1","kind":"dispatch","branch":"fix/412","engine":"claude"}
{"ts":2000,"crew_id":"c1","from":"worker:fix/412#s1","kind":"status","body":{"state":"done"}}
`
	if err := os.WriteFile(filepath.Join(bus, "events.jsonl"), []byte(rec), 0o644); err != nil {
		t.Fatal(err)
	}

	s := crewScanner(
		map[string]string{"/wt/a": bus},
		[]tmux.WindowOptions{{Session: "h", Window: 1, Branch: "fix/412", GitRoot: "/wt/a"}},
		[]tmux.PaneOptions{agentPaneAt("%307", "h:1", 2)},
	)
	s.procStart = startAt(1)

	got, ok := s.scan()
	if !ok {
		t.Fatal("scan reported failure")
	}

	r, found := got["%307"]
	if !found {
		t.Fatalf("run did not join to pane %%307 — keys: %v, want the done record to join its own idle pane", keysOf(got))
	}
	if r.State != StateDone {
		t.Errorf("State = %s, want done", r.State)
	}

	wantKey := "crew/" + bus + "/fix/412"
	if _, found := got[wantKey]; found {
		t.Errorf("run also published under the fallback key %q — must not split into two cards", wantKey)
	}
}

func TestScanEngineSession(t *testing.T) {
	// #162: the bus row names its engine session; the pane's hook session must
	// match for a terminal join, so a /clear or /resume inside the same engine
	// process does not reattach the finished worker's record to the new session.
	bus := t.TempDir()
	rec := `{"ts":1000,"crew_id":"c1","from":"worker:fix/412#s1","kind":"dispatch","branch":"fix/412","engine":"claude","engine_session":"sid-1"}
{"ts":2000,"crew_id":"c1","from":"worker:fix/412#s1","kind":"status","body":{"state":"done"}}
`
	if err := os.WriteFile(filepath.Join(bus, "events.jsonl"), []byte(rec), 0o644); err != nil {
		t.Fatal(err)
	}
	wins := []tmux.WindowOptions{{Session: "h", Window: 1, Branch: "fix/412", GitRoot: "/wt/a"}}
	now := time.Now().Unix()
	panes := []tmux.PaneOptions{{PaneID: "%307", Target: "h:1", ClaudeStatus: "idle 2 ", PanePID: 7, ServerPID: "4242", ServerStart: now - 3600}}
	view := func(sid string) hub.SessionView {
		return hub.SessionView{SessionID: sid, TmuxPane: "%307", TmuxServer: "4242", UpdatedAt: now, State: hook.StateWaiting}
	}

	t.Run("matching hook session joins", func(t *testing.T) {
		s := crewScanner(map[string]string{"/wt/a": bus}, wins, panes, fakeHookSessions{views: []hub.SessionView{view("sid-1")}})
		s.procStart = startAt(1)
		got, ok := s.scan()
		if !ok {
			t.Fatal("scan reported failure")
		}
		if _, found := got["%307"]; !found {
			t.Fatalf("run did not join %%307 — keys: %v", keysOf(got))
		}
	})

	t.Run("cleared hook session does not join", func(t *testing.T) {
		s := crewScanner(map[string]string{"/wt/a": bus}, wins, panes, fakeHookSessions{views: []hub.SessionView{view("sid-2")}})
		s.procStart = startAt(1)
		got, ok := s.scan()
		if !ok {
			t.Fatal("scan reported failure")
		}
		if _, found := got["%307"]; found {
			t.Error("run joined %%307 despite a different hook session — a cleared session must not reattach")
		}
		if _, found := got["crew/"+bus+"/fix/412"]; !found {
			t.Fatalf("no fallback run — keys: %v", keysOf(got))
		}
	})
}

func TestScanRejectsIdleNewOccupantWithinGrace(t *testing.T) {
	// The production entry point for #158: a done status posted from
	// worker:fix/412#s1000-7 (session 1000) at ts 2000000 (UpdatedAt 2000)
	// left the worker's pane idle at epoch 2100 — within terminalJoinGrace of
	// the record — but the pane's foreground engine is what decides identity.
	bus := t.TempDir()
	rec := `{"ts":1000000,"crew_id":"c1","from":"worker:fix/412#s1000","kind":"dispatch","branch":"fix/412","engine":"claude"}
{"ts":2000000,"crew_id":"c1","from":"worker:fix/412#s1000-7","kind":"status","body":{"state":"done"}}
`
	if err := os.WriteFile(filepath.Join(bus, "events.jsonl"), []byte(rec), 0o644); err != nil {
		t.Fatal(err)
	}
	wins := []tmux.WindowOptions{{Session: "h", Window: 1, Branch: "fix/412", GitRoot: "/wt/a"}}
	panes := []tmux.PaneOptions{{PaneID: "%307", Target: "h:1", ClaudeStatus: "idle 2100 ", PanePID: 4242}}

	t.Run("engine started after the record does not join", func(t *testing.T) {
		var gotPID int
		s := crewScanner(map[string]string{"/wt/a": bus}, wins, panes)
		s.procStart = func(panePID int) int64 {
			gotPID = panePID
			return 2050
		}

		got, ok := s.scan()
		if !ok {
			t.Fatal("scan reported failure")
		}
		if gotPID != 4242 {
			t.Errorf("probe saw pid %d, want 4242", gotPID)
		}
		if _, found := got["%307"]; found {
			t.Error("joined to %307 despite the engine starting after the record")
		}
		wantKey := "crew/" + bus + "/fix/412"
		if _, found := got[wantKey]; !found {
			t.Fatalf("no run under %q — keys: %v", wantKey, keysOf(got))
		}
	})

	t.Run("engine started before the record joins", func(t *testing.T) {
		s := crewScanner(map[string]string{"/wt/a": bus}, wins, panes)
		s.procStart = startAt(1500)

		got, ok := s.scan()
		if !ok {
			t.Fatal("scan reported failure")
		}
		if _, found := got["%307"]; !found {
			t.Fatalf("run did not join to %%307 — keys: %v", keysOf(got))
		}
	})
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
// exercises the join without shelling out to git. procStart defaults to
// startAt(0) — unknown, so a terminal join fails closed unless a test
// overrides s.procStart. procs likewise knows no start time, so a crew with a
// pid file does not join its dispatcher pane unless a test overrides s.procs.
func crewScanner(dirs map[string]string, wins []tmux.WindowOptions, panes []tmux.PaneOptions, sessions ...hookSessions) *CrewSource {
	s := &CrewSource{
		client:    &fakeLister{steps: []fakeStep{{wins: wins, panes: panes}}},
		crewDirs:  dirs,
		procStart: startAt(0),
		procs:     fakeProcs{},
	}
	if len(sessions) > 0 {
		s.sessions = sessions[0]
	}
	return s
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
	if r.CrewBus != bus {
		t.Errorf("CrewBus = %q, want %q", r.CrewBus, bus)
	}
}

func TestScanStampsProjectAndWorkerRole(t *testing.T) {
	bus := filepath.Join(t.TempDir(), "houston", ".git", "crew")
	if err := os.MkdirAll(bus, 0o755); err != nil {
		t.Fatal(err)
	}
	rec := `{"ts":1000,"crew_id":"c1","kind":"dispatch","branch":"fix/412","engine":"claude"}` + "\n"
	if err := os.WriteFile(filepath.Join(bus, "events.jsonl"), []byte(rec), 0o644); err != nil {
		t.Fatal(err)
	}
	s := crewScanner(
		map[string]string{"/wt/a": bus},
		[]tmux.WindowOptions{{Session: "h", Window: 1, Branch: "fix/412", GitRoot: "/wt/a"}},
		[]tmux.PaneOptions{agentPane("%307", "h:1")},
	)

	got, _ := s.scan()
	r := got["%307"]
	if r.Project != "houston" {
		t.Errorf("Project = %q, want houston — the repo owning the bus's .git", r.Project)
	}
	if r.Role != RoleWorker {
		t.Errorf("Role = %q, want worker", r.Role)
	}
}

// A worker's own "blocked" status posted to the bus is not a human prompt
// while a live dispatcher owns the crew: the dispatcher answers it.
func TestScanWorkerBlockedOnLiveDispatcherIsNotNeedsYou(t *testing.T) {
	bus := t.TempDir()
	statusTS := time.Now().Add(-60 * time.Second).UnixMilli()
	events := `{"ts":1000,"crew_id":"c1","kind":"dispatch","branch":"fix/412","engine":"claude"}` + "\n" +
		fmt.Sprintf(`{"ts":%d,"crew_id":"c1","from":"worker:fix/412#s1","kind":"status","body":{"state":"blocked","detail":"spec: which engine?"}}`, statusTS) + "\n"
	if err := os.WriteFile(filepath.Join(bus, "events.jsonl"), []byte(events), 0o644); err != nil {
		t.Fatal(err)
	}
	crewDir := filepath.Join(bus, "crews", "c1")
	if err := os.MkdirAll(crewDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(crewDir, "pane"), []byte("%1"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := crewScanner(
		map[string]string{"/wt/a": bus},
		[]tmux.WindowOptions{
			{Session: "h", Window: 1, Branch: "fix/412", GitRoot: "/wt/a"},
			{Session: "h", Window: 2, CrewName: "dispatcher"},
		},
		[]tmux.PaneOptions{
			agentPane("%307", "h:1"),
			{PaneID: "%1", Target: "h:2", ClaudeStatus: "idle 1 "},
		},
	)

	got, ok := s.scan()
	if !ok {
		t.Fatal("scan reported failure")
	}
	r, found := got["%307"]
	if !found {
		t.Fatalf("no run under the pane key — keys %v", keysOf(got))
	}
	if r.State == StateBlocked {
		t.Errorf("State = %q, want not blocked: the dispatcher is alive and owns the question", r.State)
	}
	if r.Question != nil {
		t.Errorf("Question = %+v, want none", r.Question)
	}
	if r.Crew == nil || r.Crew.Detail != "spec: which engine?" {
		t.Errorf("Crew = %+v, want Detail %q kept", r.Crew, "spec: which engine?")
	}
}

// With no dispatcher pane on record (a houston-minted crew, or one whose
// dispatcher is gone) nobody can answer the worker's question but the human.
func TestScanWorkerBlockedWithoutDispatcherNeedsYou(t *testing.T) {
	bus := t.TempDir()
	statusTS := time.Now().Add(-60 * time.Second).UnixMilli()
	events := `{"ts":1000,"crew_id":"c1","kind":"dispatch","branch":"fix/412","engine":"claude"}` + "\n" +
		fmt.Sprintf(`{"ts":%d,"crew_id":"c1","from":"worker:fix/412#s1","kind":"status","body":{"state":"blocked","detail":"spec: which engine?"}}`, statusTS) + "\n"
	if err := os.WriteFile(filepath.Join(bus, "events.jsonl"), []byte(events), 0o644); err != nil {
		t.Fatal(err)
	}
	s := crewScanner(
		map[string]string{"/wt/a": bus},
		[]tmux.WindowOptions{
			{Session: "h", Window: 1, Branch: "fix/412", GitRoot: "/wt/a"},
			{Session: "h", Window: 2, CrewName: "dispatcher"},
		},
		[]tmux.PaneOptions{
			agentPane("%307", "h:1"),
			{PaneID: "%1", Target: "h:2", ClaudeStatus: "idle 1 "},
		},
	)

	got, ok := s.scan()
	if !ok {
		t.Fatal("scan reported failure")
	}
	r, found := got["%307"]
	if !found {
		t.Fatalf("no run under the pane key — keys %v", keysOf(got))
	}
	if r.State != StateBlocked {
		t.Errorf("State = %q, want blocked: no dispatcher can answer", r.State)
	}
	if r.Question == nil || r.Question.Via != "crew" || r.Question.Text != "spec: which engine?" {
		t.Errorf("Question = %+v, want the worker's question via crew", r.Question)
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

func TestDeltasFromCrewLogCarriesTitleModelDetailAndPR(t *testing.T) {
	got := deltasFromCrewLog(strings.NewReader(crewFixture))

	r := got["fix/412"]
	if r.Crew.Title != "fix the ws drop" || r.Crew.Model != "sonnet" {
		t.Errorf("Crew = %+v, want title and model from the dispatch record", r.Crew)
	}
	if r.Crew.Detail != "Keep the legacy route?" {
		t.Errorf("Detail = %q, want the latest status detail", r.Crew.Detail)
	}

	pr := got["feat/413"]
	if pr.PR == nil || pr.PR.URL != "https://github.com/x/y/pull/9" || pr.PR.Number != "9" {
		t.Errorf("PR = %+v, want url and number 9 from pr_url", pr.PR)
	}
}

func TestDeltasFromCrewLogLaterStatusClearsDetailButKeepsPR(t *testing.T) {
	log := `{"ts":1,"crew_id":"c","from":"worker:b#s","kind":"status","body":{"state":"pr_open","detail":"opening","pr_url":"https://github.com/x/y/pull/3"}}
{"ts":2,"crew_id":"c","from":"worker:b#s","kind":"status","body":{"state":"done"}}
`
	r := deltasFromCrewLog(strings.NewReader(log))["b"]
	if r.Crew.Detail != "" {
		t.Errorf("Detail = %q, want cleared by the newer status", r.Crew.Detail)
	}
	if r.PR == nil || r.PR.URL == "" {
		t.Errorf("PR = %+v, want the URL to stick", r.PR)
	}
}

func TestPRNumberFromURL(t *testing.T) {
	for in, want := range map[string]string{
		"https://github.com/x/y/pull/9":  "9",
		"https://github.com/x/y/pull/9x": "",
		"https://example.com/mr/9":       "",
		"https://github.com/x/y/pull/":   "",
	} {
		if got := prNumberFromURL(in); got != want {
			t.Errorf("prNumberFromURL(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestDeltasFromCrewLogWatchdogPrefixes pins what each watchdog blocked prefix
// means for the card: only prompt: needs a human (at the pane); the prefixes
// that signal a stalled or limited worker mark it stuck while it reads
// running; load: and anything unlisted claim nothing. The prefix must be
// followed by a colon.
func TestDeltasFromCrewLogWatchdogPrefixes(t *testing.T) {
	cases := []struct {
		detail        string
		state         State
		question      string // "" = no Question; else its pane text
		attention     Attention
		attentionNote string
	}{
		{"prompt: interactive prompt in pane %326 — worker is waiting on input nobody can give", StateBlocked, crewWatchdogPromptNote, AttentionNone, ""},
		{"quota: quota exhausted — worker parked on the rate-limit prompt in pane %326", StateRunning, "", AttentionStuck, crewWatchdogQuotaNote},
		{"turn-stall: token count static at 25.0k for 1800s", StateRunning, "", AttentionStuck, crewWatchdogTurnStallNote},
		{"quiet: pane unchanged for 1800s", StateRunning, "", AttentionStuck, crewWatchdogQuietNote},
		{"stalled: no output for 300s", StateRunning, "", AttentionStuck, crewWatchdogStalledNote},
		{"runaway: sentinel tokens leaked", StateRunning, "", AttentionStuck, crewWatchdogRunawayNote},
		{"unread: lead has not read the reviewer verdict", StateRunning, "", AttentionStuck, crewWatchdogUnreadNote},
		{"budget: token budget reached", StateRunning, "", AttentionStuck, crewWatchdogBudgetNote},
		{"load: 1m load 9 on 8 cores for 60s", StateRunning, "", AttentionNone, ""},
		{"mystery: x", StateRunning, "", AttentionNone, ""},
		{"quietly waiting", StateRunning, "", AttentionNone, ""},
		{"", StateRunning, "", AttentionNone, ""},
	}
	for _, c := range cases {
		t.Run(c.detail, func(t *testing.T) {
			fixture := `{"ts":1000,"crew_id":"c1","kind":"dispatch","branch":"fix/10","engine":"claude"}` + "\n" +
				fmt.Sprintf(`{"ts":1100,"crew_id":"c1","from":"worker:fix/10#s1","kind":"status","body":{"state":"blocked","detail":%q,"source":"watchdog"}}`, c.detail) + "\n"
			r := deltasFromCrewLog(strings.NewReader(fixture))["fix/10"]
			if r.State != c.state {
				t.Errorf("State = %q, want %q", r.State, c.state)
			}
			switch {
			case c.question == "" && r.Question != nil:
				t.Errorf("Question = %+v, want nil", r.Question)
			case c.question != "" && (r.Question == nil || r.Question.Via != "pane" || r.Question.Text != c.question):
				t.Errorf("Question = %+v, want Via pane text %q", r.Question, c.question)
			}
			if r.Attention != c.attention || r.AttentionNote != c.attentionNote {
				t.Errorf("Attention = %q %q, want %q %q", r.Attention, r.AttentionNote, c.attention, c.attentionNote)
			}
			if r.Crew.Detail != "" {
				t.Errorf("Detail = %q, want cleared — a watchdog note never reaches the card", r.Crew.Detail)
			}
			if r.blockedSince != 0 {
				t.Errorf("blockedSince = %d, want 0 — a watchdog status is not a question to the dispatcher", r.blockedSince)
			}
		})
	}
}

func TestDeltasFromCrewLogFailedIsStuck(t *testing.T) {
	const head = `{"ts":1000,"crew_id":"c1","kind":"dispatch","branch":"fix/11","engine":"claude"}` + "\n"
	cases := []struct {
		name, status string
		note, detail string
	}{
		{"watchdog dead", `{"state":"failed","detail":"dead: engine process gone","source":"watchdog"}`, crewWatchdogDeadNote, ""},
		{"worker failed", `{"state":"failed","detail":"gate won't pass"}`, "", "gate won't pass"},
		{"exited", `{"state":"exited"}`, "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fixture := head + `{"ts":1100,"crew_id":"c1","from":"worker:fix/11#s1","kind":"status","body":` + c.status + "}\n"
			r := deltasFromCrewLog(strings.NewReader(fixture))["fix/11"]
			if r.State != StateFailed {
				t.Errorf("State = %q, want failed", r.State)
			}
			if r.AttentionNote != c.note {
				t.Errorf("AttentionNote = %q, want %q", r.AttentionNote, c.note)
			}
			if r.Crew.Detail != c.detail {
				t.Errorf("Detail = %q, want %q", r.Crew.Detail, c.detail)
			}
		})
	}

	t.Run("latest status resets the attention", func(t *testing.T) {
		fixture := head +
			`{"ts":1100,"crew_id":"c1","from":"worker:fix/11#s1","kind":"status","body":{"state":"blocked","detail":"quota: limit","source":"watchdog"}}` + "\n" +
			`{"ts":1200,"crew_id":"c1","from":"worker:fix/11#s1","kind":"status","body":{"state":"working"}}` + "\n"
		r := deltasFromCrewLog(strings.NewReader(fixture))["fix/11"]
		if r.Attention != AttentionNone || r.AttentionNote != "" {
			t.Errorf("Attention = %q %q, want none — the later status replaced the stuck one", r.Attention, r.AttentionNote)
		}
	})
}

func TestDeltasFromCrewLogDoneAndPROpen(t *testing.T) {
	const fixture = `{"ts":1000,"crew_id":"c1","kind":"dispatch","branch":"fix/13","engine":"claude"}
{"ts":1100,"crew_id":"c1","from":"worker:fix/13#s1","kind":"status","body":{"state":"done"}}
{"ts":1200,"crew_id":"c1","from":"worker:fix/14#s1","kind":"status","body":{"state":"pr_open","pr_url":"https://github.com/x/y/pull/9"}}
`
	got := deltasFromCrewLog(strings.NewReader(fixture))
	if got["fix/13"].State != StateDone || got["fix/14"].State != StateReview {
		t.Errorf("States = %q, %q; want done, review", got["fix/13"].State, got["fix/14"].State)
	}
}

func TestDeltasFromCrewLogBlockedSince(t *testing.T) {
	const head = `{"ts":500,"crew_id":"c1","kind":"dispatch","branch":"fix/12","engine":"claude"}` + "\n"
	status := func(ts int64, state string) string {
		return fmt.Sprintf(`{"ts":%d,"crew_id":"c1","from":"worker:fix/12#s1","kind":"status","body":{"state":%q,"detail":"which way?"}}`+"\n", ts, state)
	}
	reply := func(ts int64) string {
		return fmt.Sprintf(`{"ts":%d,"crew_id":"c1","from":"dispatcher:c1","to":"worker:fix/12#s1","kind":"msg","body":"this way"}`+"\n", ts)
	}
	cases := []struct {
		name string
		log  string
		want int64
	}{
		{"re-stamp keeps the episode start", head + status(1000, "blocked") + status(2000, "blocked"), 1000},
		{"working in between starts a new episode", head + status(1000, "blocked") + status(3000, "working") + status(4000, "blocked"), 4000},
		{"a reply in between starts a new episode", head + status(1000, "blocked") + reply(4500) + status(5000, "blocked"), 5000},
		{"a reply older than the episode does not end it", head + reply(900) + status(1000, "blocked") + status(2000, "blocked"), 1000},
		{"not blocked", head + status(1000, "working"), 0},
		{"answered and retired", head + status(1000, "blocked") + reply(2000), 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := deltasFromCrewLog(strings.NewReader(c.log))["fix/12"].blockedSince; got != c.want {
				t.Errorf("blockedSince = %d, want %d", got, c.want)
			}
		})
	}
}

func TestRouteCrewQuestion(t *testing.T) {
	now := time.Unix(10_000, 0)
	blockedAt := func(ago time.Duration) crewBranch {
		return crewBranch{
			Run: Run{
				State:    StateBlocked,
				Question: &Question{Text: "which engine?", Via: "crew"},
				Crew:     &CrewRef{Name: "c1", Detail: "which engine?"},
			},
			blockedSince: now.Add(-ago).UnixMilli(),
		}
	}
	watchdog := crewBranch{Run: Run{
		State:    StateBlocked,
		Question: &Question{Text: crewWatchdogPromptNote, Via: "pane"},
		Crew:     &CrewRef{Name: "c1"},
	}}
	running := crewBranch{Run: Run{State: StateRunning, Crew: &CrewRef{Name: "c1"}}}

	cases := []struct {
		name         string
		b            crewBranch
		live         bool
		wantState    State
		wantQuestion bool
	}{
		{"live dispatcher, asked a minute ago", blockedAt(time.Minute), true, StateRunning, false},
		{"live dispatcher, silent past the window", blockedAt(crewDispatcherSilence + time.Minute), true, StateBlocked, true},
		{"live dispatcher, exactly at the window", blockedAt(crewDispatcherSilence), true, StateBlocked, true},
		{"no live dispatcher", blockedAt(time.Minute), false, StateBlocked, true},
		{"watchdog prompt, live dispatcher", watchdog, true, StateBlocked, true},
		{"watchdog prompt, no dispatcher", watchdog, false, StateBlocked, true},
		{"not blocked", running, true, StateRunning, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := routeCrewQuestion(c.b, c.live, now)
			if got.State != c.wantState {
				t.Errorf("State = %q, want %q", got.State, c.wantState)
			}
			if (got.Question != nil) != c.wantQuestion {
				t.Errorf("Question = %+v, want present=%v", got.Question, c.wantQuestion)
			}
			if c.b.Crew != nil && got.Crew != c.b.Crew {
				t.Errorf("Crew pointer changed — the routing must keep the card's detail")
			}
		})
	}

	t.Run("does not write through the cached branch", func(t *testing.T) {
		b := blockedAt(time.Minute)
		q := b.Question
		got := routeCrewQuestion(b, true, now)
		if got.Question != nil {
			t.Fatalf("Question = %+v, want nil", got.Question)
		}
		if b.Question != q || b.Question == nil || b.State != StateBlocked {
			t.Errorf("input mutated: State=%q Question=%+v", b.State, b.Question)
		}
		if got.Crew == nil || got.Crew.Detail != "which engine?" {
			t.Errorf("Crew = %+v, want Detail kept", got.Crew)
		}
	})
}

// dispatcherLive is a lookup in the tick's join, so it inherits every join
// rule; these cases pin the ones routing has always relied on plus the ids a
// bus record can carry.
func TestDispatcherLive(t *testing.T) {
	dispatcherWin := tmux.WindowOptions{Session: "h", Window: 2, CrewName: "dispatcher"}
	workerWin := tmux.WindowOptions{Session: "h", Window: 3, CrewName: "bronze"}
	claudePane := dispatcherPane("%1", "h:2")
	agentDetect := piPane("%1", "h:2", "idle", 1)
	agentDetect.PanePID, agentDetect.ServerStart = dispPanePID, serverT.Unix()

	cases := []struct {
		name     string
		paneFile *string // nil: no file
		paneMod  time.Time
		crewID   string
		wins     []tmux.WindowOptions
		panes    []tmux.PaneOptions
		want     bool
	}{
		{"claude pane on a dispatcher window", ptr("%1"), regT, "c1", []tmux.WindowOptions{dispatcherWin}, []tmux.PaneOptions{claudePane}, true},
		{"agent-detect pane", ptr("%1"), regT, "c1", []tmux.WindowOptions{dispatcherWin}, []tmux.PaneOptions{agentDetect}, true},
		{"pane missing from the listing", ptr("%9"), regT, "c1", []tmux.WindowOptions{dispatcherWin}, []tmux.PaneOptions{claudePane}, false},
		{"window is not a dispatcher", ptr("%1"), regT, "c1", []tmux.WindowOptions{{Session: "h", Window: 2, CrewName: "bronze"}, workerWin}, []tmux.PaneOptions{claudePane}, false},
		{"no engine status", ptr("%1"), regT, "c1", []tmux.WindowOptions{dispatcherWin}, []tmux.PaneOptions{{PaneID: "%1", Target: "h:2"}}, false},
		{"no pane file", nil, regT, "c1", []tmux.WindowOptions{dispatcherWin}, []tmux.PaneOptions{claudePane}, false},
		{"pane file from before a tmux restart", ptr("%1"), serverT.Add(-time.Hour), "c1", []tmux.WindowOptions{dispatcherWin}, []tmux.PaneOptions{claudePane}, false},
		{"empty crew id", ptr("%1"), regT, "", []tmux.WindowOptions{dispatcherWin}, []tmux.PaneOptions{claudePane}, false},
		{"surrounding whitespace", ptr("  %1\n"), regT, "c1", []tmux.WindowOptions{dispatcherWin}, []tmux.PaneOptions{claudePane}, true},
		{"another crew", ptr("%1"), regT, "c2", []tmux.WindowOptions{dispatcherWin}, []tmux.PaneOptions{claudePane}, false},
		{"crew id escaping crews/", ptr("%1"), regT, "../c1", []tmux.WindowOptions{dispatcherWin}, []tmux.PaneOptions{claudePane}, false},
		{"crew id dot-dot", ptr("%1"), regT, "..", []tmux.WindowOptions{dispatcherWin}, []tmux.PaneOptions{claudePane}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			bus := t.TempDir()
			if c.paneFile != nil {
				writeCrew(t, bus, crewFiles{id: "c1", pane: c.paneFile, paneMod: c.paneMod})
				// Files reachable only through a crew id that climbs out of crews/.
				if err := os.MkdirAll(filepath.Join(bus, "c1"), 0o755); err != nil {
					t.Fatal(err)
				}
				for _, rel := range []string{"pane", filepath.Join("c1", "pane")} {
					if err := os.WriteFile(filepath.Join(bus, rel), []byte(*c.paneFile), 0o644); err != nil {
						t.Fatal(err)
					}
				}
			}
			joined := joinDispatchers(bus, c.wins, c.panes, liveDispatcher())
			if got := dispatcherLive(joined, c.crewID); got != c.want {
				t.Errorf("dispatcherLive = %v, want %v (join %v)", got, c.want, joined)
			}
		})
	}
}

func ptr[T any](v T) *T { return &v }

func TestCrewLogReadsEngineSessionNewestWins(t *testing.T) {
	log := `{"ts":1,"kind":"dispatch","branch":"feat/a","engine":"claude","engine_session":"old"}
{"ts":2,"from":"worker:feat/a#s1-2","kind":"status","body":{"state":"working"}}
{"ts":3,"kind":"resume","branch":"feat/a","engine":"claude","engine_session":"new"}
{"ts":4,"kind":"dispatch","branch":"feat/b","engine":"codex","engine_session":null}
{"ts":5,"kind":"dispatch","branch":"feat/c","engine":"claude"}
`
	got := deltasFromCrewLog(strings.NewReader(log))
	for branch, want := range map[string]string{"feat/a": "new", "feat/b": "", "feat/c": ""} {
		if got[branch].CrewSession != want {
			t.Errorf("%s: CrewSession = %q, want %q", branch, got[branch].CrewSession, want)
		}
	}
}

func TestDeltasFromCrewLogCarriesRosterFields(t *testing.T) {
	const fixture = `{"ts":1000,"crew_id":"c1","kind":"dispatch","branch":"feat/7","title":"add the roster","tier":"standard","engine":"claude","model":"sonnet","engine_session":"s-1"}
{"ts":1100,"crew_id":"c1","from":"worker:feat/7#s1","kind":"status","body":{"state":"working","detail":"executing plan"}}
{"ts":1200,"crew_id":"c1","kind":"resume","branch":"feat/7","engine_session":"s-2"}
{"ts":1300,"crew_id":"c1","from":"worker:feat/7#s2","kind":"status","body":{"state":"pr_open","detail":"AC1 pass","pr_url":"https://github.com/x/y/pull/12"}}
`
	r := deltasFromCrewLog(strings.NewReader(fixture))["feat/7"]
	if r.Crew == nil {
		t.Fatal("no crew ref")
	}
	c := r.Crew
	if c.Title != "add the roster" || c.Tier != "standard" || c.Model != "sonnet" || c.Detail != "AC1 pass" {
		t.Errorf("Crew = %+v, want title/tier/model/latest detail", c)
	}
	if r.Agent != "claude" {
		t.Errorf("Agent = %q, want claude", r.Agent)
	}
	if c.Sessions != 2 {
		t.Errorf("Sessions = %d, want 2 (dispatch + resume)", c.Sessions)
	}
	if r.PR == nil || r.PR.URL != "https://github.com/x/y/pull/12" || r.PR.Number != "12" {
		t.Errorf("PR = %+v, want the status pr_url", r.PR)
	}
}

// A dispatcher that has dispatched nothing yet has a crews/ dir but no log;
// its pane still gets the crew layer, which lists only beside the tmux layer.
func TestScanEmitsTheDispatcherLayer(t *testing.T) {
	bus := t.TempDir()
	writeCrew(t, bus, crewFiles{id: "c1", pane: ptr("%1"), paneMod: regT, pid: ptr("200"), pidMod: regT})
	wins := []tmux.WindowOptions{{Session: "h", Window: 2, CrewName: "dispatcher", GitRoot: "/wt/a"}}
	panes := []tmux.PaneOptions{dispatcherPane("%1", "h:2")}
	s := crewScanner(map[string]string{"/wt/a": bus}, wins, panes)
	s.procs = liveDispatcher()

	got, ok := s.scan()
	if !ok {
		t.Fatal("scan reported failure")
	}
	if len(got) != 1 {
		t.Fatalf("scan = %v, want only the dispatcher pane", keysOf(got))
	}
	d := got["%1"]
	if d.Role != RoleDispatcher || d.Crew == nil || d.Crew.Name != "c1" || d.CrewBus != bus {
		t.Fatalf("dispatcher layer = %+v (crew %+v), want role dispatcher, crew c1, bus %q", d, d.Crew, bus)
	}
	if d.Agent != "" || d.State != "" || d.Question != nil || d.CrewSession != "" {
		t.Errorf("dispatcher layer carries an opinion it must leave to tmux/hooks: %+v", d)
	}

	reg := NewRegistry(DefaultOrder)
	reg.Apply(Delta{Source: "crew", Key: "%1", Run: d})
	if snap := reg.Snapshot(); len(snap) != 0 {
		t.Fatalf("a crew-only dispatcher key is listed: %+v", snap)
	}
	for _, td := range deltasFromTmux(wins, panes, func(string) string { return "houston" }) {
		reg.Apply(td)
	}
	snap := reg.Snapshot()
	if len(snap) != 1 {
		t.Fatalf("Snapshot = %+v, want the one dispatcher run", snap)
	}
	r := snap[0]
	if r.Role != RoleDispatcher || r.Crew == nil || r.Crew.Name != "c1" || r.Crew.Codename != "dispatcher" {
		t.Errorf("composed run role %q crew %+v, want dispatcher with crew name c1 and the tmux codename", r.Role, r.Crew)
	}
	if r.CrewBus != bus || r.Project != "houston" {
		t.Errorf("composed CrewBus %q Project %q, want %q and houston", r.CrewBus, r.Project, bus)
	}
}

// One key holds one crew layer: a worker branch already joined to the pane
// keeps it.
func TestScanDispatcherJoinYieldsToAWorkerKey(t *testing.T) {
	bus := writeBus(t, "fix/412")
	writeCrew(t, bus, crewFiles{id: "c1", pane: ptr("%307"), paneMod: regT})
	s := crewScanner(
		map[string]string{"/wt/a": bus},
		[]tmux.WindowOptions{{Session: "h", Window: 1, Branch: "fix/412", GitRoot: "/wt/a", CrewName: "dispatcher"}},
		[]tmux.PaneOptions{agentPane("%307", "h:1")},
	)

	got, _ := s.scan()
	r, ok := got["%307"]
	if !ok {
		t.Fatalf("no run under the pane key — keys %v", keysOf(got))
	}
	if r.Role != RoleWorker || r.Branch != "fix/412" {
		t.Errorf("pane key holds role %q branch %q, want the worker", r.Role, r.Branch)
	}
}

// A crew whose dispatcher is gone — its pane id reused after a tmux restart,
// or its process exited — cannot answer a worker's question: it reaches the
// human at once, and no dispatcher layer is emitted.
func TestScanWorkerQuestionWithAGoneDispatcher(t *testing.T) {
	cases := []struct {
		name    string
		crew    crewFiles
		probe   fakeProcs
		blocked bool
	}{
		{"live dispatcher", crewFiles{id: "c1", pane: ptr("%1"), paneMod: regT, pid: ptr("200"), pidMod: regT}, liveDispatcher(), false},
		{"pane file from before a tmux restart", crewFiles{id: "c1", pane: ptr("%1"), paneMod: serverT.Add(-time.Hour)}, liveDispatcher(), true},
		{"dispatcher process exited", crewFiles{id: "c1", pane: ptr("%1"), paneMod: regT, pid: ptr("200"), pidMod: regT},
			fakeProcs{dead: map[int]bool{dispPID: true}}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			bus := t.TempDir()
			statusTS := time.Now().Add(-60 * time.Second).UnixMilli()
			events := `{"ts":1000,"crew_id":"c1","kind":"dispatch","branch":"fix/412","engine":"claude"}` + "\n" +
				fmt.Sprintf(`{"ts":%d,"crew_id":"c1","from":"worker:fix/412#s1","kind":"status","body":{"state":"blocked","detail":"spec: which engine?"}}`, statusTS) + "\n"
			if err := os.WriteFile(filepath.Join(bus, "events.jsonl"), []byte(events), 0o644); err != nil {
				t.Fatal(err)
			}
			writeCrew(t, bus, c.crew)
			worker := agentPane("%307", "h:1")
			worker.ServerStart = serverT.Unix()
			s := crewScanner(
				map[string]string{"/wt/a": bus},
				[]tmux.WindowOptions{
					{Session: "h", Window: 1, Branch: "fix/412", GitRoot: "/wt/a"},
					{Session: "h", Window: 2, CrewName: "dispatcher"},
				},
				[]tmux.PaneOptions{worker, dispatcherPane("%1", "h:2")},
			)
			s.procs = c.probe

			got, _ := s.scan()
			r := got["%307"]
			if blocked := r.State == StateBlocked && r.Question != nil; blocked != c.blocked {
				t.Errorf("worker State %q Question %+v, want blocked=%v", r.State, r.Question, c.blocked)
			}
			if _, joined := got["%1"]; joined == c.blocked {
				t.Errorf("dispatcher layer present = %v, want %v", joined, !c.blocked)
			}
		})
	}
}

// Every resolved bus is advanced once per scan, including one whose
// events.jsonl does not exist yet, so its feed has an epoch from the start.
func TestScanAdvancesTheFeedForEveryBus(t *testing.T) {
	logged := writeBus(t, "fix/412")
	bare := t.TempDir()
	s := crewScanner(
		map[string]string{"/wt/a": logged, "/wt/a2": logged, "/wt/b": bare},
		[]tmux.WindowOptions{
			{Session: "h", Window: 1, GitRoot: "/wt/a"},
			{Session: "h", Window: 2, GitRoot: "/wt/a2"},
			{Session: "h", Window: 3, GitRoot: "/wt/b"},
		},
		nil,
	)
	s.feed = crewfeed.NewStore()

	if _, ok := s.scan(); !ok {
		t.Fatal("scan reported failure")
	}
	for _, bus := range []string{logged, bare} {
		if _, ok := s.feed.Epoch(bus, "c1"); !ok {
			t.Errorf("bus %s was not advanced", bus)
		}
	}
	p, err := s.feed.Page(logged, "c1", 0, 0)
	if err != nil || len(p.Entries) != 1 || p.Entries[0].Branch != "fix/412" {
		t.Errorf("logged bus page = %+v, %v; want the one dispatch entry", p, err)
	}
	p, err = s.feed.Page(bare, "c1", 0, 0)
	if err != nil || len(p.Entries) != 0 || p.Epoch == "" {
		t.Errorf("bare bus page = %+v, %v; want an empty page with an epoch", p, err)
	}
}
