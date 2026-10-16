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

// scrubWorkerIdentity removes the pane's crew identity from the process
// environment for the duration of a test (#292). The reply runner builds the
// child env from os.Environ(), so when `go test` runs inside a crew worker's
// tmux pane the child `crew reply` inherits the pane's identity: the crew id
// variables name its worker, and tmux still reaches the server with TMUX unset
// via the $TMUX_TMPDIR (else /tmp) default socket, where the window's
// @crew_branch stamp restricts posting to that worker. The four variables must
// go outright (an empty-but-set TMUX still counts; os.Setenv cannot remove
// one), so the scrub rewrites os.Environ() instead of t.Setenv'ing blanks.
// CREW_ID is not dropped: the handler appends the test's own value.
func scrubWorkerIdentity(t *testing.T) {
	t.Helper()
	drop := map[string]bool{"TMUX": true, "TMUX_PANE": true, "TMUX_TMPDIR": true, "CREW_WORKER_ID": true, "CREW_ROLE_ID": true}
	prev := append([]string(nil), os.Environ()...)
	applyEnv(scrubEnv(prev, drop))
	t.Cleanup(func() { applyEnv(prev) })
}

func scrubEnv(env []string, drop map[string]bool) []string {
	kept := make([]string, 0, len(env))
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		if !drop[name] {
			kept = append(kept, kv)
		}
	}
	return kept
}

func applyEnv(env []string) {
	os.Clearenv()
	for _, kv := range env {
		name, value, _ := strings.Cut(kv, "=")
		//nolint:errcheck // os.Setenv only fails on a name with '=' or NUL, and these come from os.Environ()
		os.Setenv(name, value)
	}
}

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
	scrubWorkerIdentity(t)

	const session = "s100-1"
	root, log := newCrewBus(t, session, "blocked")
	_, otherLog := newCrewBus(t, "s200-1", "blocked")
	otherBefore, err := os.ReadFile(otherLog)
	if err != nil {
		t.Fatalf("read other bus: %v", err)
	}

	s := newReplyServer(t, execCrewReply, crewDelta(func(r *runs.Run) { r.Worktree = root }))

	// Leading "--" on purpose: crew reply parses options and refuses a
	// dash-led body unless "--" precedes it, so the endpoint must always supply
	// one. Nothing but the real CLI can check that.
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

	const plain = "go ahead with main"
	rec = doReply(t, s, replyRequest("POST", replyPath(t, s), replyBody(t, plain)))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("plain status %d, want 204 (body %q)", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(lastBusRecord(t, log).Body, &body); err != nil {
		t.Fatalf("plain body is not a JSON string: %v", err)
	}
	if body != plain {
		t.Errorf("plain body %q, want %q — the answer must round-trip verbatim", body, plain)
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
	scrubWorkerIdentity(t)

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
