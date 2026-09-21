package runs

import (
	"context"
	"strings"
	"time"

	"github.com/noamsto/houston/tmux"
)

// connStates is the control-connection surface ConnectionSource reads. It is
// satisfied structurally by *tmux.ControlManager.
type connStates interface {
	// SessionStates reports connection health for every session with a
	// tracked control client. A session absent from the map has no client and
	// is not stale — there is nothing to be stale relative to.
	SessionStates() map[string]bool
	// Changes fires on every tracked client's connection-state transition.
	Changes() <-chan struct{}
}

// ConnectionSource marks runs stale while their tmux session's control
// connection is down — or while the tmux pane list itself cannot be read, so
// a tmux-server outage still marks the retained runs stale even when no pane
// WebSocket ever opened a control client. It publishes a marker layer keyed by
// pane id, so it clears itself by going Gone for that layer and never removes
// a run — the tmux and hooks layers that own the run keep it listed.
type ConnectionSource struct {
	conns connStates
	panes lister
	every time.Duration

	// stale is the set of keys currently carrying our marker; session is the
	// last known pane -> session map. session is retained across a failed
	// list so a dead tmux server — the very case that drops the connection —
	// still marks stale rather than clearing every marker. Both are owned by
	// Run's goroutine; touch them only from there, or from tick which Run
	// calls.
	stale   map[string]bool
	session map[string]string
}

func NewConnectionSource(conns connStates, panes lister, every time.Duration) *ConnectionSource {
	if every <= 0 {
		every = 2 * time.Second
	}
	return &ConnectionSource{
		conns:   conns,
		panes:   panes,
		every:   every,
		stale:   map[string]bool{},
		session: map[string]string{},
	}
}

func (s *ConnectionSource) Name() string { return "conn" }

func (s *ConnectionSource) Run(ctx context.Context, out chan<- Delta) error {
	t := time.NewTicker(s.every)
	defer t.Stop()

	if err := s.tick(ctx, out); err != nil {
		return err
	}
	changes := s.conns.Changes() // may be nil; a nil channel just never fires
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-changes:
			if err := s.tick(ctx, out); err != nil {
				return err
			}
		case <-t.C:
			if err := s.tick(ctx, out); err != nil {
				return err
			}
		}
	}
}

// tick recomputes the marker layer. On a list error it keeps the previous
// session map rather than diffing against an empty one, which is what lets a
// dead tmux server still produce Stale instead of clearing every marker.
func (s *ConnectionSource) tick(ctx context.Context, out chan<- Delta) error {
	panes, listErr := s.panes.ListPaneOptions()
	listOK := listErr == nil
	if listOK {
		s.session = sessionsByPane(panes)
	}
	states := s.conns.SessionStates()

	// A run is stale when its session's tracked control client is down, or
	// when the tmux pane list itself cannot be read — the tmux source has
	// stopped reporting and every retained run is last-known. A session with
	// no tracked client and a healthy list is not stale: there is no
	// connection to be down.
	staleFor := func(session string) bool {
		if session == "" {
			return false
		}
		if !listOK {
			return true
		}
		connected, tracked := states[session]
		return tracked && !connected
	}

	emit := func(d Delta) error {
		select {
		case out <- d:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	for key, session := range s.session {
		if staleFor(session) && !s.stale[key] {
			s.stale[key] = true
			if err := emit(Delta{Source: s.Name(), Key: key, Run: Run{Stale: true}}); err != nil {
				return err
			}
		}
	}
	for key := range s.stale {
		if !staleFor(s.session[key]) {
			delete(s.stale, key)
			if err := emit(Delta{Source: s.Name(), Key: key, Gone: true}); err != nil {
				return err
			}
		}
	}
	return nil
}

// sessionsByPane maps each pane's correlation key to its tmux session, from
// PaneOptions.Target ("session:window"). tmux session names may not contain
// ':', so the last ':' is the boundary.
func sessionsByPane(panes []tmux.PaneOptions) map[string]string {
	out := make(map[string]string, len(panes))
	for _, p := range panes {
		i := strings.LastIndexByte(p.Target, ':')
		if i <= 0 {
			continue
		}
		out[p.PaneID] = p.Target[:i]
	}
	return out
}
