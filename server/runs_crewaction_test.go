package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/noamsto/houston/crewview"
	"github.com/noamsto/houston/runs"
)

const (
	actionBus  = "/bus"
	actionRoot = "/root"
	actionCrew = "c1"
	actionBin  = "/nix/store/x-crew/bin/crew"
)

type fakeViewer struct {
	ov      crewview.Overview
	status  crewview.Status
	ok      bool
	root    string
	rootOK  bool
	mu      sync.Mutex
	buses   []string
	rootFor []string
}

func (f *fakeViewer) Latest(bus string) (crewview.Overview, crewview.Status, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.buses = append(f.buses, bus)
	return f.ov, f.status, f.ok
}

func (f *fakeViewer) Root(bus string) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rootFor = append(f.rootFor, bus)
	return f.root, f.rootOK
}

type actionCall struct {
	crewPath string
	x        crewExec
	deadline time.Duration
	hasDL    bool
}

// fakeCrewRunner records every call; its count is the only proof a refusal
// happened before anything ran.
type fakeCrewRunner struct {
	mu     sync.Mutex
	calls  []actionCall
	result crewResult
}

func (f *fakeCrewRunner) run(ctx context.Context, crewPath string, x crewExec) crewResult {
	f.mu.Lock()
	defer f.mu.Unlock()
	dl, ok := ctx.Deadline()
	f.calls = append(f.calls, actionCall{crewPath: crewPath, x: x, deadline: time.Until(dl), hasDL: ok})
	return f.result
}

func (f *fakeCrewRunner) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeCrewRunner) last(t *testing.T) actionCall {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		t.Fatal("runner was never called")
	}
	return f.calls[len(f.calls)-1]
}

func goldenOverview(t *testing.T) crewview.Overview {
	t.Helper()
	b, err := os.ReadFile("../crewview/testdata/overview.ndjson")
	if err != nil {
		t.Fatal(err)
	}
	ov, err := crewview.Decode(bytes.TrimSpace(b))
	if err != nil {
		t.Fatal(err)
	}
	return ov
}

func goldenItem(t *testing.T, ov crewview.Overview, id string) crewview.Item {
	t.Helper()
	for _, it := range ov.Attention {
		if it.ID == id {
			return it
		}
	}
	t.Fatalf("golden view has no item %q", id)
	return crewview.Item{}
}

func workerActionDelta(branch string) runs.Delta {
	return runs.Delta{Source: "crew", Key: "crew/" + actionBus + "/" + branch, Run: runs.Run{
		Agent:   "claude",
		State:   runs.StateRunning,
		Role:    runs.RoleWorker,
		Branch:  branch,
		Crew:    &runs.CrewRef{Name: actionCrew},
		CrewBus: actionBus,
	}}
}

type actionFixture struct {
	s      *Server
	viewer *fakeViewer
	runner *fakeCrewRunner
	ov     crewview.Overview
}

func newActionServer(t *testing.T, extra ...runs.Delta) *actionFixture {
	t.Helper()
	ov := goldenOverview(t)
	deltas := append([]runs.Delta{
		workerActionDelta("feat/e"),
		workerActionDelta("feat/c"),
		workerActionDelta("feat/b"),
		dispatcherDelta(actionBus, actionCrew),
	}, extra...)
	s := newReplyServer(t, nil, deltas...)
	f := &actionFixture{
		s:      s,
		viewer: &fakeViewer{ov: ov, status: crewview.Status{State: crewview.StateOK}, ok: true, root: actionRoot, rootOK: true},
		runner: &fakeCrewRunner{result: crewResult{Stdout: "done\n"}},
		ov:     ov,
	}
	s.crewViews = f.viewer
	s.crewRunner = f.runner.run
	s.crewPath = actionBin
	return f
}

// runID finds a listed run by role and branch through the registry, so the
// test never reimplements ID derivation.
func (f *actionFixture) runID(t *testing.T, role, branch string) string {
	t.Helper()
	for _, r := range f.s.runs.Snapshot() {
		if r.Role == role && r.Branch == branch {
			return r.ID
		}
	}
	t.Fatalf("no listed %s run for branch %q", role, branch)
	return ""
}

func (f *actionFixture) post(t *testing.T, id, body string) *httptest.ResponseRecorder {
	t.Helper()
	return doReply(t, f.s, replyRequest("POST", "/api/runs/"+id+"/crew/action", body))
}

func actionBody(t *testing.T, v map[string]any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func wantAction(t *testing.T, rec *httptest.ResponseRecorder, code int, contains string) {
	t.Helper()
	if rec.Code != code || !strings.Contains(rec.Body.String(), contains) {
		t.Fatalf("got %d %q, want %d containing %q", rec.Code, rec.Body.String(), code, contains)
	}
}

func TestCrewActionRunsTheViewArgv(t *testing.T) {
	f := newActionServer(t)
	id := f.runID(t, runs.RoleWorker, "feat/e")

	rec := f.post(t, id, `{"item":"stall:c1:feat/e"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d %q, want 200", rec.Code, rec.Body.String())
	}
	var got struct {
		Output string `json:"output"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	if got.Output != "done\n" {
		t.Fatalf("output %q, want the runner's stdout", got.Output)
	}

	call := f.runner.last(t)
	want := goldenItem(t, f.ov, "stall:c1:feat/e").Action.Argv[1:]
	if call.crewPath != actionBin {
		t.Errorf("crewPath %q, want s.crewPath %q", call.crewPath, actionBin)
	}
	if !slices.Equal(call.x.Args, want) {
		t.Errorf("args %q, want %q", call.x.Args, want)
	}
	if call.x.Dir != actionRoot || call.x.CrewID != actionCrew {
		t.Errorf("dir %q crew %q, want %q %q", call.x.Dir, call.x.CrewID, actionRoot, actionCrew)
	}
	if !call.hasDL || call.deadline > 15*time.Second || call.deadline < 10*time.Second {
		t.Errorf("deadline %v (set %v), want ~15s", call.deadline, call.hasDL)
	}
	if !slices.Equal(f.viewer.buses, []string{actionBus}) || !slices.Equal(f.viewer.rootFor, []string{actionBus}) {
		t.Errorf("viewer read buses %q roots %q, want the run's bus", f.viewer.buses, f.viewer.rootFor)
	}
}

func TestCrewActionReply(t *testing.T) {
	f := newActionServer(t)
	id := f.runID(t, runs.RoleWorker, "feat/c")

	wantAction(t, f.post(t, id, `{"item":"blocked:c1:feat/c","text":"yes"}`), http.StatusOK, "output")
	want := []string{"reply", "--crew", "c1", "--", "worker:feat/c", "yes"}
	if got := f.runner.last(t).x.Args; !slices.Equal(got, want) {
		t.Fatalf("args %q, want %q", got, want)
	}

	n := f.runner.count()
	wantAction(t, f.post(t, id, `{"item":"blocked:c1:feat/c"}`), http.StatusBadRequest, "text required")
	long := actionBody(t, map[string]any{"item": "blocked:c1:feat/c", "text": strings.Repeat("a", 8<<10+1)})
	wantAction(t, f.post(t, id, long), http.StatusBadRequest, "text required")
	if f.runner.count() != n {
		t.Fatalf("runner called on a refused reply")
	}
}

func TestCrewActionMergeNeedsConfirm(t *testing.T) {
	f := newActionServer(t)
	id := f.runID(t, runs.RoleWorker, "feat/b")

	rec := f.post(t, id, `{"item":"merge_ready:c1:feat/b"}`)
	if rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("status %d %q, want 428", rec.Code, rec.Body.String())
	}
	var got struct {
		Confirm string `json:"confirm"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got.Confirm != "Squash-merge PR #12?" {
		t.Fatalf("body %q (%v), want confirm prompt", rec.Body.String(), err)
	}
	if f.runner.count() != 0 {
		t.Fatal("merge ran without confirmation")
	}

	wantAction(t, f.post(t, id, `{"item":"merge_ready:c1:feat/b","confirm":true}`), http.StatusOK, "output")
	call := f.runner.last(t)
	if want := []string{"merge", "12", "--crew", "c1"}; !slices.Equal(call.x.Args, want) {
		t.Fatalf("args %q, want %q", call.x.Args, want)
	}
	if !call.hasDL || call.deadline < 100*time.Second {
		t.Fatalf("merge deadline %v (set %v), want ≥ 100s", call.deadline, call.hasDL)
	}
}

func TestCrewActionRefusals(t *testing.T) {
	f := newActionServer(t, workerActionDelta("feat/p"))
	worker := f.runID(t, runs.RoleWorker, "feat/e")
	disp := f.runID(t, runs.RoleDispatcher, "")

	cases := []struct {
		name, id, body string
		code           int
		contains       string
	}{
		{"another worker's item", worker, `{"item":"blocked:c1:feat/c","text":"x"}`, http.StatusNotFound, "no such item"},
		{"crew-scoped item on a worker", worker, `{"item":"hold_matured:c1:h-due"}`, http.StatusNotFound, "no such item"},
		{"other crew's item on dispatcher", disp, `{"item":"lane_dead:c2:"}`, http.StatusNotFound, "no such item"},
		{"worker item on dispatcher", disp, `{"item":"stall:c1:feat/e"}`, http.StatusNotFound, "no such item"},
		{"unknown item", worker, `{"item":"nope"}`, http.StatusNotFound, "no such item"},
		{"inspect is not executable", disp, `{"item":"hold_matured:c1:h-due"}`, http.StatusForbidden, "not executable"},
		{"peer-origin item", f.runID(t, runs.RoleWorker, "feat/p"), `{"item":"blocked:c1:feat/p"}`, http.StatusForbidden, "not executable"},
		{"unknown run", "nope", `{"item":"stall:c1:feat/e"}`, http.StatusNotFound, "no such run"},
		{"malformed body", worker, `{`, http.StatusBadRequest, "malformed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wantAction(t, f.post(t, tc.id, tc.body), tc.code, tc.contains)
		})
	}
	if n := f.runner.count(); n != 0 {
		t.Fatalf("runner called %d times on refusals", n)
	}
}

func TestCrewActionRunWithoutCrew(t *testing.T) {
	f := newActionServer(t, runs.Delta{Source: "tmux", Key: "%9", Run: runs.Run{Agent: "claude", State: runs.StateIdle, Branch: "feat/z"}})
	wantAction(t, f.post(t, f.runID(t, "", "feat/z"), `{"item":"stall:c1:feat/e"}`), http.StatusNotFound, "no crew")
}

func TestCrewActionIgnoresClientCommand(t *testing.T) {
	f := newActionServer(t)
	id := f.runID(t, runs.RoleWorker, "feat/e")

	body := `{"item":"stall:c1:feat/e","argv":["rm","-rf","/"],"command":"x","extra":{"a":1}}`
	wantAction(t, f.post(t, id, body), http.StatusOK, "output")
	want := goldenItem(t, f.ov, "stall:c1:feat/e").Action.Argv[1:]
	call := f.runner.last(t)
	if !slices.Equal(call.x.Args, want) || call.crewPath != actionBin {
		t.Fatalf("ran %q %q, want %q %q", call.crewPath, call.x.Args, actionBin, want)
	}
}

func TestCrewActionRunnerErrors(t *testing.T) {
	cases := []struct {
		name     string
		res      crewResult
		code     int
		contains string
	}{
		{"crew refused", crewResult{ExitCode: 2, Stderr: "nope"}, http.StatusConflict, "nope"},
		{"timeout", crewResult{Err: context.DeadlineExceeded}, http.StatusGatewayTimeout, ""},
		{"could not run", crewResult{Err: errors.New("exec: not found")}, http.StatusBadGateway, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newActionServer(t)
			f.runner.result = tc.res
			wantAction(t, f.post(t, f.runID(t, runs.RoleWorker, "feat/e"), `{"item":"stall:c1:feat/e"}`), tc.code, tc.contains)
		})
	}
}

func TestCrewActionViewUnavailable(t *testing.T) {
	f := newActionServer(t)
	id := f.runID(t, runs.RoleWorker, "feat/e")

	f.viewer.ok = false
	f.viewer.status = crewview.Status{State: crewview.StateUnavailable, Notice: "crew view unavailable: exit 1"}
	wantAction(t, f.post(t, id, `{"item":"stall:c1:feat/e"}`), http.StatusServiceUnavailable, "crew view unavailable: exit 1")

	f.viewer.ok = true
	f.viewer.rootOK = false
	wantAction(t, f.post(t, id, `{"item":"stall:c1:feat/e"}`), http.StatusServiceUnavailable, "")

	f.s.crewViews = nil
	wantAction(t, f.post(t, id, `{"item":"stall:c1:feat/e"}`), http.StatusServiceUnavailable, "crew view not started")

	if n := f.runner.count(); n != 0 {
		t.Fatalf("runner called %d times without a view", n)
	}
}

// A peer run's actions run on the peer; this route never forwards, with or
// without auth.
func TestCrewActionPeerRunIsNeverForwarded(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		p := newPeerRecorder(t, peerConflict)
		s := newForwardServer(t, p.srv.URL)
		s.auth.enabled = enabled
		runner := &fakeCrewRunner{}
		s.crewViews = &fakeViewer{ov: goldenOverview(t), ok: true, root: actionRoot, rootOK: true}
		s.crewRunner = runner.run

		rec := doReply(t, s, browserRequest("POST", "/api/runs/"+fwdPeerRunID+"/crew/action", `{"item":"stall:c1:feat/e"}`))
		if rec.Code != http.StatusForbidden || strings.TrimSpace(rec.Body.String()) != "peer actions are not available" {
			t.Errorf("auth %v: got %d %q, want 403 peer actions are not available", enabled, rec.Code, rec.Body.String())
		}
		if n := len(p.requests()); n != 0 {
			t.Errorf("auth %v: peer saw %d requests, want 0", enabled, n)
		}
		if runner.count() != 0 {
			t.Errorf("auth %v: runner called for a peer run", enabled)
		}
	}
}
