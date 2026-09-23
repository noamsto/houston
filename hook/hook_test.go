package hook

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"sync"
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
	if got.ToolInputHint != "App.tsx" {
		t.Errorf("ToolInputHint = %q, want basename", got.ToolInputHint)
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
		tool string
		in   string
		want string
	}{
		{"file_path", "Grep", `{"file_path":"a/b.go"}`, "a/b.go"},
		{"command", "Grep", `{"command":"go test"}`, "go test"},
		{"pattern", "Grep", `{"pattern":"func Foo"}`, "func Foo"},
		{"url", "Grep", `{"url":"https://example.com"}`, "https://example.com"},
		{"empty", "Grep", `{}`, ""},
		{"truncate", "Grep", `{"command":"` + strings.Repeat("x", 200) + `"}`, strings.Repeat("x", 119) + "…"},
		{"invalid_json", "Grep", `not json`, ""},
		{"preferred_order", "Grep", `{"command":"bash","file_path":"x.go"}`, "x.go"},
		{"bash_description", "Bash", `{"description":"Build the UI","command":"npm run build"}`, "Build the UI"},
		{"bash_no_description", "Bash", `{"command":"npm run build"}`, "npm run build"},
		{"read_basename", "Read", `{"file_path":"/home/user/project/src/App.tsx"}`, "App.tsx"},
		{"edit_basename", "Edit", `{"file_path":"/home/user/project/src/App.tsx"}`, "App.tsx"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ToolHint(tc.tool, json.RawMessage(tc.in))
			if got != tc.want {
				t.Errorf("ToolHint(%q, %s) = %q, want %q", tc.tool, tc.in, got, tc.want)
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

func TestTmuxEnv(t *testing.T) {
	tests := []struct {
		name       string
		pane       string
		tmux       string
		wantPane   string
		wantServer string
	}{
		{"normal", "%20", "/tmp/tmux-1000/default,1966,0", "%20", "1966"},
		{"socket path contains a comma", "%20", "/tmp/weird,dir/tmux-1000,default,1966,0", "%20", "1966"},
		{"unset", "", "", "", ""},
		{"malformed, no commas", "%20", "not-a-tmux-value", "%20", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("TMUX_PANE", tt.pane)
			t.Setenv("TMUX", tt.tmux)

			pane, server := tmuxEnv()

			if pane != tt.wantPane || server != tt.wantServer {
				t.Errorf("tmuxEnv() = (%q, %q), want (%q, %q)", pane, server, tt.wantPane, tt.wantServer)
			}
		})
	}
}

func TestDispatchNotificationMessageLivesOnlyUntilTheNextEvent(t *testing.T) {
	for _, event := range []string{
		EventSessionStart, EventUserPromptSubmit, EventPreToolUse, EventPostToolUse,
		EventStop, EventSubagentStop, EventPreCompact, EventSessionEnd,
	} {
		t.Run(event, func(t *testing.T) {
			dir := t.TempDir()
			dispatch(t, dir, EventNotification, map[string]any{
				"session_id": "s", "notification_type": "permission_prompt", "message": "Allow Bash?",
			})
			if got := dispatch(t, dir, event, map[string]any{"session_id": "s"}); got.LastMessage != "" {
				t.Errorf("LastMessage after %s = %q, want cleared", event, got.LastMessage)
			}
		})
	}
}

func TestDispatchNotificationReplacesThePreviousMessage(t *testing.T) {
	dir := t.TempDir()
	dispatch(t, dir, EventNotification, map[string]any{"session_id": "s", "message": "first"})
	got := dispatch(t, dir, EventNotification, map[string]any{"session_id": "s", "message": "second"})
	if got.LastMessage != "second" {
		t.Errorf("LastMessage = %q, want second", got.LastMessage)
	}
}

// stubTmuxDisplay replaces tmuxDisplay for the duration of a test and counts
// invocations.
func stubTmuxDisplay(t *testing.T, f func(pane string) ([]byte, error)) *int {
	t.Helper()
	calls := 0
	orig := tmuxDisplay
	tmuxDisplay = func(pane string) ([]byte, error) {
		calls++
		return f(pane)
	}
	t.Cleanup(func() { tmuxDisplay = orig })
	return &calls
}

func TestDispatchResumedSessionFollowsTheNewPane(t *testing.T) {
	// A session started on pane %20/server 1966 must refresh
	// its recorded coordinates and server identity whenever either one
	// changes underneath it, and must not re-exec tmux when neither does.
	tests := []struct {
		name        string
		pane, tmux  string // second generation's env
		wantPane    string
		wantSession string
		wantWindow  string
		wantServer  string
		wantCalls   int // tmuxDisplay calls across the whole sequence
	}{
		{
			name: "same server, new pane (#98)",
			pane: "%24", tmux: "/s,1966,0",
			wantPane: "%24", wantSession: "toddl", wantWindow: "1", wantServer: "1966",
			wantCalls: 2,
		},
		{
			name: "same pane, new server",
			pane: "%20", tmux: "/s,2001,0",
			wantPane: "%20", wantSession: "toddl", wantWindow: "1", wantServer: "2001",
			wantCalls: 2,
		},
		{
			name: "both changed",
			pane: "%24", tmux: "/s,2001,0",
			wantPane: "%24", wantSession: "toddl", wantWindow: "1", wantServer: "2001",
			wantCalls: 2,
		},
		{
			name: "neither changed",
			pane: "%20", tmux: "/s,1966,0",
			wantPane: "%20", wantSession: "origsess", wantWindow: "0", wantServer: "1966",
			wantCalls: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			stage2 := false
			calls := stubTmuxDisplay(t, func(string) ([]byte, error) {
				if stage2 {
					return []byte("toddl\t1"), nil
				}
				return []byte("origsess\t0"), nil
			})
			t.Setenv("TMUX_PANE", "%20")
			t.Setenv("TMUX", "/s,1966,0")
			dispatch(t, dir, EventSessionStart, map[string]any{"session_id": "s"})
			dispatch(t, dir, EventPreToolUse, map[string]any{"session_id": "s", "tool_name": "Bash"})

			stage2 = true
			t.Setenv("TMUX_PANE", tt.pane)
			t.Setenv("TMUX", tt.tmux)
			got := dispatch(t, dir, EventPreToolUse, map[string]any{"session_id": "s", "tool_name": "Bash"})

			if got.TmuxPane != tt.wantPane {
				t.Errorf("TmuxPane = %q, want %q", got.TmuxPane, tt.wantPane)
			}
			if got.TmuxSession != tt.wantSession {
				t.Errorf("TmuxSession = %q, want %q", got.TmuxSession, tt.wantSession)
			}
			if got.TmuxWindow != tt.wantWindow {
				t.Errorf("TmuxWindow = %q, want %q", got.TmuxWindow, tt.wantWindow)
			}
			if got.TmuxServer != tt.wantServer {
				t.Errorf("TmuxServer = %q, want %q", got.TmuxServer, tt.wantServer)
			}
			if *calls != tt.wantCalls {
				t.Errorf("tmuxDisplay called %d times, want %d", *calls, tt.wantCalls)
			}
		})
	}
}

func TestDispatchUnchangedEnvDoesNotReexec(t *testing.T) {
	// A3: once the environment is recorded, further events with the same
	// environment must not spawn tmux again.
	dir := t.TempDir()
	calls := stubTmuxDisplay(t, func(string) ([]byte, error) {
		return []byte("sess\t0"), nil
	})
	t.Setenv("TMUX_PANE", "%20")
	t.Setenv("TMUX", "/s,1966,0")
	dispatch(t, dir, EventSessionStart, map[string]any{"session_id": "s"})
	dispatch(t, dir, EventPreToolUse, map[string]any{"session_id": "s", "tool_name": "Bash"})
	dispatch(t, dir, EventPostToolUse, map[string]any{"session_id": "s", "tool_name": "Bash"})

	if *calls != 1 {
		t.Errorf("tmuxDisplay called %d times, want 1", *calls)
	}
}

func TestDispatchRetriesAfterAFailedDisplayMessage(t *testing.T) {
	// A failed display-message leaves TmuxSession empty while TmuxPane is
	// still set; the next event must retry rather than treating that as
	// "already recorded".
	dir := t.TempDir()
	fail := true
	calls := stubTmuxDisplay(t, func(string) ([]byte, error) {
		if fail {
			return nil, errors.New("tmux: no such pane")
		}
		return []byte("sess\t0"), nil
	})
	t.Setenv("TMUX_PANE", "%20")
	t.Setenv("TMUX", "/s,1966,0")
	got := dispatch(t, dir, EventSessionStart, map[string]any{"session_id": "s"})
	if got.TmuxSession != "" || got.TmuxPane != "%20" {
		t.Fatalf("setup: got session=%q pane=%q, want empty session, pane %%20", got.TmuxSession, got.TmuxPane)
	}

	fail = false
	got = dispatch(t, dir, EventPreToolUse, map[string]any{"session_id": "s", "tool_name": "Bash"})

	if got.TmuxSession != "sess" {
		t.Errorf("TmuxSession = %q after retry, want sess", got.TmuxSession)
	}
	if *calls != 2 {
		t.Errorf("tmuxDisplay called %d times, want 2 (initial failure + retry)", *calls)
	}
}

func TestDispatchStaleUnderLockRetries(t *testing.T) {
	// The unlocked pre-read can find the pane fresh (skipping the tmux exec)
	// while another process's write lands before the lock is taken, so the
	// locked re-read is stale. Dispatch must still emit the "display-message
	// failed" shape (fetchedCoords was never set) so the next event retries.
	dir := t.TempDir()
	calls := stubTmuxDisplay(t, func(string) ([]byte, error) {
		return []byte("sess\t0"), nil
	})
	t.Setenv("TMUX_PANE", "%20")
	t.Setenv("TMUX", "/s,1966,0")
	dispatch(t, dir, EventSessionStart, map[string]any{"session_id": "s"})
	if *calls != 1 {
		t.Fatalf("setup: tmuxDisplay called %d times, want 1", *calls)
	}

	orig := beforeLock
	t.Cleanup(func() { beforeLock = orig })
	beforeLock = func() {
		if err := Write(Path(dir, "s"), SessionState{TmuxPane: "%99", TmuxServer: "1966"}); err != nil {
			t.Fatalf("beforeLock write: %v", err)
		}
	}

	before := *calls
	got := dispatch(t, dir, EventPostToolUse, map[string]any{"session_id": "s", "tool_name": "Bash"})
	if *calls != before {
		t.Errorf("tmuxDisplay called %d new times during the racing dispatch, want 0", *calls-before)
	}
	if got.TmuxPane != "%20" {
		t.Errorf("TmuxPane = %q, want %%20 (current $TMUX_PANE)", got.TmuxPane)
	}
	if got.TmuxSession != "" {
		t.Errorf("TmuxSession = %q, want empty (failed-display-message shape)", got.TmuxSession)
	}

	beforeLock = orig
	before = *calls
	got = dispatch(t, dir, EventPostToolUse, map[string]any{"session_id": "s", "tool_name": "Bash"})
	if *calls != before+1 {
		t.Errorf("tmuxDisplay called %d times on retry, want 1", *calls-before)
	}
	if got.TmuxSession != "sess" {
		t.Errorf("TmuxSession = %q after retry, want sess", got.TmuxSession)
	}
}

func TestDispatchSessionStartAlwaysRefreshes(t *testing.T) {
	// Even with an unchanged environment, SessionStart must recompute — it
	// marks a fresh (or resumed) process, and a stale entry from a killed
	// process must not linger uncorrected.
	dir := t.TempDir()
	calls := stubTmuxDisplay(t, func(string) ([]byte, error) {
		return []byte("sess\t0"), nil
	})
	t.Setenv("TMUX_PANE", "%20")
	t.Setenv("TMUX", "/s,1966,0")
	dispatch(t, dir, EventSessionStart, map[string]any{"session_id": "s"})
	dispatch(t, dir, EventSessionStart, map[string]any{"session_id": "s"})

	if *calls != 2 {
		t.Errorf("tmuxDisplay called %d times across two SessionStarts, want 2", *calls)
	}
}

func TestDispatchSetsPIDEvenWithoutARefresh(t *testing.T) {
	// Outside tmux, pane and server both stay "" across events, so stale is
	// always false and a non-SessionStart first event never takes the
	// refresh branch. PID must still end up set.
	dir := t.TempDir()
	t.Setenv("TMUX_PANE", "")
	t.Setenv("TMUX", "")
	got := dispatch(t, dir, EventPreToolUse, map[string]any{"session_id": "s", "tool_name": "Bash"})

	if got.PID == 0 {
		t.Error("PID = 0, want it set even without a refresh")
	}
}

func TestDispatchClearsPaneWhenNoLongerUnderTmux(t *testing.T) {
	dir := t.TempDir()
	stubTmuxDisplay(t, func(string) ([]byte, error) {
		return []byte("sess\t0"), nil
	})
	t.Setenv("TMUX_PANE", "%20")
	t.Setenv("TMUX", "/s,1966,0")
	dispatch(t, dir, EventSessionStart, map[string]any{"session_id": "s"})

	t.Setenv("TMUX_PANE", "")
	t.Setenv("TMUX", "")
	got := dispatch(t, dir, EventPreToolUse, map[string]any{"session_id": "s", "tool_name": "Bash"})

	if got.TmuxSession != "" || got.TmuxWindow != "" || got.TmuxPane != "" || got.TmuxServer != "" {
		t.Errorf("coordinates not cleared outside tmux: %+v", got)
	}
}

// TestDispatchConcurrentEventsDoNotLoseUpdates covers R4a: post_tool, turn_end,
// prompt_submit and session_start all arrive as detached fire-and-forget
// processes from hookyard, so concurrent Dispatch calls on the same session
// must not silently clobber one another's read-modify-write.
func TestDispatchConcurrentEventsDoNotLoseUpdates(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TMUX_PANE", "")
	t.Setenv("TMUX", "")

	payload, err := json.Marshal(map[string]any{
		"session_id":      "concurrent",
		"hook_event_name": EventUserPromptSubmit,
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}

	const n = 50
	var wg sync.WaitGroup
	errs := make(chan error, n)
	wg.Add(n)
	for range n {
		go func() {
			defer wg.Done()
			errs <- Dispatch(EventUserPromptSubmit, dir, bytes.NewReader(payload))
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("Dispatch: %v", err)
		}
	}

	got, err := Read(Path(dir, "concurrent"))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got.Turn != n {
		t.Errorf("Turn = %d after %d concurrent UserPromptSubmit, want %d", got.Turn, n, n)
	}
}
