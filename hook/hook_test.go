package hook

import (
	"encoding/json"
	"strings"
	"testing"
)

// dispatch is a test helper that builds a payload, runs Dispatch, and loads
// the resulting session file.
func dispatch(t *testing.T, stateDir string, event string, payload map[string]any) SessionState {
	t.Helper()
	payload["session_id"], _ = payload["session_id"].(string)
	if payload["session_id"] == "" {
		payload["session_id"] = "sess-test"
	}
	payload["hook_event_name"] = event
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	if err := Dispatch(event, stateDir, strings.NewReader(string(b))); err != nil {
		t.Fatalf("Dispatch %s: %v", event, err)
	}
	got, err := Read(Path(stateDir, payload["session_id"].(string)))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	return got
}

func TestDispatchPreToolUseSetsToolRunning(t *testing.T) {
	dir := t.TempDir()
	got := dispatch(t, dir, EventPreToolUse, map[string]any{
		"session_id":      "s1",
		"tool_name":       "Edit",
		"tool_input":      map[string]any{"file_path": "ui/src/App.tsx"},
		"cwd":             "/tmp",
		"transcript_path": "/tmp/t.jsonl",
	})
	if got.State != StateToolRunning {
		t.Errorf("State = %q, want %q", got.State, StateToolRunning)
	}
	if got.Tool != "Edit" {
		t.Errorf("Tool = %q, want Edit", got.Tool)
	}
	if got.ToolInputHint != "ui/src/App.tsx" {
		t.Errorf("ToolInputHint = %q, want file_path", got.ToolInputHint)
	}
	if got.Since == 0 {
		t.Errorf("Since not set on state transition")
	}
}

func TestDispatchPostToolUseClearsTool(t *testing.T) {
	dir := t.TempDir()
	dispatch(t, dir, EventPreToolUse, map[string]any{
		"session_id": "s2",
		"tool_name":  "Bash",
		"tool_input": map[string]any{"command": "go test ./..."},
	})
	got := dispatch(t, dir, EventPostToolUse, map[string]any{
		"session_id": "s2",
		"tool_name":  "Bash",
	})
	if got.State != StateThinking {
		t.Errorf("State after Post = %q, want thinking", got.State)
	}
	if got.Tool != "" {
		t.Errorf("Tool should be cleared after PostToolUse, got %q", got.Tool)
	}
}

func TestDispatchStopWaiting(t *testing.T) {
	dir := t.TempDir()
	got := dispatch(t, dir, EventStop, map[string]any{"session_id": "s3"})
	if got.State != StateWaiting {
		t.Errorf("Stop → %q, want waiting", got.State)
	}
}

func TestDispatchNotificationPermissionPrompt(t *testing.T) {
	dir := t.TempDir()
	got := dispatch(t, dir, EventNotification, map[string]any{
		"session_id":        "s4",
		"notification_type": "permission_prompt",
		"message":           "Claude needs permission to run Bash",
	})
	if got.State != StatePermission {
		t.Errorf("State = %q, want waiting:permission", got.State)
	}
	if got.LastMessage == "" {
		t.Errorf("LastMessage should be populated")
	}
}

func TestDispatchNotificationIdlePrompt(t *testing.T) {
	dir := t.TempDir()
	got := dispatch(t, dir, EventNotification, map[string]any{
		"session_id":        "s5",
		"notification_type": "idle_prompt",
		"message":           "Waiting for your input",
	})
	if got.State != StateWaiting {
		t.Errorf("State = %q, want waiting", got.State)
	}
}

func TestDispatchMergeSemantics(t *testing.T) {
	// SessionStart sets cwd + transcript. A later PreToolUse without those
	// fields should NOT wipe them.
	dir := t.TempDir()
	dispatch(t, dir, EventSessionStart, map[string]any{
		"session_id":      "s6",
		"cwd":             "/home/me/project",
		"transcript_path": "/home/me/.claude/projects/foo/s6.jsonl",
		"source":          "startup",
	})
	got := dispatch(t, dir, EventPreToolUse, map[string]any{
		"session_id": "s6",
		"tool_name":  "Read",
		"tool_input": map[string]any{"file_path": "main.go"},
	})
	if got.CWD != "/home/me/project" {
		t.Errorf("CWD wiped by PreToolUse: %q", got.CWD)
	}
	if got.TranscriptPath == "" {
		t.Errorf("TranscriptPath wiped by PreToolUse")
	}
	if got.Source != "startup" {
		t.Errorf("Source wiped: %q", got.Source)
	}
}

func TestDispatchUserPromptSubmitIncrementsTurn(t *testing.T) {
	dir := t.TempDir()
	dispatch(t, dir, EventUserPromptSubmit, map[string]any{"session_id": "s7"})
	got := dispatch(t, dir, EventUserPromptSubmit, map[string]any{"session_id": "s7"})
	if got.Turn != 2 {
		t.Errorf("Turn = %d after two prompts, want 2", got.Turn)
	}
	if got.State != StateThinking {
		t.Errorf("State = %q, want thinking", got.State)
	}
}

func TestDispatchSessionEndRecordsReason(t *testing.T) {
	dir := t.TempDir()
	got := dispatch(t, dir, EventSessionEnd, map[string]any{
		"session_id": "s8",
		"reason":     "logout",
	})
	if got.State != StateEnded {
		t.Errorf("State = %q, want ended", got.State)
	}
	if got.Reason != "logout" {
		t.Errorf("Reason = %q, want logout", got.Reason)
	}
}

func TestDispatchMissingSessionIDIsError(t *testing.T) {
	dir := t.TempDir()
	payload := map[string]any{"hook_event_name": EventStop}
	b, _ := json.Marshal(payload)
	err := Dispatch(EventStop, dir, strings.NewReader(string(b)))
	if err == nil {
		t.Fatalf("expected error for missing session_id, got nil")
	}
}

func TestToolInputHint(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"file_path", `{"file_path":"a/b.go"}`, "a/b.go"},
		{"command", `{"command":"go test"}`, "go test"},
		{"pattern", `{"pattern":"func Foo"}`, "func Foo"},
		{"url", `{"url":"https://example.com"}`, "https://example.com"},
		{"empty", `{}`, ""},
		{"truncate", `{"command":"` + strings.Repeat("x", 200) + `"}`, strings.Repeat("x", 119) + "…"},
		{"invalid_json", `not json`, ""},
		{"preferred_order", `{"command":"bash","file_path":"x.go"}`, "x.go"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := toolInputHint(json.RawMessage(tc.in))
			if got != tc.want {
				t.Errorf("toolInputHint(%s) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestTmuxCoordsReportsPaneIDNotIndex(t *testing.T) {
	// Every source correlates on the tmux pane id (%17). "#P" is the pane
	// *index* (1), which is not the same namespace and is not even unique —
	// every session has a pane 1. The pane's own environment is authoritative.
	t.Setenv("TMUX_PANE", "%307")

	_, _, pane := tmuxCoords()

	if pane != "%307" {
		t.Errorf("pane = %q, want %%307 — a pane index here can never merge with the tmux layer", pane)
	}
}

func TestTmuxCoordsEmptyOutsideTmux(t *testing.T) {
	t.Setenv("TMUX_PANE", "")

	session, window, pane := tmuxCoords()

	if session != "" || window != "" || pane != "" {
		t.Errorf("got (%q, %q, %q), want all empty outside tmux", session, window, pane)
	}
}
