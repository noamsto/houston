// Package hook implements the Claude Code hook handler and the state files
// that feed the houston UI.
//
// Two subdirectories under the state dir:
//
//	<state-dir>/claude/<session-id>.json  — rich per-Claude-session state
//	<state-dir>/*.json                    — legacy pane-keyed status (see status/)
//
// Hooks write to the former. The server's hub watches both.
package hook

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// StateSchemaVersion is bumped when the on-disk layout changes.
const StateSchemaVersion = 1

// State is the coarse status we display on an agent card.
type State string

const (
	StateStarting    State = "starting"
	StateThinking    State = "thinking"
	StateToolRunning State = "tool-running"
	StateWaiting     State = "waiting"
	StatePermission  State = "waiting:permission"
	StateCompacting  State = "compacting"
	StateEnded       State = "ended"
)

// SessionState is the document written by every hook invocation.
// Fields are merged, not replaced — a PreToolUse doesn't clobber
// fields set earlier by SessionStart.
type SessionState struct {
	Version        int    `json:"version"`
	SessionID      string `json:"session_id"`
	TranscriptPath string `json:"transcript_path,omitempty"`
	CWD            string `json:"cwd,omitempty"`

	// tmux coordinates captured at hook time (may be empty if not under tmux).
	TmuxSession string `json:"tmux_session,omitempty"`
	TmuxWindow  string `json:"tmux_window,omitempty"`
	TmuxPane    string `json:"tmux_pane,omitempty"`

	State         State  `json:"state"`
	Tool          string `json:"tool,omitempty"`           // set by PreToolUse, cleared by PostToolUse
	ToolInputHint string `json:"tool_input_hint,omitempty"`
	LastMessage   string `json:"last_message,omitempty"`   // populated by Notification
	Reason        string `json:"reason,omitempty"`          // populated by SessionEnd
	Source        string `json:"source,omitempty"`          // populated by SessionStart

	Turn      int   `json:"turn,omitempty"`
	Since     int64 `json:"since,omitempty"`      // unix-sec state entered
	UpdatedAt int64 `json:"updated_at"`           // unix-sec last hook fired
	PID       int   `json:"pid,omitempty"`
}

// Read loads a state document. If the file is missing, returns zero value with nil error.
func Read(path string) (SessionState, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return SessionState{}, nil
	}
	if err != nil {
		return SessionState{}, err
	}
	var s SessionState
	if err := json.Unmarshal(b, &s); err != nil {
		return SessionState{}, err
	}
	return s, nil
}

// Write atomically writes s to path via tmp+rename. Parent dir is created.
func Write(path string, s SessionState) error {
	s.Version = StateSchemaVersion
	if s.UpdatedAt == 0 {
		s.UpdatedAt = time.Now().Unix()
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".state-*.tmp")
	if err != nil {
		return err
	}
	enc := json.NewEncoder(tmp)
	enc.SetIndent("", "  ")
	if err := enc.Encode(s); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// Path returns the state-file path for the given session under state dir.
func Path(stateDir, sessionID string) string {
	return filepath.Join(stateDir, "claude", sessionID+".json")
}
