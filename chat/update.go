package chat

import "encoding/json"

// SessionUpdate values (ACP session/update kinds this package emits).
const (
	SessionUpdateUserMessageChunk  = "user_message_chunk"
	SessionUpdateAgentMessageChunk = "agent_message_chunk"
	SessionUpdateToolCall          = "tool_call"
	SessionUpdateToolCallUpdate    = "tool_call_update"
	SessionUpdatePlan              = "plan"
)

// Status values for tool_call / tool_call_update.
const (
	StatusPending    = "pending"
	StatusInProgress = "in_progress"
	StatusCompleted  = "completed"
	StatusFailed     = "failed"
)

// Kind values for tool_call / tool_call_update.
const (
	KindRead       = "read"
	KindEdit       = "edit"
	KindDelete     = "delete"
	KindMove       = "move"
	KindSearch     = "search"
	KindExecute    = "execute"
	KindThink      = "think"
	KindFetch      = "fetch"
	KindSwitchMode = "switch_mode"
	KindOther      = "other"
)

// Update is one ACP session/update notification, plus the three fields a
// replayable, resumable stream needs (ID, Seq, TS). Seq is left 0 by every
// Reader — it's assigned by the caller's ring, not the transcript.
type Update struct {
	ID  string `json:"id"`
	Seq uint64 `json:"seq"`
	TS  int64  `json:"ts"` // unix ms, from the record; 0 if unparseable

	SessionUpdate string    `json:"sessionUpdate"`
	Content       []Content `json:"content,omitempty"`

	// tool_call / tool_call_update
	ToolCallID string          `json:"toolCallId,omitempty"`
	Title      string          `json:"title,omitempty"`  // human line, e.g. ToolTitle
	Kind       string          `json:"kind,omitempty"`   // read|edit|delete|move|search|execute|think|fetch|switch_mode|other
	Status     string          `json:"status,omitempty"` // pending|in_progress|completed|failed
	Locations  []Location      `json:"locations,omitempty"`
	RawInput   json.RawMessage `json:"rawInput,omitempty"` // omitted on the stream; only Tool sets this

	Meta map[string]any `json:"_meta,omitempty"`
}

// ContentBlock is a plain text block inside a Content.
type ContentBlock struct {
	Type string `json:"type"` // "text"
	Text string `json:"text"`
}

// Content is a message's or a tool result's content: either text
// ({"type":"content","content":{"type":"text","text":...}}) or a diff
// ({"type":"diff","path":...,"oldText":...,"newText":...}).
type Content struct {
	Type    string        `json:"type"`
	Content *ContentBlock `json:"content,omitempty"`

	Path    string `json:"path,omitempty"`
	OldText string `json:"oldText,omitempty"`
	NewText string `json:"newText,omitempty"`
}

// Location points at a file (and optionally a line) a tool call touched.
type Location struct {
	Path string `json:"path"`
	Line int    `json:"line,omitempty"`
}

// Cursor is a Reader's resume point. Offset is the byte offset of the next
// unread line. Pending is reader-private carry-over state, opaque to
// callers; the claude reader keeps a fingerprint of the bytes before Offset
// there so a replaced file is detected.
type Cursor struct {
	Offset  int64           `json:"offset"`
	Pending json.RawMessage `json:"pending,omitempty"`
}

// Reader turns one engine's transcript file into ACP Updates.
// See the package doc for the chunking-independence and concurrency
// contract every implementation must satisfy.
type Reader interface {
	// Engine names the engine this Reader handles (e.g. "claude-code").
	Engine() string
	// Read returns the updates since from, and the cursor to resume at.
	// reset is true when the file no longer continues from from (it got
	// shorter, or the bytes before Offset changed); the caller must treat the
	// returned updates as a fresh stream starting at ordinal 1.
	Read(path string, from Cursor) (updates []Update, next Cursor, reset bool, err error)
	// Tool returns the full detail (rawInput, output/diff) of one tool
	// call, for the expand-on-tap route. ErrToolNotFound if no tool_use
	// with that id appears in the file.
	Tool(path, toolCallID string) (*Update, error)
}

// For returns the Reader for engine, or nil if none is registered.
func For(engine string) Reader {
	return readers[engine]
}
