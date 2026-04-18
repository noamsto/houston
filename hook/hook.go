package hook

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Known Claude Code hook event names.
const (
	EventSessionStart     = "SessionStart"
	EventSessionEnd       = "SessionEnd"
	EventUserPromptSubmit = "UserPromptSubmit"
	EventPreToolUse       = "PreToolUse"
	EventPostToolUse      = "PostToolUse"
	EventNotification     = "Notification"
	EventStop             = "Stop"
	EventSubagentStop     = "SubagentStop"
	EventPreCompact       = "PreCompact"
)

// MustHaveEvents is the minimum set houston needs for reliable card status.
var MustHaveEvents = []string{
	EventSessionStart,
	EventSessionEnd,
	EventNotification,
	EventStop,
	EventPreToolUse,
	EventPostToolUse,
}

// Event is the payload Claude Code sends on stdin. Fields not relevant to a
// given hook are simply empty.
type Event struct {
	HookEventName    string          `json:"hook_event_name"`
	SessionID        string          `json:"session_id"`
	TranscriptPath   string          `json:"transcript_path"`
	CWD              string          `json:"cwd"`
	ToolName         string          `json:"tool_name,omitempty"`
	ToolInput        json.RawMessage `json:"tool_input,omitempty"`
	ToolResponse     json.RawMessage `json:"tool_response,omitempty"`
	Message          string          `json:"message,omitempty"`
	NotificationType string          `json:"notification_type,omitempty"`
	Reason           string          `json:"reason,omitempty"`
	Source           string          `json:"source,omitempty"`
	Prompt           string          `json:"prompt,omitempty"`
	Trigger          string          `json:"trigger,omitempty"`
	StopHookActive   bool            `json:"stop_hook_active,omitempty"`
}

// Dispatch reads one event from stdin, merges it into the session's state
// file under stateDir, and writes the result atomically.
//
// event is the CLI arg ("PreToolUse", "Stop", …) and is trusted over the
// payload's HookEventName in case the binary is invoked via a wrapper.
func Dispatch(event string, stateDir string, stdin io.Reader) error {
	var ev Event
	if err := json.NewDecoder(stdin).Decode(&ev); err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("decode event: %w", err)
	}
	if event == "" {
		event = ev.HookEventName
	}
	if ev.SessionID == "" {
		// Without a session id we have nowhere to store state. This shouldn't
		// happen for real Claude Code events, but keep it non-fatal.
		return fmt.Errorf("missing session_id in hook payload")
	}

	path := Path(stateDir, ev.SessionID)
	prev, _ := Read(path)

	now := time.Now().Unix()
	next := prev
	next.SessionID = ev.SessionID
	if ev.TranscriptPath != "" {
		next.TranscriptPath = ev.TranscriptPath
	}
	if ev.CWD != "" {
		next.CWD = ev.CWD
	}
	next.UpdatedAt = now
	if next.TmuxSession == "" {
		next.TmuxSession, next.TmuxWindow, next.TmuxPane = tmuxCoords()
	}
	if next.PID == 0 {
		next.PID = os.Getppid()
	}

	apply(&next, event, ev, now)
	return Write(path, next)
}

func apply(s *SessionState, event string, ev Event, now int64) {
	switch event {
	case EventSessionStart:
		s.State = StateStarting
		s.Source = ev.Source
		s.Since = now
	case EventSessionEnd:
		s.State = StateEnded
		s.Reason = ev.Reason
		s.Tool = ""
		s.ToolInputHint = ""
		s.Since = now
	case EventUserPromptSubmit:
		s.State = StateThinking
		s.Tool = ""
		s.ToolInputHint = ""
		s.Turn++
		s.Since = now
	case EventPreToolUse:
		s.State = StateToolRunning
		s.Tool = ev.ToolName
		s.ToolInputHint = toolInputHint(ev.ToolInput)
		s.Since = now
	case EventPostToolUse:
		// Tool finished; until Stop fires or another PreToolUse comes in,
		// Claude is either formatting a reply or thinking.
		s.State = StateThinking
		s.Tool = ""
		s.ToolInputHint = ""
		s.Since = now
	case EventNotification:
		switch ev.NotificationType {
		case "permission_prompt":
			s.State = StatePermission
		case "idle_prompt":
			s.State = StateWaiting
		default:
			if s.State != StatePermission && s.State != StateWaiting {
				s.State = StateWaiting
			}
		}
		s.LastMessage = ev.Message
		s.Since = now
	case EventStop, EventSubagentStop:
		s.State = StateWaiting
		s.Tool = ""
		s.ToolInputHint = ""
		s.Since = now
	case EventPreCompact:
		s.State = StateCompacting
		s.Since = now
	}
}

// toolInputHint extracts a short, human-readable fragment from a tool_input blob.
// Prefers common fields across Claude's built-in tools.
func toolInputHint(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return ""
	}
	for _, k := range []string{"file_path", "path", "command", "pattern", "url", "description", "prompt"} {
		if v, ok := m[k].(string); ok && v != "" {
			return truncate(strings.TrimSpace(v), 120)
		}
	}
	return ""
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

// tmuxCoords returns (session, window, pane) by invoking tmux display-message.
// Empty strings on any failure — not all hooks fire from inside tmux.
func tmuxCoords() (string, string, string) {
	out, err := exec.Command("tmux", "display-message", "-p", "#S\t#I\t#P").Output()
	if err != nil {
		return "", "", ""
	}
	parts := strings.SplitN(strings.TrimSpace(string(out)), "\t", 3)
	for len(parts) < 3 {
		parts = append(parts, "")
	}
	return parts[0], parts[1], parts[2]
}
