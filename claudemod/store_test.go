package claudemod

import (
	"slices"
	"testing"

	"github.com/noamsto/houston/hook"
)

// feeder applies records for one session and boot with explicit seqs.
type feeder struct {
	t     *testing.T
	store *Store
	sid   string
	boot  string
}

func newFeeder(t *testing.T) *feeder {
	return &feeder{t: t, store: NewStore(), sid: "s1", boot: "b1"}
}

func (f *feeder) apply(seq int64, ev string, mut func(*Record)) bool {
	f.t.Helper()
	rec := Record{V: 1, SID: f.sid, Boot: f.boot, Seq: seq, TS: seq * 10, Ev: ev}
	if mut != nil {
		mut(&rec)
	}
	return f.store.Apply(rec)
}

func (f *feeder) toolStart(seq int64, id, tool string) {
	f.t.Helper()
	f.apply(seq, evToolStart, func(r *Record) { r.ID, r.Tool = id, tool })
}

func (f *feeder) toolEnd(seq int64, id string) {
	f.t.Helper()
	f.apply(seq, evToolEnd, func(r *Record) { r.ID = id })
}

func (f *feeder) session() Session {
	f.t.Helper()
	for _, s := range f.store.Snapshot() {
		if s.SID == f.sid {
			return s
		}
	}
	f.t.Fatalf("no session %q", f.sid)
	return Session{}
}

func (f *feeder) wantState(want hook.State) {
	f.t.Helper()
	if got := f.session().State; got != want {
		f.t.Fatalf("state = %q, want %q", got, want)
	}
}

func TestSessionsKeyedBySID(t *testing.T) {
	st := NewStore()
	st.Apply(Record{V: 1, SID: "b", Boot: "x", Seq: 1, Ev: evTurnStart})
	st.Apply(Record{V: 1, SID: "a", Boot: "x", Seq: 1, Ev: evSessionStart})
	snap := st.Snapshot()
	if len(snap) != 2 || snap[0].SID != "a" || snap[1].SID != "b" {
		t.Fatalf("snapshot = %+v", snap)
	}
	if snap[0].State != hook.StateStarting || snap[1].State != hook.StateThinking {
		t.Fatalf("states = %q, %q", snap[0].State, snap[1].State)
	}
}

func TestDuplicateSeqIsStale(t *testing.T) {
	f := newFeeder(t)
	f.apply(1, evTurnStart, nil)
	if f.apply(1, evTurnComplete, nil) {
		t.Fatal("duplicate seq applied")
	}
	s := f.session()
	if s.Stale != 1 || s.Applied != 1 || s.State != hook.StateThinking {
		t.Fatalf("session = %+v", s)
	}
}

func TestSeqOlderThanWindowIsStale(t *testing.T) {
	f := newFeeder(t)
	f.apply(1, evTurnStart, nil)
	for seq := int64(2); seq <= windowCap+3; seq++ {
		f.apply(seq, evToolCheck, nil)
	}
	if f.apply(2, evTurnComplete, nil) {
		t.Fatal("record older than the window applied")
	}
	s := f.session()
	if s.Stale != 1 || s.State != hook.StateThinking || s.LastSeq != windowCap+3 {
		t.Fatalf("session = %+v", s)
	}
}

func TestNewBootResets(t *testing.T) {
	f := newFeeder(t)
	f.apply(1, evTurnStart, nil)
	f.apply(2, evTurnComplete, nil)
	f.boot = "b2"
	if !f.apply(1, evTurnStart, nil) {
		t.Fatal("seq 1 of a new boot rejected")
	}
	s := f.session()
	if s.Boot != "b2" || s.LastSeq != 1 || s.State != hook.StateThinking || s.Applied != 3 {
		t.Fatalf("session = %+v", s)
	}
}

func TestRetiredBootIsStale(t *testing.T) {
	f := newFeeder(t)
	f.apply(1, evTurnStart, nil)
	f.boot = "b2"
	f.apply(1, evSessionStart, nil)
	f.boot = "b1"
	if f.apply(2, evTurnComplete, nil) {
		t.Fatal("record of a retired boot applied")
	}
	s := f.session()
	if s.Boot != "b2" || s.State != hook.StateStarting || s.Stale != 1 {
		t.Fatalf("session = %+v", s)
	}
}

func TestSubagentRecordChangesNothing(t *testing.T) {
	f := newFeeder(t)
	f.apply(1, evTurnStart, nil)
	f.apply(2, evToolStart, func(r *Record) { r.ID, r.Tool, r.Agent = "x", "Bash", "sub1" })
	f.apply(3, evTurnComplete, func(r *Record) { r.Agent = "sub1" })
	s := f.session()
	if s.State != hook.StateThinking || len(s.InFlight) != 0 || s.Applied != 3 {
		t.Fatalf("session = %+v", s)
	}
}

func TestParallelTools(t *testing.T) {
	f := newFeeder(t)
	f.apply(1, evTurnStart, nil)
	f.toolStart(2, "A", "Read")
	f.toolStart(3, "B", "Grep")
	f.toolEnd(4, "A")
	f.wantState(hook.StateToolRunning)
	if s := f.session(); !slices.Equal(s.InFlight, []string{"B"}) || s.Tool != "Grep" {
		t.Fatalf("session = %+v", s)
	}
	f.toolEnd(5, "B")
	f.wantState(hook.StateThinking)
	if s := f.session(); len(s.InFlight) != 0 || s.Tool != "" {
		t.Fatalf("session = %+v", s)
	}
}

func TestToolEndOfUnknownIDKeepsState(t *testing.T) {
	f := newFeeder(t)
	f.apply(1, evTurnComplete, nil)
	f.toolEnd(2, "gone")
	f.wantState(hook.StateWaiting)
}

func TestBackgroundTasks(t *testing.T) {
	f := newFeeder(t)
	f.apply(1, evTurnStart, nil)
	f.toolStart(2, "A", "Bash")
	f.apply(3, evToolEnd, func(r *Record) { r.ID, r.Tool, r.Task = "A", "Bash", "bg2" })
	f.toolStart(4, "B", "Monitor")
	f.apply(5, evToolEnd, func(r *Record) { r.ID, r.Tool, r.Task = "B", "Monitor", "bg1" })
	ids := func() []string {
		var out []string
		for _, task := range f.session().Background {
			out = append(out, task.ID)
		}
		return out
	}
	if got := ids(); !slices.Equal(got, []string{"bg1", "bg2"}) {
		t.Fatalf("after tool.end: %v", got)
	}
	f.apply(6, evTaskEnd, func(r *Record) { r.Tasks = []Task{{ID: "bg1", Status: "completed"}} })
	if got := ids(); !slices.Equal(got, []string{"bg2"}) {
		t.Fatalf("after task.end: %v", got)
	}
	f.apply(7, evStop, func(r *Record) {
		r.Tasks = []Task{{ID: "n1", Type: "shell", Status: "running"}, {ID: "n2", Status: "pending"}, {ID: "n3", Status: "completed"}}
	})
	if got := ids(); !slices.Equal(got, []string{"n1", "n2"}) {
		t.Fatalf("after stop: %v", got)
	}
	f.apply(8, evStop, func(r *Record) { r.Tasks = []Task{} })
	if got := ids(); len(got) != 0 {
		t.Fatalf("after empty stop: %v", got)
	}
}

func TestStopWithoutTasksKeepsBackground(t *testing.T) {
	f := newFeeder(t)
	f.apply(1, evTurnStart, nil)
	f.toolStart(2, "A", "Bash")
	f.apply(3, evToolEnd, func(r *Record) { r.ID, r.Tool, r.Task = "A", "Bash", "t1" })
	f.apply(4, evStop, nil)
	if s := f.session(); len(s.Background) != 1 || s.Background[0].ID != "t1" {
		t.Fatalf("after stop without tasks: %+v", s.Background)
	}
	f.apply(5, evStop, func(r *Record) { r.Tasks = []Task{} })
	if s := f.session(); len(s.Background) != 0 {
		t.Fatalf("after empty stop: %+v", s.Background)
	}
}

func TestPermissionAndNotificationStates(t *testing.T) {
	notify := func(typ string) func(*Record) { return func(r *Record) { r.Type = typ } }
	f := newFeeder(t)
	f.apply(1, evTurnStart, nil)
	f.apply(2, evPermission, nil)
	f.wantState(hook.StatePermission)
	f.apply(3, evNotification, notify("idle_prompt"))
	f.wantState(hook.StateWaiting)
	f.apply(4, evNotification, notify("permission_prompt"))
	f.wantState(hook.StatePermission)
	f.apply(5, evNotification, notify("auth_success"))
	f.wantState(hook.StatePermission)
	f.apply(6, evNotification, notify("idle_prompt"))
	f.apply(7, evNotification, notify("elicitation_dialog"))
	f.wantState(hook.StatePermission)
	f.toolStart(8, "A", "Bash")
	f.toolEnd(9, "A")
	f.wantState(hook.StateThinking)
}

func TestTurnCompleteClearsInFlight(t *testing.T) {
	f := newFeeder(t)
	f.apply(1, evTurnStart, nil)
	f.toolStart(2, "A", "Bash")
	f.apply(3, evTurnComplete, nil)
	f.wantState(hook.StateWaiting)
	if s := f.session(); len(s.InFlight) != 0 {
		t.Fatalf("in flight = %v", s.InFlight)
	}
}

func TestCompactAndSessionEnd(t *testing.T) {
	f := newFeeder(t)
	f.apply(1, evTurnStart, nil)
	f.apply(2, evCompact, nil)
	f.wantState(hook.StateCompacting)
	f.apply(3, evSessionEnd, nil)
	f.wantState(hook.StateEnded)
}

func TestOutOfOrderMatchesInOrder(t *testing.T) {
	steps := func(f *feeder, order []int64) {
		for _, seq := range order {
			switch seq {
			case 1:
				f.apply(1, evTurnStart, nil)
			case 2:
				f.toolStart(2, "A", "Bash")
			case 3:
				f.toolEnd(3, "A")
			}
		}
	}
	inOrder := newFeeder(t)
	steps(inOrder, []int64{1, 2, 3})
	late := newFeeder(t)
	steps(late, []int64{1, 3, 2})

	want, got := inOrder.session(), late.session()
	if got.State != want.State || !slices.Equal(got.InFlight, want.InFlight) || got.State != hook.StateThinking {
		t.Fatalf("late = %+v, in order = %+v", got, want)
	}
	if got.Reordered != 1 || want.Reordered != 0 || got.Applied != 3 || got.LastSeq != 3 {
		t.Fatalf("counters: late = %+v, in order = %+v", got, want)
	}
}

func TestSnapshotIsACopy(t *testing.T) {
	f := newFeeder(t)
	f.toolStart(1, "A", "Bash")
	snap := f.store.Snapshot()
	snap[0].InFlight[0] = "mutated"
	if got := f.session().InFlight; !slices.Equal(got, []string{"A"}) {
		t.Fatalf("in flight = %v", got)
	}
}
