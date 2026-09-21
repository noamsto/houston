package runs

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/noamsto/houston/tmux"
)

// fakeConnStates is a scriptable connStates for ConnectionSource's
// source-transform tests. A session is tracked while it is present in states;
// deleting it models a released/closed control client.
type fakeConnStates struct {
	mu     sync.Mutex
	states map[string]bool
	ch     chan struct{}
}

func newFakeConnStates() *fakeConnStates {
	return &fakeConnStates{states: map[string]bool{}, ch: make(chan struct{}, 8)}
}

func (f *fakeConnStates) SessionStates() map[string]bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[string]bool, len(f.states))
	for k, v := range f.states {
		out[k] = v
	}
	return out
}

func (f *fakeConnStates) Changes() <-chan struct{} { return f.ch }

func (f *fakeConnStates) set(session string, connected bool) {
	f.mu.Lock()
	f.states[session] = connected
	f.mu.Unlock()
	f.signal()
}

func (f *fakeConnStates) untrack(session string) {
	f.mu.Lock()
	delete(f.states, session)
	f.mu.Unlock()
	f.signal()
}

func (f *fakeConnStates) signal() {
	select {
	case f.ch <- struct{}{}:
	default:
	}
}

// fakePaneLister is a settable lister for ConnectionSource; only
// ListPaneOptions is read by this source.
type fakePaneLister struct {
	panes []tmux.PaneOptions
	err   error
}

func (f *fakePaneLister) ListWindowOptions() ([]tmux.WindowOptions, error) { return nil, f.err }
func (f *fakePaneLister) ListPaneOptions() ([]tmux.PaneOptions, error)     { return f.panes, f.err }

func recvDelta(t *testing.T, out <-chan Delta) Delta {
	t.Helper()
	select {
	case d := <-out:
		return d
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for a delta")
		return Delta{}
	}
}

func drainDeltas(out <-chan Delta) []Delta {
	var ds []Delta
	for {
		select {
		case d := <-out:
			ds = append(ds, d)
		default:
			return ds
		}
	}
}

// TestConnectionSourceTickMarksAndClearsStale is the source half of the
// acceptance regression: a disconnected session's pane is marked Stale exactly
// once, and a reconnect clears it. The producer half — a real dropped/
// reconnected ControlClient moving ControlManager.SessionStates/Changes — is
// covered by TestControlManagerSessionStatesAndChanges in the tmux package.
func TestConnectionSourceTickMarksAndClearsStale(t *testing.T) {
	conns := newFakeConnStates()
	conns.set("s", true)
	panes := &fakePaneLister{panes: []tmux.PaneOptions{{PaneID: "%1", Target: "s:1"}}}
	src := NewConnectionSource(conns, panes, time.Second)

	out := make(chan Delta, 8)
	if err := src.tick(context.Background(), out); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if ds := drainDeltas(out); len(ds) != 0 {
		t.Fatalf("connected session produced deltas: %+v", ds)
	}

	conns.set("s", false)
	if err := src.tick(context.Background(), out); err != nil {
		t.Fatalf("tick: %v", err)
	}
	d := recvDelta(t, out)
	if d.Gone || d.Key != "%1" || !d.Run.Stale {
		t.Fatalf("disconnected tick = %+v, want a Stale marker for %%1", d)
	}

	// Re-ticking while still disconnected must not re-emit the marker.
	if err := src.tick(context.Background(), out); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if ds := drainDeltas(out); len(ds) != 0 {
		t.Fatalf("re-tick re-emitted: %+v", ds)
	}

	conns.set("s", true)
	if err := src.tick(context.Background(), out); err != nil {
		t.Fatalf("tick: %v", err)
	}
	d = recvDelta(t, out)
	if !d.Gone || d.Key != "%1" {
		t.Fatalf("reconnect tick = %+v, want Gone to clear the marker", d)
	}
}

func TestConnectionSourceTickClearsOnUntrack(t *testing.T) {
	conns := newFakeConnStates()
	conns.set("s", false)
	panes := &fakePaneLister{panes: []tmux.PaneOptions{{PaneID: "%1", Target: "s:1"}}}
	src := NewConnectionSource(conns, panes, time.Second)
	out := make(chan Delta, 8)

	if err := src.tick(context.Background(), out); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if d := recvDelta(t, out); d.Gone || !d.Run.Stale {
		t.Fatalf("first tick = %+v, want Stale", d)
	}

	// The control client is released; the session is no longer tracked, so
	// there is nothing left to be stale relative to.
	conns.untrack("s")
	if err := src.tick(context.Background(), out); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if d := recvDelta(t, out); !d.Gone {
		t.Fatalf("after untrack = %+v, want Gone to clear the marker", d)
	}
}

// TestConnectionSourceKeepsMarkingStaleWhenListFails covers a dead tmux server:
// list-panes fails, but the retained pane->session map still marks the pane
// stale instead of clearing every marker.
func TestConnectionSourceKeepsMarkingStaleWhenListFails(t *testing.T) {
	conns := newFakeConnStates()
	conns.set("s", true)
	panes := &fakePaneLister{panes: []tmux.PaneOptions{{PaneID: "%1", Target: "s:1"}}}
	src := NewConnectionSource(conns, panes, time.Second)
	out := make(chan Delta, 8)

	if err := src.tick(context.Background(), out); err != nil {
		t.Fatalf("tick: %v", err)
	}

	panes.err = context.DeadlineExceeded
	conns.set("s", false)
	if err := src.tick(context.Background(), out); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if d := recvDelta(t, out); d.Gone || !d.Run.Stale {
		t.Fatalf("list-failure tick = %+v, want Stale from the retained map", d)
	}

	if err := src.tick(context.Background(), out); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if ds := drainDeltas(out); len(ds) != 0 {
		t.Fatalf("marker churned while the list was down: %+v", ds)
	}
}

// TestConnectionSourceMarksRunStaleWithoutRemovingIt is the run-level
// acceptance mapping: registry composition keeps the run listed and flips
// Stale off again on reconnect.
func TestConnectionSourceMarksRunStaleWithoutRemovingIt(t *testing.T) {
	conns := newFakeConnStates()
	conns.set("s", true)
	panes := &fakePaneLister{panes: []tmux.PaneOptions{{PaneID: "%1", Target: "s:1"}}}
	src := NewConnectionSource(conns, panes, time.Second)

	reg := NewRegistry(DefaultOrder)
	reg.Apply(Delta{Source: "tmux", Key: "%1", Run: Run{
		Agent: "claude", State: StateRunning,
		Tmux: &TmuxRef{Session: "s"},
	}})

	out := make(chan Delta, 8)
	apply := func() {
		for _, d := range drainDeltas(out) {
			reg.Apply(d)
		}
	}

	if err := src.tick(context.Background(), out); err != nil {
		t.Fatalf("tick: %v", err)
	}
	apply()
	if got := reg.Snapshot(); len(got) != 1 || got[0].Stale {
		t.Fatalf("connected Snapshot = %+v, want one live run", got)
	}

	conns.set("s", false)
	if err := src.tick(context.Background(), out); err != nil {
		t.Fatalf("tick: %v", err)
	}
	apply()
	if got := reg.Snapshot(); len(got) != 1 || !got[0].Stale {
		t.Fatalf("disconnected Snapshot = %+v, want one stale run (not removed)", got)
	}

	conns.set("s", true)
	if err := src.tick(context.Background(), out); err != nil {
		t.Fatalf("tick: %v", err)
	}
	apply()
	if got := reg.Snapshot(); len(got) != 1 || got[0].Stale {
		t.Fatalf("restored Snapshot = %+v, want one live run", got)
	}
}

func TestConnectionSourceRunReactsToChangeSignal(t *testing.T) {
	conns := newFakeConnStates()
	conns.set("s", true)
	panes := &fakePaneLister{panes: []tmux.PaneOptions{{PaneID: "%1", Target: "s:1"}}}
	src := NewConnectionSource(conns, panes, time.Hour) // ticker effectively off

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := make(chan Delta, 8)
	go func() { _ = src.Run(ctx, out) }()

	// Let the initial tick run while connected, then flip and signal.
	time.Sleep(20 * time.Millisecond)
	conns.set("s", false)

	select {
	case d := <-out:
		if d.Gone || !d.Run.Stale {
			t.Fatalf("delta after the change signal = %+v, want Stale", d)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not react to Changes()")
	}
}
