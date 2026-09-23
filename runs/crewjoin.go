package runs

import "github.com/noamsto/houston/tmux"

// resolvePane finds the one agent pane running (bus, branch), if there is
// exactly one. busOf maps a window's @git_root to its bus directory; "" means
// the root has no bus and disqualifies the window. An empty @git_root never
// reaches busOf: `git -C ""` is a documented no-op, so it would resolve to
// houston's own cwd and pull unrelated windows into that bus.
//
// A candidate's @claude_status must be non-empty because that is TmuxSource's
// own test for "this pane is an agent run" — joining onto a shell pane would
// let the crew layer's Agent promote that shell into a listed run.
//
// busState is the crew-bus Run.State for this branch, from the latest status
// record, and busUpdatedAt is that record's timestamp (unix seconds — see
// Run.UpdatedAt). When busState is terminal (StateDone or StateFailed), the
// worker session that wrote those bus records has ended, but its own pane
// commonly still sits idle in its window until `crew reap` reclaims it — the
// normal case, not a stale join. So a terminal record still joins iff the
// candidate pane's own activity epoch (ClaudeStatusEpoch(p.ClaudeStatus)) is
// <= busUpdatedAt: nothing has touched the pane since the worker finished, so
// it is the same session. A pane epoch newer than busUpdatedAt means a new
// occupant took the pane over — don't join. A non-positive pane epoch (0 for
// unknown/missing, or a malformed negative value) on a terminal record fails
// closed — don't join, since identity can't be confirmed. A zero busState
// (dispatch-only branch with no status yet) is not terminal and does not
// gate.
func resolvePane(bus, branch string, busState State, busUpdatedAt int64, wins []tmux.WindowOptions, panes []tmux.PaneOptions, busOf func(gitRoot string) string) (paneID string, candidates int) {
	terminal := busState == StateDone || busState == StateFailed
	byTarget := windowsByTarget(wins)
	for _, p := range panes {
		if p.ClaudeStatus == "" {
			continue
		}
		// A role-grid pane (@crew_role set) reports to the bus under
		// role:<branch>:<role>, not worker:<branch> — it must not count as a
		// second agent pane and make the join ambiguous.
		if p.CrewRole != "" {
			continue
		}
		w, ok := byTarget[p.Target]
		if !ok || w.Branch != branch || w.GitRoot == "" {
			continue
		}
		if b := busOf(w.GitRoot); b == "" || b != bus {
			continue
		}
		if terminal {
			epoch := ClaudeStatusEpoch(p.ClaudeStatus)
			if epoch <= 0 || epoch > busUpdatedAt {
				continue
			}
		}
		candidates++
		paneID = p.PaneID
	}
	if candidates != 1 {
		return "", candidates
	}
	return paneID, candidates
}

// worktreeFor is the working directory the crew layer publishes for (bus,
// branch): the lexicographically smallest @git_root among that bus's windows
// on that branch, for determinism rather than map order.
//
// With no matching window it returns "" and the reply endpoint refuses the
// run. There is deliberately no fallback root: the root set comes only from
// live windows, so with no main-checkout window open the smallest root is
// another worker's worktree, and a WORKER_TASK.md at that toplevel outranks
// the CREW_ID environment variable — the reply would run under the wrong crew
// identity.
func worktreeFor(bus, branch string, wins []tmux.WindowOptions, busOf func(gitRoot string) string) string {
	best := ""
	for _, w := range wins {
		if w.Branch != branch || w.GitRoot == "" {
			continue
		}
		if b := busOf(w.GitRoot); b == "" || b != bus {
			continue
		}
		if best == "" || w.GitRoot < best {
			best = w.GitRoot
		}
	}
	return best
}
