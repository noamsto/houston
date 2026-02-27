// Package opencode provides a client for the OpenCode AI coding agent API.
// OpenCode is an open source AI coding agent with a client/server architecture.
// See: https://opencode.ai/docs/server
package opencode

import "time"

// Session represents an OpenCode session.
type Session struct {
	ID        string     `json:"id"`
	Title     string     `json:"title"`
	CreatedAt time.Time  `json:"createdAt"`
	UpdatedAt time.Time  `json:"updatedAt"`
	ParentID  *string    `json:"parentId,omitempty"`
	Share     *ShareInfo `json:"share,omitempty"`
}

// ShareInfo contains sharing information for a session.
type ShareInfo struct {
	URL       string    `json:"url"`
	CreatedAt time.Time `json:"createdAt"`
}

// SessionStatus represents the current status of a session.
type SessionStatus struct {
	Status    string `json:"status"` // "idle", "busy", "error"
	SessionID string `json:"sessionId"`
}

// Message represents a message in a session.
type Message struct {
	ID        string    `json:"id"`
	SessionID string    `json:"sessionId"`
	Role      string    `json:"role"` // "user", "assistant"
	CreatedAt time.Time `json:"createdAt"`
}

// MessageWithParts combines a message with its parts.
type MessageWithParts struct {
	Info  Message `json:"info"`
	Parts []Part  `json:"parts"`
}

// Part represents a part of a message (text, tool call, etc).
type Part struct {
	Type string `json:"type"` // "text", "tool-invocation", "tool-result", etc.

	// For text parts
	Text string `json:"text,omitempty"`

	// For tool parts
	ToolName string      `json:"toolName,omitempty"`
	ToolID   string      `json:"toolId,omitempty"`
	Args     interface{} `json:"args,omitempty"`
	State    string      `json:"state,omitempty"` // "pending", "running", "complete", "error"
	Result   interface{} `json:"result,omitempty"`
}

// Todo represents a todo item in a session.
type Todo struct {
	ID        string `json:"id"`
	Content   string `json:"content"`
	Status    string `json:"status"` // "pending", "in_progress", "completed", "cancelled"
	Priority  string `json:"priority"`
	SessionID string `json:"sessionId"`
}

// Agent represents an available agent.
type Agent struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

// Provider represents an LLM provider.
type Provider struct {
	ID     string  `json:"id"`
	Name   string  `json:"name"`
	Models []Model `json:"models,omitempty"`
}

// Model represents an LLM model.
type Model struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Provider string `json:"provider"`
}

// Project represents an OpenCode project.
type Project struct {
	Path string `json:"path"`
	Name string `json:"name"`
}

// HealthResponse is the response from the health endpoint.
type HealthResponse struct {
	Healthy bool   `json:"healthy"`
	Version string `json:"version"`
}

// Event represents an SSE event from the OpenCode server.
type Event struct {
	Type       string                 `json:"type"`
	Properties map[string]interface{} `json:"properties,omitempty"`
}

// EventType constants for SSE events.
const (
	EventServerConnected   = "server.connected"
	EventSessionCreated    = "session.created"
	EventSessionUpdated    = "session.updated"
	EventSessionDeleted    = "session.deleted"
	EventSessionIdle       = "session.idle"
	EventSessionStatus     = "session.status"
	EventSessionError      = "session.error"
	EventMessageUpdated    = "message.updated"
	EventMessageRemoved    = "message.removed"
	EventToolExecuteBefore = "tool.execute.before"
	EventToolExecuteAfter  = "tool.execute.after"
	EventTodoUpdated       = "todo.updated"
	EventPermissionUpdated = "permission.updated"
)

// PromptRequest is the request body for sending a prompt.
type PromptRequest struct {
	Parts   []PromptPart   `json:"parts"`
	Model   *ModelSelector `json:"model,omitempty"`
	Agent   string         `json:"agent,omitempty"`
	NoReply bool           `json:"noReply,omitempty"`
}

// PromptPart is a part of a prompt (text, image, etc).
type PromptPart struct {
	Type string `json:"type"` // "text", "image"
	Text string `json:"text,omitempty"`
	// For images: URL or base64 data
	URL  string `json:"url,omitempty"`
	Data string `json:"data,omitempty"`
}

// ModelSelector specifies which model to use.
type ModelSelector struct {
	ProviderID string `json:"providerID"`
	ModelID    string `json:"modelID"`
}
