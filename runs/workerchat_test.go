package runs

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/noamsto/houston/hook"
	"github.com/noamsto/houston/tmux"
)

// TestCrewWorkerRunOffersChat composes a dispatched Claude worker from all
// three layers the way production does — tmux options, the crew bus, and a
// hook state file read through a real hub — and asserts the one run Fleet
// lists for that worker offers Chat. A grid lead is the shape dispatch gives
// every deep Claude worker: its own pane carries @crew_role=lead beside its
// role panes (#179).
func TestCrewWorkerRunOffersChat(t *testing.T) {
	const (
		branch  = "feat/179-chat"
		root    = "/wt/feat-179-chat"
		sid     = "71239473-b8f5-42b9-bf76-92f1cda0c989"
		lead    = "%4300"
		server  = "4242"
		session = int64(1790706963) // s<epoch> in the worker id
		record  = int64(1790706991) // the latest status record, in seconds
	)
	now := time.Now().Unix()
	roles := []tmux.PaneOptions{
		{PaneID: "%4301", Target: "h:3", ClaudeStatus: "idle 1 ", CrewRole: "spec-critic"},
		{PaneID: "%4302", Target: "h:3", ClaudeStatus: "idle 1 ", CrewRole: "plan-critic"},
	}

	for _, tc := range []struct {
		name       string
		leadRole   string
		roles      []tmux.PaneOptions
		busState   string
		paneStatus string
		hookState  hook.State
	}{
		{name: "plain worker", busState: "working", paneStatus: "processing 1790707000 ", hookState: hook.StateThinking},
		{name: "grid lead", leadRole: "lead", roles: roles, busState: "working", paneStatus: "processing 1790707000 ", hookState: hook.StateThinking},
		{name: "plain worker pr_open", busState: "pr_open", paneStatus: "processing 1790707000 ", hookState: hook.StateThinking},
		{name: "grid lead pr_open", leadRole: "lead", roles: roles, busState: "pr_open", paneStatus: "processing 1790707000 ", hookState: hook.StateThinking},
		{name: "plain worker done", busState: "done", paneStatus: "done 1790707000 1", hookState: hook.StateWaiting},
		{name: "grid lead done", leadRole: "lead", roles: roles, busState: "done", paneStatus: "done 1790707000 1", hookState: hook.StateWaiting},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wins := []tmux.WindowOptions{{Session: "h", Window: 3, Branch: branch, GitRoot: root, CrewName: "iris"}}
			panes := append([]tmux.PaneOptions{{
				PaneID: lead, Target: "h:3", ClaudeStatus: tc.paneStatus, CrewRole: tc.leadRole, PanePID: 77,
			}}, tc.roles...)
			for i := range panes {
				panes[i].ServerPID, panes[i].ServerStart = server, now-3600
			}

			bus := t.TempDir()
			worker := fmt.Sprintf("worker:%s#s%d-3430430", branch, session)
			records := fmt.Sprintf(`{"ts":%d529,"crew_id":"c1","kind":"dispatch","branch":%q,"session":"s%d-3430430","worker_id":%q,"engine":"claude","model":"opus","tier":"deep","title":"Show the Chat tab","name":"iris"}`+"\n", session, branch, session, worker) +
				fmt.Sprintf(`{"ts":%d348,"crew_id":"c1","from":%q,"to":"dispatcher:c1","kind":"status","body":{"state":%q}}`+"\n", record, worker, tc.busState)
			if err := os.WriteFile(filepath.Join(bus, "events.jsonl"), []byte(records), 0o644); err != nil {
				t.Fatal(err)
			}

			reg := NewRegistry(DefaultOrder)
			for _, d := range deltasFromTmux(wins, panes, func(string) string { return "houston" }) {
				reg.Apply(d)
			}
			cs := crewScanner(map[string]string{root: bus}, wins, panes)
			cs.procStart = startAt(session + 2)
			crew, ok := cs.scan()
			if !ok {
				t.Fatal("crew scan reported failure")
			}
			for key, r := range crew {
				reg.Apply(Delta{Source: "crew", Key: key, Run: r})
			}

			hookPanes := &fakePanes{}
			hookPanes.setPanes(panes...)
			hookPanes.setIdentity(server, now-3600)
			out, _ := startHookSourceWith(t, hookPanes, hook.SessionState{
				SessionID:      sid,
				State:          tc.hookState,
				CWD:            root,
				GitBranch:      branch,
				TmuxPane:       lead,
				TmuxServer:     server,
				TranscriptPath: filepath.Join(t.TempDir(), sid+".jsonl"),
				UpdatedAt:      now,
			}, nil)
			drainHookDeltas(out, reg)

			var workers []Run
			for _, r := range reg.Snapshot() {
				if r.Branch == branch {
					workers = append(workers, r)
					t.Logf("listed %s: state=%s role=%q chat=%v term=%v session=%q crew=%v", r.ID, r.State, r.Role, r.Caps.Chat, r.Caps.Terminal, r.Session, r.Crew != nil)
				}
			}
			if len(workers) != 1 {
				t.Fatalf("Fleet lists %d runs for the worker, want 1", len(workers))
			}
			w := workers[0]
			if !w.Caps.Chat || w.Session != sid {
				t.Errorf("worker run %s: Caps.Chat = %v, Session = %q; want Chat for session %s", w.ID, w.Caps.Chat, w.Session, sid)
			}
			if !w.Caps.Terminal || w.Role != RoleWorker || w.Crew == nil || w.Crew.Tier != "deep" {
				t.Errorf("worker run %s lost a layer: Terminal=%v Role=%q Crew=%+v", w.ID, w.Caps.Terminal, w.Role, w.Crew)
			}
		})
	}
}

// drainHookDeltas applies every delta the hook source emits until it has been
// quiet for hookQuietTicks ticks.
func drainHookDeltas(out <-chan Delta, reg *Registry) {
	for {
		select {
		case d := <-out:
			reg.Apply(d)
		case <-time.After(hookQuietTicks * hookTestEvery):
			return
		}
	}
}
