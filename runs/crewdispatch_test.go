package runs

import (
	"errors"
	"maps"
	"os"
	"testing"

	"github.com/noamsto/houston/crewview"
	"github.com/noamsto/houston/hub"
	"github.com/noamsto/houston/tmux"
)

// fakeProcs is a procProbe over a made-up process table: every pid is alive
// unless dead and has parents[pid] as its parent. err makes every parent walk
// fail.
type fakeProcs struct {
	dead    map[int]bool
	parents map[int]int
	err     error
}

func (f fakeProcs) Alive(pid int) bool { return !f.dead[pid] }
func (f fakeProcs) Descends(pid, root int) (bool, error) {
	if f.err != nil {
		return false, f.err
	}
	return descendsVia(func(p int) (int, error) { return f.parents[p], nil }, pid, root)
}

const (
	dispPanePID = 100 // the dispatcher pane's shell
	dispPID     = 200 // the dispatcher process, the shell's grandchild
)

// crewDisp is the dispatcher registration crew's overview reports for one
// crew's worker; a nil pid, pane or alive is one crew does not know.
type crewDisp struct {
	crew  string
	pane  *string
	pid   *int
	alive *bool
}

// dispatcherOverview is an overview with one worker per crewDisp.
func dispatcherOverview(ds ...crewDisp) crewview.Overview {
	var ov crewview.Overview
	for _, d := range ds {
		ov.Workers = append(ov.Workers, crewview.Worker{
			Crew: d.crew, Branch: "feat/" + d.crew, Origin: crewview.OriginLocal, State: "working",
			Dispatcher: &crewview.Dispatcher{Where: "here", PID: d.pid, Pane: d.pane, Alive: d.alive},
		})
	}
	return ov
}

// dispatcherPane is a Claude pane listed by the tmux server.
func dispatcherPane(id, target string) tmux.PaneOptions {
	return tmux.PaneOptions{PaneID: id, Target: target, ClaudeStatus: "idle 1 ", PanePID: dispPanePID}
}

// liveDispatcher is the probe's view of a dispatcher two levels below its
// pane's shell.
func liveDispatcher() fakeProcs {
	return fakeProcs{parents: map[int]int{dispPID: 150, 150: dispPanePID}}
}

type joinFixture struct {
	wins  []tmux.WindowOptions
	panes []tmux.PaneOptions
	ov    crewview.Overview
	probe fakeProcs
}

func defaultJoinFixture() joinFixture {
	return joinFixture{
		wins:  []tmux.WindowOptions{{Session: "h", Window: 2, CrewName: "dispatcher"}},
		panes: []tmux.PaneOptions{dispatcherPane("%1", "h:2")},
		ov:    dispatcherOverview(crewDisp{crew: "c1", pane: new("%1"), pid: new(dispPID), alive: new(true)}),
		probe: liveDispatcher(),
	}
}

func TestJoinDispatchers(t *testing.T) {
	joined := map[string]string{"c1": "%1"}
	none := map[string]string{}
	disp := func(f *joinFixture) *crewview.Dispatcher { return f.ov.Workers[0].Dispatcher }
	cases := []struct {
		name string
		edit func(f *joinFixture)
		want map[string]string
	}{
		{"live claude dispatcher", func(f *joinFixture) {}, joined},
		{"agent-detect pane", func(f *joinFixture) {
			p := piPane("%1", "h:2", "idle", 1)
			p.PanePID = dispPanePID
			f.panes = []tmux.PaneOptions{p}
		}, joined},
		{"surrounding whitespace in the pane", func(f *joinFixture) { disp(f).Pane = new("  %1\n") }, joined},
		{"pane missing from the listing", func(f *joinFixture) { disp(f).Pane = new("%9") }, none},
		{"no pane registered", func(f *joinFixture) { disp(f).Pane = nil }, none},
		{"no dispatcher reported", func(f *joinFixture) { f.ov.Workers[0].Dispatcher = nil }, none},
		{"no engine status", func(f *joinFixture) { f.panes[0].ClaudeStatus = "" }, none},
		{"window is not a dispatcher", func(f *joinFixture) { f.wins[0].CrewName = "bronze" }, none},
		{"window not listed", func(f *joinFixture) { f.wins = nil }, none},
		{"role-grid pane", func(f *joinFixture) { f.panes[0].CrewRole = "critic" }, none},
		{"grid lead pane", func(f *joinFixture) { f.panes[0].CrewRole = tmux.CrewRoleLead }, joined},
		{"crew says the dispatcher is dead", func(f *joinFixture) { disp(f).Alive = new(false) }, none},
		{"crew's verdict wins over the probe", func(f *joinFixture) { f.probe.dead = map[int]bool{dispPID: true} }, joined},
		{"alive unknown, probe says dead", func(f *joinFixture) {
			disp(f).Alive = nil
			f.probe.dead = map[int]bool{dispPID: true}
		}, none},
		{"alive unknown, probe says alive", func(f *joinFixture) { disp(f).Alive = nil }, joined},
		{"pid not in the pane's tree", func(f *joinFixture) {
			f.probe.parents = map[int]int{dispPID: 150, 150: 1}
		}, none},
		{"parent walk fails", func(f *joinFixture) { f.probe.err = errors.New("procfs gone") }, none},
		{"pid is the pane's own process", func(f *joinFixture) { disp(f).PID = new(dispPanePID) }, joined},
		{"pane pid unknown", func(f *joinFixture) { f.panes[0].PanePID = 0 }, none},
		{"no pid reported", func(f *joinFixture) {
			disp(f).PID = nil
			f.probe = fakeProcs{dead: map[int]bool{dispPID: true}, err: errors.New("never asked")}
		}, joined},
		{"no pid and dead", func(f *joinFixture) {
			disp(f).PID = nil
			disp(f).Alive = new(false)
		}, none},
		{"dispatcher registered elsewhere", func(f *joinFixture) { disp(f).Where = "elsewhere" }, none},
		{"empty overview", func(f *joinFixture) { f.ov = crewview.Overview{} }, none},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := defaultJoinFixture()
			c.edit(&f)
			got := joinDispatchers(f.ov, f.wins, f.panes, nil, f.probe)
			if !maps.Equal(got, c.want) {
				t.Errorf("joinDispatchers = %v, want %v", got, c.want)
			}
		})
	}
}

// Two dispatchers in one project each own their crew; neither borrows the
// other's pane.
func TestJoinDispatchersTwoDispatchersOneProject(t *testing.T) {
	wins := []tmux.WindowOptions{
		{Session: "h", Window: 2, CrewName: "dispatcher"},
		{Session: "h", Window: 3, CrewName: "dispatcher"},
	}
	p2 := dispatcherPane("%2", "h:3")
	p2.PanePID = 300
	panes := []tmux.PaneOptions{dispatcherPane("%1", "h:2"), p2}
	ov := dispatcherOverview(
		crewDisp{crew: "c1", pane: new("%1"), pid: new(200)},
		crewDisp{crew: "c2", pane: new("%2"), pid: new(400)},
	)
	probe := fakeProcs{parents: map[int]int{200: dispPanePID, 400: 300}}

	got := joinDispatchers(ov, wins, panes, nil, probe)
	if want := map[string]string{"c1": "%1", "c2": "%2"}; !maps.Equal(got, want) {
		t.Errorf("joinDispatchers = %v, want %v", got, want)
	}
}

// A dispatcher restarted in the same pane registers a new crew; the old
// crew's registration stays behind and must not keep the pane.
func TestJoinDispatchersTwoCrewsOnePane(t *testing.T) {
	f := defaultJoinFixture()
	t.Run("the greater crew id wins", func(t *testing.T) {
		ov := dispatcherOverview(
			crewDisp{crew: "1800000200-7", pane: new("%1")},
			crewDisp{crew: "1800000100-7", pane: new("%1")},
		)
		got := joinDispatchers(ov, f.wins, f.panes, nil, f.probe)
		if want := map[string]string{"1800000200-7": "%1"}; !maps.Equal(got, want) {
			t.Errorf("joinDispatchers = %v, want %v", got, want)
		}
	})
	t.Run("a newer crew that fails its checks does not displace the live one", func(t *testing.T) {
		ov := dispatcherOverview(
			crewDisp{crew: "c1", pane: new("%1"), pid: new(dispPID)},
			crewDisp{crew: "c2", pane: new("%1"), pid: new(999), alive: new(false)},
		)
		got := joinDispatchers(ov, f.wins, f.panes, nil, f.probe)
		if want := map[string]string{"c1": "%1"}; !maps.Equal(got, want) {
			t.Errorf("joinDispatchers = %v, want %v", got, want)
		}
	})
	t.Run("a crew with several workers joins once", func(t *testing.T) {
		d := crewDisp{crew: "c1", pane: new("%1"), pid: new(dispPID)}
		got := joinDispatchers(dispatcherOverview(d, d), f.wins, f.panes, nil, f.probe)
		if want := map[string]string{"c1": "%1"}; !maps.Equal(got, want) {
			t.Errorf("joinDispatchers = %v, want %v", got, want)
		}
	})
}

func TestDescendsVia(t *testing.T) {
	parents := map[int]int{300: 200, 200: 100, 100: 1}
	parent := func(p int) (int, error) { return parents[p], nil }
	cases := []struct {
		name      string
		pid, root int
		want      bool
	}{
		{"self", 100, 100, true},
		{"child", 200, 100, true},
		{"grandchild", 300, 100, true},
		{"ancestor is not a descendant", 100, 300, false},
		{"unrelated root", 300, 42, false},
		{"unknown parent", 77, 100, false},
	}
	for _, c := range cases {
		got, err := descendsVia(parent, c.pid, c.root)
		if err != nil || got != c.want {
			t.Errorf("%s: descendsVia(%d, %d) = %v, %v; want %v", c.name, c.pid, c.root, got, err, c.want)
		}
	}

	t.Run("a cycle stops at the step bound", func(t *testing.T) {
		cyc := func(p int) (int, error) { return map[int]int{10: 11, 11: 10}[p], nil }
		if got, err := descendsVia(cyc, 10, 100); got || err != nil {
			t.Errorf("descendsVia over a cycle = %v, %v; want false, nil", got, err)
		}
	})
	t.Run("a failing parent lookup is an error", func(t *testing.T) {
		boom := errors.New("boom")
		failing := func(int) (int, error) { return 0, boom }
		if got, err := descendsVia(failing, 300, 100); got || !errors.Is(err, boom) {
			t.Errorf("descendsVia = %v, %v; want false, boom", got, err)
		}
	})
}

func TestHostProcsAliveSelf(t *testing.T) {
	if !(hostProcs{}).Alive(os.Getpid()) {
		t.Error("Alive(self) = false")
	}
}

func TestHostProcsDescendsSelf(t *testing.T) {
	ok, err := (hostProcs{}).Descends(os.Getpid(), os.Getppid())
	if err != nil || !ok {
		t.Errorf("Descends(self, parent) = %v, %v; want true", ok, err)
	}
}

// A pi dispatcher without @claude_status or @agent_screen joins through a
// live hook session on its pane, and only through one.
func TestJoinDispatchersHookSessionFallback(t *testing.T) {
	hooks := map[string]hub.SessionView{"%1": {SessionID: "s1", TmuxPane: "%1"}}
	cases := []struct {
		name  string
		hooks map[string]hub.SessionView
		want  map[string]string
	}{
		{"hook session joins", hooks, map[string]string{"c1": "%1"}},
		{"no hook session skips", nil, map[string]string{}},
		{"hook session on another pane skips", map[string]hub.SessionView{"%9": {SessionID: "s9"}}, map[string]string{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := defaultJoinFixture()
			f.panes[0].ClaudeStatus = ""
			got := joinDispatchers(f.ov, f.wins, f.panes, c.hooks, f.probe)
			if !maps.Equal(got, c.want) {
				t.Errorf("joinDispatchers = %v, want %v", got, c.want)
			}
		})
	}
}
