package runs

import (
	"log/slog"
	"strings"

	"github.com/noamsto/houston/crewview"
	"github.com/noamsto/houston/hub"
	"github.com/noamsto/houston/tmux"
)

// procProbe is the dispatcher join's view of the host process table — an
// interface so tests stay hermetic.
type procProbe interface {
	// Alive reports whether pid exists (signal 0; EPERM counts as alive).
	Alive(pid int) bool
	// Descends reports whether pid is root or one of its descendants.
	Descends(pid, root int) (bool, error)
}

// joinDispatchers maps each crew of ov (one bus's overview) to the pane its
// dispatcher runs in, crew id → pane id. A crew joins pane P when crew reports
// its dispatcher registered with pane P, P is listed with an engine status (or
// a live hook session) in a window stamped @crew_name dispatcher and is not a
// role-grid pane, and the dispatcher's pid, when crew reports one, is alive
// and a process under P. One crew per pane: the greater crew id wins, so a
// dispatcher restarted in the same pane takes over from the crew it left
// behind.
func joinDispatchers(ov crewview.Overview, wins []tmux.WindowOptions, panes []tmux.PaneOptions, hookPanes map[string]hub.SessionView, probe procProbe) map[string]string {
	byID := make(map[string]tmux.PaneOptions, len(panes))
	for _, p := range panes {
		byID[p.PaneID] = p
	}
	byTarget := windowsByTarget(wins)

	best := map[string]string{} // pane id -> crew id
	for _, w := range ov.Workers {
		d := w.Dispatcher
		if d == nil || d.Pane == nil || w.Crew == "" {
			continue
		}
		crewID, paneID := w.Crew, strings.TrimSpace(*d.Pane)
		p, listed := byID[paneID]
		_, hasHook := hookPanes[paneID]
		if !listed || (p.ClaudeStatus == "" && p.AgentScreen == "" && !hasHook) || p.IsRolePane() ||
			byTarget[p.Target].CrewName != dispatcherCrewName {
			continue
		}
		if !dispatcherOwnsPane(d, p.PanePID, probe) {
			slog.Debug("crew source: dispatcher does not own its pane", "crew", crewID, "pane", paneID)
			continue
		}
		if held, ok := best[paneID]; ok && held >= crewID {
			continue
		}
		best[paneID] = crewID
	}

	out := make(map[string]string, len(best))
	for paneID, crewID := range best {
		out[crewID] = paneID
	}
	return out
}

// dispatcherOwnsPane is the join's process check on crew's report. No pid has
// no opinion; a dead pid, an unknown pane pid or a failed parent walk fails
// closed. crew's own liveness verdict wins over a probe here.
func dispatcherOwnsPane(d *crewview.Dispatcher, panePID int, probe procProbe) bool {
	if d.PID == nil || *d.PID <= 0 {
		return d.Alive == nil || *d.Alive
	}
	alive := probe.Alive(*d.PID)
	if d.Alive != nil {
		alive = *d.Alive
	}
	if !alive || panePID <= 0 {
		return false
	}
	ok, err := probe.Descends(*d.PID, panePID)
	return err == nil && ok
}
