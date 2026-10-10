package runs

import (
	"errors"
	"maps"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/noamsto/houston/tmux"
)

// fakeProcs is a procProbe over a made-up process table: every pid is alive
// unless dead, starts at starts[pid] (0 = unknown), and has parents[pid] as
// its parent. err makes every parent walk fail.
type fakeProcs struct {
	dead    map[int]bool
	starts  map[int]int64
	parents map[int]int
	err     error
}

func (f fakeProcs) Alive(pid int) bool  { return !f.dead[pid] }
func (f fakeProcs) Start(pid int) int64 { return f.starts[pid] }
func (f fakeProcs) Descends(pid, root int) (bool, error) {
	if f.err != nil {
		return false, f.err
	}
	return descendsVia(func(p int) (int, error) { return f.parents[p], nil }, pid, root)
}

// The join tests' clock: the tmux server started at serverT, and the
// dispatcher registered (pane and pid files) at regT.
var (
	serverT = time.Unix(1_800_000_000, 0)
	regT    = serverT.Add(30 * time.Minute)
)

const (
	dispPanePID = 100 // the dispatcher pane's shell
	dispPID     = 200 // the dispatcher process, the shell's grandchild
)

// crewFiles is one <bus>/crews/<id> directory; a nil pane or pid writes no
// file.
type crewFiles struct {
	id      string
	pane    *string
	paneMod time.Time
	pid     *string
	pidMod  time.Time
}

func writeCrew(t *testing.T, bus string, c crewFiles) {
	t.Helper()
	dir := filepath.Join(bus, "crews", c.id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []struct {
		name string
		body *string
		mod  time.Time
	}{{"pane", c.pane, c.paneMod}, {"pid", c.pid, c.pidMod}} {
		if f.body == nil {
			continue
		}
		path := filepath.Join(dir, f.name)
		if err := os.WriteFile(path, []byte(*f.body), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, f.mod, f.mod); err != nil {
			t.Fatal(err)
		}
	}
}

// dispatcherPane is a Claude pane listed by the server that started at
// serverT.
func dispatcherPane(id, target string) tmux.PaneOptions {
	return tmux.PaneOptions{PaneID: id, Target: target, ClaudeStatus: "idle 1 ", PanePID: dispPanePID, ServerStart: serverT.Unix()}
}

// liveDispatcher is the probe's view of a dispatcher that registered at regT
// two levels below its pane's shell.
func liveDispatcher() fakeProcs {
	return fakeProcs{
		starts:  map[int]int64{dispPID: regT.Add(-time.Second).Unix()},
		parents: map[int]int{dispPID: 150, 150: dispPanePID},
	}
}

type joinFixture struct {
	wins  []tmux.WindowOptions
	panes []tmux.PaneOptions
	crews []crewFiles
	probe fakeProcs
}

func defaultJoinFixture() joinFixture {
	return joinFixture{
		wins:  []tmux.WindowOptions{{Session: "h", Window: 2, CrewName: "dispatcher"}},
		panes: []tmux.PaneOptions{dispatcherPane("%1", "h:2")},
		crews: []crewFiles{{id: "c1", pane: ptr("%1"), paneMod: regT, pid: ptr("200\n"), pidMod: regT}},
		probe: liveDispatcher(),
	}
}

func TestJoinDispatchers(t *testing.T) {
	joined := map[string]string{"c1": "%1"}
	none := map[string]string{}
	cases := []struct {
		name  string
		edit  func(f *joinFixture)
		want  map[string]string
		write bool // false: no crews/ directory at all
	}{
		{"live claude dispatcher", func(f *joinFixture) {}, joined, true},
		{"agent-detect pane", func(f *joinFixture) {
			p := piPane("%1", "h:2", "idle", 1)
			p.PanePID, p.ServerStart = dispPanePID, serverT.Unix()
			f.panes = []tmux.PaneOptions{p}
		}, joined, true},
		{"surrounding whitespace in the pane file", func(f *joinFixture) { f.crews[0].pane = ptr("  %1\n") }, joined, true},
		{"pane missing from the listing", func(f *joinFixture) { f.crews[0].pane = ptr("%9") }, none, true},
		{"no pane file", func(f *joinFixture) { f.crews[0].pane = nil }, none, true},
		{"no engine status", func(f *joinFixture) {
			f.panes[0].ClaudeStatus = ""
		}, none, true},
		{"window is not a dispatcher", func(f *joinFixture) { f.wins[0].CrewName = "bronze" }, none, true},
		{"window not listed", func(f *joinFixture) { f.wins = nil }, none, true},
		{"role-grid pane", func(f *joinFixture) { f.panes[0].CrewRole = "critic" }, none, true},
		{"grid lead pane", func(f *joinFixture) { f.panes[0].CrewRole = tmux.CrewRoleLead }, joined, true},
		{"pane id reused after a tmux restart", func(f *joinFixture) {
			f.crews[0].paneMod = serverT.Add(-time.Hour)
			f.crews[0].pid = nil
		}, none, true},
		{"pane file written the second the server started", func(f *joinFixture) {
			f.crews[0].paneMod = serverT
		}, joined, true},
		{"server start unknown", func(f *joinFixture) {
			f.panes[0].ServerStart = 0
			f.crews[0].paneMod = serverT.Add(-time.Hour)
		}, joined, true},
		{"dead pid", func(f *joinFixture) { f.probe.dead = map[int]bool{dispPID: true} }, none, true},
		{"recycled pid", func(f *joinFixture) {
			f.probe.starts[dispPID] = regT.Add(3 * time.Second).Unix()
		}, none, true},
		{"start within the recycle slack", func(f *joinFixture) {
			f.probe.starts[dispPID] = regT.Add(2 * time.Second).Unix()
		}, joined, true},
		{"start unknown", func(f *joinFixture) { f.probe.starts = nil }, none, true},
		{"pid not in the pane's tree", func(f *joinFixture) {
			f.probe.parents = map[int]int{dispPID: 150, 150: 1}
		}, none, true},
		{"parent walk fails", func(f *joinFixture) { f.probe.err = errors.New("procfs gone") }, none, true},
		{"pid is the pane's own process", func(f *joinFixture) {
			f.crews[0].pid = ptr("100")
			f.probe.starts = map[int]int64{dispPanePID: regT.Unix()}
		}, joined, true},
		{"pane pid unknown", func(f *joinFixture) { f.panes[0].PanePID = 0 }, none, true},
		{"missing pid file", func(f *joinFixture) {
			f.crews[0].pid = nil
			f.probe = fakeProcs{dead: map[int]bool{dispPID: true}, err: errors.New("never asked")}
		}, joined, true},
		{"unparseable pid file", func(f *joinFixture) {
			f.crews[0].pid = ptr("not a pid")
			f.probe = fakeProcs{dead: map[int]bool{dispPID: true}, err: errors.New("never asked")}
		}, joined, true},
		{"no crews directory", func(f *joinFixture) {}, none, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := defaultJoinFixture()
			c.edit(&f)
			bus := t.TempDir()
			if c.write {
				for _, cf := range f.crews {
					writeCrew(t, bus, cf)
				}
			}
			got := joinDispatchers(bus, f.wins, f.panes, f.probe)
			if !maps.Equal(got, c.want) {
				t.Errorf("joinDispatchers = %v, want %v", got, c.want)
			}
		})
	}
}

func TestJoinDispatchersIgnoresStrayFiles(t *testing.T) {
	f := defaultJoinFixture()
	bus := t.TempDir()
	writeCrew(t, bus, f.crews[0])
	if err := os.WriteFile(filepath.Join(bus, "crews", "README"), []byte("%1"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := joinDispatchers(bus, f.wins, f.panes, f.probe)
	if !maps.Equal(got, map[string]string{"c1": "%1"}) {
		t.Errorf("joinDispatchers = %v, want only c1", got)
	}
}

// Two dispatchers in one project each own their crew; neither borrows the
// other's pane.
func TestJoinDispatchersTwoDispatchersOneProject(t *testing.T) {
	bus := t.TempDir()
	wins := []tmux.WindowOptions{
		{Session: "h", Window: 2, CrewName: "dispatcher"},
		{Session: "h", Window: 3, CrewName: "dispatcher"},
	}
	p2 := dispatcherPane("%2", "h:3")
	p2.PanePID = 300
	panes := []tmux.PaneOptions{dispatcherPane("%1", "h:2"), p2}
	writeCrew(t, bus, crewFiles{id: "c1", pane: ptr("%1"), paneMod: regT, pid: ptr("200"), pidMod: regT})
	writeCrew(t, bus, crewFiles{id: "c2", pane: ptr("%2"), paneMod: regT, pid: ptr("400"), pidMod: regT})
	probe := fakeProcs{
		starts:  map[int]int64{200: regT.Unix(), 400: regT.Unix()},
		parents: map[int]int{200: dispPanePID, 400: 300},
	}

	got := joinDispatchers(bus, wins, panes, probe)
	if want := map[string]string{"c1": "%1", "c2": "%2"}; !maps.Equal(got, want) {
		t.Errorf("joinDispatchers = %v, want %v", got, want)
	}
}

// A dispatcher restarted in the same pane registers a new crew; the old
// crew's files stay behind and must not keep the pane.
func TestJoinDispatchersTwoCrewsOnePane(t *testing.T) {
	f := defaultJoinFixture()
	t.Run("newest pane file wins", func(t *testing.T) {
		bus := t.TempDir()
		writeCrew(t, bus, crewFiles{id: "c9", pane: ptr("%1"), paneMod: regT.Add(-time.Minute)})
		writeCrew(t, bus, crewFiles{id: "c1", pane: ptr("%1"), paneMod: regT})
		got := joinDispatchers(bus, f.wins, f.panes, f.probe)
		if want := map[string]string{"c1": "%1"}; !maps.Equal(got, want) {
			t.Errorf("joinDispatchers = %v, want %v", got, want)
		}
	})
	t.Run("tie goes to the greater crew id", func(t *testing.T) {
		bus := t.TempDir()
		writeCrew(t, bus, crewFiles{id: "1800000100-7", pane: ptr("%1"), paneMod: regT})
		writeCrew(t, bus, crewFiles{id: "1800000200-7", pane: ptr("%1"), paneMod: regT})
		got := joinDispatchers(bus, f.wins, f.panes, f.probe)
		if want := map[string]string{"1800000200-7": "%1"}; !maps.Equal(got, want) {
			t.Errorf("joinDispatchers = %v, want %v", got, want)
		}
	})
	t.Run("a newer crew that fails its checks does not displace the live one", func(t *testing.T) {
		bus := t.TempDir()
		writeCrew(t, bus, crewFiles{id: "c1", pane: ptr("%1"), paneMod: regT, pid: ptr("200"), pidMod: regT})
		writeCrew(t, bus, crewFiles{id: "c2", pane: ptr("%1"), paneMod: regT.Add(time.Minute), pid: ptr("999"), pidMod: regT})
		probe := liveDispatcher()
		probe.dead = map[int]bool{999: true}
		got := joinDispatchers(bus, f.wins, f.panes, probe)
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
