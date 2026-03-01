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
}

func NewControlManager() *ControlManager {
	return &ControlManager{
		clients: make(map[string]*managedClient),
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

	cc := NewControlClient(session)
	if err := cc.Start(); err != nil {
		return nil, err
	}

	m.clients[session] = &managedClient{client: cc, refCount: 1}
	slog.Info("started control client", "session", session)

	// Monitor for unexpected exit
	go func() {
		<-cc.Done()
		m.mu.Lock()
		delete(m.clients, session)
		m.mu.Unlock()
		slog.Info("control client exited", "session", session)
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
		go mc.client.Close()
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
