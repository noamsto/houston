package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/noamsto/houston/runs"
)

// requireCrew skips when the real CLI is absent. Every other test in this
// package runs against a stub; this one exists to check the two choices a stub
// cannot check — the working directory and CREW_ID.
func requireCrew(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("crew"); err != nil {
		t.Skip("crew not on PATH: skipping the only test that exercises the real bus, the working directory and CREW_ID")
	}
}

// newCrewBus builds a scratch git repo with its own bus, holding a dispatch
// record for replyBranch and one status for session s. It returns the repo
// root (the worktree the handler will run crew in) and the bus log path.
func newCrewBus(t *testing.T, session, state string) (root, log string) {
	t.Helper()
	root = t.TempDir()
	if out, err := exec.Command("git", "-C", root, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}

	dir := filepath.Join(root, ".git", "crew")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir bus: %v", err)
	}
	log = filepath.Join(dir, "events.jsonl")

	ts := time.Now().UnixMilli()
	lines := []string{
		fmt.Sprintf(`{"ts":%d,"crew_id":%q,"from":"dispatcher:%s","kind":"dispatch","branch":%q,"session":%q}`,
			ts, replyCrew, replyCrew, replyBranch, session),
		fmt.Sprintf(`{"ts":%d,"crew_id":%q,"from":"worker:%s#%s","kind":"status","body":{"state":%q,"detail":"which base branch?"}}`,
			ts+1, replyCrew, replyBranch, session, state),
	}
	if err := os.WriteFile(log, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write bus: %v", err)
	}
	return root, log
}

type busRecord struct {
	CrewID string          `json:"crew_id"`
	From   string          `json:"from"`
	To     string          `json:"to"`
	Kind   string          `json:"kind"`
	Body   json.RawMessage `json:"body"`
}

func lastBusRecord(t *testing.T, log string) busRecord {
	t.Helper()
	b, err := os.ReadFile(log)
	if err != nil {
		t.Fatalf("read bus: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	var rec busRecord
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &rec); err != nil {
		t.Fatalf("decode bus line %q: %v", lines[len(lines)-1], err)
	}
	return rec
}

// The only test that can falsify the working-directory and CREW_ID choices: a
// wrong Dir writes to a different repo's bus (or houston's own), and a missing
// CREW_ID makes crew exit 1 before it writes anything.
func TestReplyReachesTheRealCrewBus(t *testing.T) {
	requireCrew(t)

	const session = "s100-1"
	root, log := newCrewBus(t, session, "blocked")
	_, otherLog := newCrewBus(t, "s200-1", "blocked")
	otherBefore, err := os.ReadFile(otherLog)
	if err != nil {
		t.Fatalf("read other bus: %v", err)
	}

	s := newReplyServer(t, execCrewReply, crewDelta(func(r *runs.Run) { r.Worktree = root }))

	// Leading "--" on purpose: crew reply does no option parsing, so the
	// endpoint must not insert a separator of its own — one would be consumed
	// as the recipient. Nothing but the real CLI can check that.
	const answer = "-- rebase onto main, not merge; `id` $(id)"
	rec := doReply(t, s, replyRequest("POST", replyPath(t, s), replyBody(t, answer)))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status %d, want 204 (body %q)", rec.Code, rec.Body.String())
	}
	got := lastBusRecord(t, log)
	if got.Kind != "msg" {
		t.Errorf("kind %q, want %q", got.Kind, "msg")
	}
	if want := "worker:" + replyBranch + "#" + session; got.To != want {
		t.Errorf("to %q, want %q — the answer must be addressed to the live worker", got.To, want)
	}
	if got.CrewID != replyCrew {
		t.Errorf("crew_id %q, want %q — CREW_ID did not reach crew", got.CrewID, replyCrew)
	}
	if want := "dispatcher:" + replyCrew; got.From != want {
		t.Errorf("from %q, want %q", got.From, want)
	}
	var body string
	if err := json.Unmarshal(got.Body, &body); err != nil {
		t.Fatalf("body is not a JSON string: %v", err)
	}
	if body != answer {
		t.Errorf("body %q, want %q — the answer must round-trip verbatim", body, answer)
	}

	otherAfter, err := os.ReadFile(otherLog)
	if err != nil {
		t.Fatalf("read other bus: %v", err)
	}
	if string(otherAfter) != string(otherBefore) {
		t.Error("a second repo's bus changed — the working directory did not pin the delivery")
	}
}

// A terminal session is the worker's own refusal, not houston's failure.
func TestReplyToFinishedWorkerIsRefusedWithCrewsWords(t *testing.T) {
	requireCrew(t)

	root, log := newCrewBus(t, "s100-1", "done")
	before, err := os.ReadFile(log)
	if err != nil {
		t.Fatalf("read bus: %v", err)
	}

	s := newReplyServer(t, execCrewReply, crewDelta(func(r *runs.Run) { r.Worktree = root }))

	rec := doReply(t, s, replyRequest("POST", replyPath(t, s), replyBody(t, "go ahead")))
	if rec.Code != http.StatusConflict {
		t.Fatalf("status %d, want 409 (body %q)", rec.Code, rec.Body.String())
	}
	body := strings.TrimSpace(rec.Body.String())
	if !strings.Contains(body, "is done") {
		t.Errorf("body %q does not carry crew's own refusal", body)
	}

	after, err := os.ReadFile(log)
	if err != nil {
		t.Fatalf("read bus: %v", err)
	}
	if string(after) != string(before) {
		t.Error("a refused reply still wrote to the bus")
	}
}
