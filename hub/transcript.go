// Package hub watches hook state files and Claude Code transcripts and
// exposes a live, aggregated view of every active Claude session.
//
// State comes from two sources:
//
//   - hook state files at <state-dir>/claude/<session-id>.json,
//     written by `houston hook` (status, tool, timing).
//
//   - transcript JSONL at the path the state file points to
//     (activity trail, preview text, token/cost telemetry).
//
// The hub owns both.
package hub

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"strings"
	"time"
)

// TranscriptEvent is a normalized subset of a single JSONL line from a
// Claude Code transcript. Not every field is populated for every event; the
// UI consumer decides what's interesting.
type TranscriptEvent struct {
	Offset        int64     // byte offset of the line start in the file
	Timestamp     time.Time // event timestamp (best effort)
	Type          string    // "user" | "assistant" | "tool_use" | "tool_result" | "thinking" | "text"
	Role          string    // for user/assistant messages
	ToolName      string    // for tool_use / tool_result
	ToolUseID     string    // links tool_use to tool_result
	IsError       bool      // tool_result failure
	Text          string    // plain text content (assistant text / thinking / tool inputs squashed)
	InputTokens   int
	OutputTokens  int
	CacheReadTokens int
	CacheWriteTokens int
}

// jsonlRecord is the on-disk envelope Claude Code writes. Layout:
//
//	{
//	  "type": "user" | "assistant" | "system" | "tool_use" | "tool_result",
//	  "timestamp": "2026-04-18T16:05:12.000Z",
//	  "message": { "role": "...", "content": [ ... blocks ... ], "usage": {...} },
//	  ...
//	}
//
// We unmarshal loosely — the live schema evolves and we don't want to fail
// a whole transcript because one new field shows up.
type jsonlRecord struct {
	Type      string           `json:"type"`
	Timestamp string           `json:"timestamp"`
	Message   *anthropicMsg    `json:"message,omitempty"`
	ToolName  string           `json:"tool_name,omitempty"`
	ToolInput json.RawMessage  `json:"tool_input,omitempty"`
	ToolUseID string           `json:"tool_use_id,omitempty"`
	IsError   bool             `json:"is_error,omitempty"`
}

type anthropicMsg struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
	Usage   *usage          `json:"usage,omitempty"`
}

type contentBlock struct {
	Type       string          `json:"type"`
	Text       string          `json:"text,omitempty"`
	Thinking   string          `json:"thinking,omitempty"`
	ID         string          `json:"id,omitempty"`
	Name       string          `json:"name,omitempty"`
	Input      json.RawMessage `json:"input,omitempty"`
	ToolUseID  string          `json:"tool_use_id,omitempty"`
	IsError    bool            `json:"is_error,omitempty"`
	Content    json.RawMessage `json:"content,omitempty"` // tool_result body
}

type usage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
}

// ReadTranscriptFrom reads the transcript file starting at byteOffset and
// yields one TranscriptEvent per JSONL line. Returns the new offset
// (i.e. file size after reading) and any I/O error.
//
// The caller typically stashes the offset so subsequent reads pick up only
// appended lines.
func ReadTranscriptFrom(path string, byteOffset int64) ([]TranscriptEvent, int64, error) {
	f, err := os.Open(path)
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
		n := int64(len(line))
		trimmed := strings.TrimSpace(string(line))
		if trimmed != "" {
			for _, ev := range parseLine(trimmed, pos) {
				events = append(events, ev)
			}
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
	base := TranscriptEvent{Offset: offset, Timestamp: ts, Type: rec.Type}

	// Top-level tool_use / tool_result rows (some schema variants).
	if rec.ToolName != "" {
		base.ToolName = rec.ToolName
		base.ToolUseID = rec.ToolUseID
		base.IsError = rec.IsError
		base.Text = firstStringField(rec.ToolInput, "file_path", "command", "pattern", "url", "prompt", "description")
		return []TranscriptEvent{base}
	}

	// Otherwise decompose the message content blocks.
	if rec.Message == nil || len(rec.Message.Content) == 0 {
		return []TranscriptEvent{base}
	}

	var blocks []contentBlock
	// content may be a string OR an array of blocks.
	if len(rec.Message.Content) > 0 && rec.Message.Content[0] == '"' {
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
		case "text":
			ev.Type = "text"
			ev.Text = blk.Text
		case "thinking":
			ev.Type = "thinking"
			ev.Text = blk.Thinking
		case "tool_use":
			ev.Type = "tool_use"
			ev.ToolName = blk.Name
			ev.ToolUseID = blk.ID
			ev.Text = firstStringField(blk.Input, "file_path", "command", "pattern", "url", "prompt", "description")
		case "tool_result":
			ev.Type = "tool_result"
			ev.ToolUseID = blk.ToolUseID
			ev.IsError = blk.IsError
			ev.Text = extractToolResultText(blk.Content)
		default:
			ev.Type = blk.Type
		}
		out = append(out, ev)
	}

	if rec.Message.Usage != nil && len(out) > 0 {
		last := &out[len(out)-1]
		last.InputTokens = rec.Message.Usage.InputTokens
		last.OutputTokens = rec.Message.Usage.OutputTokens
		last.CacheReadTokens = rec.Message.Usage.CacheReadInputTokens
		last.CacheWriteTokens = rec.Message.Usage.CacheCreationInputTokens
	}
	return out
}

// firstStringField returns the first non-empty string field from a JSON object
// matching one of the given keys. Used to get a human hint from tool inputs.
func firstStringField(raw json.RawMessage, keys ...string) string {
	if len(raw) == 0 {
		return ""
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return ""
	}
	for _, k := range keys {
		if v, ok := m[k].(string); ok && v != "" {
			return truncate(strings.TrimSpace(v), 160)
		}
	}
	return ""
}

// extractToolResultText flattens a tool_result content blob into a short string.
// Content may be a bare string, an array of blocks, or missing entirely.
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
