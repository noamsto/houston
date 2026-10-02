package server

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/noamsto/houston/tmux"
)

func TestNormalizeTask(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"  fix\n the  bug\r\n", "fix the bug"},
		{"a\tb c", "a b c"},
		{"\n\n", ""},
		{"👨\u200d👩\u200d👧 x", "👨\u200d👩\u200d👧 x"},
		{"a\u2028b\u2029c", "a b c"},
	} {
		if got := normalizeTask(tc.in); got != tc.want {
			t.Errorf("normalizeTask(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func validDispatcherRequest() dispatcherRequest {
	return dispatcherRequest{Repo: "/repo", Tasks: []string{"fix the bug"}, Engine: "claude", Model: "opus", Effort: "high"}
}

func TestValidateDispatcherRejections(t *testing.T) {
	engines := []string{"claude", "pi"}
	many := make([]string, maxDispatcherTasks+1)
	for i := range many {
		many[i] = "task"
	}
	longPrompt := make([]string, maxDispatcherTasks)
	for i := range longPrompt {
		longPrompt[i] = strings.Repeat("x", 500)
	}

	for _, tc := range []struct {
		name   string
		mutate func(*dispatcherRequest)
		field  string
	}{
		{"unknown engine", func(r *dispatcherRequest) { r.Engine = "bogus" }, "engine"},
		{"engine not enabled", func(r *dispatcherRequest) { r.Engine = "codex"; r.Model = "" }, "engine"},
		{"model for other engine", func(r *dispatcherRequest) { r.Model = "gpt-5.6-sol" }, "model"},
		{"bad effort", func(r *dispatcherRequest) { r.Effort = "extreme" }, "effort"},
		{"too many tasks", func(r *dispatcherRequest) { r.Tasks = many }, "tasks"},
		{"task too long", func(r *dispatcherRequest) { r.Tasks = []string{strings.Repeat("é", maxTaskRunes+1)} }, "tasks"},
		{"task with NUL", func(r *dispatcherRequest) { r.Tasks = []string{"a\x00b"} }, "tasks"},
		{"task starts with dash", func(r *dispatcherRequest) { r.Tasks = []string{"  --model x"} }, "tasks"},
		{"prompt too long", func(r *dispatcherRequest) { r.Tasks = longPrompt }, "tasks"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := validDispatcherRequest()
			tc.mutate(&req)
			_, derr := validateDispatcher(req, engines)
			if derr == nil {
				t.Fatal("expected a validation error")
			}
			if derr.code != http.StatusBadRequest || derr.field != tc.field {
				t.Errorf("err = %+v, want 400 on %s", derr, tc.field)
			}
		})
	}
}

func TestValidateDispatcherAccepts(t *testing.T) {
	req := validDispatcherRequest()
	req.Model, req.Effort = "", ""
	req.Tasks = []string{"line one\nline two", "", "  \n ", "ship 👨\u200d👩\u200d👧 \u200fשלום", "@notes.md", "/review"}

	got, derr := validateDispatcher(req, []string{"claude"})
	if derr != nil {
		t.Fatalf("unexpected error: %v", derr)
	}
	want := []string{"line one line two", "ship 👨\u200d👩\u200d👧 \u200fשלום", "@notes.md", "/review"}
	if !slices.Equal(got.Tasks, want) {
		t.Errorf("tasks = %q, want %q", got.Tasks, want)
	}
}

func TestComposeDispatcherPrompt(t *testing.T) {
	for _, tc := range []struct {
		tasks []string
		want  string
	}{
		{nil, ""},
		{[]string{"fix it"}, "fix it"},
		{[]string{"a", "b c", "d"}, "3 tasks: (1) a (2) b c (3) d"},
	} {
		if got := composeDispatcherPrompt(tc.tasks); got != tc.want {
			t.Errorf("composeDispatcherPrompt(%q) = %q, want %q", tc.tasks, got, tc.want)
		}
	}
}

func TestTmuxSafeArg(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want bool
	}{
		{"/home/u/git/proj", true},
		{"/home/u/git/a#b", false},
		{"/home/u/git/proj;", false},
		{"/home/u/git/a;b", true},
		{"/home/u/git/a\nb", false},
		{"/home/u/git/a\x7fb", false},
	} {
		if got := tmuxSafeArg(tc.in); got != tc.want {
			t.Errorf("tmuxSafeArg(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestSanitizeSessionName(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"houston", "houston"},
		{"my.repo name", "my_repo_name"},
		{"a_b-C9", "a_b-C9"},
		{"קוד", "___"},
		{"", "repo"},
	} {
		if got := sanitizeSessionName(tc.in); got != tc.want {
			t.Errorf("sanitizeSessionName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// scriptedTmux records every tmux argv and env and answers by subcommand.
type scriptedTmux struct {
	mu    sync.Mutex
	calls [][]string
	envs  [][]string
	reply map[string]func(args []string) (string, error)
}

func (f *scriptedTmux) run(_ context.Context, args []string, env ...string) (string, error) {
	f.mu.Lock()
	f.calls = append(f.calls, slices.Clone(args))
	f.envs = append(f.envs, slices.Clone(env))
	h := f.reply[args[0]]
	f.mu.Unlock()
	if h == nil {
		return "", nil
	}
	return h(args)
}

func (f *scriptedTmux) set(cmd string, h func(args []string) (string, error)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reply[cmd] = h
}

func (f *scriptedTmux) all() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

func (f *scriptedTmux) find(cmd string) []string {
	for _, c := range f.all() {
		if c[0] == cmd {
			return c
		}
	}
	return nil
}

type launchFixture struct {
	s       *Server
	tmux    *scriptedTmux
	repo    dispatchRepo
	crewDir string
}

// newLaunchFixture serves one fake main checkout named "proj" with no tmux
// windows; by default tmux reports no session, starts the window as pane %7,
// and the pane stays alive without registering (still starting).
func newLaunchFixture(t *testing.T) launchFixture {
	t.Helper()
	path := fakeRepo(t, filepath.Join(realTempDir(t), "proj"))
	repo := dispatchRepo{Path: path, Name: "proj", Crews: []string{}, Home: []string{}, commonDir: filepath.Join(path, ".git")}

	ft := &scriptedTmux{reply: map[string]func([]string) (string, error){
		"has-session":      func([]string) (string, error) { return "", errors.New("can't find session: proj") },
		"show-environment": func([]string) (string, error) { return "PATH=" + testServerPath, nil },
		"new-window":       func([]string) (string, error) { return "proj\t@3\t%7", nil },
		"new-session":      func([]string) (string, error) { return "proj\t@3\t%7", nil },
		"display-message":  func([]string) (string, error) { return "0 ", nil },
		"capture-pane":     func([]string) (string, error) { return "dispatcher: engine claude is not enabled", nil },
	}}

	s := newDispatchServer(t, nil, stubDispatchRepos([]dispatchRepo{repo}, nil))
	s.wsTmux = &fakeWorkspaceLister{}
	s.launchCommonDir = func(root string) (string, error) { return filepath.Join(root, ".git"), nil }
	s.tmuxRun = ft.run
	s.houstonExe = "/opt/houston"
	s.launchDir = filepath.Join(realTempDir(t), "launch")
	s.launchSlot = make(chan struct{}, 1)
	s.launchCheck = 300 * time.Millisecond
	s.launchPoll = 10 * time.Millisecond

	return launchFixture{
		s:       s,
		tmux:    ft,
		repo:    repo,
		crewDir: filepath.Join(repo.commonDir, "crew", "crews", dispatchTestNewCrewID),
	}
}

// registerOnStart makes the stubbed window "start" a launcher that registers
// itself, the way crew register writes <crew dir>/pid.
func (f launchFixture) registerOnStart(t *testing.T, cmd string) {
	t.Helper()
	f.tmux.set(cmd, func([]string) (string, error) {
		if err := os.WriteFile(filepath.Join(f.crewDir, "pid"), []byte("123"), 0o644); err != nil {
			t.Error(err)
		}
		return "proj\t@3\t%7", nil
	})
}

func (f launchFixture) post(t *testing.T, req dispatcherRequest) (*httptest.ResponseRecorder, dispatcherResponse) {
	t.Helper()
	b, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	return f.postRaw(t, string(b))
}

func (f launchFixture) postRaw(t *testing.T, body string) (*httptest.ResponseRecorder, dispatcherResponse) {
	t.Helper()
	req := httptest.NewRequest("POST", "http://"+dispatchHost+"/api/dispatch/dispatcher", strings.NewReader(body))
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

func (f launchFixture) request() dispatcherRequest {
	return dispatcherRequest{Repo: f.repo.Path, Tasks: []string{"fix the $(flaky) test; now"}, Engine: "claude", Model: "opus"}
}

func wantStatus(t *testing.T, rec *httptest.ResponseRecorder, code int) {
	t.Helper()
	if rec.Code != code {
		t.Fatalf("status %d, want %d (body %s)", rec.Code, code, rec.Body.String())
	}
}

func launchFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return []string{}
	}
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, e := range entries {
		names = append(names, filepath.Join(dir, e.Name()))
	}
	return names
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func TestDispatcherLaunchNewSessionSuccess(t *testing.T) {
	f := newLaunchFixture(t)
	f.registerOnStart(t, "new-session")

	rec, resp := f.post(t, f.request())
	wantStatus(t, rec, http.StatusOK)
	want := dispatcherResponse{Crew: dispatchTestNewCrewID, Session: "proj", Window: "@3", Pane: "%7", RunID: "pane-7"}
	if resp != want {
		t.Errorf("response = %+v, want %+v", resp, want)
	}

	if got := f.tmux.find("has-session"); !slices.Equal(got, []string{"has-session", "-t", "=proj"}) {
		t.Errorf("has-session argv = %q", got)
	}
	files := launchFiles(t, f.s.launchDir)
	if len(files) != 1 {
		t.Fatalf("launch files = %q, want 1", files)
	}
	wantArgv := []string{
		"new-session", "-d", "-E", "-P", "-F", launchFormat, "-s", "proj", "-n", "dispatcher",
		"-c", f.repo.Path, "--", "/opt/houston", "launch-dispatcher", files[0],
	}
	if got := f.tmux.find("new-session"); !slices.Equal(got, wantArgv) {
		t.Errorf("new-session argv = %q\nwant %q", got, wantArgv)
	}
	if !exists(f.crewDir) {
		t.Error("minted crew dir is gone after success")
	}
}

func TestDispatcherLaunchFileContents(t *testing.T) {
	f := newLaunchFixture(t)
	f.registerOnStart(t, "new-session")
	req := f.request()
	req.Tasks = []string{"  fix the $(flaky)\n test; now ", "", "write docs"}

	rec, _ := f.post(t, req)
	wantStatus(t, rec, http.StatusOK)

	files := launchFiles(t, f.s.launchDir)
	if len(files) != 1 {
		t.Fatalf("launch files = %q, want 1", files)
	}
	info, err := os.Stat(files[0])
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("launch file mode = %v, want 0600", info.Mode().Perm())
	}
	dinfo, err := os.Stat(f.s.launchDir)
	if err != nil {
		t.Fatal(err)
	}
	if dinfo.Mode().Perm() != 0o700 {
		t.Errorf("launch dir mode = %v, want 0700", dinfo.Mode().Perm())
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(readFile(t, files[0])), &raw); err != nil {
		t.Fatal(err)
	}
	if keys := slices.Sorted(maps.Keys(raw)); !slices.Equal(keys, []string{"args", "crew_id"}) {
		t.Errorf("launch file keys = %q, want [args crew_id]", keys)
	}
	var lf dispatcherLaunchFile
	if err := json.Unmarshal([]byte(readFile(t, files[0])), &lf); err != nil {
		t.Fatal(err)
	}
	prompt := "2 tasks: (1) fix the $(flaky) test; now (2) write docs"
	want := dispatcherLaunchFile{Args: []string{"--agent", "claude", "--model", "opus", prompt}, CrewID: dispatchTestNewCrewID}
	if lf.CrewID != want.CrewID || !slices.Equal(lf.Args, want.Args) {
		t.Errorf("launch file = %+v, want %+v", lf, want)
	}
	for _, call := range f.tmux.all() {
		for _, arg := range call {
			if strings.Contains(arg, "flaky") || strings.Contains(arg, "docs") {
				t.Errorf("task text reached tmux argv: %q", call)
			}
		}
	}
}

func TestDispatcherLaunchNoTasksNoPrompt(t *testing.T) {
	f := newLaunchFixture(t)
	f.registerOnStart(t, "new-session")
	req := f.request()
	req.Tasks, req.Model, req.Effort = nil, "", "max"

	rec, _ := f.post(t, req)
	wantStatus(t, rec, http.StatusOK)
	var lf dispatcherLaunchFile
	if err := json.Unmarshal([]byte(readFile(t, launchFiles(t, f.s.launchDir)[0])), &lf); err != nil {
		t.Fatal(err)
	}
	if want := []string{"--agent", "claude", "--effort", "max"}; !slices.Equal(lf.Args, want) {
		t.Errorf("args = %q, want %q", lf.Args, want)
	}
}

func TestDispatcherLaunchPicksSessionWithMostRepoWindows(t *testing.T) {
	f := newLaunchFixture(t)
	other := fakeRepo(t, filepath.Join(realTempDir(t), "other"))
	f.s.wsTmux = &fakeWorkspaceLister{wins: []tmux.WindowOptions{
		{Session: "zeta", GitRoot: f.repo.Path},
		{Session: "work", GitRoot: f.repo.Path},
		{Session: "work", GitRoot: f.repo.Path},
		{Session: "alpha", GitRoot: other},
		{Session: "alpha", GitRoot: other},
		{Session: "alpha", GitRoot: other},
	}}
	f.registerOnStart(t, "new-window")

	rec, resp := f.post(t, f.request())
	wantStatus(t, rec, http.StatusOK)
	if resp.Session != "proj" { // whatever tmux printed
		t.Errorf("session = %q", resp.Session)
	}
	got := f.tmux.find("new-window")
	if len(got) < 6 || got[4] != "-t" || got[5] != "=work:" {
		t.Errorf("new-window argv = %q, want -t =work:", got)
	}
	if f.tmux.find("has-session") != nil {
		t.Error("has-session ran although a session already holds the repo")
	}
}

func TestDispatcherLaunchSessionTieBreaksByName(t *testing.T) {
	f := newLaunchFixture(t)
	f.s.wsTmux = &fakeWorkspaceLister{wins: []tmux.WindowOptions{
		{Session: "zeta", GitRoot: f.repo.Path},
		{Session: "beta", GitRoot: f.repo.Path},
	}}
	f.registerOnStart(t, "new-window")

	rec, _ := f.post(t, f.request())
	wantStatus(t, rec, http.StatusOK)
	files := launchFiles(t, f.s.launchDir)
	wantArgv := []string{
		"new-window", "-P", "-F", launchFormat, "-t", "=beta:", "-n", "dispatcher",
		"-c", f.repo.Path, "--", "/opt/houston", "launch-dispatcher", files[0],
	}
	if got := f.tmux.find("new-window"); !slices.Equal(got, wantArgv) {
		t.Errorf("new-window argv = %q\nwant %q", got, wantArgv)
	}
}

func TestDispatcherLaunchExistingSessionByName(t *testing.T) {
	f := newLaunchFixture(t)
	f.tmux.set("has-session", func([]string) (string, error) { return "", nil })
	f.registerOnStart(t, "new-window")

	rec, _ := f.post(t, f.request())
	wantStatus(t, rec, http.StatusOK)
	got := f.tmux.find("new-window")
	if len(got) < 6 || got[5] != "=proj:" {
		t.Errorf("new-window argv = %q, want -t =proj:", got)
	}
	if f.tmux.find("new-session") != nil {
		t.Error("new-session ran although has-session found the session")
	}
}

func TestDispatcherLaunchDeadPane(t *testing.T) {
	f := newLaunchFixture(t)
	f.tmux.set("has-session", func([]string) (string, error) { return "", nil })
	f.tmux.set("display-message", func([]string) (string, error) { return "1 2", nil })

	rec, resp := f.post(t, f.request())
	wantStatus(t, rec, http.StatusUnprocessableEntity)
	if resp.Error != "dispatcher exited with status 2" {
		t.Errorf("error = %q", resp.Error)
	}
	if resp.Output != "dispatcher: engine claude is not enabled" {
		t.Errorf("output = %q", resp.Output)
	}
	if resp.Crew != "" {
		t.Errorf("crew = %q, want none (removed)", resp.Crew)
	}
	if got := f.tmux.find("capture-pane"); !slices.Equal(got, []string{"capture-pane", "-p", "-J", "-t", "%7", "-S", "-40"}) {
		t.Errorf("capture-pane argv = %q", got)
	}
	if got := f.tmux.find("display-message"); !slices.Equal(got, []string{"display-message", "-p", "-t", "%7", "#{pane_dead} #{pane_dead_status}"}) {
		t.Errorf("display-message argv = %q", got)
	}
	if got := f.tmux.find("kill-window"); !slices.Equal(got, []string{"kill-window", "-t", "@3"}) {
		t.Errorf("kill-window argv = %q", got)
	}
	if f.tmux.find("kill-session") != nil {
		t.Error("kill-session ran for a session houston did not create")
	}
	if exists(f.crewDir) {
		t.Error("crew dir kept after the dispatcher died")
	}
}

func TestDispatcherLaunchDeadPaneInCreatedSession(t *testing.T) {
	f := newLaunchFixture(t)
	f.tmux.set("display-message", func([]string) (string, error) { return "1 1", nil })

	rec, _ := f.post(t, f.request())
	wantStatus(t, rec, http.StatusUnprocessableEntity)
	if got := f.tmux.find("kill-session"); !slices.Equal(got, []string{"kill-session", "-t", "=proj"}) {
		t.Errorf("kill-session argv = %q", got)
	}
	if f.tmux.find("kill-window") != nil {
		t.Error("kill-window ran; the whole created session should go")
	}
}

func TestDispatcherLaunchPaneGone(t *testing.T) {
	for _, tc := range []struct {
		name string
		out  string
		err  error
	}{
		{"pane not found", "", errors.New("tmux display-message: exit status 1: can't find pane: %7")},
		{"server gone", "", errors.New("tmux display-message: exit status 1: no server running on /tmp/tmux-1000/default")},
		// A live server that no longer knows the pane exits 0 with every
		// pane field blank.
		{"blank fields", "", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newLaunchFixture(t)
			f.tmux.set("display-message", func([]string) (string, error) { return tc.out, tc.err })

			rec, resp := f.post(t, f.request())
			wantStatus(t, rec, http.StatusUnprocessableEntity)
			if resp.Error != "dispatcher exited immediately" {
				t.Errorf("error = %q", resp.Error)
			}
			if exists(f.crewDir) {
				t.Error("empty crew dir kept after the pane vanished")
			}
			if files := launchFiles(t, f.s.launchDir); len(files) != 0 {
				t.Errorf("launch files left behind: %q", files)
			}
		})
	}
}

// Only a tmux answer that the pane or server is gone means the launcher died;
// any other failure to check leaves the window, crew and launch file alone.
func TestDispatcherLaunchPaneCheckFailed(t *testing.T) {
	f := newLaunchFixture(t)
	f.tmux.set("display-message", func([]string) (string, error) {
		return "", errors.New("tmux display-message: exit status 1: protocol version mismatch (client 8, server 7)")
	})

	rec, resp := f.post(t, f.request())
	wantStatus(t, rec, http.StatusBadGateway)
	if !strings.HasPrefix(resp.Error, "could not check the dispatcher pane: ") || !strings.Contains(resp.Error, "protocol version mismatch") {
		t.Errorf("error = %q", resp.Error)
	}
	want := dispatcherResponse{Error: resp.Error, Crew: dispatchTestNewCrewID, Session: "proj", Window: "@3", Pane: "%7"}
	if resp != want {
		t.Errorf("response = %+v, want %+v", resp, want)
	}
	if !exists(f.crewDir) {
		t.Error("crew dir removed although the pane was never confirmed gone")
	}
	if files := launchFiles(t, f.s.launchDir); len(files) != 1 {
		t.Errorf("launch files = %q, want the one still unread", files)
	}
	if f.tmux.find("kill-window") != nil || f.tmux.find("kill-session") != nil {
		t.Error("houston killed a window it could not check")
	}
}

// has-session failing for any reason other than a missing session means tmux
// can't be asked; new-session must not run in the dark.
func TestDispatcherLaunchSessionLookupFailed(t *testing.T) {
	for _, msg := range []string{
		"tmux has-session: exit status 1: no server running on /tmp/tmux-1000/default",
		"tmux has-session: signal: killed",
	} {
		t.Run(msg, func(t *testing.T) {
			f := newLaunchFixture(t)
			f.tmux.set("has-session", func([]string) (string, error) { return "", errors.New(msg) })

			rec, resp := f.post(t, f.request())
			wantStatus(t, rec, http.StatusServiceUnavailable)
			if !strings.HasPrefix(resp.Error, "tmux unavailable") {
				t.Errorf("error = %q", resp.Error)
			}
			if resp.Crew != "" {
				t.Errorf("crew = %q, want none", resp.Crew)
			}
			if f.tmux.find("new-session") != nil || f.tmux.find("new-window") != nil {
				t.Error("tmux was asked to create a window")
			}
			if files := launchFiles(t, f.s.launchDir); len(files) != 0 {
				t.Errorf("launch files left behind: %q", files)
			}
			if exists(f.crewDir) {
				t.Error("crew dir kept after tmux was unavailable")
			}
		})
	}
}

func TestDispatcherLaunchStillStarting(t *testing.T) {
	f := newLaunchFixture(t)

	start := time.Now()
	rec, resp := f.post(t, f.request())
	wantStatus(t, rec, http.StatusOK)
	if time.Since(start) < f.s.launchCheck {
		t.Error("returned before the startup check window elapsed")
	}
	if resp.Pane != "%7" || resp.Crew != dispatchTestNewCrewID {
		t.Errorf("response = %+v", resp)
	}
	if !exists(f.crewDir) {
		t.Error("crew dir removed while the dispatcher is still starting")
	}
}

func TestDispatcherLaunchTmuxFailure(t *testing.T) {
	f := newLaunchFixture(t)
	f.tmux.set("new-session", func([]string) (string, error) { return "", errors.New("tmux new-session: exit status 1: no server") })

	rec, resp := f.post(t, f.request())
	wantStatus(t, rec, http.StatusBadGateway)
	if !strings.Contains(resp.Error, "no server") {
		t.Errorf("error = %q", resp.Error)
	}
	if files := launchFiles(t, f.s.launchDir); len(files) != 0 {
		t.Errorf("launch files left behind: %q", files)
	}
	if exists(f.crewDir) {
		t.Error("crew dir kept after tmux failed")
	}
	if resp.Crew != "" {
		t.Errorf("crew = %q, want none", resp.Crew)
	}
}

func TestDispatcherLaunchRefusalsBeforeTmux(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(f *launchFixture, req *dispatcherRequest)
		code  int
	}{
		{"unknown repo", func(_ *launchFixture, req *dispatcherRequest) { req.Repo = "/elsewhere" }, http.StatusNotFound},
		{"tmux-unsafe repo path", func(f *launchFixture, req *dispatcherRequest) {
			path := fakeRepo(t, filepath.Join(realTempDir(t), "a#b"))
			f.s.dispatchRepos = stubDispatchRepos([]dispatchRepo{{Path: path, Name: "a#b", commonDir: filepath.Join(path, ".git")}}, nil)
			req.Repo = path
		}, http.StatusUnprocessableEntity},
		{"engines lookup failed", func(f *launchFixture, _ *dispatcherRequest) {
			f.s.dispatchEngines = func(context.Context, string) ([]string, error) { return nil, errors.New("dispatch not found") }
		}, http.StatusBadGateway},
		{"tmux unreachable", func(f *launchFixture, _ *dispatcherRequest) {
			f.tmux.set("show-environment", func([]string) (string, error) { return "", errors.New("no server running") })
		}, http.StatusServiceUnavailable},
		{"global PATH removed", func(f *launchFixture, _ *dispatcherRequest) {
			f.tmux.set("show-environment", func([]string) (string, error) { return "-PATH", nil })
		}, http.StatusServiceUnavailable},
		{"engine not enabled", func(_ *launchFixture, req *dispatcherRequest) { req.Engine, req.Model = "pi", "" }, http.StatusBadRequest},
		{"slot busy", func(f *launchFixture, _ *dispatcherRequest) { f.s.launchSlot <- struct{}{} }, http.StatusTooManyRequests},
		{"crew collision", func(f *launchFixture, _ *dispatcherRequest) { mkdirAll(t, f.crewDir) }, http.StatusConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newLaunchFixture(t)
			req := f.request()
			tc.setup(&f, &req)

			rec, resp := f.post(t, req)
			wantStatus(t, rec, tc.code)
			if resp.Error == "" {
				t.Error("no error message")
			}
			for _, c := range f.tmux.all() {
				if c[0] != "show-environment" {
					t.Errorf("tmux ran: %q", c)
				}
			}
			if files := launchFiles(t, f.s.launchDir); len(files) != 0 {
				t.Errorf("launch files written: %q", files)
			}
		})
	}
}

func TestDispatcherLaunchBodyGates(t *testing.T) {
	f := newLaunchFixture(t)
	rec, _ := f.postRaw(t, `{"repo":"/x","tasks":[],"engine":"claude","extra":1}`)
	wantStatus(t, rec, http.StatusBadRequest)

	rec, _ = f.postRaw(t, `{"repo":"`+strings.Repeat("x", maxDispatchBody)+`"}`)
	wantStatus(t, rec, http.StatusRequestEntityTooLarge)
}

func TestDispatcherLaunchSeparateSlotFromDispatch(t *testing.T) {
	f := newLaunchFixture(t)
	f.registerOnStart(t, "new-session")
	f.s.dispatchSlot <- struct{}{}

	rec, _ := f.post(t, f.request())
	wantStatus(t, rec, http.StatusOK)
}

func TestDispatcherLaunchRemembersRepo(t *testing.T) {
	requireGit(t)
	f := newLaunchFixture(t)
	root := realTempDir(t)
	path := gitInit(t, filepath.Join(root, "proj"))
	f.repo.Path, f.repo.commonDir = path, filepath.Join(path, ".git")
	f.crewDir = filepath.Join(f.repo.commonDir, "crew", "crews", dispatchTestNewCrewID)
	f.s.dispatchRepos = stubDispatchRepos([]dispatchRepo{f.repo}, nil)
	f.s.repoReg = newRepoRegistry(filepath.Join(realTempDir(t), "repos.json"), []string{root}, gitCommonDir)
	f.registerOnStart(t, "new-session")

	rec, _ := f.post(t, f.request())
	wantStatus(t, rec, http.StatusOK)
	if !f.s.repoReg.Has(path) {
		t.Errorf("registry does not hold %s after a successful launch", path)
	}
}

func TestDispatcherLaunchLogsNoTaskText(t *testing.T) {
	logs := captureServerLogs(t)
	f := newLaunchFixture(t)
	f.registerOnStart(t, "new-session")
	req := f.request()
	req.Tasks = []string{"supersecret-task-text"}

	rec, _ := f.post(t, req)
	wantStatus(t, rec, http.StatusOK)

	out := logs.String()
	if !strings.Contains(out, "dispatcher launch") {
		t.Fatalf("no launch log line in %q", out)
	}
	if strings.Contains(out, "supersecret") {
		t.Errorf("task text leaked into logs: %q", out)
	}
}

func TestSweepLaunchDir(t *testing.T) {
	dir := realTempDir(t)
	old := filepath.Join(dir, "old.json")
	fresh := filepath.Join(dir, "fresh.json")
	for _, p := range []string{old, fresh} {
		if err := os.WriteFile(p, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	stale := time.Now().Add(-launchFileMaxAge - time.Minute)
	if err := os.Chtimes(old, stale, stale); err != nil {
		t.Fatal(err)
	}

	sweepLaunchDir(dir)
	if exists(old) {
		t.Error("stale launch file kept")
	}
	if !exists(fresh) {
		t.Error("fresh launch file removed")
	}
	sweepLaunchDir(filepath.Join(dir, "missing"))
}

func TestDispatcherLaunchNoServerPath(t *testing.T) {
	for _, tc := range []struct {
		name, out, want string
		err             error
	}{
		{"removed", "-PATH", "the tmux server has no global PATH", nil},
		{"empty", "PATH=", "the tmux server has no global PATH", nil},
		{"absent", "", "the tmux server has no global PATH", errors.New("tmux show-environment: exit status 1: unknown variable: PATH")},
		{"unreachable", "", "tmux unavailable: no server running", errors.New("no server running")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newLaunchFixture(t)
			f.tmux.set("show-environment", func([]string) (string, error) { return tc.out, tc.err })
			f.s.dispatchEngines = func(context.Context, string) ([]string, error) {
				t.Error("engines looked up without the server PATH")
				return nil, nil
			}

			rec, resp := f.post(t, f.request())
			wantStatus(t, rec, http.StatusServiceUnavailable)
			if resp.Error != tc.want {
				t.Errorf("error = %q, want %q", resp.Error, tc.want)
			}
			if exists(f.crewDir) {
				t.Error("crew dir created")
			}
		})
	}
}

// TestDispatcherLaunchServerPathEnv: only the window-creating client runs
// with the tmux server's PATH; the engines lookup gets it too.
func TestDispatcherLaunchServerPathEnv(t *testing.T) {
	for _, cmd := range []string{"new-session", "new-window"} {
		t.Run(cmd, func(t *testing.T) {
			f := newLaunchFixture(t)
			if cmd == "new-window" {
				f.tmux.set("has-session", func([]string) (string, error) { return "", nil })
			}
			f.registerOnStart(t, cmd)
			var enginesPath string
			f.s.dispatchEngines = func(_ context.Context, serverPath string) ([]string, error) {
				enginesPath = serverPath
				return []string{"claude"}, nil
			}

			rec, _ := f.post(t, f.request())
			wantStatus(t, rec, http.StatusOK)
			if enginesPath != testServerPath {
				t.Errorf("engines lookup server PATH = %q, want %q", enginesPath, testServerPath)
			}
			f.tmux.mu.Lock()
			defer f.tmux.mu.Unlock()
			for i, c := range f.tmux.calls {
				var want []string
				if c[0] == cmd {
					want = []string{"PATH=" + testServerPath}
				}
				if !slices.Equal(f.tmux.envs[i], want) {
					t.Errorf("tmux %s env = %q, want %q", c[0], f.tmux.envs[i], want)
				}
			}
		})
	}
}

func TestDispatcherLaunchSweepsStaleLaunchFiles(t *testing.T) {
	f := newLaunchFixture(t)
	f.registerOnStart(t, "new-session")
	mkdirAll(t, f.s.launchDir)
	old := filepath.Join(f.s.launchDir, "dispatcher-old.json")
	if err := os.WriteFile(old, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	stale := time.Now().Add(-launchFileMaxAge - time.Minute)
	if err := os.Chtimes(old, stale, stale); err != nil {
		t.Fatal(err)
	}

	rec, _ := f.post(t, f.request())
	wantStatus(t, rec, http.StatusOK)
	if exists(old) {
		t.Error("stale launch file kept past a launch")
	}
}
