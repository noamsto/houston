// status/panes.go
package status

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const PanesDir = "/tmp/claude-status/panes"

// PaneState represents the state of a Claude pane
type PaneState string

const (
	PaneStateProcessing PaneState = "processing"
	PaneStateWaiting    PaneState = "waiting"
	PaneStateDone       PaneState = "done"
	PaneStateIdle       PaneState = "idle"
)

// Priority returns a number for sorting (lower = higher priority)
func (s PaneState) Priority() int {
	switch s {
	case PaneStateWaiting:
		return 0 // Highest priority - needs immediate attention
	case PaneStateProcessing:
		return 1
	case PaneStateDone:
		return 2
	default:
		return 100
	}
}

// PaneStatus represents a Claude pane's status
type PaneStatus struct {
	PaneID    int
	Session   string
	State     PaneState
	Timestamp int64
}

// ReadPaneStatuses reads all pane status files
func ReadPaneStatuses() []PaneStatus {
	var statuses []PaneStatus

	entries, err := os.ReadDir(PanesDir)
	if err != nil {
		return statuses
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		paneID, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}

		path := filepath.Join(PanesDir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}

		ps := PaneStatus{PaneID: paneID}
		for _, line := range strings.Split(string(data), "\n") {
			parts := strings.SplitN(line, "=", 2)
			if len(parts) != 2 {
				continue
			}
			switch parts[0] {
			case "session":
				ps.Session = parts[1]
			case "state":
				ps.State = PaneState(parts[1])
			case "timestamp":
				ps.Timestamp, _ = strconv.ParseInt(parts[1], 10, 64)
			}
		}

		if ps.Session != "" && ps.State != "" && ps.State != PaneStateIdle {
			statuses = append(statuses, ps)
		}
	}

	return statuses
}

// FindPriorityPane finds the pane that most needs attention for a session
// Returns pane ID or -1 if no priority pane found
func FindPriorityPane(session string) int {
	statuses := ReadPaneStatuses()

	var best *PaneStatus
	for i := range statuses {
		ps := &statuses[i]
		if ps.Session != session {
			continue
		}
		if best == nil || ps.State.Priority() < best.State.Priority() {
			best = ps
		}
	}

	if best != nil {
		return best.PaneID
	}
	return -1
}
