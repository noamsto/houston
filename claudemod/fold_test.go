package claudemod

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/noamsto/houston/hook"
)

func (f *feeder) check(seq int64, id, tool, agent string) {
	f.t.Helper()
	f.apply(seq, evToolCheck, func(r *Record) { r.ID, r.Tool, r.Agent, r.Decision = id, tool, agent, "ask" })
}

func (f *feeder) permission(seq int64, tool, agent string, in *ToolInput) {
	f.t.Helper()
	f.apply(seq, evPermission, func(r *Record) { r.Tool, r.Agent, r.Input = tool, agent, in })
}

func TestLateToolEndAfterTurnCompleteStaysWaiting(t *testing.T) {
	t.Run("lower seq arriving last", func(t *testing.T) {
		f := newFeeder(t)
		f.apply(1, evTurnStart, nil)
		f.toolStart(2, "a", "Bash")
		f.apply(4, evTurnComplete, nil)
		if !f.apply(3, evToolEnd, func(r *Record) { r.ID, r.Tool = "a", "Bash" }) {
			t.Fatal("late tool.end rejected")
		}
		f.wantState(hook.StateWaiting)
		if s := f.session(); s.Reordered != 1 || len(s.InFlight) != 0 {
			t.Fatalf("session = %+v", s)
		}
	})
	t.Run("higher seq", func(t *testing.T) {
		f := newFeeder(t)
		f.apply(1, evTurnStart, nil)
		f.toolStart(2, "a", "Bash")
		f.apply(3, evTurnComplete, nil)
		f.apply(4, evToolEnd, func(r *Record) { r.ID, r.Tool = "a", "Bash" })
		f.wantState(hook.StateWaiting)
		if s := f.session(); s.Reordered != 0 {
			t.Fatalf("session = %+v", s)
		}
	})
}

func TestEscOnDialogReadsWaiting(t *testing.T) {
	f := newFeeder(t)
	f.apply(1, evTurnStart, nil)
	f.toolStart(2, "a", "Bash")
	f.check(3, "a", "Bash", "")
	f.permission(4, "Bash", "", nil)
	f.wantState(hook.StatePermission)
	f.apply(5, evTurnComplete, func(r *Record) { r.Reason = "answer" })
	s := f.session()
	if s.State != hook.StateWaiting || s.Tool != "" || s.Message != "" || len(s.InFlight) != 0 {
		t.Fatalf("session = %+v", s)
	}
}

func TestEscOnRunningToolReadsWaiting(t *testing.T) {
	f := newFeeder(t)
	f.apply(1, evTurnStart, nil)
	f.toolStart(2, "a", "Bash")
	f.wantState(hook.StateToolRunning)
	f.apply(3, evTurnComplete, func(r *Record) { r.Reason = "aborted" })
	f.wantState(hook.StateWaiting)
}

func TestPermissionBlocksAndToolEndClears(t *testing.T) {
	in := &ToolInput{Description: "Remove the build dir", Command: "rm -rf build"}
	f := newFeeder(t)
	f.apply(1, evTurnStart, nil)
	f.apply(2, evToolStart, func(r *Record) { r.ID, r.Tool, r.Input = "a", "Bash", in })
	f.check(3, "a", "Bash", "")
	f.permission(4, "Bash", "", in)
	s := f.session()
	if s.State != hook.StatePermission || s.Tool != "Bash" || s.Hint != "Remove the build dir" ||
		s.Message != "Claude needs your permission to use Bash" || s.Since != ts0/1000+4 {
		t.Fatalf("after permission: %+v", s)
	}
	f.apply(5, evNotification, func(r *Record) { r.Type, r.Message = "permission_prompt", "Claude wants to run rm" })
	if s := f.session(); s.Message != "Claude wants to run rm" || s.Since != ts0/1000+4 {
		t.Fatalf("after notification: %+v", s)
	}
	f.apply(6, evToolEnd, func(r *Record) { r.ID, r.Tool = "a", "Bash" })
	s = f.session()
	if s.State != hook.StateThinking || s.Tool != "" || s.Hint != "" || s.Message != "" || s.Since != ts0/1000+6 {
		t.Fatalf("after tool.end: %+v", s)
	}
}

func TestPermissionBlocksAndTurnCompleteClears(t *testing.T) {
	f := newFeeder(t)
	f.apply(1, evTurnStart, nil)
	f.toolStart(2, "a", "Edit")
	f.permission(3, "Edit", "", &ToolInput{FilePath: "/r/main.go"})
	if s := f.session(); s.State != hook.StatePermission || s.Hint != "main.go" {
		t.Fatalf("after permission: %+v", s)
	}
	f.apply(4, evTurnComplete, nil)
	if s := f.session(); s.State != hook.StateWaiting || s.Message != "" {
		t.Fatalf("after turn.complete: %+v", s)
	}
}

func TestPermissionWithoutCandidateClearsOnSameTool(t *testing.T) {
	f := newFeeder(t)
	f.apply(1, evTurnStart, nil)
	f.toolStart(2, "a", "Read")
	f.toolStart(3, "b", "Bash")
	f.permission(4, "Bash", "", nil)
	f.apply(5, evToolEnd, func(r *Record) { r.ID, r.Tool = "a", "Read" })
	f.wantState(hook.StatePermission)
	f.apply(6, evToolEnd, func(r *Record) { r.ID, r.Tool = "b", "Bash" })
	f.wantState(hook.StateThinking)
}

func TestSubagentPermissionClearsOnItsToolEnd(t *testing.T) {
	f := newFeeder(t)
	f.apply(1, evTurnStart, nil)
	f.toolStart(2, "task", "Agent")
	f.apply(3, evToolStart, func(r *Record) { r.ID, r.Tool, r.Agent = "x", "Bash", "sub1" })
	f.check(4, "x", "Bash", "sub1")
	f.permission(5, "Bash", "sub1", &ToolInput{Command: "make test"})
	s := f.session()
	if s.State != hook.StatePermission || s.Tool != "Bash" || s.Hint != "make test" || s.MainUpdated != ts0+2000 {
		t.Fatalf("after subagent permission: %+v", s)
	}
	f.apply(6, evToolStart, func(r *Record) { r.ID, r.Tool, r.Agent = "y", "Bash", "sub1" })
	f.apply(7, evToolEnd, func(r *Record) { r.ID, r.Tool, r.Agent = "y", "Bash", "sub1" })
	f.wantState(hook.StatePermission)
	f.apply(8, evToolEnd, func(r *Record) { r.ID, r.Tool, r.Agent = "x", "Bash", "sub1" })
	s = f.session()
	if s.State != hook.StateToolRunning || s.Tool != "Agent" || !slices.Equal(s.InFlight, []string{"task"}) {
		t.Fatalf("after subagent tool.end: %+v", s)
	}
}

func TestIdlePromptKeepsDialog(t *testing.T) {
	notify := func(typ string) func(*Record) { return func(r *Record) { r.Type = typ } }
	f := newFeeder(t)
	f.apply(1, evTurnStart, nil)
	f.toolStart(2, "a", "Bash")
	f.permission(3, "Bash", "", nil)
	f.apply(4, evNotification, notify("idle_prompt"))
	f.wantState(hook.StatePermission)
	f.apply(5, evNotification, notify("auth_success"))
	f.wantState(hook.StatePermission)
}

func TestNotificationAloneRaisesDialog(t *testing.T) {
	f := newFeeder(t)
	f.apply(1, evTurnStart, nil)
	f.toolStart(2, "a", "Bash")
	f.check(3, "a", "Bash", "")
	f.apply(4, evNotification, func(r *Record) { r.Type, r.Message = "elicitation_dialog", "Pick one" })
	s := f.session()
	if s.State != hook.StatePermission || s.Tool != "Bash" || s.Message != "Pick one" {
		t.Fatalf("after notification: %+v", s)
	}
	f.apply(5, evToolEnd, func(r *Record) { r.ID, r.Tool = "a", "Bash" })
	f.wantState(hook.StateThinking)
}

func TestElicitationWithoutCandidateClearsOnToolEnd(t *testing.T) {
	f := newFeeder(t)
	f.apply(1, evTurnStart, nil)
	f.toolStart(2, "m", "mcp__docs__search")
	f.apply(3, evToolCheck, func(r *Record) { r.ID, r.Tool, r.Decision = "m", "mcp__docs__search", "allow" })
	f.apply(4, evNotification, func(r *Record) { r.Type, r.Message = "elicitation_dialog", "Pick a source" })
	if s := f.session(); s.State != hook.StatePermission || s.Tool != "" || s.Message != "Pick a source" {
		t.Fatalf("after elicitation: %+v", s)
	}
	f.apply(5, evToolEnd, func(r *Record) { r.ID, r.Tool, r.Agent = "x", "Read", "sub1" })
	f.wantState(hook.StatePermission)
	f.apply(6, evToolEnd, func(r *Record) { r.ID, r.Tool = "m", "mcp__docs__search" })
	f.wantState(hook.StateThinking)
}

func TestNewDialogDoesNotInheritMessage(t *testing.T) {
	f := newFeeder(t)
	f.apply(1, evTurnStart, nil)
	f.toolStart(2, "a", "Bash")
	f.check(3, "a", "Bash", "")
	f.permission(4, "Bash", "", nil)
	f.apply(5, evNotification, func(r *Record) { r.Type, r.Message = "permission_prompt", "A text" })
	if s := f.session(); s.Message != "A text" {
		t.Fatalf("dialog A: %+v", s)
	}
	f.permission(6, "Bash", "", nil)
	if s := f.session(); s.Message != "A text" {
		t.Fatalf("dialog A re-raised: %+v", s)
	}
	f.apply(7, evToolStart, func(r *Record) { r.ID, r.Tool, r.Agent = "x", "Edit", "sub1" })
	f.permission(8, "Edit", "sub1", nil)
	if s := f.session(); s.Tool != "Edit" || s.Message != "Claude needs your permission to use Edit" {
		t.Fatalf("dialog B: %+v", s)
	}
	f.apply(9, evNotification, func(r *Record) { r.Type, r.Message = "permission_prompt", "B text" })
	if s := f.session(); s.Message != "B text" {
		t.Fatalf("dialog B's notification: %+v", s)
	}
}

func TestCompactEndResumesPhase(t *testing.T) {
	t.Run("auto compaction mid-turn", func(t *testing.T) {
		f := newFeeder(t)
		f.apply(1, evTurnStart, nil)
		f.apply(2, evCompact, func(r *Record) { r.Trigger = "auto" })
		f.wantState(hook.StateCompacting)
		f.apply(3, evCompactEnd, nil)
		f.wantState(hook.StateThinking)
	})
	t.Run("manual compaction at idle", func(t *testing.T) {
		f := newFeeder(t)
		f.apply(1, evTurnStart, nil)
		f.apply(2, evTurnComplete, nil)
		f.apply(3, evCompact, func(r *Record) { r.Trigger = "manual" })
		f.wantState(hook.StateCompacting)
		f.apply(4, evCompactEnd, nil)
		f.wantState(hook.StateWaiting)
	})
	t.Run("compaction before the first prompt", func(t *testing.T) {
		f := newFeeder(t)
		f.apply(1, evSessionStart, nil)
		f.apply(2, evCompact, nil)
		f.apply(3, evCompactEnd, nil)
		f.wantState(hook.StateWaiting)
	})
	t.Run("late compact.end", func(t *testing.T) {
		f := newFeeder(t)
		f.apply(1, evTurnStart, nil)
		f.apply(2, evCompact, nil)
		f.apply(3, evTurnComplete, nil)
		f.apply(4, evCompactEnd, nil)
		f.wantState(hook.StateWaiting)
	})
	t.Run("subagent compact.end", func(t *testing.T) {
		f := newFeeder(t)
		f.apply(1, evTurnStart, nil)
		f.apply(2, evCompact, nil)
		f.apply(3, evCompactEnd, func(r *Record) { r.Agent = "sub1" })
		f.wantState(hook.StateCompacting)
	})
	t.Run("compact.end alone is not known", func(t *testing.T) {
		f := newFeeder(t)
		f.apply(1, evCompactEnd, nil)
		if s := f.session(); s.Known {
			t.Fatalf("session = %+v", s)
		}
	})
}

func TestHeartbeatRefreshesUpdatedOnly(t *testing.T) {
	clock := time.Unix(1_000_000, 0)
	f := newFeeder(t)
	f.store.now = func() time.Time { return clock }
	f.apply(1, evHeartbeat, nil)
	if s := f.session(); s.Known || s.State != "" || s.Applied != 1 || s.Updated != ts0+1000 || s.MainUpdated != 0 {
		t.Fatalf("after first heartbeat: %+v", s)
	}
	f.apply(2, evTurnComplete, nil)
	before := f.session()
	f.store.TakeDirty()
	<-f.store.Notify()

	clock = clock.Add(time.Minute)
	if !f.apply(3, evHeartbeat, nil) {
		t.Fatal("heartbeat rejected")
	}
	s := f.session()
	if s.State != hook.StateWaiting || s.Since != before.Since || !s.Known || s.Turns != before.Turns ||
		s.Updated != ts0+3000 || s.MainUpdated != before.MainUpdated || s.Applied != 3 {
		t.Fatalf("after heartbeat: %+v, before: %+v", s, before)
	}
	select {
	case <-f.store.Notify():
	default:
		t.Fatal("no signal after a heartbeat")
	}
	if got := f.store.TakeDirty(); !slices.Equal(got, []string{"s1"}) {
		t.Fatalf("dirty = %v", got)
	}
	f.store.mu.Lock()
	defer f.store.mu.Unlock()
	if ss := f.store.sessions["s1"]; ss == nil || !ss.lastApply.Equal(clock) {
		t.Fatalf("session = %+v, want lastApply %v", ss, clock)
	}
}

func TestToolRunningShowsNewestMainCall(t *testing.T) {
	f := newFeeder(t)
	f.apply(1, evTurnStart, nil)
	f.apply(2, evToolStart, func(r *Record) { r.ID, r.Tool, r.Input = "a", "Bash", &ToolInput{Command: "ls"} })
	f.apply(3, evToolStart, func(r *Record) { r.ID, r.Tool, r.Agent = "x", "Read", "sub1" })
	if s := f.session(); s.Tool != "Bash" || s.Hint != "ls" {
		t.Fatalf("session = %+v", s)
	}
}

func TestBackgroundFromMod(t *testing.T) {
	bgStart := func(f *feeder, seq int64, id, tool string, in *ToolInput) {
		f.apply(seq, evToolStart, func(r *Record) { r.ID, r.Tool, r.BG, r.Input = id, tool, tool == "Bash", in })
	}
	bgEnd := func(f *feeder, seq int64, id, tool, task string, mut func(*Record)) {
		f.apply(seq, evToolEnd, func(r *Record) {
			r.ID, r.Tool, r.Task = id, tool, task
			if mut != nil {
				mut(r)
			}
		})
	}
	ids := func(ts []BGTask) []string {
		var out []string
		for _, task := range ts {
			out = append(out, task.ID)
		}
		return out
	}

	t.Run("shell start and completion", func(t *testing.T) {
		f := newFeeder(t)
		f.apply(1, evTurnStart, nil)
		bgStart(f, 2, "a", "Bash", &ToolInput{Description: "  Run the dev server ", Command: "just dev"})
		bgEnd(f, 3, "a", "Bash", "bg1", nil)
		s := f.session()
		want := []BGTask{{ID: "bg1", Kind: "shell", Hint: "Run the dev server", Since: ts0/1000 + 2}}
		if !slices.Equal(s.Background, want) || s.BGKnown {
			t.Fatalf("background = %+v, known = %v", s.Background, s.BGKnown)
		}
		f.apply(4, evTaskEnd, func(r *Record) { r.Tasks = []Task{{ID: "bg1", Status: "completed"}} })
		if s := f.session(); len(s.Background) != 0 {
			t.Fatalf("after task.end: %+v", s.Background)
		}
	})

	t.Run("task.end by tool_use", func(t *testing.T) {
		f := newFeeder(t)
		bgStart(f, 1, "a", "Bash", &ToolInput{Command: "sleep 99"})
		bgEnd(f, 2, "a", "Bash", "bg1", nil)
		f.apply(3, evTaskEnd, func(r *Record) { r.Tasks = []Task{{ToolUse: "a", Status: "completed"}} })
		if s := f.session(); len(s.Background) != 0 {
			t.Fatalf("after task.end: %+v", s.Background)
		}
	})

	t.Run("failed launch and command hint", func(t *testing.T) {
		long := strings.Repeat("é", 130)
		f := newFeeder(t)
		bgStart(f, 1, "a", "Bash", &ToolInput{Command: "x"})
		bgEnd(f, 2, "a", "Bash", "bg1", func(r *Record) { r.Err = true })
		bgStart(f, 3, "b", "Bash", &ToolInput{Command: long})
		bgEnd(f, 4, "b", "Bash", "bg2", nil)
		s := f.session()
		if len(s.Background) != 1 || s.Background[0].ID != "bg2" ||
			s.Background[0].Hint != strings.Repeat("é", 119)+"…" {
			t.Fatalf("background = %+v", s.Background)
		}
	})

	t.Run("TaskStop drops its target", func(t *testing.T) {
		f := newFeeder(t)
		bgStart(f, 1, "a", "Bash", &ToolInput{Command: "tail -f log"})
		bgEnd(f, 2, "a", "Bash", "bg1", nil)
		f.toolStart(3, "k", "TaskStop")
		bgEnd(f, 4, "k", "TaskStop", "", func(r *Record) { r.StopTask = "bg1"; r.Err = true })
		if s := f.session(); len(s.Background) != 1 {
			t.Fatalf("failed TaskStop dropped the task: %+v", s.Background)
		}
		f.toolStart(5, "k2", "TaskStop")
		bgEnd(f, 6, "k2", "TaskStop", "", func(r *Record) { r.StopTask = "bg1" })
		if s := f.session(); len(s.Background) != 0 {
			t.Fatalf("after TaskStop: %+v", s.Background)
		}
	})

	t.Run("monitor expiry", func(t *testing.T) {
		f := newFeeder(t)
		bgStart(f, 1, "m", "Monitor", &ToolInput{Description: "Watch CI"})
		bgEnd(f, 2, "m", "Monitor", "mon1", func(r *Record) { r.TimeoutMS = 60_000 })
		bgStart(f, 3, "p", "Monitor", &ToolInput{Description: "Watch forever"})
		bgEnd(f, 4, "p", "Monitor", "mon2", nil)
		s := f.session()
		end := int64(ts0/1000 + 2 + 60)
		if len(s.Background) != 2 || s.Background[0].Kind != "monitor" || s.Background[0].Expires != end ||
			s.Background[1].Expires != 0 {
			t.Fatalf("background = %+v", s.Background)
		}
		if got := ids(s.BackgroundAt(time.Unix(end-1, 0))); !slices.Equal(got, []string{"mon1", "mon2"}) {
			t.Fatalf("before expiry: %v", got)
		}
		if got := ids(s.BackgroundAt(time.Unix(end, 0))); !slices.Equal(got, []string{"mon2"}) {
			t.Fatalf("at expiry: %v", got)
		}
	})

	t.Run("stop snapshot", func(t *testing.T) {
		f := newFeeder(t)
		f.apply(1, evTurnStart, nil)
		bgStart(f, 2, "a", "Bash", &ToolInput{Command: "killed in the UI"})
		bgEnd(f, 3, "a", "Bash", "bg1", nil)
		bgStart(f, 4, "m", "Monitor", &ToolInput{Description: "Watch CI"})
		bgEnd(f, 5, "m", "Monitor", "mon1", func(r *Record) { r.TimeoutMS = 60_000 })
		f.apply(6, evStop, nil)
		if s := f.session(); len(s.Background) != 2 || s.BGKnown || !s.StopSeen {
			t.Fatalf("after stop without tasks: %+v", s)
		}
		f.apply(7, evStop, func(r *Record) {
			r.Tasks = []Task{
				{ID: "mon1", Type: "shell", Status: "running"},
				{ID: "new", Type: "shell", Status: "running", Description: "Found running"},
			}
		})
		s := f.session()
		want := []BGTask{
			{ID: "mon1", Kind: "monitor", Hint: "Watch CI", Since: ts0/1000 + 4, Expires: ts0/1000 + 5 + 60},
			{ID: "new", Kind: "shell", Hint: "Found running", Since: ts0/1000 + 7},
		}
		if !slices.Equal(s.Background, want) || !s.BGKnown {
			t.Fatalf("after snapshot: %+v, known = %v", s.Background, s.BGKnown)
		}
	})

	t.Run("subagent records leave background alone", func(t *testing.T) {
		f := newFeeder(t)
		f.apply(1, evToolStart, func(r *Record) { r.ID, r.Tool, r.BG, r.Agent = "a", "Bash", true, "sub1" })
		f.apply(2, evToolEnd, func(r *Record) { r.ID, r.Tool, r.Task, r.Agent = "a", "Bash", "bg1", "sub1" })
		f.apply(3, evStop, func(r *Record) { r.Agent, r.Tasks = "sub1", []Task{} })
		if s := f.session(); len(s.Background) != 0 || s.BGKnown || s.StopSeen {
			t.Fatalf("session = %+v", s)
		}
	})
}

func TestKnownOnlyAfterStateRecord(t *testing.T) {
	f := newFeeder(t)
	f.toolEnd(1, "unknown")
	f.apply(2, evNotification, func(r *Record) { r.Type = "idle_prompt" })
	f.apply(3, evStop, nil)
	f.apply(4, evTurnStart, func(r *Record) { r.Agent = "sub1" })
	if s := f.session(); s.Known {
		t.Fatalf("known after non-state records: %+v", s)
	}
	f.apply(5, evTurnComplete, nil)
	if s := f.session(); !s.Known || s.State != hook.StateWaiting {
		t.Fatalf("session = %+v", s)
	}
}

func TestTurnsAndStopTail(t *testing.T) {
	f := newFeeder(t)
	f.apply(1, evTurnStart, nil)
	f.apply(2, evStop, func(r *Record) { r.Last = "Shall I go on?" })
	f.apply(3, evTurnComplete, nil)
	if s := f.session(); s.Turns != 1 || !s.StopSeen || s.Last != "Shall I go on?" {
		t.Fatalf("after stop: %+v", s)
	}
	f.apply(4, evTurnStart, nil)
	if s := f.session(); s.Turns != 2 || s.StopSeen || s.Last != "" {
		t.Fatalf("after turn.start: %+v", s)
	}
}

func TestCoordsLastNonEmptyWins(t *testing.T) {
	f := newFeeder(t)
	f.apply(1, evSessionStart, func(r *Record) { r.CWD, r.Pane, r.TmuxServer = "/r", "%1", "100" })
	f.apply(2, evNotification, func(r *Record) { r.Transcript, r.Pane = "/t.jsonl", "%2" })
	f.apply(3, evTurnStart, nil)
	s := f.session()
	if s.CWD != "/r" || s.Transcript != "/t.jsonl" || s.Pane != "%2" || s.TmuxServer != "100" {
		t.Fatalf("session = %+v", s)
	}
}
