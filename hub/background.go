package hub

import (
	"encoding/json"
	"regexp"
	"sort"
	"strings"
)

// Background task kinds.
const (
	BackgroundShell   = "shell"
	BackgroundMonitor = "monitor"
)

// BackgroundTask is a background shell or monitor a session has started and
// not yet seen finish.
type BackgroundTask struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"`
	Hint  string `json:"hint,omitempty"`
	Since int64  `json:"since,omitempty"` // unix seconds of the starting tool_use
}

var (
	// "Command running in background with ID: bla32qgus." / "Monitor started (task b2cxst3ae, …".
	bgResultID = regexp.MustCompile(`(?:with ID:|\(task) ([A-Za-z0-9_-]+)`)
	notifID    = regexp.MustCompile(`<task-id>([^<]*)</task-id>`)
	notifTool  = regexp.MustCompile(`<tool-use-id>([^<]*)</tool-use-id>`)
	notifState = regexp.MustCompile(`<status>[^<]+</status>`)
)

// backgroundStart classifies a tool_use block: the kind of background task it
// launches, or the task id a TaskStop targets.
func backgroundStart(name string, input json.RawMessage) (kind, stopTask string) {
	switch name {
	case "Monitor":
		return BackgroundMonitor, ""
	case "Bash":
		var in struct {
			Background bool `json:"run_in_background"`
		}
		if json.Unmarshal(input, &in) == nil && in.Background {
			return BackgroundShell, ""
		}
	case "TaskStop":
		var in struct {
			TaskID string `json:"task_id"`
		}
		if json.Unmarshal(input, &in) == nil {
			return "", in.TaskID
		}
	}
	return "", ""
}

// backgroundHint prefers the call's own description over its command.
func backgroundHint(input json.RawMessage) string {
	var in struct {
		Description string `json:"description"`
		Command     string `json:"command"`
	}
	_ = json.Unmarshal(input, &in)
	h := strings.TrimSpace(in.Description)
	if h == "" {
		h = strings.TrimSpace(in.Command)
	}
	return truncate(h, 120)
}

type bgEntry struct {
	task    BackgroundTask
	started bool // its tool_result confirmed the launch and named a task id
}

// bgTracker derives a session's outstanding background tasks from its
// transcript events. It is a pure fold over the events in file order, so a
// replay from byte 0 and an incremental read end in the same state.
type bgTracker struct {
	byToolUse map[string]*bgEntry
	stops     map[string]string // TaskStop tool_use id → task id
}

func (t *bgTracker) apply(ev TranscriptEvent) {
	switch ev.Type {
	case EventTypeToolUse:
		switch {
		case ev.Background != "":
			if t.byToolUse == nil {
				t.byToolUse = map[string]*bgEntry{}
			}
			t.byToolUse[ev.ToolUseID] = &bgEntry{task: BackgroundTask{
				Kind: ev.Background, Hint: ev.BackgroundHint, Since: ev.Timestamp.Unix(),
			}}
		case ev.StopTask != "":
			if t.stops == nil {
				t.stops = map[string]string{}
			}
			t.stops[ev.ToolUseID] = ev.StopTask
		}
	case EventTypeToolResult:
		if e, ok := t.byToolUse[ev.ToolUseID]; ok && !e.started {
			m := bgResultID.FindStringSubmatch(ev.Text)
			if ev.IsError || m == nil {
				delete(t.byToolUse, ev.ToolUseID)
				break
			}
			e.started, e.task.ID = true, m[1]
		}
		if id, ok := t.stops[ev.ToolUseID]; ok {
			delete(t.stops, ev.ToolUseID)
			if !ev.IsError {
				t.finish(id, "")
			}
		}
	case EventTypeText:
		if ev.Role == "user" && strings.Contains(ev.Text, "<task-notification>") && notifState.MatchString(ev.Text) {
			// Only a notification carrying a <status> is terminal; a
			// monitor's per-event notifications are not.
			for _, m := range notifID.FindAllStringSubmatch(ev.Text, -1) {
				t.finish(m[1], "")
			}
			for _, m := range notifTool.FindAllStringSubmatch(ev.Text, -1) {
				t.finish("", m[1])
			}
		}
	}
}

func (t *bgTracker) finish(taskID, toolUseID string) {
	for k, e := range t.byToolUse {
		if (taskID != "" && e.started && e.task.ID == taskID) || (toolUseID != "" && k == toolUseID) {
			delete(t.byToolUse, k)
		}
	}
}

// list returns the outstanding tasks, oldest first.
func (t *bgTracker) list() []BackgroundTask {
	var out []BackgroundTask
	for _, e := range t.byToolUse {
		if e.started {
			out = append(out, e.task)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Since != out[j].Since {
			return out[i].Since < out[j].Since
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// backgroundSignature is a cheap comparable summary of a task list.
func backgroundSignature(tasks []BackgroundTask) string {
	var b strings.Builder
	for _, t := range tasks {
		b.WriteString(t.ID)
		b.WriteByte(',')
	}
	return b.String()
}
