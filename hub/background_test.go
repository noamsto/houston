package hub

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func bgStartLine(id, tool, input, ts string) string {
	return fmt.Sprintf(`{"type":"assistant","timestamp":%q,"message":{"role":"assistant","content":[{"type":"tool_use","id":%q,"name":%q,"input":%s}]}}`, ts, id, tool, input) + "\n"
}

func bgResultLine(id, text string, isErr bool) string {
	return fmt.Sprintf(`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":%q,"content":%q,"is_error":%t}]}}`, id, text, isErr) + "\n"
}

func notificationText(taskIDs []string, toolUse, status string) string {
	var b strings.Builder
	b.WriteString("<task-notification>\n")
	for _, id := range taskIDs {
		b.WriteString("<task-id>" + id + "</task-id>\n")
	}
	if toolUse != "" {
		b.WriteString("<tool-use-id>" + toolUse + "</tool-use-id>\n")
	}
	if status != "" {
		b.WriteString("<status>" + status + "</status>\n")
	}
	b.WriteString("<summary>x</summary>\n</task-notification>")
	return b.String()
}

func bgNotificationUserLine(text string) string {
	return fmt.Sprintf(`{"type":"user","message":{"role":"user","content":%q},"origin":{"kind":"task-notification"}}`, text) + "\n"
}

func bgNotificationQueueLine(text string) string {
	return fmt.Sprintf(`{"type":"queue-operation","operation":"enqueue","content":%q}`, text) + "\n"
}

const (
	shellInput   = `{"command":"sleep 300","description":"Sleep five minutes","run_in_background":true}`
	monitorInput = `{"command":"gh pr checks 1","description":"CI checks","timeout_ms":1800000,"persistent":false}`
	shellResult  = "Command running in background with ID: %s. Output is being written to: /tmp/x.output"
)

func trackerFor(t *testing.T, transcript string) []BackgroundTask {
	t.Helper()
	path := writeJSONL(t, transcript)
	evs, _, err := ReadTranscriptFrom(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	var tr bgTracker
	for _, ev := range evs {
		tr.apply(ev)
	}
	return tr.list(time.Time{})
}

func TestBackgroundStartIsOutstanding(t *testing.T) {
	got := trackerFor(t,
		bgStartLine("tu1", "Bash", shellInput, "2026-01-01T00:00:00Z")+
			bgResultLine("tu1", fmt.Sprintf(shellResult, "bsh1"), false)+
			bgStartLine("tu2", "Monitor", monitorInput, "2026-01-01T00:01:00Z")+
			bgResultLine("tu2", "Monitor started (task bmon2, timeout 1800000ms). You will be notified", false))
	if len(got) != 2 {
		t.Fatalf("got %+v, want 2 tasks", got)
	}
	if got[0].ID != "bsh1" || got[0].Kind != BackgroundShell || got[0].Hint != "Sleep five minutes" || got[0].Since != 1767225600 {
		t.Errorf("shell = %+v", got[0])
	}
	if got[1].ID != "bmon2" || got[1].Kind != BackgroundMonitor || got[1].Hint != "CI checks" {
		t.Errorf("monitor = %+v", got[1])
	}
}

func TestBackgroundForegroundAndFailedLaunchesAreIgnored(t *testing.T) {
	got := trackerFor(t,
		bgStartLine("tu1", "Bash", `{"command":"ls"}`, "2026-01-01T00:00:00Z")+
			bgResultLine("tu1", "file", false)+
			bgStartLine("tu2", "Bash", shellInput, "2026-01-01T00:00:00Z")+
			bgResultLine("tu2", "boom", true)+
			bgStartLine("tu3", "Bash", shellInput, "2026-01-01T00:00:00Z"))
	if len(got) != 0 {
		t.Fatalf("got %+v, want none", got)
	}
}

func TestBackgroundCleared(t *testing.T) {
	start := bgStartLine("tu1", "Bash", shellInput, "2026-01-01T00:00:00Z") +
		bgResultLine("tu1", fmt.Sprintf(shellResult, "bsh1"), false)
	cases := map[string]string{
		"notification by task id":       bgNotificationUserLine(notificationText([]string{"bsh1"}, "", "completed")),
		"notification by tool use id":   bgNotificationUserLine(notificationText([]string{"zzz"}, "tu1", "failed")),
		"queued notification":           bgNotificationQueueLine(notificationText([]string{"bsh1"}, "tu1", "killed")),
		"orphan summary lists many ids": bgNotificationUserLine(notificationText([]string{"a", "bsh1", "b"}, "", "stopped")),
		"KillShell":                     bgStartLine("tu9", "KillShell", `{"shell_id":"bsh1"}`, "2026-01-01T00:00:01Z") + bgResultLine("tu9", "killed", false),
		"TaskStop":                      bgStartLine("tu9", "TaskStop", `{"task_id":"bsh1"}`, "2026-01-01T00:00:01Z") + bgResultLine("tu9", "stopped", false),
	}
	for name, tail := range cases {
		if got := trackerFor(t, start+tail); len(got) != 0 {
			t.Errorf("%s: got %+v, want cleared", name, got)
		}
	}
}

func TestBackgroundNotificationWithoutStatusKeepsTask(t *testing.T) {
	got := trackerFor(t,
		bgStartLine("tu1", "Monitor", monitorInput, "2026-01-01T00:00:00Z")+
			bgResultLine("tu1", "Monitor started (task bmon, timeout 1ms)", false)+
			bgNotificationUserLine(notificationText([]string{"bmon"}, "tu1", "")))
	if len(got) != 1 {
		t.Fatalf("got %+v, want the monitor still outstanding", got)
	}
}

func TestBackgroundFailedTaskStopKeepsTask(t *testing.T) {
	got := trackerFor(t,
		bgStartLine("tu1", "Bash", shellInput, "2026-01-01T00:00:00Z")+
			bgResultLine("tu1", fmt.Sprintf(shellResult, "bsh1"), false)+
			bgStartLine("tu2", "TaskStop", `{"task_id":"bsh1"}`, "2026-01-01T00:00:01Z")+
			bgResultLine("tu2", "no such task", true))
	if len(got) != 1 {
		t.Fatalf("got %+v, want task kept", got)
	}
}

// A houston restart replays the transcript from byte 0; reading it in
// arbitrary chunks must land on the same outstanding set.
func TestBackgroundReplayEqualsIncremental(t *testing.T) {
	lines := []string{
		bgStartLine("tu1", "Bash", shellInput, "2026-01-01T00:00:00Z"),
		bgResultLine("tu1", fmt.Sprintf(shellResult, "bsh1"), false),
		bgStartLine("tu2", "Bash", shellInput, "2026-01-01T00:00:05Z"),
		bgResultLine("tu2", fmt.Sprintf(shellResult, "bsh2"), false),
		bgNotificationQueueLine(notificationText([]string{"bsh1"}, "tu1", "completed")),
		bgNotificationUserLine(notificationText([]string{"bsh1"}, "tu1", "completed")),
	}
	full := trackerFor(t, strings.Join(lines, ""))

	path := writeJSONL(t, "")
	var tr bgTracker
	var off int64
	for _, l := range lines {
		f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
		_, _ = f.WriteString(l)
		_ = f.Close()
		evs, n, err := ReadTranscriptFrom(path, off)
		if err != nil {
			t.Fatal(err)
		}
		off = n
		for _, ev := range evs {
			tr.apply(ev)
		}
	}
	inc := tr.list(time.Time{})
	if len(full) != 1 || full[0].ID != "bsh2" || len(inc) != 1 || inc[0] != full[0] {
		t.Fatalf("replay %+v incremental %+v, want only bsh2", full, inc)
	}
}

func TestReadTranscriptLeavesPartialLineForNextPoll(t *testing.T) {
	line := bgNotificationUserLine(notificationText([]string{"bsh1"}, "tu1", "completed"))
	path := writeJSONL(t, line[:len(line)/2])
	evs, off, err := ReadTranscriptFrom(path, 0)
	if err != nil || len(evs) != 0 || off != 0 {
		t.Fatalf("partial: evs=%d off=%d err=%v, want nothing consumed", len(evs), off, err)
	}
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	_, _ = f.WriteString(line[len(line)/2:])
	_ = f.Close()
	evs, off, _ = ReadTranscriptFrom(path, off)
	if len(evs) == 0 || off != int64(len(line)) {
		t.Fatalf("completed: evs=%d off=%d, want line consumed", len(evs), off)
	}
}

func TestHubRefreshClearsBackgroundAcrossPartialWrite(t *testing.T) {
	start := bgStartLine("tu1", "Bash", shellInput, "2026-01-01T00:00:00Z") +
		bgResultLine("tu1", fmt.Sprintf(shellResult, "bsh1"), false)
	end := bgNotificationUserLine(notificationText([]string{"bsh1"}, "tu1", "completed"))
	path := writeJSONL(t, start+end[:20])

	h := New(t.TempDir(), nil)
	h.sessions["s1"] = &Session{transcriptPath: path}
	h.refreshTranscript("s1")
	if got := h.sessions["s1"].view.Background; len(got) != 1 {
		t.Fatalf("before end record: %+v, want 1 outstanding", got)
	}
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	_, _ = f.WriteString(end[20:])
	_ = f.Close()
	h.refreshTranscript("s1")
	if got := h.sessions["s1"].view.Background; len(got) != 0 {
		t.Fatalf("after end record: %+v, want cleared", got)
	}
}

func TestBackgroundEventPayloadTagsEndNothing(t *testing.T) {
	payload := "<task-notification>\n<task-id>bmon</task-id>\n<tool-use-id>tu1</tool-use-id>\n<event>\nlog: <status>completed</status> <task-id>bsh2</task-id>\n</event>\n</task-notification>"
	got := trackerFor(t,
		bgStartLine("tu1", "Monitor", monitorInput, "2026-01-01T00:00:00Z")+
			bgResultLine("tu1", "Monitor started (task bmon, timeout 1ms)", false)+
			bgStartLine("tu2", "Bash", shellInput, "2026-01-01T00:00:00Z")+
			bgResultLine("tu2", fmt.Sprintf(shellResult, "bsh2"), false)+
			bgNotificationUserLine(payload))
	if len(got) != 2 {
		t.Fatalf("got %+v, want both still outstanding", got)
	}
}

func TestBackgroundMissingTimestampLeavesSinceUnset(t *testing.T) {
	got := trackerFor(t,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"tu1","name":"Bash","input":`+shellInput+`}]}}`+"\n"+
			bgResultLine("tu1", fmt.Sprintf(shellResult, "bsh1"), false))
	if len(got) != 1 || got[0].Since != 0 {
		t.Fatalf("got %+v, want one task with Since 0", got)
	}
}

func TestBackgroundMonitorExpiresAtItsTimeout(t *testing.T) {
	path := writeJSONL(t,
		bgStartLine("tu1", "Monitor", `{"command":"x","description":"a","timeout_ms":60000}`, "2026-01-01T00:00:00Z")+
			bgResultLine("tu1", "Monitor started (task bmon, timeout 60000ms)", false)+
			bgStartLine("tu2", "Monitor", `{"command":"y","description":"b","timeout_ms":60000,"persistent":true}`, "2026-01-01T00:00:00Z")+
			bgResultLine("tu2", "Monitor started (task bper, timeout 60000ms)", false))
	evs, _, _ := ReadTranscriptFrom(path, 0)
	var tr bgTracker
	for _, ev := range evs {
		tr.apply(ev)
	}
	start := time.Unix(1767225600, 0)
	if got := tr.list(start.Add(30 * time.Second)); len(got) != 2 {
		t.Fatalf("before timeout: %+v, want 2", got)
	}
	if got := tr.list(start.Add(61 * time.Second)); len(got) != 1 || got[0].ID != "bper" {
		t.Fatalf("after timeout: %+v, want only the persistent monitor", got)
	}
}
