package runs

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

	r, ok := got["feat/x"]
	if !ok {
		t.Fatalf("scan found nothing from inside a worktree — keys %v", keysOf(got))
	}
	if r.State != StateBlocked || r.Question == nil {
		t.Fatalf("got %+v, want a blocked run carrying its question", r)
	}
}

func TestTickCrewDeltasEvictsFinishedRunsAfterGrace(t *testing.T) {
	now := time.Now()
	current := map[string]Run{
		"fix/412": {Branch: "fix/412", State: StateDone, UpdatedAt: now.Add(-30 * time.Minute).Unix()},
	}
	seen := map[string]bool{"branch/fix/412": true}

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
	if gone[0].Key != "branch/fix/412" {
		t.Errorf("Gone delta Key = %q, want branch/fix/412", gone[0].Key)
	}
	if gone[0].Source != "crew" {
		t.Errorf("Gone delta Source = %q, want crew — Registry.Apply deletes r.layers[key][Source], so an empty Source silently no-ops eviction", gone[0].Source)
	}
	if newSeen["branch/fix/412"] {
		t.Error("branch/fix/412 still present in the new seen set, want evicted")
	}
}

func TestTickCrewDeltasKeepsFreshlyFinishedRuns(t *testing.T) {
	now := time.Now()
	current := map[string]Run{
		"fix/412": {Branch: "fix/412", State: StateDone, UpdatedAt: now.Add(-1 * time.Minute).Unix()},
	}

	deltas, newSeen := tickCrewDeltas(current, map[string]bool{}, now)

	if len(deltas) != 1 || deltas[0].Gone {
		t.Fatalf("deltas = %+v, want one non-Gone delta for a freshly finished run", deltas)
	}
	if !newSeen["branch/fix/412"] {
		t.Error("branch/fix/412 missing from the new seen set, want present")
	}
}

func TestTickCrewDeltasNeverEvictsAQuietBlockedRun(t *testing.T) {
	now := time.Now()
	current := map[string]Run{
		"fix/412": {Branch: "fix/412", State: StateBlocked, UpdatedAt: now.Add(-24 * time.Hour).Unix()},
	}

	deltas, newSeen := tickCrewDeltas(current, map[string]bool{}, now)

	if len(deltas) != 1 || deltas[0].Gone {
		t.Fatalf("deltas = %+v, want one non-Gone delta — a quiet blocked run must never be evicted", deltas)
	}
	if !newSeen["branch/fix/412"] {
		t.Error("branch/fix/412 missing from the new seen set, want present")
	}
}

func TestTickCrewDeltasKeepsADispatchedRunWithNoStatusYet(t *testing.T) {
	now := time.Now()
	current := map[string]Run{
		"fix/412": {Branch: "fix/412"},
	}

	deltas, newSeen := tickCrewDeltas(current, map[string]bool{}, now)

	if len(deltas) != 1 || deltas[0].Gone {
		t.Fatalf("deltas = %+v, want one non-Gone delta — a dispatch-only run has nothing to evict", deltas)
	}
	if !newSeen["branch/fix/412"] {
		t.Error("branch/fix/412 missing from the new seen set, want present")
	}
}

func keysOf(m map[string]Run) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
