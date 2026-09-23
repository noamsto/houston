package hook

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
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

// AgentClaude is the SessionState.Agent value for the native Claude Code
// payload path and for a hookyard envelope with engine "claude-code".
const AgentClaude = "claude"

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
// stdin is either Claude Code's native hook payload or a hookyard envelope
// (auto-detected by a non-empty top-level "engine"); event (CLI arg) is
// trusted over the native payload's HookEventName so wrappers work, but is
// ignored in envelope mode, where the envelope's own event is authoritative.
func Dispatch(event string, stateDir string, stdin io.Reader) error {
	var in input
	if err := json.NewDecoder(stdin).Decode(&in); err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("decode event: %w", err)
	}

	var ev Event
	var agent string
	if in.Engine != "" {
		var err error
		event, ev, agent, err = fromEnvelope(in)
		if err != nil {
			return err
		}
		if event == "" {
			return nil // envelope event houston doesn't track
		}
	} else {
		ev = in.Event
		if event == "" {
			event = ev.HookEventName
		}
	}
	if ev.SessionID == "" {
		return fmt.Errorf("missing session_id in hook payload")
	}

	path := Path(stateDir, ev.SessionID)

	// The tmux exec must stay outside the lock below (it's the slow part of
	// Dispatch), so decide up front — from an unlocked read — whether this
	// event needs a fresh display-message. A --resume of the same session id
	// in another pane (or after a tmux restart) must not keep the first pane
	// recorded: the stale id is another agent's pane, and the run's
	// reply/terminal target follows it. Outside tmux both env values are
	// empty, so a recorded pane is cleared, not kept.
	pane, server := tmuxEnv()
	pre, _ := Read(path)
	var fetchedCoords bool
	var sess, win, p string
	if event == EventSessionStart || paneStale(pre, pane, server) {
		sess, win, p = tmuxCoords()
		fetchedCoords = true
	}

	beforeLock()

	unlock, err := lockStateDir(filepath.Dir(path))
	if err != nil {
		return fmt.Errorf("lock state dir: %w", err)
	}
	defer unlock()

	// Re-read under the lock: another process may have written since pre was
	// read above, so pre's staleness verdict can be out of date.
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
	if agent != "" {
		next.Agent = agent
	}
	if event == EventSessionStart || paneStale(prev, pane, server) {
		if fetchedCoords {
			next.TmuxSession, next.TmuxWindow, next.TmuxPane = sess, win, p
		} else {
			// The unlocked check found nothing stale, so no display-message
			// ran; report the "failed" shape so the next event retries — a
			// second exec can't happen inside the lock.
			next.TmuxSession, next.TmuxWindow, next.TmuxPane = "", "", pane
		}
		next.TmuxServer = server
		next.PID = os.Getppid()
	} else if next.PID == 0 {
		next.PID = os.Getppid()
	}

	apply(&next, event, ev, now)
	return Write(path, next)
}

// paneStale reports whether s's recorded tmux pane/server no longer matches
// the environment the hook is running under now.
func paneStale(s SessionState, pane, server string) bool {
	return s.TmuxPane != pane || s.TmuxServer != server ||
		(s.TmuxPane != "" && s.TmuxSession == "") // last display-message failed
}

// lockStateDir takes an exclusive flock on dir's lock file, serializing
// concurrent Dispatch calls (post_tool, turn_end, prompt_submit and
// session_start all arrive as detached fire-and-forget processes from
// hookyard, so two can otherwise race a Read-apply-Write and silently drop
// one's update). One lock file for the whole dir, not one per session, so
// nothing accumulates for hub/prune to skip over.
func lockStateDir(dir string) (unlock func(), err error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, ".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, err
	}
	return func() { _ = f.Close() }, nil
}

func apply(s *SessionState, event string, ev Event, now int64) {
	clearTool := func() { s.Tool = ""; s.ToolInputHint = "" }
	s.Since = now
	if event != EventNotification {
		s.LastMessage = ""
	}
	switch event {
	case EventSessionStart:
		s.State = StateStarting
		s.Source = ev.Source
		s.TurnTool = false
	case EventSessionEnd:
		s.State = StateEnded
		s.Reason = ev.Reason
		clearTool()
	case EventUserPromptSubmit:
		s.State = StateThinking
		s.Turn++
		s.TurnTool = false
		clearTool()
	case EventPreToolUse:
		s.State = StateToolRunning
		s.Tool = ev.ToolName
		s.ToolInputHint = ToolHint(ev.ToolName, ev.ToolInput)
		s.TurnTool = true
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
		clearTool()
		// pi fires turn_end (mapped to Stop) after every LLM response, not just
		// the final one: a turn that ran a tool is followed by another LLM
		// call, so it's still "thinking", not "waiting".
		if s.Agent == "pi" && s.TurnTool {
			s.State = StateThinking
		} else {
			s.State = StateWaiting
		}
		s.TurnTool = false
	case EventPreCompact:
		s.State = StateCompacting
	}
}

// ToolHintKeys lists the tool_input fields we pull a display hint from, in
// priority order. Shared with transcript parsing.
var ToolHintKeys = []string{"file_path", "path", "command", "pattern", "url", "description", "prompt"}

// ToolHint extracts a short display hint from a tool_input payload, tailored
// to the tool that produced it. Bash prefers its own description over the
// raw command; Read/Edit show a file basename instead of a full path.
func ToolHint(toolName string, raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return ""
	}

	switch toolName {
	case "Bash":
		if v, ok := m["description"].(string); ok && v != "" {
			return truncate(strings.TrimSpace(v), 120)
		}
		if v, ok := m["command"].(string); ok && v != "" {
			return truncate(strings.TrimSpace(v), 120)
		}
		return ""
	case "Read", "Edit":
		for _, k := range []string{"file_path", "path"} {
			if v, ok := m[k].(string); ok && v != "" {
				return truncate(filepath.Base(strings.TrimSpace(v)), 120)
			}
		}
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

// tmuxEnv reports the pane id and server pid the hook is currently running
// under, read from the environment alone — no exec, so the hot path
// (PreToolUse/PostToolUse) can check for staleness on every event without
// spawning tmux.
func tmuxEnv() (pane, server string) {
	pane = os.Getenv("TMUX_PANE")
	// $TMUX is "<socket>,<server-pid>,<session-id>", but the socket path can
	// itself contain a comma, so the pid is indexed from the right.
	fields := strings.Split(os.Getenv("TMUX"), ",")
	if len(fields) >= 2 {
		server = fields[len(fields)-2]
	}
	return pane, server
}

// tmuxDisplay is overridden in tests to stub tmux's output and count calls.
var tmuxDisplay = func(pane string) ([]byte, error) {
	return exec.Command("tmux", "display-message", "-p", "-t", pane, "#S\t#I").Output()
}

// beforeLock is overridden in tests to inject a write between the unlocked
// pre-read/exec and the locked re-read, exercising the race where the
// pre-read found the pane fresh (skipping the exec) but another process's
// write makes the locked re-read stale.
var beforeLock = func() {}

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
	out, err := tmuxDisplay(pane)
	if err != nil {
		return "", "", pane
	}
	parts := strings.SplitN(strings.TrimSpace(string(out)), "\t", 2)
	for len(parts) < 2 {
		parts = append(parts, "")
	}
	return parts[0], parts[1], pane
}
