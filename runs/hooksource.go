package runs

import (
	"context"
	"log/slog"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/noamsto/houston/hook"
	"github.com/noamsto/houston/hub"
	"github.com/noamsto/houston/tmux"
)

// hookGoneCheckInterval is how often HookSource diffs against hub.Snapshot to
// catch a session hub deleted without broadcasting, and re-checks which panes
// still exist (see Run).
const hookGoneCheckInterval = 5 * time.Second

type paneLister interface {
	ListPaneOptions() ([]tmux.PaneOptions, error)
}

// HookSource publishes hook state for every engine houston's hub tracks
// (Claude Code, and pi/codex/cursor via hookyard envelopes). It rides the
// existing hub rather than re-watching the state dir and re-tailing
// transcripts.
type HookSource struct {
	hub      *hub.Hub
	panes    paneLister
	projects *projectResolver
	every    time.Duration
}

// NewHookSource returns a source over h. panes lets it end runs whose pane has
// vanished and supplies the tmux server identity the distrust rule depends
// on; nil disables both checks.
func NewHookSource(h *hub.Hub, panes paneLister) *HookSource {
	return &HookSource{hub: h, panes: panes, projects: newProjectResolver(), every: hookGoneCheckInterval}
}

func (s *HookSource) Name() string { return "hooks" }

func (s *HookSource) Run(ctx context.Context, out chan<- Delta) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	sub := s.hub.Subscribe()
	defer s.hub.Unsubscribe(sub)

	// A session whose agent died without SessionEnd (killed pane, crashed
	// tmux) leaves its state file at the last live state forever. Only a
	// successful pane listing can declare a pane gone: on error the previous
	// verdicts stand. The first listing is synchronous so no ghost is ever
	// emitted live; later ones run off this loop so a hung tmux cannot stall
	// hub delivery.
	var (
		panes  paneSet
		paneCh chan paneSet // nil without a lister, so its case never fires
	)
	if s.panes != nil {
		if ps, ok := listPanes(s.panes); ok {
			panes = ps
		}
		paneCh = make(chan paneSet)
		pollDone := make(chan struct{})
		go func() {
			defer close(pollDone)
			s.pollPanes(ctx, paneCh)
		}()
		defer func() {
			cancel()
			<-pollDone
		}()
	}

	// endedAt records the state-file time a session was ended at. Hook
	// activity newer than that proves the agent is alive somewhere houston
	// cannot see (another tmux server, a resumed session in a new pane), so it
	// is never ended again rather than flapping on every tick. The catch is a
	// dying agent's last hook write also counts as activity. Both maps are
	// keyed by session, not pane: /clear and resume leave several sessions on
	// one pane.
	endedAt := map[string]int64{}
	revived := map[string]bool{}

	// normalize drops coordinates this session cannot vouch for, so the key,
	// the TmuxRef and the dedup all agree that it owns no pane here: a pane
	// paneForeign rejects, and the pane an ended session recorded — tmux hands
	// that id to the next occupant the moment the pane is reused, and a session
	// that announced its own end can never prove it is still there (#120).
	// Only the foreign bit feeds gone: an ended session is already StateDone,
	// so routing it through the revive bookkeeping would change nothing.
	normalize := func(v hub.SessionView) (hub.SessionView, bool) {
		foreign := paneForeign(v, panes)
		if !foreign && v.State != hook.StateEnded {
			return v, false
		}
		v.TmuxSession, v.TmuxWindow, v.TmuxPane = "", "", ""
		return v, foreign
	}

	build := func(v hub.SessionView) (string, Run) {
		gone := paneGone(v, panes.live, panes.at)
		// The pane's @claude_status may only be trusted for a session that can
		// still vouch for the pane: a foreign session's pane id may already
		// belong to the next server incarnation's occupant. normalize returns
		// exactly that foreign bit (it is the same paneForeign test that keeps
		// the coordinates at all).
		origPane := v.TmuxPane
		v, foreign := normalize(v)
		var tmuxState State
		if !foreign && origPane != "" {
			if st := panes.status[origPane]; st != "" {
				tmuxState = FromClaudeStatus(st)
			}
		}
		gone = gone || foreign
		key, r := runFromSessionView(v, s.projects.resolved(v.CWD))
		// A hook turn-end `waiting` (idle_prompt, Stop, a default notification)
		// must not outrank the tmux layer's own verdict for the same pane: idle
		// means not working and neither needs a
		// human. permission_prompt is a distinct hook state and is never
		// demoted. A hook-only run has no tmux verdict to consult and keeps the
		// hook's blocked verdict.
		if v.State == hook.StateWaiting && tmuxState == StateIdle {
			r.State = tmuxState
			r.Question = nil
			// LastMessage is a waiting-only field (hub only populates it while
			// StateWaiting), so a demoted idle run must drop it too, or the
			// stale "Claude is waiting for your input" text survives the merge
			// and reaches the detail view.
			r.Activity.Message = ""
		}
		switch {
		case gone:
			if at, ok := endedAt[v.SessionID]; ok && v.UpdatedAt > at {
				revived[v.SessionID] = true
			}
			if !revived[v.SessionID] {
				endedAt[v.SessionID] = v.UpdatedAt
				return key, endRun(r)
			}
		case panes.live[v.TmuxPane]:
			delete(endedAt, v.SessionID)
			delete(revived, v.SessionID)
		}
		return key, r
	}

	// current is the session that speaks for each key: the most recently
	// updated one, so a stale session left on a reused pane can neither
	// shadow the live one nor make the run flap. It also prunes the ended
	// bookkeeping of sessions hub has dropped.
	current := func() map[string]hub.SessionView {
		snap := s.hub.Snapshot()
		sids := make(map[string]bool, len(snap))
		byKey := make(map[string]hub.SessionView, len(snap))
		for _, v := range snap {
			sids[v.SessionID] = true
			// A role-grid pane parks on the crew bus under role:<branch>:<role>,
			// not worker:<branch>; the tmux and crew layers already skip it (#99),
			// and a role pane whose Claude runs with hooks installed must not
			// slip back in through the hooks layer (#111). Gate on not-foreign
			// for the same reason as the tmux verdict below.
			if v.TmuxPane != "" && !paneForeign(v, panes) && panes.roles[v.TmuxPane] {
				continue
			}
			nv, _ := normalize(v)
			k := sessionKey(nv)
			if w, ok := byKey[k]; !ok || newerSession(v, w) {
				byKey[k] = v
			}
		}
		for sid := range endedAt {
			if !sids[sid] {
				delete(endedAt, sid)
				delete(revived, sid)
			}
		}
		return byKey
	}

	seen := map[string]string{} // key -> signature of the last emitted layer
	emit := func(key string, r Run) error {
		seen[key] = runSignature(r)
		select {
		case out <- Delta{Source: s.Name(), Key: key, Run: r}:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	// resync re-derives every session, emitting those whose layer changed since
	// last emitted (a pane verdict flipped, a project resolved late, a hub
	// update dropped on a full channel), and Gone for any session hub deleted.
	// hub deletes a session on file removal and broadcasts nothing about it,
	// so a killed agent would otherwise stay listed forever at its last known
	// state, capabilities and all.
	resync := func() error {
		now := map[string]bool{}
		for _, v := range current() {
			key, r := build(v)
			now[key] = true
			if sig, ok := seen[key]; ok && sig == runSignature(r) {
				continue
			}
			if err := emit(key, r); err != nil {
				return err
			}
		}
		for key := range seen {
			if now[key] {
				continue
			}
			delete(seen, key)
			select {
			case out <- Delta{Source: s.Name(), Key: key, Gone: true}:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return nil
	}

	for _, v := range current() {
		key, r := build(v)
		if err := emit(key, r); err != nil {
			return err
		}
	}

	t := time.NewTicker(s.every)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case v, ok := <-sub:
			if !ok {
				return nil
			}
			nv, _ := normalize(v)
			w, ok := current()[sessionKey(nv)]
			if !ok {
				continue
			}
			key, r := build(w)
			if err := emit(key, r); err != nil {
				return err
			}
		case panes = <-paneCh:
			if err := resync(); err != nil {
				return err
			}
		case <-t.C:
			if err := resync(); err != nil {
				return err
			}
		}
	}
}

type paneSet struct {
	live        map[string]bool
	at          time.Time
	server      string
	serverStart int64
	// roles marks panes carrying a non-empty @crew_role; status carries each
	// pane's raw @claude_status. Both are keyed by pane id off the same listing
	// as live, and both are only trusted for a session that can vouch for the
	// pane (not foreign).
	roles  map[string]bool
	status map[string]string
}

// listPanes lists the live panes along with the identity of the server that
// produced this listing. The identity comes off the same list-panes exec as
// the pane ids themselves, so the two can never disagree about which server
// incarnation minted them.
func listPanes(l paneLister) (paneSet, bool) {
	at := time.Now()
	panes, err := l.ListPaneOptions()
	if err != nil {
		slog.Debug("hooks: tmux pane list", "error", err)
		return paneSet{}, false
	}
	live := make(map[string]bool, len(panes))
	roles := make(map[string]bool)
	status := make(map[string]string, len(panes))
	for _, p := range panes {
		live[p.PaneID] = true
		if p.CrewRole != "" {
			roles[p.PaneID] = true
		}
		if p.ClaudeStatus != "" {
			status[p.PaneID] = p.ClaudeStatus
		}
	}
	var server string
	var serverStart int64
	if len(panes) > 0 {
		// A property of the listing, not of a pane: one exec, one server.
		server, serverStart = panes[0].ServerPID, panes[0].ServerStart
		if server == "" {
			// A tmux that cannot expand the identity leaves paneForeign with
			// nothing to compare, which silently restores the stale-pane
			// behaviour this guard exists to prevent.
			slog.Warn("hooks: pane listing carries no server identity, foreign-pane guard disabled")
		}
	}
	return paneSet{live: live, at: at, server: server, serverStart: serverStart, roles: roles, status: status}, true
}

// pollPanes publishes each successful listing until ctx ends. A failed one is
// simply not published, which is what leaves earlier verdicts standing.
func (s *HookSource) pollPanes(ctx context.Context, out chan<- paneSet) {
	t := time.NewTicker(s.every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		ps, ok := listPanes(s.panes)
		if !ok {
			continue
		}
		select {
		case out <- ps:
		case <-ctx.Done():
			return
		}
	}
}

// sessionKey is the correlation key of v's run: its pane id, else its own id.
func sessionKey(v hub.SessionView) string {
	if v.TmuxPane != "" {
		return v.TmuxPane
	}
	return "claude/" + v.SessionID
}

func newerSession(a, b hub.SessionView) bool {
	if a.UpdatedAt != b.UpdatedAt {
		return a.UpdatedAt > b.UpdatedAt
	}
	return a.SessionID > b.SessionID
}

// paneGone reports whether v's pane is provably absent. live is nil until a
// listing succeeded. The listing must postdate the state file's last write so a
// pane created after a cached listing is never mistaken for a dead one.
func paneGone(v hub.SessionView, live map[string]bool, listedAt time.Time) bool {
	return v.TmuxPane != "" && live != nil && listedAt.Unix() > v.UpdatedAt && !live[v.TmuxPane]
}

// paneForeign reports whether v's pane id was minted by a tmux server other
// than the one being listed: either v names a different server, or v was
// last written before this server started, so its pane id belongs to an
// earlier incarnation (ids restart at %0). Unknown on either side ⇒ false.
func paneForeign(v hub.SessionView, ps paneSet) bool {
	if v.TmuxPane == "" {
		return false
	}
	if ps.server != "" && v.TmuxServer != "" && v.TmuxServer != ps.server {
		return true
	}
	return ps.serverStart > 0 && v.UpdatedAt < ps.serverStart
}

// endRun marks r finished while keeping what it last showed (repo, branch,
// transcript preview), so it ages into history instead of vanishing.
func endRun(r Run) Run {
	r.State = StateDone
	r.Question = nil
	r.Activity.Tool, r.Activity.Hint, r.Activity.Message = "", "", ""
	return r
}

// runFromSessionView converts one hub view into this source's layer. The key is
// the tmux pane id when there is one, because that is what tmux and crew
// deltas can also produce; a headless session falls back to its own id and
// simply never correlates with them.
func runFromSessionView(v hub.SessionView, project string) (string, Run) {
	repo, branch := repoAndBranch(v.CWD, v.GitBranch)
	r := Run{
		Agent:     v.Agent,
		State:     FromHookState(v.State),
		Repo:      repo,
		Branch:    branch,
		Project:   project,
		Worktree:  v.CWD,
		UpdatedAt: v.UpdatedAt,
		Since:     v.Since,
		Tokens:    Tokens{Input: v.InputTokens, Output: v.OutputTokens},
		Activity: Activity{
			Tool:    v.Tool,
			Hint:    v.ToolInputHint,
			Message: v.LastMessage,
			Preview: v.Preview,
			Turn:    v.Turn,
		},
	}
	for _, c := range v.Trail {
		r.Activity.Trail = append(r.Activity.Trail, TrailChip{
			Tool: c.Tool, Hint: c.Hint, Done: c.Done, IsError: c.IsError,
		})
	}

	key := sessionKey(v)
	if v.TmuxPane != "" {
		win, _ := strconv.Atoi(v.TmuxWindow)
		r.Tmux = &TmuxRef{Session: v.TmuxSession, Window: win, PaneID: v.TmuxPane, Server: v.TmuxServer}
	}

	if r.State == StateBlocked && v.LastMessage != "" {
		r.Question = &Question{Text: v.LastMessage, Via: "pane"}
	}
	return key, r
}

// repoAndBranch names a run that has no tmux layer to inherit a name from.
// Without it the card falls back to the raw session id.
//
// Worktree-per-branch is this project's mandated topology, so the leaf
// directory is usually the branch rather than the repo — recognisably so,
// because worktrunk names the directory after the branch with "/" flattened.
func repoAndBranch(cwd, gitBranch string) (string, string) {
	if cwd == "" {
		return "", gitBranch
	}
	repo := filepath.Base(cwd)
	if gitBranch != "" && repo == strings.ReplaceAll(gitBranch, "/", "-") {
		repo = filepath.Base(filepath.Dir(cwd))
	}
	return repo, gitBranch
}
