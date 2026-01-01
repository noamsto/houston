// status/watcher.go
package status

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Status int

const (
	StatusUnknown Status = iota
	StatusIdle
	StatusWorking
	StatusWaiting    // Waiting for user input
	StatusPermission // Waiting for permission
)

func (s Status) String() string {
	return [...]string{"unknown", "idle", "working", "waiting", "permission"}[s]
}

func (s Status) NeedsAttention() bool {
	return s == StatusWaiting || s == StatusPermission
}

type SessionStatus struct {
	Session   string
	Status    Status
	Message   string
	Tool      string
	UpdatedAt time.Time
}

// IsFresh returns true if the status was updated within the given duration
func (s SessionStatus) IsFresh(d time.Duration) bool {
	return time.Since(s.UpdatedAt) < d
}

type Watcher struct {
	dir string
}

func NewWatcher(dir string) *Watcher {
	return &Watcher{dir: dir}
}

// statusFile represents the JSON structure from the hook script
type statusFile struct {
	TmuxSession      string `json:"tmux_session"`
	Status           string `json:"status"`
	Message          string `json:"message"`
	Tool             string `json:"tool"`
	NotificationType string `json:"notification_type"`
	Timestamp        int64  `json:"timestamp"`
}

func parseStatus(s string) Status {
	switch s {
	case "idle":
		return StatusIdle
	case "working":
		return StatusWorking
	case "waiting":
		return StatusWaiting
	case "permission":
		return StatusPermission
	default:
		return StatusUnknown
	}
}

func readStatusFile(path string) (SessionStatus, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return SessionStatus{}, err
	}

	// Try JSON first (new format)
	var sf statusFile
	if err := json.Unmarshal(data, &sf); err == nil && sf.Status != "" {
		return SessionStatus{
			Session:   sf.TmuxSession,
			Status:    parseStatus(sf.Status),
			Message:   sf.Message,
			Tool:      sf.Tool,
			UpdatedAt: time.Unix(sf.Timestamp, 0),
		}, nil
	}

	// Fallback to plain text (old format)
	content := strings.TrimSpace(string(data))
	info, _ := os.Stat(path)
	modTime := time.Now()
	if info != nil {
		modTime = info.ModTime()
	}

	var status Status
	switch content {
	case "needs_attention":
		status = StatusWaiting
	case "working":
		status = StatusWorking
	case "idle":
		status = StatusIdle
	default:
		status = StatusUnknown
	}

	return SessionStatus{
		Status:    status,
		UpdatedAt: modTime,
	}, nil
}

// filenameToSession converts escaped filename back to session name
func filenameToSession(filename string) string {
	// Remove .json extension if present
	name := strings.TrimSuffix(filename, ".json")
	// Convert % back to /
	return strings.ReplaceAll(name, "%", "/")
}

// sessionToFilename converts session name to safe filename
func sessionToFilename(session string) string {
	return strings.ReplaceAll(session, "/", "%") + ".json"
}

func (w *Watcher) GetAll() map[string]SessionStatus {
	result := make(map[string]SessionStatus)

	entries, err := os.ReadDir(w.dir)
	if err != nil {
		return result
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		path := filepath.Join(w.dir, entry.Name())
		status, err := readStatusFile(path)
		if err != nil {
			continue
		}

		sessionName := filenameToSession(entry.Name())
		if status.Session == "" {
			status.Session = sessionName
		}

		result[sessionName] = status
	}

	return result
}

func (w *Watcher) Get(session string) (SessionStatus, bool) {
	// Try JSON file first
	jsonPath := filepath.Join(w.dir, sessionToFilename(session))
	if status, err := readStatusFile(jsonPath); err == nil {
		if status.Session == "" {
			status.Session = session
		}
		return status, true
	}

	// Try plain filename (old format)
	path := filepath.Join(w.dir, session)
	if status, err := readStatusFile(path); err == nil {
		status.Session = session
		return status, true
	}

	return SessionStatus{}, false
}
