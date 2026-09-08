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

// Event is the payload Claude Code sends on stdin.
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

// Dispatch merges one event into the session's state file under stateDir.
// event (CLI arg) is trusted over the payload's HookEventName so wrappers work.
func Dispatch(event string, stateDir string, stdin io.Reader) error {
	var ev Event
	if err := json.NewDecoder(stdin).Decode(&ev); err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("decode event: %w", err)
	}
	if event == "" {
		event = ev.HookEventName
	}
	if ev.SessionID == "" {
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
	clearTool := func() { s.Tool = ""; s.ToolInputHint = "" }
	s.Since = now
	switch event {
	case EventSessionStart:
		s.State = StateStarting
		s.Source = ev.Source
	case EventSessionEnd:
		s.State = StateEnded
		s.Reason = ev.Reason
		clearTool()
	case EventUserPromptSubmit:
		s.State = StateThinking
		s.Turn++
		clearTool()
	case EventPreToolUse:
		s.State = StateToolRunning
		s.Tool = ev.ToolName
		s.ToolInputHint = toolInputHint(ev.ToolInput)
	case EventPostToolUse:
		s.State = StateThinking
		clearTool()
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
	case EventStop, EventSubagentStop:
		s.State = StateWaiting
		clearTool()
	case EventPreCompact:
		s.State = StateCompacting
	}
}

// ToolHintKeys lists the tool_input fields we pull a display hint from, in
// priority order. Shared with transcript parsing.
var ToolHintKeys = []string{"file_path", "path", "command", "pattern", "url", "description", "prompt"}

func toolInputHint(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return ""
	}
	for _, k := range ToolHintKeys {
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

// tmuxCoords returns (session, window, pane). Empty on any failure — hooks
// can fire outside tmux.
func tmuxCoords() (string, string, string) {
	// $TMUX_PANE is the pane id ("%307") and is set per pane, so it is both the
	// right namespace to correlate on — every other source keys on the pane id,
	// never the index — and immune to which client tmux considers current.
	pane := os.Getenv("TMUX_PANE")
	if pane == "" {
		return "", "", ""
	}
	out, err := exec.Command("tmux", "display-message", "-p", "-t", pane, "#S\t#I").Output()
	if err != nil {
		return "", "", pane
	}
	parts := strings.SplitN(strings.TrimSpace(string(out)), "\t", 2)
	for len(parts) < 2 {
		parts = append(parts, "")
	}
	return parts[0], parts[1], pane
}
