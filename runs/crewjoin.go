package runs

import "github.com/noamsto/houston/tmux"

// resolvePane finds the one agent pane running (bus, branch), if there is
// exactly one. busOf maps a window's @git_root to its bus directory; "" means
// the root has no bus and disqualifies the window. An empty @git_root never
// reaches busOf: `git -C ""` is a documented no-op, so it would resolve to
// houston's own cwd and pull unrelated windows into that bus.
//
// A candidate must carry a non-empty @claude_status or @agent_screen, the two
// tmux options that mark an agent pane — Claude hooks stamp the former, and
// agent-detect the latter for pi, codex and cursor. Joining onto a shell pane
// would let the crew layer's Agent promote that shell into a listed run.
//
// busState is the crew-bus Run.State for this branch, from the latest status
// record, busUpdatedAt is that record's timestamp (unix seconds — see
// Run.UpdatedAt), and busSession is the s<epoch> parsed from that same
// record's worker id (sessionEpoch; 0 when the worker id is bare or unknown).
// When busState is terminal (StateDone or StateFailed), the worker session
// that wrote those bus records has ended, but its own pane commonly still
// sits idle in its window until `crew reap` reclaims it — the normal case,
// not a stale join. A terminal record still joins iff the pane looks like
// that same finished session:
//
//   - its activity epoch — ClaudeStatusEpoch(@claude_status) on a Claude pane,
//     else AgentScreenEpoch(@agent_screen) — is within terminalJoinGrace seconds
//     of the record. The worker posts its terminal status and then ends its
//     final turn, so the Stop hook stamps the pane a few seconds later, not
//     earlier;
//   - its state word says the session has finished: done/idle/error (the
//     @claude_status vocabulary — error maps to StateFailed) for a Claude pane,
//     idle for @agent_screen. An actively working pane (processing, or waiting
//     on a prompt) is a new occupant — issue #132 — even inside the grace
//     window;
//   - and its foreground engine — the tty's foreground process-group leader,
//     reached from #{pane_pid} via procStart — started within
//     [session epoch − sessionStartSlack, record]. The state word alone can't
//     tell the finished worker's preserved idle stamp from a brand-new
//     session's SessionStart idle (#158), but an engine typed into the pane
//     after the record was written started too late to have written it.
//     An unknown start (procStart returns 0) or an unknown session (a bare
//     worker id) fails closed.
//
// Engine-session identity: when the bus record carries an engine_session and
// the pane's current hook session is known, they must be equal for a terminal
// join. A `/clear` or `/resume` inside the SAME finished engine process starts
// a new session id that the process identity can't see, so the hook session no
// longer matches and the stale record does not join; either side unknown keeps
// the process rule above exactly. Residual: the bus row keeps the pre-clear
// engine_session until a resume row replaces it, so a worker that cleared
// mid-run and then finished no longer joins its own pane (it splits into a bus
// fallback card plus the hooks card) instead of showing one card.
//
// A non-positive pane epoch (0 for unknown/missing, or a malformed negative
// value) on a terminal record fails closed — don't join, since identity can't
// be confirmed. A zero busState (dispatch-only branch with no status yet) is
// not terminal and does not gate.
//
// busEngineSession is the bus record's engine_session (Run.CrewSession) and
// paneSessions the pane id → current hook session id map; both are research
// inputs for the terminal join below.
func resolvePane(bus, branch string, busState State, busUpdatedAt, busSession int64, busEngineSession string, paneSessions map[string]string, wins []tmux.WindowOptions, panes []tmux.PaneOptions, busOf func(gitRoot string) string, procStart procStartFunc) (paneID string, candidates int) {
	terminal := busState == StateDone || busState == StateFailed
	byTarget := windowsByTarget(wins)
	for _, p := range panes {
		if p.ClaudeStatus == "" && p.AgentScreen == "" {
			continue
		}
		// A role-grid pane reports to the bus under role:<branch>:<role>,
		// not worker:<branch> — it must not count as a second agent pane and
		// make the join ambiguous.
		if p.IsRolePane() {
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
			epoch := paneActivityEpoch(p)
			if epoch <= 0 || epoch > busUpdatedAt+terminalJoinGrace || !finishedPaneState(p) {
				continue
			}
			// When both sides name a session, they must be the same one: a
			// /clear or /resume starts a new session id inside the same engine
			// process, so the process identity above cannot see it. Either side
			// unknown (an older dispatcher row, codex/cursor null, no hook state
			// for the pane) falls back to that process rule, unchanged.
			if busEngineSession != "" {
				if sid := paneSessions[p.PaneID]; sid != "" && sid != busEngineSession {
					continue
				}
			}
			if !sameSession(procStart(p.PanePID), busSession, busUpdatedAt) {
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

// terminalJoinGrace bounds how far a terminal bus record's timestamp may lag
// the finished session's own last pane stamp. The worker posts its terminal
// status and then ends its final turn, so the Stop hook writes the pane's
// `done` epoch seconds later (observed +4s and +13s on a live bus). Five
// minutes covers a slow final turn with wide margin; an actively working new
// occupant is rejected by finishedPaneState, and an idle new occupant within
// the window is rejected by the process-start check below (#158).
const terminalJoinGrace int64 = 300

// sessionStartSlack tolerates procfs btime truncation (integer seconds) and
// clock granularity around the session epoch: the real margin is ~1-2s
// (shell +1s, engine +2.3s after the epoch, measured live on dispatch's
// claude path; pi's was +21s). The bound only has to reject agents that
// predate the session by minutes or more. The upper bound has no slack: an
// engine that started after the terminal record was written cannot have
// written it.
const sessionStartSlack int64 = 30

// sameSession reports whether an engine started at start could have written
// the record: 0 for start or session means unknown and never matches.
func sameSession(start, session, recordAt int64) bool {
	return start > 0 && session > 0 && start >= session-sessionStartSlack && start <= recordAt
}

// paneActivityEpoch is the candidate pane's last agent-activity epoch: the
// @claude_status epoch on a Claude pane, else the @agent_screen epoch on a
// screen-scraped one (pi, codex, cursor). 0 when neither parses.
func paneActivityEpoch(p tmux.PaneOptions) int64 {
	if p.ClaudeStatus != "" {
		return ClaudeStatusEpoch(p.ClaudeStatus)
	}
	return AgentScreenEpoch(p.AgentScreen)
}

// finishedPaneState reports whether the pane's own state word says the session
// on it has finished, so a terminal bus record may join it. A Claude pane
// finishing normally ends on `done`; a passive `idle` write preserves that
// stamp, and a StopFailure ends on `error`. A screen-scraped pane has no
// done/failed word — agent-detect's `idle` is its at-rest state, so anything
// else (processing, waiting) is an actively working occupant. A pane with
// neither option passes; it is not a candidate in the first place.
func finishedPaneState(p tmux.PaneOptions) bool {
	if p.ClaudeStatus != "" {
		switch FromClaudeStatus(p.ClaudeStatus) {
		case StateDone, StateIdle, StateFailed:
			return true
		default:
			return false
		}
	}
	if p.AgentScreen != "" {
		return AgentScreenState(p.AgentScreen) == "idle"
	}
	return true
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
