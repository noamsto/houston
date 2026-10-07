package chat

import (
	"encoding/json"
	"path/filepath"
	"strings"
)

// TitleKeys lists the tool_input fields ToolTitle pulls a display hint from,
// in priority order. Mirrors hook.ToolHintKeys byte for byte — chat cannot
// import hook (boundary_test.go), so hub/chat_title_test.go pins the two in
// step instead.
var TitleKeys = []string{"file_path", "path", "command", "pattern", "url", "description", "prompt"}

// ToolTitle extracts a short display hint from a tool_input payload, tailored
// to the tool that produced it. Bash prefers its own description over the
// raw command; Read/Edit show a file basename instead of a full path.
// Mirrors hook.ToolHint exactly.
func ToolTitle(toolName string, raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return ""
	}

	switch toolName {
	case "Bash", "bash":
		if v, ok := m["description"].(string); ok && v != "" {
			return truncateTitle(strings.TrimSpace(v), 120)
		}
		if v, ok := m["command"].(string); ok && v != "" {
			return truncateTitle(strings.TrimSpace(v), 120)
		}
		return ""
	case "Read", "Edit", "read", "edit":
		for _, k := range []string{"file_path", "path"} {
			if v, ok := m[k].(string); ok && v != "" {
				return truncateTitle(filepath.Base(strings.TrimSpace(v)), 120)
			}
		}
		return ""
	case "grep", "find":
		for _, k := range []string{"pattern", "path"} {
			if v, ok := m[k].(string); ok && v != "" {
				return truncateTitle(strings.TrimSpace(v), 120)
			}
		}
		return ""
	case "AskUserQuestion":
		qs, _ := m["questions"].([]any)
		if len(qs) == 0 {
			return ""
		}
		q, _ := qs[0].(map[string]any)
		for _, k := range []string{"header", "question"} {
			if v, ok := q[k].(string); ok {
				if v = strings.TrimSpace(v); v != "" {
					return truncateTitle(v, 120)
				}
			}
		}
		return ""
	}

	for _, k := range TitleKeys {
		if v, ok := m[k].(string); ok && v != "" {
			return truncateTitle(strings.TrimSpace(v), 120)
		}
	}
	return ""
}

func truncateTitle(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

// ToolKind maps a tool name to an ACP kind.
func ToolKind(name string) string {
	switch name {
	case "Read", "read":
		return KindRead
	case "Edit", "Write", "MultiEdit", "NotebookEdit", "edit", "write":
		return KindEdit
	case "Grep", "Glob", "grep", "find", "ls":
		return KindSearch
	case "Bash", "bash":
		return KindExecute
	case "WebFetch", "WebSearch":
		return KindFetch
	case "Task", "Agent":
		return KindOther
	default:
		return KindOther
	}
}
