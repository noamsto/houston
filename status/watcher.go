// status/watcher.go
package status

import (
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
	StatusNeedsAttention
)

func (s Status) String() string {
	return [...]string{"unknown", "idle", "working", "needs_attention"}[s]
}

type SessionStatus struct {
	Session   string
	Status    Status
	UpdatedAt time.Time
}

type Watcher struct {
	dir string
}

func NewWatcher(dir string) *Watcher {
	return &Watcher{dir: dir}
}

func readStatusFile(path string) (Status, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return StatusUnknown, err
	}

	content := strings.TrimSpace(string(data))
	switch content {
	case "needs_attention":
		return StatusNeedsAttention, nil
	case "working":
		return StatusWorking, nil
	case "idle":
		return StatusIdle, nil
	default:
		return StatusUnknown, nil
	}
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
		info, err := entry.Info()
		if err != nil {
			continue
		}

		status, err := readStatusFile(path)
		if err != nil {
			continue
		}

		result[entry.Name()] = SessionStatus{
			Session:   entry.Name(),
			Status:    status,
			UpdatedAt: info.ModTime(),
		}
	}

	return result
}

func (w *Watcher) Get(session string) (SessionStatus, bool) {
	path := filepath.Join(w.dir, session)
	info, err := os.Stat(path)
	if err != nil {
		return SessionStatus{}, false
	}

	status, err := readStatusFile(path)
	if err != nil {
		return SessionStatus{}, false
	}

	return SessionStatus{
		Session:   session,
		Status:    status,
		UpdatedAt: info.ModTime(),
	}, true
}
