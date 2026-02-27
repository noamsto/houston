package opencode

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// SessionState represents the computed state of an OpenCode session.
type SessionState struct {
	Session        Session
	Status         string // "idle", "busy", "error", "needs_attention"
	LastMessage    *MessageWithParts
	LastActivity   string // Brief description of last activity
	Todos          []Todo
	ActiveTodos    int
	CompletedTodos int
	Project        *Project
	ServerURL      string
}

// Manager provides high-level operations for OpenCode integration.
type Manager struct {
	discovery *Discovery

	// Cache of session states per server
	states   map[string][]SessionState // serverURL -> session states
	statesMu sync.RWMutex

	// Event subscriptions per server
	eventCtxs map[string]context.CancelFunc
	eventsMu  sync.Mutex
}

// NewManager creates a new OpenCode manager.
func NewManager(discovery *Discovery) *Manager {
	return &Manager{
		discovery: discovery,
		states:    make(map[string][]SessionState),
		eventCtxs: make(map[string]context.CancelFunc),
	}
}

// GetAllSessions returns session states from all discovered servers.
func (m *Manager) GetAllSessions(ctx context.Context) []SessionState {
	servers := m.discovery.GetServers()
	if len(servers) == 0 {
		return nil
	}

	var allStates []SessionState
	var mu sync.Mutex
	var wg sync.WaitGroup

	for _, server := range servers {
		wg.Add(1)
		go func(server *Server) {
			defer wg.Done()

			states, err := m.fetchServerSessions(ctx, server)
			if err != nil {
				slog.Warn("failed to fetch OpenCode sessions",
					"server", server.URL,
					"error", err)
				return
			}

			mu.Lock()
			allStates = append(allStates, states...)
			mu.Unlock()
		}(server)
	}

	wg.Wait()
	return allStates
}

// fetchServerSessions gets sessions from a single server.
func (m *Manager) fetchServerSessions(ctx context.Context, server *Server) ([]SessionState, error) {
	client := NewClient(server.URL)

	// Get sessions and their statuses in parallel
	var sessions []Session
	var statuses map[string]SessionStatus
	var sessErr, statusErr error

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		sessions, sessErr = client.ListSessions(ctx)
	}()

	go func() {
		defer wg.Done()
		statuses, statusErr = client.GetSessionStatus(ctx)
	}()

	wg.Wait()

	if sessErr != nil {
		return nil, sessErr
	}
	if statusErr != nil {
		slog.Debug("could not get session statuses", "error", statusErr)
		statuses = make(map[string]SessionStatus)
	}

	// Build session states with last message preview
	states := make([]SessionState, 0, len(sessions))

	// Fetch last message for each session in parallel (limit concurrency)
	type sessionWithMsg struct {
		sess Session
		msg  *MessageWithParts
	}
	msgChan := make(chan sessionWithMsg, len(sessions))

	var msgWg sync.WaitGroup
	sem := make(chan struct{}, 5) // Max 5 concurrent requests

	for _, sess := range sessions {
		msgWg.Add(1)
		go func(s Session) {
			defer msgWg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			msgs, err := client.GetMessages(ctx, s.ID, 1)
			var lastMsg *MessageWithParts
			if err == nil && len(msgs) > 0 {
				lastMsg = &msgs[0]
			}
			msgChan <- sessionWithMsg{sess: s, msg: lastMsg}
		}(sess)
	}

	go func() {
		msgWg.Wait()
		close(msgChan)
	}()

	for sm := range msgChan {
		state := SessionState{
			Session:   sm.sess,
			Status:    "idle",
			Project:   server.Project,
			ServerURL: server.URL,
		}

		// Apply status from status endpoint
		if status, ok := statuses[sm.sess.ID]; ok {
			state.Status = status.Status
		}

		// Extract last activity from message
		if sm.msg != nil {
			state.LastMessage = sm.msg
			state.LastActivity = extractActivity(sm.msg)
		}

		states = append(states, state)
	}

	// Cache states
	m.statesMu.Lock()
	m.states[server.URL] = states
	m.statesMu.Unlock()

	return states, nil
}

// extractActivity gets a brief description from a message.
func extractActivity(msg *MessageWithParts) string {
	if msg == nil || len(msg.Parts) == 0 {
		return ""
	}

	// Look for text content
	for _, part := range msg.Parts {
		if part.Type == "text" && part.Text != "" {
			// Truncate to first line or 60 chars
			text := part.Text
			if idx := firstLineBreak(text); idx > 0 {
				text = text[:idx]
			}
			if len(text) > 60 {
				text = text[:57] + "..."
			}
			return text
		}
		// For tool invocations, show the tool name
		if part.Type == "tool-invocation" && part.ToolName != "" {
			return "Using " + part.ToolName
		}
	}
	return ""
}

func firstLineBreak(s string) int {
	for i, c := range s {
		if c == '\n' || c == '\r' {
			return i
		}
	}
	return -1
}

// GetSessionDetails fetches full details for a session.
func (m *Manager) GetSessionDetails(ctx context.Context, serverURL, sessionID string) (*SessionState, error) {
	client := NewClient(serverURL)

	// Fetch session, messages, and todos in parallel
	var session *Session
	var messages []MessageWithParts
	var todos []Todo
	var sessErr, msgErr, todoErr error

	var wg sync.WaitGroup
	wg.Add(3)

	go func() {
		defer wg.Done()
		session, sessErr = client.GetSession(ctx, sessionID)
	}()

	go func() {
		defer wg.Done()
		messages, msgErr = client.GetMessages(ctx, sessionID, 10)
	}()

	go func() {
		defer wg.Done()
		todos, todoErr = client.GetTodos(ctx, sessionID)
	}()

	wg.Wait()

	if sessErr != nil {
		return nil, sessErr
	}

	state := &SessionState{
		Session:   *session,
		Status:    "idle",
		ServerURL: serverURL,
	}

	if msgErr == nil && len(messages) > 0 {
		state.LastMessage = &messages[len(messages)-1]
	}

	if todoErr == nil {
		state.Todos = todos
		for _, t := range todos {
			switch t.Status {
			case "pending", "in_progress":
				state.ActiveTodos++
			case "completed":
				state.CompletedTodos++
			}
		}
	}

	// Get status
	statuses, err := client.GetSessionStatus(ctx)
	if err == nil {
		if status, ok := statuses[sessionID]; ok {
			state.Status = status.Status
		}
	}

	// Get project
	project, err := client.GetCurrentProject(ctx)
	if err == nil {
		state.Project = project
	}

	return state, nil
}

// SendPrompt sends a text prompt to a session.
func (m *Manager) SendPrompt(ctx context.Context, serverURL, sessionID, text string) error {
	client := NewClient(serverURL)

	req := PromptRequest{
		Parts: []PromptPart{
			{Type: "text", Text: text},
		},
	}

	// Use async to not block
	return client.SendPromptAsync(ctx, sessionID, req)
}

// AbortSession aborts a running session.
func (m *Manager) AbortSession(ctx context.Context, serverURL, sessionID string) error {
	client := NewClient(serverURL)
	return client.AbortSession(ctx, sessionID)
}

// SubscribeToServer starts listening for events from a server.
func (m *Manager) SubscribeToServer(ctx context.Context, serverURL string, handler func(Event)) error {
	client := NewClient(serverURL)

	events, err := client.SubscribeEvents(ctx)
	if err != nil {
		return err
	}

	// Cancel any existing subscription
	m.eventsMu.Lock()
	if cancel, ok := m.eventCtxs[serverURL]; ok {
		cancel()
	}
	ctx, cancel := context.WithCancel(ctx)
	m.eventCtxs[serverURL] = cancel
	m.eventsMu.Unlock()

	go func() {
		for event := range events {
			select {
			case <-ctx.Done():
				return
			default:
				handler(event)
			}
		}
	}()

	return nil
}

// UnsubscribeFromServer stops listening for events from a server.
func (m *Manager) UnsubscribeFromServer(serverURL string) {
	m.eventsMu.Lock()
	if cancel, ok := m.eventCtxs[serverURL]; ok {
		cancel()
		delete(m.eventCtxs, serverURL)
	}
	m.eventsMu.Unlock()
}

// GetCachedStates returns cached session states (for fast access).
func (m *Manager) GetCachedStates() []SessionState {
	m.statesMu.RLock()
	defer m.statesMu.RUnlock()

	var all []SessionState
	for _, states := range m.states {
		all = append(all, states...)
	}
	return all
}

// StartBackgroundRefresh starts periodic session refresh.
func (m *Manager) StartBackgroundRefresh(ctx context.Context, interval time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				m.GetAllSessions(ctx)
			}
		}
	}()
}

// Close cleans up all subscriptions.
func (m *Manager) Close() {
	m.eventsMu.Lock()
	for _, cancel := range m.eventCtxs {
		cancel()
	}
	m.eventCtxs = make(map[string]context.CancelFunc)
	m.eventsMu.Unlock()
}
