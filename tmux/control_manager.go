package tmux

import (
	"log/slog"
	"sync"
)

type managedClient struct {
	client   *ControlClient
	refCount int
}

// ControlManager manages ControlClient instances across sessions.
// Creates on demand, ref-counted, cleans up when last reference released.
type ControlManager struct {
	mu      sync.Mutex
	clients map[string]*managedClient

	// changes coalesces every tracked client's connect/disconnect transitions
	// into one signal. A reader re-reads SessionStates(); it does not count
	// edges. Buffered to one so a transition never blocks a client lifecycle.
	changes chan struct{}

	// newClient builds the per-session client. Unexported test seam: production
	// leaves it NewControlClient, in-package tests substitute a client whose
	// dial is faked so the watcher/aggregation runs without a tmux binary.
	newClient func(session string) *ControlClient
}

func NewControlManager() *ControlManager {
	return &ControlManager{
		clients:   make(map[string]*managedClient),
		changes:   make(chan struct{}, 1),
		newClient: NewControlClient,
	}
}

// Changes receives on every tracked client's connection-state transition,
// coalesced. SessionStates() is the value to re-read on a signal.
func (m *ControlManager) Changes() <-chan struct{} { return m.changes }

// SessionStates reports connection health for every session with a tracked
// control client. A session absent from the map has no client and is not
// "stale" — there is nothing to be stale relative to.
func (m *ControlManager) SessionStates() map[string]bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]bool, len(m.clients))
	for session, mc := range m.clients {
		out[session] = mc.client.Connected()
	}
	return out
}

// signalChange wakes Changes' reader without blocking the caller.
func (m *ControlManager) signalChange() {
	select {
	case m.changes <- struct{}{}:
	default:
	}
}

// GetClient returns a ControlClient for the session, creating one if needed.
// Each call increments the ref count; call ReleaseClient when done.
func (m *ControlManager) GetClient(session string) (*ControlClient, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if mc, ok := m.clients[session]; ok {
		mc.refCount++
		return mc.client, nil
	}

	cc := m.newClient(session)
	if err := cc.Start(); err != nil {
		return nil, err
	}

	m.clients[session] = &managedClient{client: cc, refCount: 1}
	slog.Info("started control client", "session", session)

	// The client reconnects on its own; Done() now fires only on Close.
	go func() {
		<-cc.Done()
		m.mu.Lock()
		delete(m.clients, session)
		m.mu.Unlock()
		slog.Info("control client closed", "session", session)
	}()

	// Forward this client's connection-state changes into the manager-wide
	// signal. Start() has already attached and left one buffered transition,
	// so this watcher also announces the new session.
	go func() {
		for {
			select {
			case <-cc.StateChanged():
				m.signalChange()
			case <-cc.Done():
				return
			}
		}
	}()

	return cc, nil
}

// ReleaseClient decrements the ref count and closes the client when it reaches 0.
func (m *ControlManager) ReleaseClient(session string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	mc, ok := m.clients[session]
	if !ok {
		return
	}

	mc.refCount--
	if mc.refCount <= 0 {
		delete(m.clients, session)
		go func() { _ = mc.client.Close() }()
		m.signalChange() // the session is no longer tracked; clear any marker
		slog.Info("closed control client (no more subscribers)", "session", session)
	}
}

// Close shuts down all control clients.
func (m *ControlManager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for session, mc := range m.clients {
		_ = mc.client.Close()
		delete(m.clients, session)
		slog.Info("closed control client (shutdown)", "session", session)
	}
}
