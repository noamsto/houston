package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/noamsto/houston/runs"
	"github.com/noamsto/houston/tmux"
)

func TestParseLauncherArgv(t *testing.T) {
	tests := []struct {
		name string
		argv []string
		want launcherSettings
	}{
		{"houston shaped", []string{"/nix/store/x-bash/bin/bash", "/nix/store/y-dispatcher/bin/dispatcher", "--agent", "claude", "--model", "opus", "--effort", "high"},
			launcherSettings{"claude", "opus", "high"}},
		{"hand launched", []string{"bash", "/usr/bin/dispatcher"}, launcherSettings{}},
		{"task text between flags", []string{"bash", "/usr/bin/dispatcher", "--agent", "pi", "fix the flaky test", "--model", "m1"},
			launcherSettings{Engine: "pi", Model: "m1"}},
		{"trailing flag without value", []string{"bash", "/usr/bin/dispatcher", "--agent", "claude", "--model"},
			launcherSettings{Engine: "claude"}},
		{"new before agent", []string{"bash", "/usr/bin/dispatcher", "--new", "--agent", "pi"}, launcherSettings{Engine: "pi"}},
		{"last occurrence wins", []string{"bash", "/usr/bin/dispatcher", "--model", "a", "--model", "b"}, launcherSettings{Model: "b"}},
		{"no dispatcher element", []string{"bash", "script.sh", "--agent", "claude"}, launcherSettings{}},
		{"autocompact consumes its value", []string{"bash", "/usr/bin/dispatcher", "--autocompact", "5000", "--effort", "low"},
			launcherSettings{Effort: "low"}},
		{"autocompact value is not a flag", []string{"bash", "/usr/bin/dispatcher", "--autocompact", "--model", "x"}, launcherSettings{}},
		{"dispatcher-old does not match", []string{"bash", "/x/dispatcher-old", "--agent", "claude"}, launcherSettings{}},
		{"flag before dispatcher ignored", []string{"--agent", "pi", "dispatcher", "--model", "m"}, launcherSettings{Model: "m"}},
		{"empty", nil, launcherSettings{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseLauncherArgv(tt.argv); got != tt.want {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestReadLauncherArgv(t *testing.T) {
	argv, ok := readLauncherArgv(os.Getpid())
	if runtime.GOOS != "linux" {
		if ok || argv != nil {
			t.Fatalf("got %v, %v on a non-linux host", argv, ok)
		}
		return
	}
	if !ok || len(argv) == 0 || argv[0] == "" {
		t.Fatalf("own argv: got %v, %v", argv, ok)
	}
	if _, ok := readLauncherArgv(1 << 30); ok {
		t.Fatal("a pid that does not exist read as ok")
	}
}

func TestRestartSettings(t *testing.T) {
	s := func(v string) *string { return &v }
	inh := launcherSettings{Engine: "claude", Model: "opus", Effort: "high"}
	tests := []struct {
		name     string
		req      restartRequest
		inh      launcherSettings
		runAgent string
		want     dispatcherRequest
	}{
		{"inherit all", restartRequest{}, inh, "claude", dispatcherRequest{Engine: "claude", Model: "opus", Effort: "high"}},
		{"model override", restartRequest{Model: s("sonnet")}, inh, "claude", dispatcherRequest{Engine: "claude", Model: "sonnet", Effort: "high"}},
		{"effort override", restartRequest{Effort: s("low")}, inh, "claude", dispatcherRequest{Engine: "claude", Model: "opus", Effort: "low"}},
		{"engine change drops model", restartRequest{Engine: s("pi")}, inh, "claude", dispatcherRequest{Engine: "pi", Model: "", Effort: "high"}},
		{"engine change with model", restartRequest{Engine: s("pi"), Model: s("m")}, inh, "claude", dispatcherRequest{Engine: "pi", Model: "m", Effort: "high"}},
		{"engine equal to inherited keeps model", restartRequest{Engine: s("claude")}, inh, "claude", dispatcherRequest{Engine: "claude", Model: "opus", Effort: "high"}},
		{"model cleared", restartRequest{Model: s("")}, inh, "claude", dispatcherRequest{Engine: "claude", Model: "", Effort: "high"}},
		{"effort cleared", restartRequest{Effort: s("")}, inh, "claude", dispatcherRequest{Engine: "claude", Model: "opus", Effort: ""}},
		{"no inherited engine uses run agent", restartRequest{}, launcherSettings{}, "pi", dispatcherRequest{Engine: "pi"}},
		{"engine equal to run agent keeps model", restartRequest{Engine: s("pi")}, launcherSettings{Model: "m"}, "pi", dispatcherRequest{Engine: "pi", Model: "m"}},
		{"engine differs from run agent drops model", restartRequest{Engine: s("claude")}, launcherSettings{Model: "m"}, "pi", dispatcherRequest{Engine: "claude"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := restartSettings(tt.req, tt.inh, tt.runAgent)
			if got.Engine != tt.want.Engine || got.Model != tt.want.Model || got.Effort != tt.want.Effort {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestRestartBusy(t *testing.T) {
	q := func(kind string) *runs.Question { return &runs.Question{Text: "?", Kind: kind} }
	tests := []struct {
		name string
		run  runs.Run
		want bool
	}{
		{"thinking", runs.Run{State: runs.StateThinking}, true},
		{"running", runs.Run{State: runs.StateRunning}, true},
		{"compacting", runs.Run{State: runs.StateCompacting}, true},
		{"idle", runs.Run{State: runs.StateIdle}, false},
		{"review", runs.Run{State: runs.StateReview}, false},
		{"done", runs.Run{State: runs.StateDone}, false},
		{"failed", runs.Run{State: runs.StateFailed}, false},
		{"blocked without question", runs.Run{State: runs.StateBlocked}, true},
		{"blocked on turn question", runs.Run{State: runs.StateBlocked, Question: q(runs.QuestionTurn)}, false},
		{"blocked on dialog", runs.Run{State: runs.StateBlocked, Question: q(runs.QuestionDialog)}, true},
		{"blocked on ask", runs.Run{State: runs.StateBlocked, Question: q(runs.QuestionAsk)}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := restartBusy(tt.run); got != tt.want {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}

// restartFakePanes is the runPaneOps a restart sees. resolve gets the 1-based
// ResolvePane call number so a test can change the answer at the kill.
type restartFakePanes struct {
	mu       sync.Mutex
	resolves int
	captures int
	resolve  func(n int) (tmux.Pane, error)
	capture  string
	width    int
}

func (p *restartFakePanes) ResolvePane(string) (tmux.Pane, error) {
	p.mu.Lock()
	p.resolves++
	n := p.resolves
	p.mu.Unlock()
	return p.resolve(n)
}

func (p *restartFakePanes) CapturePaneWidth(tmux.Pane, int) (string, int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.captures++
	return p.capture, p.width, nil
}

func (p *restartFakePanes) captureCalls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.captures
}

func (*restartFakePanes) SendKeys(context.Context, tmux.Pane, string, bool) error {
	return errors.New("unused")
}
func (*restartFakePanes) SendSpecialKey(tmux.Pane, string) error { return errors.New("unused") }
func (*restartFakePanes) PaneInMode(tmux.Pane) (bool, error)     { return false, errors.New("unused") }

const restartTestCrew = "1700000001-777"

type restartFixture struct {
	launchFixture
	crew   string
	panes  *restartFakePanes
	runID  string
	mu     sync.Mutex
	launch *dispatcherLaunchFile
}

// newRestartFixture serves a joined dispatcher run %5 (pane pid 123, launched
// with --agent claude --model opus --effort high) in session "proj".
func newRestartFixture(t *testing.T) *restartFixture {
	t.Helper()
	lf := newLaunchFixture(t)
	f := &restartFixture{launchFixture: lf, crew: restartTestCrew}
	bus := filepath.Join(lf.repo.commonDir, "crew")
	f.crewDir = filepath.Join(bus, "crews", f.crew)
	if err := os.MkdirAll(f.crewDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.crewDir, "pid"), []byte("123\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	f.panes = &restartFakePanes{
		resolve: func(int) (tmux.Pane, error) { return tmux.Pane{ID: "%5", Server: "100"}, nil },
		capture: "plain screen\n",
		width:   60,
	}
	s := lf.s
	s.runPanes = f.panes
	s.launcherFound = func(string) bool { return true }
	s.launcherArgv = func(int) ([]string, bool) {
		return []string{"/nix/store/x-bash/bin/bash", "/nix/store/y/bin/dispatcher", "--agent", "claude", "--model", "opus", "--effort", "high"}, true
	}
	s.dispatchEngines = func(context.Context, string) ([]string, error) { return []string{"claude", "pi"}, nil }

	f.tmux.set("has-session", func(args []string) (string, error) {
		if args[len(args)-1] != "=proj" {
			return "", errors.New("can't find session: " + args[len(args)-1])
		}
		return "", nil
	})
	f.tmux.set("new-window", func(args []string) (string, error) {
		b, err := os.ReadFile(args[len(args)-1])
		if err != nil {
			t.Errorf("read launch file: %v", err)
			return "", err
		}
		var got dispatcherLaunchFile
		if err := json.Unmarshal(b, &got); err != nil {
			t.Errorf("decode launch file: %v", err)
		}
		f.mu.Lock()
		f.launch = &got
		f.mu.Unlock()
		if err := os.WriteFile(filepath.Join(f.crewDir, "pid"), []byte("456\n"), 0o644); err != nil {
			t.Error(err)
		}
		return "proj:@9:%11", nil
	})
	f.tmux.set("kill-pane", func([]string) (string, error) { return "", nil })

	s.runs = runs.NewRegistry(runs.DefaultOrder)
	f.runID = runs.PaneRunID("%5")
	f.addRun(t, "%5", runs.StateIdle, true)

	got, ok := f.findRun(f.runID)
	if !ok {
		t.Fatalf("fixture run %s not in snapshot", f.runID)
	}
	if !got.Caps.Terminal || got.Role != runs.RoleDispatcher || got.Crew == nil || got.Crew.Name != f.crew || got.CrewBus != bus {
		t.Fatalf("fixture run is not a joined dispatcher: %+v", got)
	}
	return f
}

// addRun applies a tmux layer for key, plus a crew layer when joined.
func (f *restartFixture) addRun(t *testing.T, key string, state runs.State, joined bool) {
	t.Helper()
	f.s.runs.Apply(runs.Delta{Source: "tmux", Key: key, Run: runs.Run{
		Agent: "claude", State: state, Role: runs.RoleDispatcher,
		Tmux: &runs.TmuxRef{Session: "proj", PaneID: key, Server: "100"},
	}})
	if joined {
		f.s.runs.Apply(runs.Delta{Source: "crew", Key: key, Run: runs.Run{
			Role: runs.RoleDispatcher, Crew: &runs.CrewRef{Name: f.crew}, CrewBus: filepath.Join(f.repo.commonDir, "crew"),
		}})
	}
}

func (f *restartFixture) findRun(id string) (runs.Run, bool) { return f.s.findRun(id) }

func (f *restartFixture) setState(t *testing.T, state runs.State) {
	t.Helper()
	f.addRun(t, "%5", state, true)
}

func (f *restartFixture) post(t *testing.T, body string) (*httptest.ResponseRecorder, dispatcherResponse) {
	t.Helper()
	return f.postTo(t, f.runID, body)
}

func (f *restartFixture) postTo(t *testing.T, id, body string) (*httptest.ResponseRecorder, dispatcherResponse) {
	t.Helper()
	req := httptest.NewRequest("POST", "http://"+dispatchHost+"/api/runs/"+id+"/restart", strings.NewReader(body))
	req.Host = dispatchHost
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: authCookie, Value: dispatchToken})
	rec := doDispatch(t, f.s, req)
	var resp dispatcherResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal %q: %v", rec.Body.String(), err)
	}
	return rec, resp
}

func (f *restartFixture) recordedLaunch() *dispatcherLaunchFile {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.launch
}

// index is the position of the first tmux call to cmd, or -1.
func (f *restartFixture) index(cmd string) int {
	return slices.IndexFunc(f.tmux.all(), func(c []string) bool { return c[0] == cmd })
}

// noWindowOrKill asserts the restart touched neither the new window nor the old pane.
func (f *restartFixture) noWindowOrKill(t *testing.T) {
	t.Helper()
	for _, cmd := range []string{"new-window", "new-session", "kill-pane", "kill-window"} {
		if f.index(cmd) >= 0 {
			t.Errorf("tmux %s was called: %v", cmd, f.tmux.all())
		}
	}
	if files := launchFiles(t, f.s.launchDir); len(files) != 0 {
		t.Errorf("launch files left: %v", files)
	}
}

func permissionDialogCapture(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "answer", "testdata", "perm-bash.txt"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestRestartNoSuchRun(t *testing.T) {
	f := newRestartFixture(t)
	rec, resp := f.postTo(t, "pane-99", "{}")
	wantStatus(t, rec, http.StatusNotFound)
	if resp.Error != "no such run" {
		t.Errorf("error %q", resp.Error)
	}
	f.noWindowOrKill(t)
}

func TestRestartNotADispatcher(t *testing.T) {
	f := newRestartFixture(t)
	f.s.runs.Apply(runs.Delta{Source: "tmux", Key: "%6", Run: runs.Run{
		Agent: "claude", State: runs.StateIdle, Tmux: &runs.TmuxRef{Session: "proj", PaneID: "%6", Server: "100"},
	}})
	rec, resp := f.postTo(t, runs.PaneRunID("%6"), "{}")
	wantStatus(t, rec, http.StatusNotFound)
	if resp.Error != "run is not a dispatcher" {
		t.Errorf("error %q", resp.Error)
	}
	f.noWindowOrKill(t)
}

func TestRestartDispatcherWithoutCrew(t *testing.T) {
	f := newRestartFixture(t)
	f.addRun(t, "%7", runs.StateIdle, false)
	rec, resp := f.postTo(t, runs.PaneRunID("%7"), "{}")
	wantStatus(t, rec, http.StatusNotFound)
	if resp.Error != "dispatcher has no crew" {
		t.Errorf("error %q", resp.Error)
	}
	f.noWindowOrKill(t)
}

func TestRestartRefusesBusy(t *testing.T) {
	cases := []struct {
		name   string
		state  runs.State
		dialog bool // the pane shows a permission dialog
	}{
		{"running turn", runs.StateRunning, false},
		{"permission dialog on screen", runs.StateIdle, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newRestartFixture(t)
			f.setState(t, tc.state)
			if tc.dialog {
				f.panes.capture, f.panes.width = permissionDialogCapture(t), 60
			}
			rec, resp := f.post(t, "{}")
			wantStatus(t, rec, http.StatusConflict)
			if !resp.Busy || resp.Crew != f.crew || resp.Error != restartBusyMsg {
				t.Errorf("response %+v", resp)
			}
			f.noWindowOrKill(t)
		})
	}
}

func TestRestartIdle(t *testing.T) {
	for _, body := range []string{"{}", ""} {
		t.Run("body "+body, func(t *testing.T) {
			f := newRestartFixture(t)
			rec, resp := f.post(t, body)
			wantStatus(t, rec, http.StatusOK)
			want := dispatcherResponse{Crew: f.crew, Session: "proj", Window: "@9", Pane: "%11", RunID: runs.PaneRunID("%11")}
			if resp != want {
				t.Errorf("response %+v, want %+v", resp, want)
			}

			nw := f.tmux.find("new-window")
			if nw == nil {
				t.Fatalf("no new-window: %v", f.tmux.all())
			}
			if i := slices.Index(nw, "-t"); i < 0 || nw[i+1] != "=proj:" {
				t.Errorf("new-window target: %v", nw)
			}
			if n := len(nw); nw[n-2] != "launch-dispatcher" {
				t.Errorf("new-window does not end with launch-dispatcher <file>: %v", nw)
			}
			lf := f.recordedLaunch()
			if lf == nil {
				t.Fatal("no launch file recorded")
			}
			wantFile := dispatcherLaunchFile{Args: []string{"--agent", "claude", "--model", "opus", "--effort", "high"}, CrewID: f.crew, WaitPID: 123}
			if lf.CrewID != wantFile.CrewID || lf.WaitPID != wantFile.WaitPID || !slices.Equal(lf.Args, wantFile.Args) {
				t.Errorf("launch file %+v, want %+v", *lf, wantFile)
			}
			if lf.CrewID == dispatchTestNewCrewID {
				t.Error("launch file used the minted test crew id")
			}

			kill := f.tmux.find("kill-pane")
			if !slices.Equal(kill, []string{"kill-pane", "-t", "%5"}) {
				t.Errorf("kill-pane: %v", kill)
			}
			if f.index("kill-pane") < f.index("new-window") {
				t.Errorf("old pane killed before the new window opened: %v", f.tmux.all())
			}
			if !exists(f.crewDir) {
				t.Error("crew dir removed")
			}
		})
	}
}

func TestRestartForceSkipsBusyChecks(t *testing.T) {
	f := newRestartFixture(t)
	f.setState(t, runs.StateRunning)
	rec, _ := f.post(t, `{"force":true}`)
	wantStatus(t, rec, http.StatusOK)
	if f.tmux.find("kill-pane") == nil {
		t.Errorf("no kill-pane: %v", f.tmux.all())
	}
	if n := f.panes.captureCalls(); n != 0 {
		t.Errorf("CapturePaneWidth called %d times under force", n)
	}
}

func TestRestartRejectsBadBody(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"invalid engine", `{"engine":"nope"}`},
		{"model not for engine", `{"model":"gpt-x"}`},
		{"effort not allowed", `{"effort":"ultra"}`},
		{"unknown field", `{"tasks":["x"]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRestartFixture(t)
			rec, resp := f.post(t, tc.body)
			wantStatus(t, rec, http.StatusBadRequest)
			if resp.Error == "" || resp.Crew != f.crew {
				t.Errorf("response %+v", resp)
			}
			f.noWindowOrKill(t)
		})
	}
}

func TestRestartInheritedModelNoLongerAllowed(t *testing.T) {
	f := newRestartFixture(t)
	f.s.launcherArgv = func(int) ([]string, bool) {
		return []string{"bash", "/x/dispatcher", "--agent", "claude", "--model", "bogus"}, true
	}
	rec, resp := f.post(t, "{}")
	wantStatus(t, rec, http.StatusBadRequest)
	if !strings.Contains(resp.Error, "model") {
		t.Errorf("error %q does not name the model", resp.Error)
	}
	f.noWindowOrKill(t)
}

func TestRestartEngineOverrideDropsModel(t *testing.T) {
	f := newRestartFixture(t)
	rec, _ := f.post(t, `{"engine":"pi"}`)
	wantStatus(t, rec, http.StatusOK)
	lf := f.recordedLaunch()
	if lf == nil {
		t.Fatal("no launch file recorded")
	}
	if want := []string{"--agent", "pi", "--effort", "high"}; !slices.Equal(lf.Args, want) {
		t.Errorf("args %v, want %v", lf.Args, want)
	}
}

func TestRestartLauncherNotOnPath(t *testing.T) {
	f := newRestartFixture(t)
	f.s.launcherFound = func(string) bool { return false }
	rec, resp := f.post(t, "{}")
	wantStatus(t, rec, http.StatusBadGateway)
	if resp.Crew != f.crew {
		t.Errorf("response %+v", resp)
	}
	f.noWindowOrKill(t)
}

func TestRestartKillFailureRollsBack(t *testing.T) {
	f := newRestartFixture(t)
	f.tmux.set("kill-pane", func([]string) (string, error) {
		return "", errors.New("tmux kill-pane: exit status 1: some failure")
	})
	rec, resp := f.post(t, "{}")
	wantStatus(t, rec, http.StatusBadGateway)
	if resp.Crew != f.crew || !strings.Contains(resp.Error, "some failure") {
		t.Errorf("response %+v", resp)
	}
	if kw := f.tmux.find("kill-window"); !slices.Equal(kw, []string{"kill-window", "-t", "@9"}) {
		t.Errorf("kill-window: %v", kw)
	}
	if files := launchFiles(t, f.s.launchDir); len(files) != 0 {
		t.Errorf("launch files left: %v", files)
	}
	if !exists(f.crewDir) {
		t.Error("crew dir removed")
	}
}

func TestRestartServerMismatchAtKill(t *testing.T) {
	f := newRestartFixture(t)
	f.panes.resolve = func(n int) (tmux.Pane, error) {
		if n >= 2 {
			return tmux.Pane{ID: "%5", Server: "200"}, nil
		}
		return tmux.Pane{ID: "%5", Server: "100"}, nil
	}
	rec, resp := f.post(t, "{}")
	wantStatus(t, rec, http.StatusConflict)
	if resp.Crew != f.crew {
		t.Errorf("response %+v", resp)
	}
	if f.index("kill-pane") >= 0 {
		t.Errorf("kill-pane called: %v", f.tmux.all())
	}
	if kw := f.tmux.find("kill-window"); !slices.Equal(kw, []string{"kill-window", "-t", "@9"}) {
		t.Errorf("kill-window: %v", kw)
	}
	if files := launchFiles(t, f.s.launchDir); len(files) != 0 {
		t.Errorf("launch files left: %v", files)
	}
}

func TestRestartOldPaneAlreadyGone(t *testing.T) {
	f := newRestartFixture(t)
	f.panes.resolve = func(n int) (tmux.Pane, error) {
		if n >= 2 {
			return tmux.Pane{}, tmux.ErrPaneNotFound
		}
		return tmux.Pane{ID: "%5", Server: "100"}, nil
	}
	rec, _ := f.post(t, "{}")
	wantStatus(t, rec, http.StatusOK)
	if f.index("kill-pane") >= 0 {
		t.Errorf("kill-pane called: %v", f.tmux.all())
	}
}

func TestRestartLauncherDies(t *testing.T) {
	f := newRestartFixture(t)
	f.tmux.set("new-window", func([]string) (string, error) { return "proj:@9:%11", nil })
	f.tmux.set("display-message", func([]string) (string, error) { return "1 3", nil })
	rec, resp := f.post(t, "{}")
	wantStatus(t, rec, http.StatusUnprocessableEntity)
	if resp.Crew != f.crew || !strings.Contains(resp.Error, "status 3") {
		t.Errorf("response %+v", resp)
	}
	if !exists(f.crewDir) {
		t.Error("crew dir removed")
	}
}
