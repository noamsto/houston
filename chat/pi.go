package chat

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// pi reads a pi session file (JSONL of tree entries; `message` entries are
// chat; `branch_summary` and `compaction` become dividers). The shape is pi's
// own docs/session-format.md.
type pi struct{}

// NewPi returns a pi Reader.
func NewPi() Reader { return pi{} }

var piReader Reader = NewPi()

func (pi) Engine() string { return "pi" }

type piEntry struct {
	Type      string     `json:"type"`
	ID        string     `json:"id"`
	Timestamp string     `json:"timestamp"`
	Message   *piMessage `json:"message"`
	Summary   string     `json:"summary"` // branch_summary, compaction
}

type piMessage struct {
	Role         string          `json:"role"`
	Timestamp    int64           `json:"timestamp"` // unix ms
	Content      json.RawMessage `json:"content"`   // string, or []piBlock
	ToolCallID   string          `json:"toolCallId"`
	IsError      bool            `json:"isError"`
	StopReason   string          `json:"stopReason"`
	ErrorMessage string          `json:"errorMessage"` // set with stopReason "error"
}

type piBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	ID        string          `json:"id"`        // toolCall id
	Name      string          `json:"name"`      // toolCall tool name
	Arguments json.RawMessage `json:"arguments"` // toolCall input
}

func (pi) Read(path string, from Cursor) (updates []Update, next Cursor, reset bool, err error) {
	return readLines(path, from, decodePiLine)
}

func decodePiLine(line []byte, lineOffset int64) []Update {
	var e piEntry
	if err := json.Unmarshal(line, &e); err != nil {
		return nil // malformed line: skipped, but already consumed by the caller
	}
	switch e.Type {
	case "branch_summary":
		return []Update{userChunk(fmt.Sprintf("%d:0", lineOffset), parseClaudeTS(e.Timestamp), e.Summary, map[string]any{"origin": "pi-branch"})}
	case "compaction":
		return []Update{userChunk(fmt.Sprintf("%d:0", lineOffset), parseClaudeTS(e.Timestamp), e.Summary, map[string]any{"origin": "pi-compaction"})}
	}
	if e.Type != "message" || e.Message == nil {
		return nil
	}
	m := e.Message

	ts := m.Timestamp
	if ts == 0 {
		ts = parseClaudeTS(e.Timestamp)
	}
	n := 0
	nextID := func() string {
		id := fmt.Sprintf("%d:%d", lineOffset, n)
		n++
		return id
	}

	switch m.Role {
	case "user":
		text, _ := piText(m.Content)
		if text == "" {
			return nil
		}
		return []Update{userChunk(nextID(), ts, text, nil)}
	case "assistant":
		var ups []Update
		toolStatus := StatusInProgress
		if piNeverRan(m.StopReason) {
			toolStatus = StatusFailed
		}
		for _, b := range piBlocks(m.Content) {
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
					Meta:          map[string]any{"messageId": e.ID},
				})
			case "toolCall":
				ups = append(ups, Update{
					ID:            nextID(),
					TS:            ts,
					SessionUpdate: SessionUpdateToolCall,
					ToolCallID:    b.ID,
					Status:        toolStatus,
					Title:         ToolTitle(b.Name, b.Arguments),
					Kind:          ToolKind(b.Name),
					Locations:     toolLocations(b.Arguments),
					Meta:          map[string]any{"tool": b.Name, "messageId": e.ID},
				})
			}
		}
		if m.StopReason == "error" && m.ErrorMessage != "" {
			ups = append(ups, Update{
				ID:            nextID(),
				TS:            ts,
				SessionUpdate: SessionUpdateAgentMessageChunk,
				Content:       []Content{textContent("Error: " + m.ErrorMessage)},
				Meta:          map[string]any{"messageId": e.ID},
			})
		}
		return ups
	case "toolResult":
		status := StatusCompleted
		if m.IsError {
			status = StatusFailed
		}
		// Tool output never rides Read's updates; only Tool() serves it.
		return []Update{{
			ID:            nextID(),
			TS:            ts,
			SessionUpdate: SessionUpdateToolCallUpdate,
			ToolCallID:    m.ToolCallID,
			Status:        status,
		}}
	default:
		return nil
	}
}

// piNeverRan reports whether an assistant message's stop reason means pi
// dropped its tool calls without running them or persisting a result.
func piNeverRan(stopReason string) bool {
	return stopReason == "aborted" || stopReason == "error"
}

// piBlocks decodes an array content field; a string or anything else is nil.
func piBlocks(raw json.RawMessage) []piBlock {
	var bs []piBlock
	if err := json.Unmarshal(raw, &bs); err != nil {
		return nil
	}
	return bs
}

// piText flattens a content field (string, or text/image blocks) into text,
// with "[image]" standing in for an image.
func piText(raw json.RawMessage) (string, bool) {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s, true
	}
	var parts []string
	for _, b := range piBlocks(raw) {
		switch b.Type {
		case "text":
			parts = append(parts, b.Text)
		case "image":
			parts = append(parts, "[image]")
		}
	}
	return strings.Join(parts, "\n"), len(parts) > 0
}

func (pi) Tool(path, toolCallID string) (*Update, error) {
	data, err := os.ReadFile(path) //nolint:gosec // transcript path is agent-supplied via hook state (not a request); reading it is the feature
	if err != nil {
		return nil, err
	}

	var (
		found  bool
		name   string
		input  json.RawMessage
		ts     int64
		result *piMessage
		stop   string
	)

	for _, line := range bytes.Split(data, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var e piEntry
		if err := json.Unmarshal(line, &e); err != nil || e.Type != "message" || e.Message == nil {
			continue
		}
		switch e.Message.Role {
		case "assistant":
			for _, b := range piBlocks(e.Message.Content) {
				if b.Type == "toolCall" && b.ID == toolCallID {
					found = true
					name = b.Name
					input = b.Arguments
					stop = e.Message.StopReason
					ts = e.Message.Timestamp
					if ts == 0 {
						ts = parseClaudeTS(e.Timestamp)
					}
				}
			}
		case "toolResult":
			if e.Message.ToolCallID == toolCallID {
				result = e.Message
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
		if piNeverRan(stop) {
			u.Status = StatusFailed
		}
		return u, nil
	}

	u.SessionUpdate = SessionUpdateToolCallUpdate
	u.Status = StatusCompleted
	if result.IsError {
		u.Status = StatusFailed
	}
	if text, ok := piText(result.Content); ok && text != "" {
		u.Content = []Content{textContent(text)}
	}
	// A failed edit changed nothing; its output is the error. write gets no
	// diff: the original file is unknown.
	if !result.IsError && name == "edit" {
		if diff := piEditDiff(input); diff != nil {
			u.Content = append(u.Content, *diff)
		}
	}
	return u, nil
}

func piEditDiff(input json.RawMessage) *Content {
	var in struct {
		Path  string `json:"path"`
		Edits []struct {
			OldText string `json:"oldText"`
			NewText string `json:"newText"`
		} `json:"edits"`
	}
	if json.Unmarshal(input, &in) != nil {
		return nil
	}
	if len(in.Edits) == 0 {
		return nil
	}
	olds := make([]string, len(in.Edits))
	news := make([]string, len(in.Edits))
	for i, e := range in.Edits {
		olds[i] = e.OldText
		news[i] = e.NewText
	}
	return &Content{Type: "diff", Path: in.Path, OldText: strings.Join(olds, "\n\n"), NewText: strings.Join(news, "\n\n")}
}
