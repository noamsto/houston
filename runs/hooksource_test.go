package runs

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/noamsto/houston/hook"
	"github.com/noamsto/houston/hub"
	"github.com/noamsto/houston/tmux"
)

func TestRunFromSessionViewKeysOnPane(t *testing.T) {
	key, r := runFromSessionView(hub.SessionView{
		SessionID:   "abc-123",
		TmuxSession: "houston",
		TmuxWindow:  "1",
		TmuxPane:    "%307",
		TmuxServer:  "1234",
		State:       hook.StateToolRunning,
		Tool:        "Bash",
	}, "")

	if key != "%307" {
		t.Fatalf("key = %q, want the tmux pane id — that is what every source correlates on", key)
	}
	if r.State != StateRunning {
		t.Errorf("State = %q, want %q", r.State, StateRunning)
	}
	if r.Tmux == nil || r.Tmux.PaneID != "%307" || r.Tmux.Session != "houston" || r.Tmux.Window != 1 {
		t.Errorf("TmuxRef = %+v, want session houston window 1 pane %%307", r.Tmux)
	}
	if r.Tmux == nil || r.Tmux.Server != "1234" {
		t.Errorf("TmuxRef.Server = %v, want 1234 to flow through from v.TmuxServer", r.Tmux)
	}
	if r.Agent != "claude" {
		t.Errorf("Agent = %q, want claude", r.Agent)
	}
}

func TestRunFromSessionViewFallsBackToSessionID(t *testing.T) {
	key, r := runFromSessionView(hub.SessionView{SessionID: "abc-123"}, "")

	if key != "claude/abc-123" {
		t.Fatalf("key = %q, want claude/abc-123 when there is no pane", key)
	}
	if r.Tmux != nil {
		t.Errorf("TmuxRef = %+v, want nil without a pane", r.Tmux)
	}
}

func TestRunFromSessionViewCarriesQuestionWhenBlocked(t *testing.T) {
	_, r := runFromSessionView(hub.SessionView{
		SessionID:   "abc",
		TmuxPane:    "%1",
		State:       hook.StatePermission,
		LastMessage: "Allow Bash(rm -rf)?",
	}, "")
	if r.State != StateBlocked {
		t.Fatalf("State = %q, want blocked", r.State)
	}
	if r.Question == nil || r.Question.Text != "Allow Bash(rm -rf)?" {
		t.Fatalf("Question = %+v, want the permission prompt", r.Question)
	}
	if r.Question.Via != "pane" {
		t.Errorf("Question.Via = %q, want pane", r.Question.Via)
	}
}

func TestRunFromSessionViewNamesTheRepo(t *testing.T) {
	_, r := runFromSessionView(hub.SessionView{
		SessionID: "abc-123",
		CWD:       "/home/noams/git/nix-amd-ai",
		GitBranch: "main",
	}, "")

	if r.Repo != "nix-amd-ai" {
		t.Errorf("Repo = %q, want nix-amd-ai — without it the card falls back to the raw session id", r.Repo)
	}
	if r.Branch != "main" {
		t.Errorf("Branch = %q, want main", r.Branch)
	}
}

func TestRunFromSessionViewNamesTheRepoFromAWorktree(t *testing.T) {
	// Worktree-per-branch is this project's mandated topology, so the leaf
	// directory is the branch, not the repo. It is recognisable as such:
	// worktrunk names the directory after the branch with "/" flattened.
	_, r := runFromSessionView(hub.SessionView{
		SessionID: "abc-123",
		CWD:       "/home/noams/Data/git/.worktrees/git/houston/fix-build-guard-ui-dist",
		GitBranch: "fix/build-guard-ui-dist",
	}, "")

	if r.Repo != "houston" {
		t.Errorf("Repo = %q, want houston — the leaf directory is the branch", r.Repo)
	}
	if r.Branch != "fix/build-guard-ui-dist" {
		t.Errorf("Branch = %q, want fix/build-guard-ui-dist", r.Branch)
	}
}

func TestRunFromSessionViewCarriesTheProject(t *testing.T) {
	_, r := runFromSessionView(hub.SessionView{SessionID: "abc", CWD: "/wt/feat"}, "houston")
	if r.Project != "houston" {
		t.Errorf("Project = %q, want houston", r.Project)
	}
	_, r = runFromSessionView(hub.SessionView{SessionID: "abc", CWD: "/wt/feat"}, "")
	if r.Project != "" {
		t.Errorf("Project = %q, want empty when unresolved", r.Project)
	}
}

func TestPaneGone(t *testing.T) {
	listedAt := time.Unix(1000, 500)
	live := map[string]bool{"%1": true}
	tests := []struct {
		name     string
		v        hub.SessionView
		live     map[string]bool
		listedAt time.Time
		want     bool
	}{
		{"pane is live", hub.SessionView{TmuxPane: "%1", UpdatedAt: 900}, live, listedAt, false},
		{"pane absent from a newer list", hub.SessionView{TmuxPane: "%2", UpdatedAt: 900}, live, listedAt, true},
		{"absent from an empty list", hub.SessionView{TmuxPane: "%2", UpdatedAt: 900}, map[string]bool{}, listedAt, true},
		{"list not newer than the state file", hub.SessionView{TmuxPane: "%2", UpdatedAt: 1000}, live, listedAt, false},
		{"list older than the state file", hub.SessionView{TmuxPane: "%2", UpdatedAt: 1001}, live, listedAt, false},
		{"no successful list yet", hub.SessionView{TmuxPane: "%2", UpdatedAt: 900}, nil, time.Time{}, false},
		{"headless session", hub.SessionView{UpdatedAt: 900}, live, listedAt, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := paneGone(tt.v, tt.live, tt.listedAt); got != tt.want {
				t.Errorf("paneGone = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestEndRunClearsWhatOnlyALiveRunHas(t *testing.T) {
	r := endRun(Run{
		State:     StateBlocked,
		Since:     50,
		UpdatedAt: 60,
		Repo:      "houston",
		Question:  &Question{Text: "Allow?", Via: "pane"},
		Activity:  Activity{Tool: "Bash", Hint: "ls", Message: "Allow?", Preview: "keep"},
	})
	if r.State != StateDone {
		t.Errorf("State = %q, want done", r.State)
	}
	if r.Question != nil || r.Activity.Tool != "" || r.Activity.Hint != "" || r.Activity.Message != "" {
		t.Errorf("run still carries live-only fields: %+v", r)
	}
	if r.Since != 50 || r.UpdatedAt != 60 || r.Repo != "houston" || r.Activity.Preview != "keep" {
		t.Errorf("endRun dropped last-known data: %+v", r)
	}
}

type fakePanes struct {
	mu          sync.Mutex
	panes       []tmux.PaneOptions
	err         error
	server      string
	serverStart int64
}

// set replaces the listed pane ids. Each PaneOptions ListPaneOptions returns
// is stamped with this fake's current identity, same as a real listing
// stamps every line with the server that produced it.
func (f *fakePanes) set(err error, ids ...string) {
	ps := make([]tmux.PaneOptions, len(ids))
	for i, id := range ids {
		ps[i] = tmux.PaneOptions{PaneID: id}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
	f.panes = ps
}

// setPanes replaces the listing with fully-formed pane options, for tests that
// need a pane's @crew_role or @claude_status. The fake's identity is stamped
// onto each option at list time, same as set.
func (f *fakePanes) setPanes(ps ...tmux.PaneOptions) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = nil
	f.panes = ps
}

// setIdentity configures the identity stamped onto panes returned after this
// call. Left uncalled, a fakePanes stamps server "" / serverStart 0, which
// paneForeign treats as unknown.
func (f *fakePanes) setIdentity(server string, startedAt int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.server, f.serverStart = server, startedAt
}

func (f *fakePanes) ListPaneOptions() ([]tmux.PaneOptions, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	panes := make([]tmux.PaneOptions, len(f.panes))
	copy(panes, f.panes)
	for i := range panes {
		panes[i].ServerPID = f.server
		panes[i].ServerStart = f.serverStart
	}
	return panes, nil
}

// blockedState is a permission-blocked session on pane %9 whose state file is
// hours old.
func blockedState() hook.SessionState {
	return hook.SessionState{
		SessionID:   "s1",
		State:       hook.StatePermission,
		LastMessage: "Allow Bash?",
		TmuxPane:    "%9",
		UpdatedAt:   time.Now().Add(-3 * time.Hour).Unix(),
	}
}

// startHub runs h and returns once its watcher is live; the returned context
// is cancelled at cleanup. Call it after t.TempDir(): cleanup is LIFO, so Run
// has closed the watcher before the state dir is removed.
func startHub(t *testing.T, h *hub.Hub, dir string) context.Context {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = h.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("hub.Run did not return after cancel")
		}
	})

	// Run installs the watch before its initial scan, so the probe appearing
	// proves the watch exists; its removal is only observable through the watch.
	const probe = "startHub-probe"
	has := func() bool {
		for _, v := range h.Snapshot() {
			if v.SessionID == probe {
				return true
			}
		}
		return false
	}
	wait := func(what string, cond func() bool) {
		for deadline := time.Now().Add(5 * time.Second); !cond(); time.Sleep(time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s", what)
			}
		}
	}
	if err := hook.Write(hook.Path(dir, probe), hook.SessionState{SessionID: probe, State: hook.StateIdle}); err != nil {
		t.Fatal(err)
	}
	wait("hub to load the probe state", has)
	if err := os.Remove(hook.Path(dir, probe)); err != nil {
		t.Fatal(err)
	}
	wait("hub to drop the probe state", func() bool { return !has() })
	return ctx
}

const (
	hookTestEvery  = 2 * time.Millisecond
	hookQuietTicks = 25
)

// startHookSource runs a HookSource over a real hub seeded with blockedState.
func startHookSource(t *testing.T, panes paneLister) <-chan Delta {
	t.Helper()
	out, _ := startHookSourceWith(t, panes, blockedState(), nil)
	return out
}

// startHookSourceWith seeds the hub with st, lets tweak adjust the source, and
// returns the state dir so a test can rewrite the file.
func startHookSourceWith(t *testing.T, panes paneLister, st hook.SessionState, tweak func(*HookSource), extra ...hook.SessionState) (<-chan Delta, string) {
	t.Helper()
	dir := t.TempDir()
	for _, w := range append([]hook.SessionState{st}, extra...) {
		if err := hook.Write(hook.Path(dir, w.SessionID), w); err != nil {
			t.Fatal(err)
		}
	}

	h := hub.NewWithOptions(dir, hub.Options{ClaudeProjectsDir: "-"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx := startHub(t, h, dir)
	for deadline := time.Now().Add(2 * time.Second); len(h.Snapshot()) < 1+len(extra); time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("hub never loaded the state file")
		}
	}

	src := NewHookSource(h, panes)
	src.every = hookTestEvery
	if tweak != nil {
		tweak(src)
	}
	out := make(chan Delta, 64)
	go func() { _ = src.Run(ctx, out) }()
	return out, dir
}

// waitDelta reads deltas until one satisfies pred, failing after a deadline.
func waitDelta(t *testing.T, out <-chan Delta, what string, pred func(Delta) bool) Delta {
	t.Helper()
	timeout := time.After(2 * time.Second)
	for {
		select {
		case d := <-out:
			if pred(d) {
				return d
			}
		case <-timeout:
			t.Fatalf("no delta: %s", what)
		}
	}
}

func TestHookSourceEndsARunWhosePaneIsGone(t *testing.T) {
	panes := &fakePanes{}
	panes.set(errors.New("tmux down"))
	out := startHookSource(t, panes)

	waitDelta(t, out, "initial blocked run", func(d Delta) bool { return d.Run.State == StateBlocked })

	panes.set(nil, "%1")
	d := waitDelta(t, out, "ended run", func(d Delta) bool { return d.Run.State == StateDone })
	if d.Key != "%9" || d.Run.Question != nil || d.Run.Activity.Message != "" {
		t.Errorf("ended delta = %+v, want key %%9 with no question or message", d)
	}

	panes.set(nil, "%1", "%9")
	waitDelta(t, out, "restored blocked run", func(d Delta) bool {
		return d.Run.State == StateBlocked && d.Run.Question != nil
	})
}

func TestHookSourceKeepsTheRunWhileTmuxIsUnreachable(t *testing.T) {
	panes := &fakePanes{}
	panes.set(errors.New("tmux down"))
	out := startHookSource(t, panes)

	waitDelta(t, out, "initial blocked run", func(d Delta) bool { return d.Run.State == StateBlocked })

	expectNoDelta(t, out, "run ended while tmux was unreachable", func(d Delta) bool {
		return d.Run.State == StateDone || d.Gone
	})
}

func TestHookSourceWithoutAPaneListerNeverEndsARun(t *testing.T) {
	out := startHookSource(t, nil)

	waitDelta(t, out, "initial blocked run", func(d Delta) bool { return d.Run.State == StateBlocked })

	expectNoDelta(t, out, "run ended with the pane check disabled", func(d Delta) bool {
		return d.Run.State == StateDone
	})
}

// expectNoDelta fails if any delta satisfying bad arrives within a short
// window of hookQuietTicks pane-poll and resync ticks.
func expectNoDelta(t *testing.T, out <-chan Delta, what string, bad func(Delta) bool) {
	t.Helper()
	quiet := time.After(hookQuietTicks * hookTestEvery)
	for {
		select {
		case d := <-out:
			if bad(d) {
				t.Fatalf("%s: %+v", what, d)
			}
		case <-quiet:
			return
		}
	}
}

func TestHookSourceKeepsAnEndedVerdictThroughATmuxOutage(t *testing.T) {
	panes := &fakePanes{}
	panes.set(nil, "%1")
	out := startHookSource(t, panes)
	waitDelta(t, out, "ended run", func(d Delta) bool { return d.Run.State == StateDone })

	panes.set(errors.New("tmux down"))
	expectNoDelta(t, out, "verdict changed during an outage", func(d Delta) bool {
		return d.Run.State != StateDone || d.Gone
	})
}

func TestHookSourceNeverReEndsARunThatShowedLaterActivity(t *testing.T) {
	// A session whose pane houston cannot see (another tmux server, a resume in
	// a new pane) keeps firing hooks. Once it does, it must stay live rather
	// than flip back to done on every pane listing.
	panes := &fakePanes{}
	panes.set(nil, "%1")
	out, dir := startHookSourceWith(t, panes, blockedState(), nil)
	waitDelta(t, out, "ended run", func(d Delta) bool { return d.Run.State == StateDone })

	st := blockedState()
	st.UpdatedAt = time.Now().Add(-30 * time.Second).Unix()
	if err := hook.Write(hook.Path(dir, st.SessionID), st); err != nil {
		t.Fatal(err)
	}
	waitDelta(t, out, "revived run", func(d Delta) bool { return d.Run.State == StateBlocked })
	expectNoDelta(t, out, "run ended again despite later activity", func(d Delta) bool {
		return d.Run.State == StateDone
	})
}

func TestHookSourceResolvesTheProjectFromTheCWD(t *testing.T) {
	st := blockedState()
	st.CWD = "/repos/main/wt/feat-x"

	tests := []struct {
		name string
		dir  func(string) (string, error)
		want string
	}{
		{"resolved", func(string) (string, error) { return "/repos/main/.git", nil }, "main"},
		{"unresolved leaves the tmux layer's project alone", func(string) (string, error) { return "", errors.New("not a repo") }, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, _ := startHookSourceWith(t, nil, st, func(s *HookSource) { s.projects.commonDir = tt.dir })
			d := waitDelta(t, out, "initial run", func(d Delta) bool { return !d.Gone })
			if d.Run.Project != tt.want {
				t.Errorf("Project = %q, want %q", d.Run.Project, tt.want)
			}
		})
	}
}

func TestHookSourceSpeaksForOneSessionPerPane(t *testing.T) {
	// /clear and resume mint a new session id on the same pane and leave the old
	// state file behind. The newest session speaks for the pane; the old one
	// must not shadow it or make the run flap between the two layers.
	panes := &fakePanes{}
	panes.set(nil, "%9")

	old := blockedState()
	old.SessionID = "old"
	old.UpdatedAt = time.Now().Add(-5 * time.Hour).Unix()
	cur := blockedState()
	cur.SessionID = "cur"
	cur.State = hook.StateThinking
	cur.LastMessage = ""
	cur.UpdatedAt = time.Now().Add(-2 * time.Hour).Unix()

	out, _ := startHookSourceWith(t, panes, old, nil, cur)
	waitDelta(t, out, "current session", func(d Delta) bool { return d.Key == "%9" && d.Run.State == StateThinking })
	expectNoDelta(t, out, "the stale session spoke for the pane", func(d Delta) bool {
		return d.Key == "%9" && d.Run.State != StateThinking
	})
}

func TestPaneForeign(t *testing.T) {
	tests := []struct {
		name string
		v    hub.SessionView
		ps   paneSet
		want bool
	}{
		{"no pane", hub.SessionView{}, paneSet{server: "2001", serverStart: 100}, false},
		{"different server", hub.SessionView{TmuxPane: "%1", TmuxServer: "1966", UpdatedAt: 150}, paneSet{server: "2001", serverStart: 100}, true},
		{"same server", hub.SessionView{TmuxPane: "%1", TmuxServer: "2001", UpdatedAt: 150}, paneSet{server: "2001", serverStart: 100}, false},
		{"legacy state predates server start", hub.SessionView{TmuxPane: "%1", UpdatedAt: 99}, paneSet{server: "2001", serverStart: 100}, true},
		{"legacy state at server start", hub.SessionView{TmuxPane: "%1", UpdatedAt: 100}, paneSet{server: "2001", serverStart: 100}, false},
		{"legacy state after server start", hub.SessionView{TmuxPane: "%1", UpdatedAt: 101}, paneSet{server: "2001", serverStart: 100}, false},
		{"identity unknown", hub.SessionView{TmuxPane: "%1", TmuxServer: "1966", UpdatedAt: 150}, paneSet{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := paneForeign(tt.v, tt.ps); got != tt.want {
				t.Errorf("paneForeign = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestListPanesTakesIdentityFromTheListing(t *testing.T) {
	panes := &fakePanes{}
	panes.setIdentity("2001", 1790086864)
	panes.set(nil, "%1", "%2")

	ps, ok := listPanes(panes)
	if !ok {
		t.Fatal("listPanes ok = false, want true")
	}
	if !ps.live["%1"] || !ps.live["%2"] {
		t.Errorf("live = %+v, want %%1 and %%2 present", ps.live)
	}
	if ps.server != "2001" || ps.serverStart != 1790086864 {
		t.Errorf("server = %q serverStart = %d, want 2001/1790086864 straight off the listing", ps.server, ps.serverStart)
	}
}

// A state file naming a tmux server other than the one being listed.
func TestHookSourceDistrustsAForeignServer(t *testing.T) {
	panes := &fakePanes{}
	panes.set(nil, "%20")
	panes.setIdentity("2001", time.Now().Unix())

	st := blockedState()
	st.TmuxPane = "%20"
	st.TmuxServer = "1966"

	out, _ := startHookSourceWith(t, panes, st, nil)
	d := waitDelta(t, out, "foreign-keyed run", func(d Delta) bool { return d.Key == "claude/"+st.SessionID })
	if d.Run.Tmux != nil {
		t.Errorf("Tmux = %+v, want nil for a pane owned by a different tmux server", d.Run.Tmux)
	}
}

// A legacy state file (no tmux_server) whose updated_at predates this
// server's start time, so its pane id belongs to an earlier incarnation.
func TestHookSourceDistrustsAPaneFromBeforeTheServerStarted(t *testing.T) {
	panes := &fakePanes{}
	panes.set(nil, "%20")
	serverStart := time.Now().Unix()
	panes.setIdentity("2001", serverStart)

	st := blockedState()
	st.TmuxPane = "%20"
	st.TmuxServer = ""
	st.UpdatedAt = serverStart - 10

	out, _ := startHookSourceWith(t, panes, st, nil)
	d := waitDelta(t, out, "foreign-keyed run", func(d Delta) bool { return d.Key == "claude/"+st.SessionID })
	if d.Run.Tmux != nil {
		t.Errorf("Tmux = %+v, want nil for a pane predating this tmux server", d.Run.Tmux)
	}
}

// TestHookSourceDistrustsAPaneIdReusedAfterAServerRestart covers the state
// files written before tmux_server existed: nothing names the old server, so
// only the start-time clause can tell that %20 was re-minted by a restart.
func TestHookSourceDistrustsAPaneIdReusedAfterAServerRestart(t *testing.T) {
	st := blockedState()
	st.TmuxPane = "%20"

	panes := &fakePanes{}
	panes.setIdentity("2001", st.UpdatedAt+1)
	panes.set(nil, "%20")

	out, _ := startHookSourceWith(t, panes, st, nil)
	d := waitDelta(t, out, "foreign-keyed run", func(d Delta) bool { return d.Key == "claude/"+st.SessionID })
	if d.Run.Tmux != nil {
		t.Errorf("Tmux = %+v, want nil for a pane id reused by a restarted tmux server", d.Run.Tmux)
	}
}

func TestHookSourceForeignSessionRevivesWithoutATmuxRef(t *testing.T) {
	panes := &fakePanes{}
	panes.set(nil, "%20")
	panes.setIdentity("2001", time.Now().Unix())

	st := blockedState()
	st.TmuxPane = "%20"
	st.TmuxServer = "1966"

	out, dir := startHookSourceWith(t, panes, st, nil)
	waitDelta(t, out, "foreign run ended", func(d Delta) bool {
		return d.Key == "claude/"+st.SessionID && d.Run.State == StateDone
	})

	st.UpdatedAt = time.Now().Add(-30 * time.Second).Unix()
	if err := hook.Write(hook.Path(dir, st.SessionID), st); err != nil {
		t.Fatal(err)
	}
	d := waitDelta(t, out, "revived run", func(d Delta) bool {
		return d.Key == "claude/"+st.SessionID && d.Run.State == StateBlocked
	})
	if d.Run.Tmux != nil {
		t.Errorf("Tmux = %+v, want nil — the run stays live but owns no pane here", d.Run.Tmux)
	}
	expectNoDelta(t, out, "run ended again despite later activity", func(d Delta) bool {
		return d.Key == "claude/"+st.SessionID && d.Run.State == StateDone
	})
}

// TestHookSourceDistrustsAForeignSessionAmongTwoOnOnePane seeds a live and a
// foreign session on the same pane id, with the foreign one newer. Without
// normalize()'d keys, both current() and the <-sub branch would dedupe them
// onto the raw pane id and newerSession would let the foreign session win,
// silently dropping the live one.
func TestHookSourceDistrustsAForeignSessionAmongTwoOnOnePane(t *testing.T) {
	panes := &fakePanes{}
	panes.set(nil, "%20")
	serverStart := time.Now().Add(-3 * time.Hour).Unix()
	panes.setIdentity("2001", serverStart)

	live := blockedState()
	live.SessionID = "live"
	live.TmuxPane = "%20"
	live.TmuxServer = "2001"
	live.UpdatedAt = time.Now().Add(-2 * time.Hour).Unix()

	foreign := blockedState()
	foreign.SessionID = "foreign"
	foreign.TmuxPane = "%20"
	foreign.TmuxServer = "1966"                               // a different tmux server minted this pane id
	foreign.UpdatedAt = time.Now().Add(-1 * time.Hour).Unix() // newer than live

	// A large every keeps the periodic pane-poll/resync ticks from firing
	// during this test, so the revival delta below can only come from the
	// <-sub branch's own normalize handling, not resync's independent one.
	out, dir := startHookSourceWith(t, panes, live, func(s *HookSource) { s.every = time.Hour }, foreign)

	foreignKey := "claude/" + foreign.SessionID
	deltas := map[string]Delta{}
	timeout := time.After(2 * time.Second)
	for len(deltas) < 2 {
		select {
		case d := <-out:
			deltas[d.Key] = d
		case <-timeout:
			t.Fatalf("got %d of 2 expected initial deltas: %+v", len(deltas), deltas)
		}
	}

	liveDelta, ok := deltas["%20"]
	if !ok {
		t.Fatalf("no delta keyed on the pane; deltas = %+v", deltas)
	}
	if liveDelta.Run.Tmux == nil || liveDelta.Run.Tmux.PaneID != "%20" {
		t.Errorf("live Tmux = %+v, want a TmuxRef for %%20", liveDelta.Run.Tmux)
	}

	foreignDelta, ok := deltas[foreignKey]
	if !ok {
		t.Fatalf("no delta keyed on the foreign session id; deltas = %+v", deltas)
	}
	if foreignDelta.Run.Tmux != nil {
		t.Errorf("foreign Tmux = %+v, want nil for a pane owned by a different tmux server", foreignDelta.Run.Tmux)
	}

	// Exercise the <-sub branch's own normalize call: an update to the
	// foreign session must resolve to its own key, not the live session's.
	foreign.UpdatedAt = time.Now().Add(-30 * time.Second).Unix()
	foreign.LastMessage = "Allow Write?"
	if err := hook.Write(hook.Path(dir, foreign.SessionID), foreign); err != nil {
		t.Fatal(err)
	}
	revived := waitDelta(t, out, "foreign session revives on its own key", func(d Delta) bool {
		return d.Key == foreignKey && d.Run.State == StateBlocked
	})
	if revived.Run.Tmux != nil {
		t.Errorf("revived Tmux = %+v, want nil", revived.Run.Tmux)
	}
}

// endedState is a session that announced SessionEnd while still recorded on
// pane %9, with a state file newer than the server start so neither paneGone
// nor paneForeign can fire — the #120 shape.
func endedState(sid string) hook.SessionState {
	return hook.SessionState{
		SessionID: sid,
		State:     hook.StateEnded,
		TmuxPane:  "%9",
		CWD:       "/home/me/houston",
		UpdatedAt: time.Now().Unix(),
	}
}

// awaitDisowned reads every delta until the ended session's own key arrives,
// failing the moment one speaks for pane %9. waitDelta discards what it does
// not match, so a regression that emitted %9 first would slip past a trailing
// expectNoDelta unnoticed.
func awaitDisowned(t *testing.T, out <-chan Delta, sid string) Delta {
	t.Helper()
	timeout := time.After(2 * time.Second)
	for {
		select {
		case d := <-out:
			if d.Key == "%9" {
				t.Fatalf("a run kept speaking for the reused pane: %+v", d)
			}
			if d.Key == "claude/"+sid {
				return d
			}
		case <-timeout:
			t.Fatal("no delta: ended session keyed on itself")
		}
	}
}

// TestHookSourceDisownsThePaneOfAnEndedSession is the #120 shape itself: the
// pane is live and belongs to the same tmux server, so paneGone and
// paneForeign both stay silent, yet the session announced its own end and can
// no longer vouch for who is there now.
func TestHookSourceDisownsThePaneOfAnEndedSession(t *testing.T) {
	panes := &fakePanes{}
	panes.set(nil, "%9")
	panes.setIdentity("1966", time.Now().Add(-time.Hour).Unix())

	out, _ := startHookSourceWith(t, panes, endedState("s1"), nil)
	d := awaitDisowned(t, out, "s1")
	if d.Run.Tmux != nil {
		t.Errorf("Tmux = %+v, want nil — an ended session can no longer vouch for its pane", d.Run.Tmux)
	}
	if d.Run.State != StateDone {
		t.Errorf("State = %q, want done", d.Run.State)
	}

	expectNoDelta(t, out, "a run kept speaking for the reused pane", func(d Delta) bool { return d.Key == "%9" })
}

// TestHookSourceDisownsAnEndedPaneWithoutAPaneLister proves the loss of proof
// is the agent's own, not tmux's: no listing happens at all, yet StateEnded
// alone is enough to disown the pane.
func TestHookSourceDisownsAnEndedPaneWithoutAPaneLister(t *testing.T) {
	out, _ := startHookSourceWith(t, nil, endedState("s1"), nil)
	d := awaitDisowned(t, out, "s1")
	if d.Run.Tmux != nil {
		t.Errorf("Tmux = %+v, want nil — an ended session can no longer vouch for its pane", d.Run.Tmux)
	}
	if d.Run.State != StateDone {
		t.Errorf("State = %q, want done", d.Run.State)
	}

	expectNoDelta(t, out, "a run kept speaking for the reused pane", func(d Delta) bool { return d.Key == "%9" })
}

// TestHookSourceReclaimsThePaneWhenAnEndedSessionResumes shows keying follows
// the *current* state, never a sticky "was ended once" flag: the same session
// id resuming on the same pane must key back on that pane.
func TestHookSourceReclaimsThePaneWhenAnEndedSessionResumes(t *testing.T) {
	panes := &fakePanes{}
	panes.set(nil, "%9")
	panes.setIdentity("1966", time.Now().Add(-time.Hour).Unix())

	st := endedState("s1")
	out, dir := startHookSourceWith(t, panes, st, nil)
	waitDelta(t, out, "ended session keyed on itself", func(d Delta) bool { return d.Key == "claude/s1" })

	st.State = hook.StateStarting
	st.TmuxSession = "houston"
	st.TmuxWindow = "3"
	st.UpdatedAt = time.Now().Unix()
	if err := hook.Write(hook.Path(dir, st.SessionID), st); err != nil {
		t.Fatal(err)
	}

	d := waitDelta(t, out, "resumed session reclaims the pane", func(d Delta) bool { return d.Key == "%9" })
	if d.Run.Tmux == nil || d.Run.Tmux.PaneID != "%9" {
		t.Errorf("Tmux = %+v, want a TmuxRef for %%9", d.Run.Tmux)
	}
	if d.Run.State == StateDone {
		t.Errorf("State = %q, want a live state", d.Run.State)
	}
}

// TestHookSourceSpeaksForEveryEndedSessionOnAPane seeds two ended sessions
// that were left behind on the same pane. Unlike two live sessions on one
// pane (TestHookSourceSpeaksForOneSessionPerPane), they no longer share a
// key once disowned, so current() cannot collapse one onto the other —
// both must be reported.
func TestHookSourceSpeaksForEveryEndedSessionOnAPane(t *testing.T) {
	panes := &fakePanes{}
	panes.set(nil, "%9")
	panes.setIdentity("1966", time.Now().Add(-time.Hour).Unix())

	s1 := endedState("s1")
	s2 := endedState("s2")
	s2.UpdatedAt = s1.UpdatedAt + 1

	out, _ := startHookSourceWith(t, panes, s1, nil, s2)

	seen := map[string]bool{}
	timeout := time.After(2 * time.Second)
	for !seen["claude/s1"] || !seen["claude/s2"] {
		select {
		case d := <-out:
			// Tmux == nil && State == StateDone matches the disowned shape
			// TestHookSourceWithdrawsThePaneWhenASessionEnds checks — a split
			// that left the Tmux ref on one key must still fail here.
			if (d.Key == "claude/s1" || d.Key == "claude/s2") && d.Run.Tmux == nil && d.Run.State == StateDone {
				seen[d.Key] = true
			}
		case <-timeout:
			t.Fatalf("saw %v, want deltas for both claude/s1 and claude/s2", seen)
		}
	}
}

// TestHookSourceWithdrawsThePaneWhenASessionEnds covers the transition rather
// than an already-ended state: ending must also retract the pane-keyed layer,
// or a stale hooks layer keeps granting caps at %9 while every other test here
// still passes.
func TestHookSourceWithdrawsThePaneWhenASessionEnds(t *testing.T) {
	panes := &fakePanes{}
	panes.set(nil, "%9")
	panes.setIdentity("1966", time.Now().Add(-4*time.Hour).Unix())

	st := blockedState()
	out, dir := startHookSourceWith(t, panes, st, nil)
	waitDelta(t, out, "initial live run on the pane", func(d Delta) bool { return d.Key == "%9" })

	st.State = hook.StateEnded
	st.UpdatedAt = time.Now().Unix()
	if err := hook.Write(hook.Path(dir, st.SessionID), st); err != nil {
		t.Fatal(err)
	}

	sawEnded, sawGone := false, false
	timeout := time.After(2 * time.Second)
	for !sawEnded || !sawGone {
		select {
		case d := <-out:
			if d.Key == "claude/"+st.SessionID && d.Run.Tmux == nil && d.Run.State == StateDone {
				sawEnded = true
			}
			if d.Key == "%9" && d.Gone {
				sawGone = true
			}
		case <-timeout:
			t.Fatalf("sawEnded=%v sawGone=%v, want both", sawEnded, sawGone)
		}
	}
}

// TestHookSourceSkipsRoleGridPanes is #111/#143: a role-grid pane parks on the
// crew bus under role:<branch>:<role>, not worker:<branch>. The tmux and crew
// layers already skip @crew_role panes (#99); a role pane running Claude with
// hooks installed must not slip back in through the hooks layer.
func TestHookSourceSkipsRoleGridPanes(t *testing.T) {
	panes := &fakePanes{}
	panes.setPanes(tmux.PaneOptions{PaneID: "%9", CrewRole: "plan-critic"})

	// blockedState() is a live, same-server session on %9; without the filter
	// it would emit a blocked run carrying a Question.
	out := startHookSource(t, panes)
	expectNoDelta(t, out, "a role-grid pane surfaced as its own run", func(d Delta) bool { return true })
}

// TestHookSourceDemotesTurnEndWaitingAgainstTmux is #143 root cause 2: the
// hooks layer's turn-end waiting (idle_prompt, Stop) must not outrank the tmux
// layer's idle/done verdict for the same pane. A permission prompt is a
// distinct hook state and always stays blocked.
func TestHookSourceDemotesTurnEndWaitingAgainstTmux(t *testing.T) {
	tests := []struct {
		name      string
		status    string
		hookState hook.State
		wantState State
		wantQuest bool
	}{
		{"idle demotes a turn-end waiting", "idle 100 0", hook.StateWaiting, StateIdle, false},
		{"done demotes a turn-end waiting", "done 100 0", hook.StateWaiting, StateDone, false},
		{"waiting keeps a turn-end waiting blocked", "waiting 100 0", hook.StateWaiting, StateBlocked, true},
		{"processing keeps a turn-end waiting blocked", "processing 100 0", hook.StateWaiting, StateBlocked, true},
		{"no status keeps a turn-end waiting blocked", "", hook.StateWaiting, StateBlocked, true},
		{"idle never demotes a permission prompt", "idle 100 0", hook.StatePermission, StateBlocked, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			panes := &fakePanes{}
			panes.setPanes(tmux.PaneOptions{PaneID: "%9", ClaudeStatus: tt.status})

			st := blockedState()
			st.State = tt.hookState
			st.LastMessage = "Claude is waiting for your input"
			out, _ := startHookSourceWith(t, panes, st, nil)

			d := waitDelta(t, out, "waiting run", func(d Delta) bool { return d.Key == "%9" })
			if d.Run.State != tt.wantState {
				t.Errorf("State = %q, want %q", d.Run.State, tt.wantState)
			}
			if got := d.Run.Question != nil; got != tt.wantQuest {
				t.Errorf("Question present = %v, want %v (%+v)", got, tt.wantQuest, d.Run.Question)
			}
		})
	}
}
