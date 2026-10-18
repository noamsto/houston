package claudemod

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/noamsto/houston/hook"
)

// feeder applies records for one session and boot with explicit seqs.
type feeder struct {
	t     *testing.T
	store *Store
	sid   string
	boot  string
}

// ts0 is the feeder's ms clock origin; record seq lands at ts0 + seq seconds.
const ts0 = 1_700_000_000_000

func newFeeder(t *testing.T) *feeder {
	return &feeder{t: t, store: NewStore(), sid: "s1", boot: "b1"}
}

func (f *feeder) apply(seq int64, ev string, mut func(*Record)) bool {
	f.t.Helper()
	rec := Record{V: 1, SID: f.sid, Boot: f.boot, Seq: seq, TS: ts0 + seq*1000, Ev: ev}
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
	s, ok := f.store.Session(f.sid)
	if !ok {
		f.t.Fatalf("no session %q", f.sid)
	}
	return s
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
	if got := ids(); !slices.Equal(got, []string{"bg2", "bg1"}) {
		t.Fatalf("after tool.end: %v", got)
	}
	f.apply(6, evTaskEnd, func(r *Record) { r.Tasks = []Task{{ID: "bg1", Status: "completed"}} })
	if got := ids(); !slices.Equal(got, []string{"bg2"}) {
		t.Fatalf("after task.end: %v", got)
	}
	f.apply(7, evStop, func(r *Record) {
		r.Tasks = []Task{{ID: "n1", Type: "shell", Status: "running"}, {ID: "n2", Type: "monitor", Status: "pending"},
			{ID: "n3", Type: "shell", Status: "completed"}, {ID: "n4", Type: "agent", Status: "running"}}
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

func TestDelayedOlderBootDoesNotRetireLiveBoot(t *testing.T) {
	f := newFeeder(t)
	f.boot = "B"
	f.apply(1, evTurnStart, func(r *Record) { r.TS = 2000 })
	f.boot = "A"
	if f.apply(5, evTurnComplete, func(r *Record) { r.TS = 1000 }) {
		t.Fatal("record of an older boot applied")
	}
	f.boot = "B"
	if !f.apply(2, evToolStart, func(r *Record) { r.TS = 2010; r.ID, r.Tool = "x", "Bash" }) {
		t.Fatal("live boot's next record dropped")
	}
	s := f.session()
	if s.Boot != "B" || s.State != hook.StateToolRunning || s.Stale != 1 {
		t.Fatalf("session = %+v", s)
	}
}

func TestReloadSwitchesToLaterBoot(t *testing.T) {
	f := newFeeder(t)
	f.apply(1, evTurnStart, func(r *Record) { r.TS = 1000 })
	f.boot = "b2"
	if !f.apply(1, evSessionStart, func(r *Record) { r.TS = 2000 }) {
		t.Fatal("later boot rejected")
	}
	if s := f.session(); s.Boot != "b2" || s.State != hook.StateStarting {
		t.Fatalf("session = %+v", s)
	}
}

func TestWindowDropsUnusedPayload(t *testing.T) {
	f := newFeeder(t)
	f.apply(1, evNotification, func(r *Record) { r.Message = "kept"; r.Ask = []Ask{{Question: "q"}} })
	f.apply(2, evToolStart, func(r *Record) { r.Message = "big"; r.Ask = []Ask{{Question: "q"}} })
	f.store.mu.Lock()
	defer f.store.mu.Unlock()
	ss := f.store.sessions["s1"]
	if ss == nil || len(ss.window) != 2 || ss.window[0].Message != "kept" || ss.window[0].Ask != nil ||
		ss.window[1].Message != "" || ss.window[1].Ask != nil {
		t.Fatalf("session = %+v", ss)
	}
}

func TestSessionEndFoldsWindow(t *testing.T) {
	f := newFeeder(t)
	f.apply(1, evTurnStart, nil)
	f.apply(2, evSessionEnd, nil)
	f.store.mu.Lock()
	ss := f.store.sessions["s1"]
	if ss == nil || len(ss.window) != 0 || ss.folded != 2 {
		t.Fatalf("session = %+v", ss)
	}
	f.store.mu.Unlock()
	if f.apply(1, evToolCheck, nil) {
		t.Fatal("record older than the end applied")
	}
	f.wantState(hook.StateEnded)
}

func TestEvictsEndedAndIdleSessions(t *testing.T) {
	clock := time.Unix(1_000_000, 0)
	st := NewStore()
	st.now = func() time.Time { return clock }
	apply := func(sid string, seq int64, ev string) {
		st.Apply(Record{V: 1, SID: sid, Boot: "b", Seq: seq, Ev: ev})
	}
	sids := func() []string {
		var out []string
		for _, s := range st.Snapshot() {
			out = append(out, s.SID)
		}
		return out
	}
	apply("ended", 1, evSessionEnd)
	apply("idle", 1, evTurnStart)

	clock = clock.Add(min(endedTTL, idleTTL) - time.Minute)
	apply("live", 1, evTurnStart)
	if got := sids(); !slices.Equal(got, []string{"ended", "idle", "live"}) {
		t.Fatalf("before the TTLs: %v", got)
	}

	clock = clock.Add(2 * time.Minute)
	apply("live", 2, evToolCheck)
	if got := sids(); !slices.Equal(got, []string{"live"}) {
		t.Fatalf("after the TTLs: %v", got)
	}
}

func TestSweepRunsOncePerMinute(t *testing.T) {
	clock := time.Unix(1_000_000, 0)
	st := NewStore()
	st.now = func() time.Time { return clock }
	st.Apply(Record{V: 1, SID: "e", Boot: "b", Seq: 1, Ev: evSessionEnd})
	clock = clock.Add(endedTTL + time.Second)
	st.Apply(Record{V: 1, SID: "x", Boot: "b", Seq: 1, Ev: evTurnStart}) // sweeps: e is old
	if n := len(st.Snapshot()); n != 1 {
		t.Fatalf("sessions = %d, want 1", n)
	}
	st.Apply(Record{V: 1, SID: "e", Boot: "b", Seq: 1, Ev: evSessionEnd})
	clock = clock.Add(endedTTL + time.Second - sweepEvery/2)
	st.Apply(Record{V: 1, SID: "x", Boot: "b", Seq: 2, Ev: evToolCheck})
	clock = clock.Add(sweepEvery / 4) // under a minute since the last sweep
	st.Apply(Record{V: 1, SID: "x", Boot: "b", Seq: 3, Ev: evToolCheck})
	if n := len(st.Snapshot()); n != 2 {
		t.Fatalf("sessions = %d, want 2 (sweep throttled)", n)
	}
}

func TestClockStepBackAcrossReloadKeepsNewBoot(t *testing.T) {
	clock := time.Unix(1_000_000, 0)
	f := newFeeder(t)
	f.store.now = func() time.Time { return clock }
	f.boot = "A"
	f.apply(1, evTurnStart, func(r *Record) { r.TS = 1000 })
	clock = clock.Add(6 * time.Second)
	f.boot = "B"
	if !f.apply(1, evSessionStart, func(r *Record) { r.TS = 900 }) {
		t.Fatal("reloaded boot with an earlier clock rejected")
	}
	if !f.apply(2, evTurnStart, func(r *Record) { r.TS = 910 }) {
		t.Fatal("new boot's next record rejected")
	}
	s := f.session()
	if s.Boot != "B" || s.State != hook.StateThinking || s.Stale != 0 || s.Turns != 2 {
		t.Fatalf("session = %+v", s)
	}
	f.boot = "A"
	if f.apply(2, evTurnComplete, func(r *Record) { r.TS = 1010 }) {
		t.Fatal("late record of the replaced boot applied")
	}
}

func TestUnseenOlderBootWithinSlackIsStale(t *testing.T) {
	clock := time.Unix(1_000_000, 0)
	f := newFeeder(t)
	f.store.now = func() time.Time { return clock }
	f.boot = "A"
	f.apply(1, evTurnStart, func(r *Record) { r.TS = 1000 })
	clock = clock.Add(6 * time.Second)
	f.boot = "B"
	f.apply(1, evSessionStart, func(r *Record) { r.TS = 900 })
	clock = clock.Add(bootDelaySlack - time.Second)
	f.boot = "C"
	if f.apply(1, evTurnStart, func(r *Record) { r.TS = 800 }) {
		t.Fatal("delayed record of an older boot applied")
	}
	s := f.session()
	if s.Boot != "B" || s.State != hook.StateStarting || s.Stale != 1 {
		t.Fatalf("session = %+v", s)
	}
}

func TestStaleRecordsDoNotRefreshEviction(t *testing.T) {
	clock := time.Unix(1_000_000, 0)
	st := NewStore()
	st.now = func() time.Time { return clock }
	rec := Record{V: 1, SID: "s", Boot: "b", Seq: 1, TS: 1000, Ev: evTurnStart}
	st.Apply(rec)
	clock = clock.Add(idleTTL - time.Minute)
	if st.Apply(rec) {
		t.Fatal("duplicate applied")
	}
	clock = clock.Add(2 * time.Minute)
	st.Sweep()
	if _, ok := st.Session("s"); ok {
		t.Fatal("stale records kept the session alive")
	}
}

func TestNotifyAfterApply(t *testing.T) {
	f := newFeeder(t)
	f.apply(1, evTurnStart, nil)
	f.sid = "s2"
	f.apply(1, evTurnStart, nil)
	select {
	case <-f.store.Notify():
	default:
		t.Fatal("no signal after Apply")
	}
	if got := f.store.TakeDirty(); !slices.Equal(got, []string{"s1", "s2"}) {
		t.Fatalf("dirty = %v", got)
	}
	if got := f.store.TakeDirty(); len(got) != 0 {
		t.Fatalf("dirty again = %v", got)
	}
	if f.apply(1, evTurnComplete, nil) {
		t.Fatal("duplicate applied")
	}
	select {
	case <-f.store.Notify():
		t.Fatal("signal after a stale record")
	default:
	}
	if got := f.store.TakeDirty(); len(got) != 0 {
		t.Fatalf("dirty after a stale record = %v", got)
	}
}

func TestSweepEvictsAndMarksDirty(t *testing.T) {
	clock := time.Unix(1_000_000, 0)
	st := NewStore()
	st.now = func() time.Time { return clock }
	st.Apply(Record{V: 1, SID: "old", Boot: "b", Seq: 1, TS: 1000, Ev: evSessionEnd})
	st.TakeDirty()
	<-st.Notify()

	clock = clock.Add(endedTTL + time.Second)
	st.Sweep()
	select {
	case <-st.Notify():
	default:
		t.Fatal("no signal after eviction")
	}
	if got := st.TakeDirty(); !slices.Equal(got, []string{"old"}) {
		t.Fatalf("dirty = %v", got)
	}
	if _, ok := st.Session("old"); ok {
		t.Fatal("ended session not evicted")
	}

	st.Sweep()
	select {
	case <-st.Notify():
		t.Fatal("signal from a sweep that evicted nothing")
	default:
	}
}

func TestSessionCap(t *testing.T) {
	clock := time.Unix(1_000_000, 0)
	st := NewStore()
	st.now = func() time.Time { return clock }
	for i := range maxSessions {
		clock = clock.Add(time.Millisecond)
		st.Apply(Record{V: 1, SID: fmt.Sprintf("s%04d", i), Boot: "b", Seq: 1, TS: 1000, Ev: evTurnStart})
	}
	clock = clock.Add(time.Millisecond)
	st.Apply(Record{V: 1, SID: "s0000", Boot: "b", Seq: 2, TS: 2000, Ev: evTurnComplete})
	st.TakeDirty()

	clock = clock.Add(time.Millisecond)
	st.Apply(Record{V: 1, SID: "new", Boot: "b", Seq: 1, TS: 3000, Ev: evTurnStart})
	if n := len(st.Snapshot()); n != maxSessions {
		t.Fatalf("sessions = %d, want %d", n, maxSessions)
	}
	if _, ok := st.Session("s0001"); ok {
		t.Fatal("least recently applied session kept")
	}
	if _, ok := st.Session("s0000"); !ok {
		t.Fatal("recently applied session evicted")
	}
	if got := st.TakeDirty(); !slices.Equal(got, []string{"new", "s0001"}) {
		t.Fatalf("dirty = %v", got)
	}
}
