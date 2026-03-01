package opencode

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client is an HTTP client for the OpenCode server API.
type Client struct {
	baseURL    string
	httpClient *http.Client
}

// NewClient creates a new OpenCode API client.
func NewClient(baseURL string) *Client {
	return &Client{
		baseURL: strings.TrimSuffix(baseURL, "/"),
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// Health checks if the server is healthy and returns version info.
func (c *Client) Health(ctx context.Context) (*HealthResponse, error) {
	resp, err := c.get(ctx, "/global/health")
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	var health HealthResponse
	if err := json.NewDecoder(resp.Body).Decode(&health); err != nil {
		return nil, fmt.Errorf("decode health response: %w", err)
	}
	return &health, nil
}

// ListSessions returns all sessions.
func (c *Client) ListSessions(ctx context.Context) ([]Session, error) {
	resp, err := c.get(ctx, "/session")
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	var sessions []Session
	if err := json.NewDecoder(resp.Body).Decode(&sessions); err != nil {
		return nil, fmt.Errorf("decode sessions: %w", err)
	}
	return sessions, nil
}

// GetSession returns a single session by ID.
func (c *Client) GetSession(ctx context.Context, id string) (*Session, error) {
	resp, err := c.get(ctx, "/session/"+id)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	var session Session
	if err := json.NewDecoder(resp.Body).Decode(&session); err != nil {
		return nil, fmt.Errorf("decode session: %w", err)
	}
	return &session, nil
}

// GetSessionStatus returns the status of all sessions.
func (c *Client) GetSessionStatus(ctx context.Context) (map[string]SessionStatus, error) {
	resp, err := c.get(ctx, "/session/status")
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	var statuses map[string]SessionStatus
	if err := json.NewDecoder(resp.Body).Decode(&statuses); err != nil {
		return nil, fmt.Errorf("decode session status: %w", err)
	}
	return statuses, nil
}

// GetMessages returns messages for a session.
func (c *Client) GetMessages(ctx context.Context, sessionID string, limit int) ([]MessageWithParts, error) {
	path := fmt.Sprintf("/session/%s/message", sessionID)
	if limit > 0 {
		path = fmt.Sprintf("%s?limit=%d", path, limit)
	}

	resp, err := c.get(ctx, path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	var messages []MessageWithParts
	if err := json.NewDecoder(resp.Body).Decode(&messages); err != nil {
		return nil, fmt.Errorf("decode messages: %w", err)
	}
	return messages, nil
}

// GetTodos returns the todo list for a session.
func (c *Client) GetTodos(ctx context.Context, sessionID string) ([]Todo, error) {
	resp, err := c.get(ctx, fmt.Sprintf("/session/%s/todo", sessionID))
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	var todos []Todo
	if err := json.NewDecoder(resp.Body).Decode(&todos); err != nil {
		return nil, fmt.Errorf("decode todos: %w", err)
	}
	return todos, nil
}

// SendPrompt sends a prompt to a session and waits for the response.
func (c *Client) SendPrompt(ctx context.Context, sessionID string, req PromptRequest) (*MessageWithParts, error) {
	resp, err := c.post(ctx, fmt.Sprintf("/session/%s/message", sessionID), req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	var msg MessageWithParts
	if err := json.NewDecoder(resp.Body).Decode(&msg); err != nil {
		return nil, fmt.Errorf("decode message: %w", err)
	}
	return &msg, nil
}

// SendPromptAsync sends a prompt without waiting for a response.
func (c *Client) SendPromptAsync(ctx context.Context, sessionID string, req PromptRequest) error {
	resp, err := c.post(ctx, fmt.Sprintf("/session/%s/prompt_async", sessionID), req)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	return nil
}

// AbortSession aborts a running session.
func (c *Client) AbortSession(ctx context.Context, sessionID string) error {
	resp, err := c.post(ctx, fmt.Sprintf("/session/%s/abort", sessionID), nil)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	return nil
}

// CreateSession creates a new session.
func (c *Client) CreateSession(ctx context.Context, title string, parentID *string) (*Session, error) {
	body := map[string]any{}
	if title != "" {
		body["title"] = title
	}
	if parentID != nil {
		body["parentID"] = *parentID
	}

	resp, err := c.post(ctx, "/session", body)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	var session Session
	if err := json.NewDecoder(resp.Body).Decode(&session); err != nil {
		return nil, fmt.Errorf("decode session: %w", err)
	}
	return &session, nil
}

// DeleteSession deletes a session.
func (c *Client) DeleteSession(ctx context.Context, sessionID string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.baseURL+"/session/"+sessionID, nil)
	if err != nil {
		return err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	_ = resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("delete session failed: %s", resp.Status)
	}
	return nil
}

// GetAgents returns all available agents.
func (c *Client) GetAgents(ctx context.Context) ([]Agent, error) {
	resp, err := c.get(ctx, "/agent")
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	var agents []Agent
	if err := json.NewDecoder(resp.Body).Decode(&agents); err != nil {
		return nil, fmt.Errorf("decode agents: %w", err)
	}
	return agents, nil
}

// GetCurrentProject returns the current project.
func (c *Client) GetCurrentProject(ctx context.Context) (*Project, error) {
	resp, err := c.get(ctx, "/project/current")
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	var project Project
	if err := json.NewDecoder(resp.Body).Decode(&project); err != nil {
		return nil, fmt.Errorf("decode project: %w", err)
	}
	return &project, nil
}

// SubscribeEvents opens an SSE connection to receive real-time events.
// The returned channel will receive events until the context is cancelled.
// The caller must read from the channel to prevent blocking.
func (c *Client) SubscribeEvents(ctx context.Context) (<-chan Event, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/event", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")

	// Use a client without timeout for SSE
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("connect to event stream: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("event stream returned %s", resp.Status)
	}

	events := make(chan Event, 100)

	go func() {
		defer func() { _ = resp.Body.Close() }()
		defer close(events)

		reader := bufio.NewReader(resp.Body)
		var eventData strings.Builder

		for {
			select {
			case <-ctx.Done():
				return
			default:
			}

			line, err := reader.ReadString('\n')
			if err != nil {
					return
			}

			line = strings.TrimSpace(line)

			// SSE format: "data: {...}"
			if data, ok := strings.CutPrefix(line, "data: "); ok {
				eventData.WriteString(data)
			} else if line == "" && eventData.Len() > 0 {
				// Empty line = end of event
				var event Event
				if err := json.Unmarshal([]byte(eventData.String()), &event); err == nil {
					select {
					case events <- event:
					case <-ctx.Done():
						return
					}
				}
				eventData.Reset()
			}
		}
	}()

	return events, nil
}

// get performs a GET request.
func (c *Client) get(ctx context.Context, path string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request %s: %w", path, err)
	}

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return nil, fmt.Errorf("request %s failed: %s - %s", path, resp.Status, string(body))
	}

	return resp, nil
}

// post performs a POST request with JSON body.
func (c *Client) post(ctx context.Context, path string, body any) (*http.Response, error) {
	var bodyReader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal body: %w", err)
		}
		bodyReader = strings.NewReader(string(data))
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bodyReader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request %s: %w", path, err)
	}

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return nil, fmt.Errorf("request %s failed: %s - %s", path, resp.Status, string(body))
	}

	return resp, nil
}

// IsAvailable checks if an OpenCode server is running at the given URL.
func IsAvailable(ctx context.Context, baseURL string) bool {
	client := NewClient(baseURL)
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	health, err := client.Health(ctx)
	return err == nil && health.Healthy
}
