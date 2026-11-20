package runs

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/noamsto/houston/tmux"
)

// finishedBus is a bus where c1's worker on fix/412 posted done at 2000 s from
// session 1000, plus a dispatch row naming its codename.
const finishedBus = `{"ts":1000000,"crew_id":"c1","from":"dispatcher:c1","kind":"dispatch","branch":"fix/412","engine":"claude","title":"Fix the thing","name":"rust","tmux":"colour208"}
{"ts":2000000,"crew_id":"c1","from":"worker:fix/412#s1000-7","kind":"status","body":{"state":"done","detail":"{\"final\":true}"}}
`

func writeFinishedBus(t *testing.T, log string) string {
	t.Helper()
	bus := t.TempDir()
	if err := os.WriteFile(filepath.Join(bus, "events.jsonl"), []byte(log), 0o644); err != nil {
		t.Fatal(err)
	}
	return bus
}

// A finished worker crew's view dropped, with its window open and its pane
// stamp stuck on processing, joins its crew and reads done.
func TestScanFinishedWorkerMissingFromViewJoinsItsPane(t *testing.T) {
	bus := writeFinishedBus(t, finishedBus)
	wins := []tmux.WindowOptions{{Session: "h", Window: 1, Branch: "fix/412", GitRoot: "/wt/a", CrewName: "rust"}}
	stuck := tmux.PaneOptions{PaneID: "%449", Target: "h:1", ClaudeStatus: "processing 1990 ", PanePID: 7}
	view := overviewOf(nil, viewWorker("fix/500", "working", "", 1)) // another live worker, not this one

	s := crewScanner(map[string]string{"/wt/a": bus}, viewsOn(bus, view), wins, []tmux.PaneOptions{stuck})
	s.procStart = startAt(1500)
	got, ok := s.scan()
	if !ok {
		t.Fatal("scan failed")
	}
	r, found := got["%449"]
	if !found {
		t.Fatalf("finished worker did not join %%449; keys %v", keysOf(got))
	}
	if r.Crew == nil || r.Crew.Name != "c1" || r.State != StateDone || r.Role != RoleWorker || !r.CrewFinished {
		t.Errorf("run = %+v crew=%+v, want crew c1, done, worker", r, r.Crew)
	}
	if r.Crew.Title != "Fix the thing" || r.Crew.Codename != "rust" {
		t.Errorf("crew = %+v, want the dispatch row's title and codename", r.Crew)
	}
	if _, dup := got["crew/"+bus+"/fix/412"]; dup {
		t.Error("also published under the windowless fallback key")
	}

	// Composed with the stale pane layers well past the eviction grace: done, not thinking.
	reg := NewRegistry(DefaultOrder)
	reg.Apply(Delta{Source: "tmux", Key: "%449", Run: Run{State: StateRunning, Agent: "claude", Crew: &CrewRef{Codename: "rust"}, Activity: Activity{Task: `Assignment: {"final":true}`}}})
	reg.Apply(Delta{Source: "hooks", Key: "%449", Run: Run{State: StateThinking, Agent: "claude"}})
	deltas, _ := tickCrewDeltas(got, map[string]bool{}, time.Unix(2000, 0).Add(crewEvictionGrace+time.Minute))
	for _, d := range deltas {
		reg.Apply(d)
	}
	var composed Run
	for _, c := range reg.Snapshot() {
		if c.ID == PaneRunID("%449") {
			composed = c
		}
	}
	if composed.Crew == nil {
		t.Fatalf("no composed run for c1: %+v", reg.Snapshot())
	}
	if composed.State != StateDone || composed.Attention != AttentionDone {
		t.Errorf("composed state/attention = %s/%s, want done/done after the eviction grace", composed.State, composed.Attention)
	}
}

func TestScanFinishedWorkerDoesNotCrossAttachByCodename(t *testing.T) {
	// c2 finished a worker named rust on fix/999 (no window). A pane in another
	// window carries the same codename on a branch the bus never mentions.
	log := `{"ts":1000000,"crew_id":"c2","from":"dispatcher:c2","kind":"dispatch","branch":"fix/999","engine":"claude","name":"rust","tmux":"colour208"}
{"ts":2000000,"crew_id":"c2","from":"worker:fix/999#s1000-7","kind":"status","body":{"state":"done"}}
`
	bus := writeFinishedBus(t, log)
	wins := []tmux.WindowOptions{{Session: "h", Window: 1, Branch: "fix/412", GitRoot: "/wt/a", CrewName: "rust"}}
	panes := []tmux.PaneOptions{{PaneID: "%1", Target: "h:1", ClaudeStatus: "processing 1990 ", PanePID: 7}}

	s := crewScanner(map[string]string{"/wt/a": bus}, viewsOn(bus, overviewOf(nil)), wins, panes)
	s.procStart = startAt(1500)
	got, _ := s.scan()
	if r, found := got["%1"]; found {
		t.Errorf("pane %%1 joined crew %+v by codename alone", r.Crew)
	}
}

func TestScanFinishedWorkerNewOccupantDoesNotJoin(t *testing.T) {
	bus := writeFinishedBus(t, finishedBus)
	wins := []tmux.WindowOptions{{Session: "h", Window: 1, Branch: "fix/412", GitRoot: "/wt/a", CrewName: "rust"}}
	view := overviewOf(nil, viewWorker("fix/500", "working", "", 1))
	cases := map[string]struct {
		pane  tmux.PaneOptions
		start int64
	}{
		"engine started after the terminal row": {tmux.PaneOptions{PaneID: "%1", Target: "h:1", ClaudeStatus: "processing 1990 ", PanePID: 7}, 2050},
		"processing stamp after the slack":      {tmux.PaneOptions{PaneID: "%1", Target: "h:1", ClaudeStatus: "processing 2060 ", PanePID: 7}, 1500},
		"pane touched after the grace":          {tmux.PaneOptions{PaneID: "%1", Target: "h:1", ClaudeStatus: "processing 9000 ", PanePID: 7}, 1500},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			s := crewScanner(map[string]string{"/wt/a": bus}, viewsOn(bus, view), wins, []tmux.PaneOptions{tc.pane})
			s.procStart = startAt(tc.start)
			got, _ := s.scan()
			if r, found := got["%1"]; found {
				t.Errorf("new occupant joined: %+v", r.Crew)
			}
		})
	}
}

func TestComposeNeverShowsCrewPayloadAsSummary(t *testing.T) {
	wins := []tmux.WindowOptions{{Session: "s", Window: 1, Task: `Assignment: {"final":true}`, CrewName: "rust"}}
	panes := []tmux.PaneOptions{{PaneID: "%1", Target: "s:1", ClaudeStatus: "idle 1 ", ClaudeTask: `{"final":true}`}}
	got := deltasFromTmux(wins, panes, fakeProject)
	if len(got) != 1 || got[0].Run.Activity.Task != "" {
		t.Fatalf("Task = %+v, want empty for a crew payload", got)
	}
}

func TestScanFinishedWorkerWithFinishedPaneKeepsNoPinnedState(t *testing.T) {
	bus := writeFinishedBus(t, finishedBus)
	wins := []tmux.WindowOptions{{Session: "h", Window: 1, Branch: "fix/412", GitRoot: "/wt/a", CrewName: "rust"}}
	pane := tmux.PaneOptions{PaneID: "%1", Target: "h:1", ClaudeStatus: "done 2005 ", PanePID: 7}
	view := overviewOf(nil, viewWorker("fix/500", "working", "", 1))

	s := crewScanner(map[string]string{"/wt/a": bus}, viewsOn(bus, view), wins, []tmux.PaneOptions{pane})
	s.procStart = startAt(1500)
	got, _ := s.scan()
	r, found := got["%1"]
	if !found || r.Crew == nil || r.Crew.Name != "c1" || r.State != StateDone {
		t.Fatalf("run = %+v (found=%v), want it joined as done under c1", r, found)
	}
	if r.CrewFinished {
		t.Error("CrewFinished set for a pane whose own state says finished")
	}
	deltas, _ := tickCrewDeltas(got, map[string]bool{}, time.Unix(2000, 0).Add(crewEvictionGrace+time.Minute))
	for _, d := range deltas {
		if d.Key == "%1" && d.Run.State != "" {
			t.Errorf("state %q kept past the eviction grace, want identity only", d.Run.State)
		}
	}
}
