package runs

import (
	"context"
	"errors"
	"io"
	"log/slog"
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
	mu    sync.Mutex
	panes []tmux.PaneOptions
	err   error
}

func (f *fakePanes) set(err error, ids ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
	f.panes = nil
	for _, id := range ids {
		f.panes = append(f.panes, tmux.PaneOptions{PaneID: id})
	}
}

func (f *fakePanes) ListPaneOptions() ([]tmux.PaneOptions, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.panes, f.err
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
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = h.Run(ctx) }()
	for deadline := time.Now().Add(2 * time.Second); len(h.Snapshot()) < 1+len(extra); time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("hub never loaded the state file")
		}
	}

	src := NewHookSource(h, panes)
	src.every = 10 * time.Millisecond
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

// expectNoDelta fails if any delta satisfying bad arrives within a short window.
func expectNoDelta(t *testing.T, out <-chan Delta, what string, bad func(Delta) bool) {
	t.Helper()
	quiet := time.After(200 * time.Millisecond)
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
