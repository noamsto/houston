// Package hub watches hook state files and Claude Code transcripts and
// exposes a live, aggregated view of every active Claude session.
//
// State comes from two sources:
//
//   - hook state files at <state-dir>/claude/<session-id>.json,
//     written by `houston hook` (status, tool, timing).
//
//   - transcript JSONL at the path the state file points to
//     (activity trail, token/cost telemetry).
//
// The hub owns both.
package hub

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"strings"
	"time"

	"github.com/noamsto/houston/hook"
)

// TranscriptEvent type constants.
const (
	EventTypeToolUse    = "tool_use"
	EventTypeToolResult = "tool_result"
	EventTypeText       = "text"
	EventTypeThinking   = "thinking"
)

// TranscriptEvent is a normalized subset of a single JSONL line from a
// Claude Code transcript. Not every field is populated for every event; the
// UI consumer decides what's interesting.
type TranscriptEvent struct {
	Offset           int64     // byte offset of the line start in the file
	Timestamp        time.Time // event timestamp (best effort)
	Type             string    // "user" | "assistant" | "tool_use" | "tool_result" | "thinking" | "text"
	Role             string    // for user/assistant messages
	ToolName         string    // for tool_use / tool_result
	ToolUseID        string    // links tool_use to tool_result
	IsError          bool      // tool_result failure
	Text             string    // plain text content (assistant text / thinking / tool inputs squashed)
	CWD              string    // working directory recorded on the record, if any
	GitBranch        string    // git branch recorded on the record, if any
	InputTokens      int
	OutputTokens     int
	CacheReadTokens  int
	CacheWriteTokens int

	// Model, ContextTokens and CostUSD ride on the last event of an assistant
	// message that carries usage. ContextTokens is everything the model read
	// for that message (uncached + cached input); CostUSD is pi's recorded
	// per-message cost, nil for engines that record none.
	Model         string
	ContextTokens int
	CostUSD       *float64

	// Background is the kind ("shell" or "monitor") of a tool_use that starts
	// a background task; StopTask is the task id a TaskStop tool_use targets.
	Background     string
	BackgroundHint string
	// BackgroundTimeout is a non-persistent Monitor's lifetime, else zero.
	BackgroundTimeout time.Duration
	StopTask          string
}

// jsonlRecord is the on-disk envelope. Unmarshalled loosely — the schema
// evolves and one new field shouldn't poison a whole transcript.
type jsonlRecord struct {
	Type      string          `json:"type"`
	Timestamp string          `json:"timestamp"`
	CWD       string          `json:"cwd,omitempty"`
	GitBranch string          `json:"gitBranch,omitempty"`
	Message   *anthropicMsg   `json:"message,omitempty"`
	ToolName  string          `json:"tool_name,omitempty"`
	ToolInput json.RawMessage `json:"tool_input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`

	// queue-operation records carry the queued text as a bare string.
	Operation string          `json:"operation,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`
}

type anthropicMsg struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
	Model   string          `json:"model,omitempty"`
	Usage   *usage          `json:"usage,omitempty"`
}

type contentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	Thinking  string          `json:"thinking,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"` // tool_result body
}

type usage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`

	// pi's shape: input/output are uncached, cacheRead/cacheWrite the cached
	// parts, cost.total the message's recorded dollars.
	PiInput      int      `json:"input"`
	PiOutput     int      `json:"output"`
	PiCacheRead  int      `json:"cacheRead"`
	PiCacheWrite int      `json:"cacheWrite"`
	PiCost       *piCosts `json:"cost,omitempty"`
}

type piCosts struct {
	Total *float64 `json:"total"`
}

// ReadTranscriptFrom reads JSONL events from byteOffset and returns the new
// offset so callers can resume.
func ReadTranscriptFrom(path string, byteOffset int64) ([]TranscriptEvent, int64, error) {
	// Polled every tick for every session; most haven't grown.
	fi, err := os.Stat(path)
	if err != nil {
		return nil, byteOffset, err
	}
	if fi.Size() <= byteOffset {
		return nil, byteOffset, nil
	}

	f, err := os.Open(path) //nolint:gosec // transcript path is agent-supplied via hook state (not a request); reading it is the feature
	if err != nil {
		return nil, byteOffset, err
	}
	defer func() { _ = f.Close() }()

	if _, err := f.Seek(byteOffset, io.SeekStart); err != nil {
		return nil, byteOffset, err
	}

	r := bufio.NewReaderSize(f, 64*1024)
	var (
		events []TranscriptEvent
		pos    = byteOffset
	)
	for {
		line, err := r.ReadBytes('\n')
		if err == io.EOF && !bytes.HasSuffix(line, []byte{'\n'}) && !json.Valid(bytes.TrimSpace(line)) {
			// A line Claude is still writing: leave it for the next poll,
			// or a start/end record the background tracker needs is lost.
			break
		}
		n := int64(len(line))
		trimmed := strings.TrimSpace(string(line))
		if trimmed != "" {
			events = append(events, parseLine(trimmed, pos)...)
		}
		pos += n
		if err == io.EOF {
			break
		}
		if err != nil {
			return events, pos, err
		}
	}
	return events, pos, nil
}

func parseLine(line string, offset int64) []TranscriptEvent {
	var rec jsonlRecord
	if err := json.Unmarshal([]byte(line), &rec); err != nil {
		return nil
	}
	ts, _ := time.Parse(time.RFC3339Nano, rec.Timestamp)
	base := TranscriptEvent{Offset: offset, Timestamp: ts, Type: rec.Type, CWD: rec.CWD, GitBranch: rec.GitBranch}

	// Top-level tool_use / tool_result rows (some schema variants).
	if rec.ToolName != "" {
		base.ToolName = rec.ToolName
		base.ToolUseID = rec.ToolUseID
		base.IsError = rec.IsError
		base.Text = hook.ToolHint(rec.ToolName, rec.ToolInput)
		return []TranscriptEvent{base}
	}

	if rec.Type == "queue-operation" && rec.Operation == "enqueue" && len(rec.Content) > 0 && rec.Content[0] == '"' {
		var s string
		if err := json.Unmarshal(rec.Content, &s); err == nil && strings.Contains(s, "<task-notification>") {
			base.Type, base.Role, base.Text = EventTypeText, "user", s
		}
		return []TranscriptEvent{base}
	}

	if rec.Message == nil || len(rec.Message.Content) == 0 {
		return []TranscriptEvent{base}
	}

	var blocks []contentBlock
	// content may be a string OR an array of blocks.
	if rec.Message.Content[0] == '"' {
		var s string
		if err := json.Unmarshal(rec.Message.Content, &s); err == nil {
			blocks = append(blocks, contentBlock{Type: "text", Text: s})
		}
	} else {
		_ = json.Unmarshal(rec.Message.Content, &blocks)
	}

	var out []TranscriptEvent
	for _, blk := range blocks {
		ev := base
		ev.Role = rec.Message.Role
		switch blk.Type {
		case EventTypeText:
			ev.Type = EventTypeText
			ev.Text = blk.Text
		case EventTypeThinking:
			ev.Type = EventTypeThinking
			ev.Text = blk.Thinking
		case EventTypeToolUse:
			ev.Type = EventTypeToolUse
			ev.ToolName = blk.Name
			ev.ToolUseID = blk.ID
			ev.Text = hook.ToolHint(blk.Name, blk.Input)
			if ev.Background, ev.StopTask = backgroundStart(blk.Name, blk.Input); ev.Background != "" {
				ev.BackgroundHint = backgroundHint(blk.Input)
				if ev.Background == BackgroundMonitor {
					ev.BackgroundTimeout = monitorTimeout(blk.Input)
				}
			}
		case EventTypeToolResult:
			ev.Type = EventTypeToolResult
			ev.ToolUseID = blk.ToolUseID
			ev.IsError = blk.IsError
			ev.Text = extractToolResultText(blk.Content)
		default:
			ev.Type = blk.Type
		}
		out = append(out, ev)
	}

	if u := rec.Message.Usage; u != nil {
		if len(out) == 0 {
			// An aborted pi message has usage but no content blocks.
			ev := base
			ev.Role = rec.Message.Role
			out = append(out, ev)
		}
		last := &out[len(out)-1]
		last.InputTokens = u.InputTokens + u.PiInput
		last.OutputTokens = u.OutputTokens + u.PiOutput
		last.CacheReadTokens = u.CacheReadInputTokens + u.PiCacheRead
		last.CacheWriteTokens = u.CacheCreationInputTokens + u.PiCacheWrite
		last.ContextTokens = last.InputTokens + last.CacheReadTokens + last.CacheWriteTokens
		last.Model = rec.Message.Model
		if u.PiCost != nil {
			last.CostUSD = u.PiCost.Total
		}
	}
	return out
}

// extractToolResultText handles the three shapes Claude writes: bare string,
// block array, or missing.
func extractToolResultText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return truncate(s, 400)
	}
	var blocks []contentBlock
	if err := json.Unmarshal(raw, &blocks); err == nil {
		var b strings.Builder
		for _, blk := range blocks {
			if blk.Text != "" {
				b.WriteString(blk.Text)
				b.WriteString("\n")
			}
		}
		return truncate(strings.TrimSpace(b.String()), 400)
	}
	return ""
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
