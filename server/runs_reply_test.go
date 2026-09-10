package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/noamsto/houston/runs"
)

const (
	replyToken  = "reply-secret"
	replyOrigin = "http://good.example"
	replyHost   = "127.0.0.1:9090"
	replyBranch = "feat/x"
	replyBus    = "/repo/.git/crew"
	replyCrew   = "c-test"
	replyDir    = "/tmp/houston-reply-worktree"
)

// stubRunner stands in for the real crew invocation. Its call count is the
// only way to prove a refusal happened before anything ran.
type stubRunner struct {
	mu     sync.Mutex
	calls  []replyExec
	result replyResult
}

func (s *stubRunner) run(_ context.Context, x replyExec) replyResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, x)
	return s.result
}

func (s *stubRunner) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

func (s *stubRunner) last(t *testing.T) replyExec {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.calls) == 0 {
		t.Fatal("runner was never called")
	}
	return s.calls[len(s.calls)-1]
}

// crewDelta is a blocked crew-sourced run that satisfies every precondition.
// mut relaxes exactly one of them per test.
func crewDelta(mut func(*runs.Run)) runs.Delta {
	r := runs.Run{
		Agent:    "claude",
		State:    runs.StateBlocked,
		Branch:   replyBranch,
		Worktree: replyDir,
		Crew:     &runs.CrewRef{Name: replyCrew},
		Question: &runs.Question{Text: "rebase or merge?", Via: "crew"},
	}
	if mut != nil {
		mut(&r)
	}
	return runs.Delta{Source: "crew", Key: "crew/" + replyBus + "/" + replyBranch, Run: r}
}

// newReplyServer wires the real gates the way New does, without touching disk
// or spawning tmux.
func newReplyServer(t *testing.T, runner replyRunner, deltas ...runs.Delta) *Server {
	t.Helper()
	reg := runs.NewRegistry(runs.DefaultOrder)
	for _, d := range deltas {
		reg.Apply(d)
	}
	allowed := []string{replyOrigin}
	return &Server{
		auth:        &authGate{token: replyToken, enabled: true, allowedOrigins: allowed},
		hosts:       deriveHosts(nil, allowed),
		runs:        reg,
		replyRunner: runner,
	}
}

// replyPath is the route for the single seeded run, read back from the
// registry so the test never has to reimplement ID derivation.
func replyPath(t *testing.T, s *Server) string {
	t.Helper()
	snap := s.runs.Snapshot()
	if len(snap) != 1 {
		t.Fatalf("registry holds %d listed runs, want 1", len(snap))
	}
	return "/api/runs/" + snap[0].ID + "/reply"
}

func replyRequest(method, path, body string) *http.Request {
	req := httptest.NewRequest(method, "http://"+replyHost+path, strings.NewReader(body))
	req.Host = replyHost
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: authCookie, Value: replyToken})
	return req
}

func replyBody(t *testing.T, text string) string {
	t.Helper()
	b, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

func doReply(t *testing.T, s *Server, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

// The falsifiable question: is there any residual path from an unauthenticated,
// cross-origin, rebound or non-POST request to a command running? Each case
// asserts zero invocations, not just a status code.
func TestReplyGatesLeaveNoPathToACommand(t *testing.T) {
	cases := []struct {
		name    string
		want    int
		prepare func(*http.Request)
	}{
		{"no token", http.StatusUnauthorized, func(r *http.Request) {
			r.Header.Del("Cookie")
		}},
		{"cross origin", http.StatusForbidden, func(r *http.Request) {
			r.Header.Set("Origin", "http://evil.example")
		}},
		{"rebound host", http.StatusMisdirectedRequest, func(r *http.Request) {
			r.Host = "evil.example"
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub := &stubRunner{}
			s := newReplyServer(t, stub.run, crewDelta(nil))
			req := replyRequest("POST", replyPath(t, s), replyBody(t, "yes"))
			tc.prepare(req)

			rec := doReply(t, s, req)
			if rec.Code != tc.want {
				t.Errorf("status %d, want %d", rec.Code, tc.want)
			}
			if n := stub.count(); n != 0 {
				t.Fatalf("runner called %d times behind a closed gate — a command ran without a valid request", n)
			}
		})
	}

	t.Run("GET", func(t *testing.T) {
		stub := &stubRunner{}
		s := newReplyServer(t, stub.run, crewDelta(nil))
		rec := doReply(t, s, replyRequest("GET", replyPath(t, s), ""))

		// 405 rather than merely non-2xx: the method pattern is what refuses
		// this, in the mux, before the handler exists.
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("status %d, want 405 — a GET must not reach a mutating route", rec.Code)
		}
		if n := stub.count(); n != 0 {
			t.Fatalf("runner called %d times on a GET, want 0", n)
		}
	})
}

// The reply is an argv element, so shell metacharacters are inert and a
// leading dash is an operand rather than a flag.
func TestReplyArgvIsOneLiteralElement(t *testing.T) {
	const nasty = "-n; rm -rf / `id` $(id) \"quoted\" && echo pwned\nsecond line"

	stub := &stubRunner{}
	s := newReplyServer(t, stub.run, crewDelta(nil))

	rec := doReply(t, s, replyRequest("POST", replyPath(t, s), replyBody(t, nasty)))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status %d, want 204", rec.Code)
	}

	got := stub.last(t)
	want := []string{"crew", "reply", "worker:" + replyBranch, nasty}
	if len(got.Argv) != len(want) {
		t.Fatalf("argv %q, want %q", got.Argv, want)
	}
	for i := range want {
		if got.Argv[i] != want[i] {
			t.Errorf("argv[%d] = %q, want %q", i, got.Argv[i], want[i])
		}
	}
	if got.Dir != replyDir {
		t.Errorf("Dir = %q, want %q", got.Dir, replyDir)
	}
	if got.CrewID != replyCrew {
		t.Errorf("CrewID = %q, want %q", got.CrewID, replyCrew)
	}
}

func TestReplyOutcomes(t *testing.T) {
	cases := []struct {
		name     string
		result   replyResult
		want     int
		wantBody string
	}{
		{"delivered", replyResult{}, http.StatusNoContent, ""},
		{
			"crew refused",
			replyResult{ExitCode: 1, Stderr: "crew: newest session on feat/x is done — a stopped session never reads its inbox"},
			http.StatusConflict,
			"crew: newest session on feat/x is done — a stopped session never reads its inbox",
		},
		{
			"crew not on PATH",
			replyResult{Err: &exec.Error{Name: "crew", Err: exec.ErrNotFound}},
			http.StatusBadGateway,
			"crew",
		},
		{"timed out", replyResult{Err: context.DeadlineExceeded}, http.StatusGatewayTimeout, "timed out"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub := &stubRunner{result: tc.result}
			s := newReplyServer(t, stub.run, crewDelta(nil))

			rec := doReply(t, s, replyRequest("POST", replyPath(t, s), replyBody(t, "yes")))
			if rec.Code != tc.want {
				t.Fatalf("status %d, want %d (body %q)", rec.Code, tc.want, rec.Body.String())
			}
			body := strings.TrimSpace(rec.Body.String())
			if tc.wantBody == "" {
				if body != "" {
					t.Errorf("body %q, want empty", body)
				}
				return
			}
			if !strings.Contains(body, tc.wantBody) {
				t.Errorf("body %q, want it to contain %q", body, tc.wantBody)
			}
			if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
				t.Errorf("Content-Type %q, want text/plain", ct)
			}
		})
	}
}

// 409 carries crew's own words so the UI can show the worker's refusal rather
// than houston's paraphrase of it.
func TestReplyRefusalIsCrewStderrVerbatim(t *testing.T) {
	const stderr = "crew: no session on feat/x — dispatch a worker before replying to one"

	stub := &stubRunner{result: replyResult{ExitCode: 1, Stderr: stderr}}
	s := newReplyServer(t, stub.run, crewDelta(nil))

	rec := doReply(t, s, replyRequest("POST", replyPath(t, s), replyBody(t, "yes")))
	if rec.Code != http.StatusConflict {
		t.Fatalf("status %d, want 409", rec.Code)
	}
	if got := strings.TrimSpace(rec.Body.String()); got != stderr {
		t.Errorf("body %q, want %q", got, stderr)
	}
}

func TestReplyPreconditionsRefuseBeforeRunning(t *testing.T) {
	cases := []struct {
		name  string
		delta runs.Delta
		body  string
		path  string
		want  int
	}{
		{
			name:  "unknown id",
			delta: crewDelta(nil),
			path:  "/api/runs/branch-bm9wZQ/reply",
			want:  http.StatusNotFound,
		},
		{
			name:  "no crew",
			delta: crewDelta(func(r *runs.Run) { r.Crew = nil }),
			want:  http.StatusBadRequest,
		},
		{
			name:  "empty crew name",
			delta: crewDelta(func(r *runs.Run) { r.Crew = &runs.CrewRef{Codename: "sage"} }),
			want:  http.StatusBadRequest,
		},
		{
			name:  "no branch",
			delta: crewDelta(func(r *runs.Run) { r.Branch = "" }),
			want:  http.StatusBadRequest,
		},
		{
			name:  "no worktree",
			delta: crewDelta(func(r *runs.Run) { r.Worktree = "" }),
			want:  http.StatusBadRequest,
		},
		{
			name:  "no question",
			delta: crewDelta(func(r *runs.Run) { r.Question = nil }),
			want:  http.StatusBadRequest,
		},
		{
			name:  "question answered at the pane",
			delta: crewDelta(func(r *runs.Run) { r.Question = &runs.Question{Text: "1 or 2?", Via: "pane"} }),
			want:  http.StatusBadRequest,
		},
		{
			name:  "empty text",
			delta: crewDelta(nil),
			body:  `{"text":"   \n  "}`,
			want:  http.StatusBadRequest,
		},
		{
			name:  "missing text",
			delta: crewDelta(nil),
			body:  `{}`,
			want:  http.StatusBadRequest,
		},
		{
			name:  "malformed body",
			delta: crewDelta(nil),
			body:  `{"text":`,
			want:  http.StatusBadRequest,
		},
		{
			name:  "text over maxReplyText",
			delta: crewDelta(nil),
			body:  `{"text":"` + strings.Repeat("a", maxReplyText+1) + `"}`,
			want:  http.StatusRequestEntityTooLarge,
		},
		{
			name:  "body over MaxBytesReader",
			delta: crewDelta(nil),
			body:  `{"text":"` + strings.Repeat("a", maxReplyBody+1) + `"}`,
			want:  http.StatusRequestEntityTooLarge,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub := &stubRunner{}
			s := newReplyServer(t, stub.run, tc.delta)

			path := tc.path
			if path == "" {
				path = replyPath(t, s)
			}
			body := tc.body
			if body == "" {
				body = replyBody(t, "yes")
			}

			rec := doReply(t, s, replyRequest("POST", path, body))
			if rec.Code != tc.want {
				t.Errorf("status %d, want %d (body %q)", rec.Code, tc.want, rec.Body.String())
			}
			if n := stub.count(); n != 0 {
				t.Fatalf("runner called %d times on a failed precondition, want 0", n)
			}
		})
	}
}

// A branch is never taken from the request: two runs on different branches
// differ only by the id in the path, and that id picks the argv target.
func TestReplyTargetComesFromTheRegistryNotTheRequest(t *testing.T) {
	stub := &stubRunner{}
	s := newReplyServer(t, stub.run, crewDelta(nil))

	req := replyRequest("POST", replyPath(t, s), replyBody(t, "yes"))
	req.URL.RawQuery = "branch=attacker/branch&worktree=/etc"
	req.Header.Set("X-Branch", "attacker/branch")

	if rec := doReply(t, s, req); rec.Code != http.StatusNoContent {
		t.Fatalf("status %d, want 204", rec.Code)
	}
	if got := stub.last(t); got.Argv[2] != "worker:"+replyBranch || got.Dir != replyDir {
		t.Errorf("argv[2]=%q dir=%q — the target must come from the registry", got.Argv[2], got.Dir)
	}
}

// execCrewReply's own error mapping, exercised without a crew binary.
func TestExecCrewReplyReportsMissingBinary(t *testing.T) {
	res := execCrewReply(context.Background(), replyExec{
		Dir:    t.TempDir(),
		CrewID: replyCrew,
		Argv:   []string{"houston-no-such-binary", "reply", "worker:x", "hi"},
	})
	if res.Err == nil {
		t.Fatalf("Err = nil, want a not-found error (exit %d, stderr %q)", res.ExitCode, res.Stderr)
	}
	if res.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0 — a command that never ran has no exit code", res.ExitCode)
	}
}

// The deadline branch is the tricky one: killing the child makes Run report an
// ExitError, which would otherwise be mistaken for crew's own refusal.
func TestExecCrewReplyReportsTimeoutNotRefusal(t *testing.T) {
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Skip("sleep not on PATH")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	res := execCrewReply(ctx, replyExec{Dir: t.TempDir(), CrewID: replyCrew, Argv: []string{"sleep", "10"}})
	if !errors.Is(res.Err, context.DeadlineExceeded) {
		t.Fatalf("Err = %v, want context.DeadlineExceeded", res.Err)
	}
	if res.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0 — a killed command did not choose its exit code", res.ExitCode)
	}
}
