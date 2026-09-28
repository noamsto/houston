package chat

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ErrToolNotFound is returned by Tool when no tool_use with the requested id
// appears in the file.
var ErrToolNotFound = errors.New("chat: tool call not found")

// claude reads a Claude Code transcript (~/.claude/projects/<slug>/<sid>.jsonl).
// See spec resolutions R1-R7 (docs/superpowers/specs/... slice-1 artifact) for
// the mapping this implements.
type claude struct {
	logf func(string)

	mu        sync.Mutex
	seenKinds map[string]bool
}

// NewClaude returns a claude-code Reader. logf, if non-nil, is called once
// per distinct unknown user origin.kind ever seen by this Reader.
func NewClaude(logf func(string)) Reader {
	return &claude{logf: logf}
}

var claudeReader Reader = NewClaude(nil)

var readers = map[string]Reader{
	"claude":      claudeReader,
	"claude-code": claudeReader,
}

func (c *claude) Engine() string { return "claude-code" }

// claudeRecord is one decoded JSONL line. Fields the reader doesn't use are
// left out; an unrecognized field or top-level type is simply dropped, not
// an error.
type claudeRecord struct {
	Type          string          `json:"type"`
	Timestamp     string          `json:"timestamp"`
	IsSidechain   bool            `json:"isSidechain"`
	IsMeta        bool            `json:"isMeta"`
	Message       *claudeMessage  `json:"message"`
	Origin        *claudeOrigin   `json:"origin"`
	Attachment    *claudeAttach   `json:"attachment"`
	ToolUseResult json.RawMessage `json:"toolUseResult"`
}

type claudeOrigin struct {
	Kind string `json:"kind"`
}

type claudeAttach struct {
	Type        string `json:"type"`
	CommandMode string `json:"commandMode"`
	Prompt      string `json:"prompt"`
}

type claudeMessage struct {
	ID      string          `json:"id"`
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"` // string, or []claudeBlock
}

type claudeBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	ID        string          `json:"id"`   // tool_use id
	Name      string          `json:"name"` // tool name
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"` // tool_result body: string, or []claudeBlock of text blocks
	IsError   bool            `json:"is_error"`
}

// claudeToolUseResult is the subset of toolUseResult shapes the reader
// interprets (Agent's agentId; Write's originalFile/content/filePath).
type claudeToolUseResult struct {
	AgentID      string  `json:"agentId"`
	FilePath     string  `json:"filePath"`
	OriginalFile *string `json:"originalFile"`
	Content      string  `json:"content"`
}

func (c *claude) Read(path string, from Cursor) (updates []Update, next Cursor, reset bool, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, from, false, err
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil {
		return nil, from, false, err
	}

	offset := from.Offset
	if info.Size() < offset {
		reset = true
		offset = 0
	}

	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil, from, false, err
	}

	dir := filepath.Dir(path)
	sid := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))

	r := bufio.NewReaderSize(f, 64*1024)
	pos := offset
	for {
		line, rerr := r.ReadBytes('\n')
		if rerr != nil {
			// A partial (unterminated) line, or clean EOF with nothing left.
			// Left for the next call — see the chunking-independence
			// contract in doc.go.
			break
		}
		lineOffset := pos
		pos += int64(len(line))

		trimmed := bytes.TrimSpace(line)
		if len(trimmed) > 0 {
			updates = append(updates, c.decodeLine(trimmed, lineOffset, dir, sid)...)
		}
	}

	return updates, Cursor{Offset: pos}, reset, nil
}

func (c *claude) decodeLine(line []byte, lineOffset int64, dir, sid string) []Update {
	var rec claudeRecord
	if err := json.Unmarshal(line, &rec); err != nil {
		return nil // malformed line: skipped, but already consumed by the caller
	}
	if rec.IsSidechain {
		return nil
	}

	ts := parseClaudeTS(rec.Timestamp)
	n := 0
	nextID := func() string {
		id := fmt.Sprintf("%d:%d", lineOffset, n)
		n++
		return id
	}

	switch rec.Type {
	case "user":
		return c.decodeUser(rec, ts, nextID, dir, sid)
	case "assistant":
		return decodeAssistant(rec, ts, nextID)
	case "attachment":
		return decodeAttachment(rec, ts, nextID)
	default:
		return nil
	}
}

func (c *claude) decodeUser(rec claudeRecord, ts int64, nextID func() string, dir, sid string) []Update {
	if rec.IsMeta || rec.Message == nil {
		return nil
	}

	text, blocks, isArray, ok := decodeClaudeContent(rec.Message.Content)
	if !ok {
		return nil
	}

	if isArray {
		var ups []Update
		for _, b := range blocks {
			if b.Type != "tool_result" {
				continue
			}
			ups = append(ups, c.toolResultUpdate(b, rec, ts, nextID(), dir, sid))
		}
		if len(ups) > 0 {
			return ups
		}
	}

	if rec.Origin != nil {
		switch rec.Origin.Kind {
		case "human":
			t, k := joinTextBlocks(text, blocks, isArray)
			if !k {
				return nil
			}
			return []Update{userChunk(nextID(), ts, t, nil)}
		case "task-notification":
			t, k := joinTextBlocks(text, blocks, isArray)
			if !k {
				return nil
			}
			return []Update{userChunk(nextID(), ts, t, map[string]any{"origin": "task-notification"})}
		default:
			c.logOnce(rec.Origin.Kind)
			return nil
		}
	}

	// R6 legacy fallback: no origin, not isMeta, not a tool_result — a
	// human prompt iff its content is plain text not starting with "<".
	t, ok := joinTextBlocks(text, blocks, isArray)
	if !ok || strings.HasPrefix(t, "<") {
		return nil
	}
	return []Update{userChunk(nextID(), ts, t, nil)}
}

func (c *claude) toolResultUpdate(b claudeBlock, rec claudeRecord, ts int64, id string, dir, sid string) Update {
	status := StatusCompleted
	if b.IsError {
		status = StatusFailed
	}

	// Tool output never rides Read's updates (only Tool() serves it, on
	// tap) — see the "common rules" in the parent spec.
	u := Update{
		ID:            id,
		TS:            ts,
		SessionUpdate: SessionUpdateToolCallUpdate,
		ToolCallID:    b.ToolUseID,
		Status:        status,
	}

	if len(rec.ToolUseResult) > 0 {
		var tr claudeToolUseResult
		if err := json.Unmarshal(rec.ToolUseResult, &tr); err == nil && tr.AgentID != "" {
			if subagentFileExists(dir, sid, tr.AgentID) {
				u.Meta = map[string]any{"subagent": tr.AgentID}
			}
		}
	}

	return u
}

func decodeAssistant(rec claudeRecord, ts int64, nextID func() string) []Update {
	if rec.Message == nil {
		return nil
	}
	_, blocks, isArray, ok := decodeClaudeContent(rec.Message.Content)
	if !ok || !isArray {
		return nil
	}

	var ups []Update
	for _, b := range blocks {
		switch b.Type {
		case "text":
			if b.Text == "" {
				continue
			}
			ups = append(ups, Update{
				ID:            nextID(),
				TS:            ts,
				SessionUpdate: SessionUpdateAgentMessageChunk,
				Content:       []Content{textContent(b.Text)},
				Meta:          map[string]any{"messageId": rec.Message.ID},
			})
		case "tool_use":
			ups = append(ups, Update{
				ID:            nextID(),
				TS:            ts,
				SessionUpdate: SessionUpdateToolCall,
				ToolCallID:    b.ID,
				Status:        StatusInProgress,
				Title:         ToolTitle(b.Name, b.Input),
				Kind:          ToolKind(b.Name),
				Locations:     toolLocations(b.Input),
				Meta:          map[string]any{"tool": b.Name, "messageId": rec.Message.ID},
			})
		case "thinking":
			// dropped
		}
	}
	return ups
}

func decodeAttachment(rec claudeRecord, ts int64, nextID func() string) []Update {
	a := rec.Attachment
	if a == nil || a.Type != "queued_command" || a.CommandMode != "prompt" {
		return nil
	}
	return []Update{userChunk(nextID(), ts, a.Prompt, nil)}
}

func (c *claude) logOnce(kind string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.seenKinds == nil {
		c.seenKinds = map[string]bool{}
	}
	if c.seenKinds[kind] {
		return
	}
	c.seenKinds[kind] = true
	if c.logf != nil {
		c.logf(fmt.Sprintf("chat: dropping user record with unknown origin.kind %q", kind))
	}
}

func (c *claude) Tool(path, toolCallID string) (*Update, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var (
		found  bool
		name   string
		input  json.RawMessage
		ts     int64
		result *claudeBlock
		tr     claudeToolUseResult
	)

	for _, line := range bytes.Split(data, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var rec claudeRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			continue
		}
		if rec.IsSidechain || rec.Message == nil {
			continue
		}

		switch rec.Type {
		case "assistant":
			_, blocks, isArray, ok := decodeClaudeContent(rec.Message.Content)
			if !ok || !isArray {
				continue
			}
			for _, b := range blocks {
				if b.Type == "tool_use" && b.ID == toolCallID {
					found = true
					name = b.Name
					input = b.Input
					ts = parseClaudeTS(rec.Timestamp)
				}
			}
		case "user":
			_, blocks, isArray, ok := decodeClaudeContent(rec.Message.Content)
			if !ok || !isArray {
				continue
			}
			for _, b := range blocks {
				if b.Type == "tool_result" && b.ToolUseID == toolCallID {
					blk := b
					result = &blk
					if len(rec.ToolUseResult) > 0 {
						_ = json.Unmarshal(rec.ToolUseResult, &tr)
					}
				}
			}
		}
	}

	if !found {
		return nil, ErrToolNotFound
	}

	u := &Update{
		TS:         ts,
		ToolCallID: toolCallID,
		Title:      ToolTitle(name, input),
		Kind:       ToolKind(name),
		RawInput:   input,
		Locations:  toolLocations(input),
		Meta:       map[string]any{"tool": name},
	}

	if result == nil {
		u.SessionUpdate = SessionUpdateToolCall
		u.Status = StatusInProgress
		return u, nil
	}

	u.SessionUpdate = SessionUpdateToolCallUpdate
	if result.IsError {
		u.Status = StatusFailed
	} else {
		u.Status = StatusCompleted
	}
	u.Content = toolResultContent(result.Content)
	if diff := buildDiff(name, input, tr); diff != nil {
		u.Content = append(u.Content, *diff)
	}

	return u, nil
}

func buildDiff(name string, input json.RawMessage, tr claudeToolUseResult) *Content {
	switch name {
	case "Edit":
		var in struct {
			FilePath  string `json:"file_path"`
			OldString string `json:"old_string"`
			NewString string `json:"new_string"`
		}
		if json.Unmarshal(input, &in) != nil {
			return nil
		}
		return &Content{Type: "diff", Path: in.FilePath, OldText: in.OldString, NewText: in.NewString}
	case "MultiEdit":
		var in struct {
			FilePath string `json:"file_path"`
			Edits    []struct {
				OldString string `json:"old_string"`
				NewString string `json:"new_string"`
			} `json:"edits"`
		}
		if json.Unmarshal(input, &in) != nil {
			return nil
		}
		olds := make([]string, len(in.Edits))
		news := make([]string, len(in.Edits))
		for i, e := range in.Edits {
			olds[i] = e.OldString
			news[i] = e.NewString
		}
		return &Content{Type: "diff", Path: in.FilePath, OldText: strings.Join(olds, "\n\n"), NewText: strings.Join(news, "\n\n")}
	case "Write":
		old := ""
		if tr.OriginalFile != nil {
			old = *tr.OriginalFile
		}
		return &Content{Type: "diff", Path: tr.FilePath, OldText: old, NewText: tr.Content}
	default:
		return nil
	}
}

// decodeClaudeContent decodes a message.content field, which is either a
// plain string or an array of blocks.
func decodeClaudeContent(raw json.RawMessage) (text string, blocks []claudeBlock, isArray bool, ok bool) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return "", nil, false, false
	}
	switch trimmed[0] {
	case '"':
		var s string
		if err := json.Unmarshal(trimmed, &s); err != nil {
			return "", nil, false, false
		}
		return s, nil, false, true
	case '[':
		var bs []claudeBlock
		if err := json.Unmarshal(trimmed, &bs); err != nil {
			return "", nil, true, false
		}
		return "", bs, true, true
	default:
		return "", nil, false, false
	}
}

// joinTextBlocks turns decoded content into plain text: a string passes
// through; an array must hold only text blocks (anything else fails).
func joinTextBlocks(text string, blocks []claudeBlock, isArray bool) (string, bool) {
	if !isArray {
		return text, true
	}
	var parts []string
	for _, b := range blocks {
		switch b.Type {
		case "text":
			parts = append(parts, b.Text)
		case "image":
			parts = append(parts, "[image]")
		default:
			return "", false
		}
	}
	return strings.Join(parts, "\n"), true
}

// toolResultContent decodes a tool_result block's content, which is either a
// plain string or an array of text blocks.
func toolResultContent(raw json.RawMessage) []Content {
	text, blocks, isArray, ok := decodeClaudeContent(raw)
	if !ok {
		return nil
	}
	if !isArray {
		if text == "" {
			return nil
		}
		return []Content{textContent(text)}
	}
	var out []Content
	for _, b := range blocks {
		if b.Type == "text" && b.Text != "" {
			out = append(out, textContent(b.Text))
		}
	}
	return out
}

func toolLocations(input json.RawMessage) []Location {
	if len(input) == 0 {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(input, &m); err != nil {
		return nil
	}
	for _, k := range []string{"file_path", "path"} {
		if v, ok := m[k].(string); ok && v != "" {
			return []Location{{Path: v}}
		}
	}
	return nil
}

func textContent(s string) Content {
	return Content{Type: "content", Content: &ContentBlock{Type: "text", Text: s}}
}

func userChunk(id string, ts int64, text string, meta map[string]any) Update {
	return Update{
		ID:            id,
		TS:            ts,
		SessionUpdate: SessionUpdateUserMessageChunk,
		Content:       []Content{textContent(text)},
		Meta:          meta,
	}
}

func subagentFileExists(dir, sid, agentID string) bool {
	if dir == "" || sid == "" || agentID == "" {
		return false
	}
	p := filepath.Join(dir, sid, "subagents", "agent-"+agentID+".jsonl")
	info, err := os.Stat(p)
	return err == nil && !info.IsDir()
}

func parseClaudeTS(s string) int64 {
	if s == "" {
		return 0
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return 0
	}
	return t.UnixMilli()
}
