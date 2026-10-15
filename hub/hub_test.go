package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/noamsto/houston/hook"
)

// writeState writes a hook state file at <dir>/claude/<sessionID>.json.
func writeState(t *testing.T, dir string, s hook.SessionState) {
	t.Helper()
	if err := hook.Write(hook.Path(dir, s.SessionID), s); err != nil {
		t.Fatalf("Write state: %v", err)
	}
}

// waitUntil polls cond every millisecond, failing after 5s.
func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); !cond(); time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

// startHub runs h and returns once its watcher is live. Call it after
// t.TempDir(): cleanup is LIFO, so Run has closed the watcher before the state
// dir is removed.
func startHub(t *testing.T, h *Hub) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = h.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("hub.Run did not return after cancel")
		}
	})

	// Run installs the watch before its initial scan, so the probe appearing
	// proves the watch exists; its removal is only observable through the watch.
	const probe = "startHub-probe"
	writeState(t, h.stateDir, hook.SessionState{SessionID: probe, State: hook.StateIdle})
	waitUntil(t, "hub to load the probe state", func() bool { return findSession(h, probe) != nil })
	if err := os.Remove(hook.Path(h.stateDir, probe)); err != nil {
		t.Fatalf("remove probe: %v", err)
	}
	waitUntil(t, "hub to drop the probe state", func() bool { return findSession(h, probe) == nil })
}

func silentLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

func TestHubLoadsExistingStateOnStart(t *testing.T) {
	dir := t.TempDir()
	writeState(t, dir, hook.SessionState{
		SessionID: "s1",
		State:     hook.StateThinking,
		Turn:      3,
		UpdatedAt: time.Now().Unix(),
	})

	h := NewWithOptions(dir, Options{ClaudeProjectsDir: "-"}, silentLog())
	startHub(t, h)

	waitForSnapshot(t, h, 1)

	snap := h.Snapshot()
	if snap[0].SessionID != "s1" || snap[0].State != hook.StateThinking || snap[0].Turn != 3 {
		t.Errorf("snapshot mismatch: %+v", snap[0])
	}
}

func TestHubPicksUpNewSessionAfterStart(t *testing.T) {
	dir := t.TempDir()
	h := NewWithOptions(dir, Options{ClaudeProjectsDir: "-"}, silentLog())
	startHub(t, h)

	sub := h.Subscribe()
	defer h.Unsubscribe(sub)

	writeState(t, dir, hook.SessionState{
		SessionID: "late",
		State:     hook.StateToolRunning,
		Tool:      "Edit",
		UpdatedAt: time.Now().Unix(),
	})

	select {
	case v := <-sub:
		if v.SessionID != "late" || v.Tool != "Edit" {
			t.Errorf("got %+v", v)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no update received")
	}
}

func TestHubIngestsTranscriptTrail(t *testing.T) {
	dir := t.TempDir()

	// Write a transcript containing one tool_use + tool_result.
	transcript := filepath.Join(t.TempDir(), "t.jsonl")
	body := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"tu_1","name":"Edit","input":{"file_path":"x.go"}}]}}` + "\n"
	if err := os.WriteFile(transcript, []byte(body), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}

	writeState(t, dir, hook.SessionState{
		SessionID:      "tr-1",
		TranscriptPath: transcript,
		State:          hook.StateToolRunning,
		Tool:           "Edit",
		UpdatedAt:      time.Now().Unix(),
	})

	h := NewWithOptions(dir, Options{ClaudeProjectsDir: "-"}, silentLog())
	startHub(t, h)

	waitForTrail(t, h, "tr-1", 1)

	snap := findSession(h, "tr-1")
	if snap == nil {
		t.Fatal("session tr-1 not in snapshot")
	}
	if len(snap.Trail) != 1 {
		t.Fatalf("trail len = %d, want 1", len(snap.Trail))
	}
	if snap.Trail[0].Tool != "Edit" || snap.Trail[0].Hint != "x.go" {
		t.Errorf("trail[0] = %+v", snap.Trail[0])
	}
	if snap.Trail[0].Done {
		t.Errorf("trail[0] should still be current (Done=false) before tool_result")
	}
}

// TestHubTrailNotResetOnSameTurnEvent pins the #100 invariant: the Activity
// trail is cleared only when a new turn starts (UserPromptSubmit increments
// Turn), not on every hook event. PostToolUse bumps Since and returns the state
// to thinking, which used to look like a new turn and wipe the trail.
func TestHubTrailNotResetOnSameTurnEvent(t *testing.T) {
	dir := t.TempDir()

	transcript := filepath.Join(t.TempDir(), "t.jsonl")
	toolUse := func(id, name, file string) string {
		return `{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"` + id + `","name":"` + name + `","input":{"file_path":"` + file + `"}}]}}` + "\n"
	}
	body := toolUse("tu_1", "Read", "a.go") + toolUse("tu_2", "Edit", "b.go")
	if err := os.WriteFile(transcript, []byte(body), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}

	state := hook.SessionState{
		SessionID:      "tr-2",
		TranscriptPath: transcript,
		State:          hook.StateThinking,
		Turn:           1,
		Since:          100,
		UpdatedAt:      time.Now().Unix(),
	}
	writeState(t, dir, state)

	h := NewWithOptions(dir, Options{ClaudeProjectsDir: "-"}, silentLog())
	startHub(t, h)

	waitForTrail(t, h, "tr-2", 2)

	// A later event in the same turn (e.g. PostToolUse) bumps Since but not
	// Turn. It must not clear the trail.
	state.Since = 200
	writeState(t, dir, state)

	// Append a third tool_use and force a transcript refresh with another
	// same-turn event; the trail must still hold all three chips.
	f, err := os.OpenFile(transcript, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open transcript: %v", err)
	}
	if _, err := f.WriteString(toolUse("tu_3", "Grep", "c.go")); err != nil {
		t.Fatalf("append transcript: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close transcript: %v", err)
	}
	state.Since = 300
	writeState(t, dir, state)

	waitUntil(t, "third chip ingested", func() bool {
		s := findSession(h, "tr-2")
		return s != nil && len(s.Trail) > 0 && s.Trail[len(s.Trail)-1].Tool == "Grep"
	})
	got := findSession(h, "tr-2")
	if got == nil {
		t.Fatal("session tr-2 vanished")
	}
	if len(got.Trail) < 3 {
		t.Fatalf("trail reset on same-turn event: len = %d, want >= 3", len(got.Trail))
	}
}

func TestHubBroadcastsOnStateRewrite(t *testing.T) {
	dir := t.TempDir()
	h := NewWithOptions(dir, Options{ClaudeProjectsDir: "-"}, silentLog())
	startHub(t, h)

	sub := h.Subscribe()
	defer h.Unsubscribe(sub)

	writeState(t, dir, hook.SessionState{SessionID: "b1", State: hook.StateThinking, UpdatedAt: time.Now().Unix()})
	drain(sub, time.Second)

	writeState(t, dir, hook.SessionState{SessionID: "b1", State: hook.StateWaiting, UpdatedAt: time.Now().Unix()})
	got := recv(t, sub, 2*time.Second)
	if got.State != hook.StateWaiting {
		t.Errorf("second update State = %q, want waiting", got.State)
	}
}

func TestSessionViewIsJSONMarshalable(t *testing.T) {
	v := SessionView{
		SessionID: "x",
		State:     hook.StateWaiting,
		Trail:     []TrailChip{{Tool: "Edit", Hint: "foo.go", Done: false}},
	}
	if _, err := json.Marshal(v); err != nil {
		t.Fatalf("marshal: %v", err)
	}
}

// ---- helpers ----

func waitForSnapshot(t *testing.T, h *Hub, want int) {
	t.Helper()
	waitUntil(t, "snapshot entries", func() bool { return len(h.Snapshot()) >= want })
}

func waitForTrail(t *testing.T, h *Hub, sessionID string, wantLen int) {
	t.Helper()
	waitUntil(t, "trail entries", func() bool {
		s := findSession(h, sessionID)
		return s != nil && len(s.Trail) >= wantLen
	})
}

func findSession(h *Hub, id string) *SessionView {
	for _, v := range h.Snapshot() {
		if v.SessionID == id {
			vv := v
			return &vv
		}
	}
	return nil
}

func drain(ch <-chan SessionView, timeout time.Duration) {
	deadline := time.After(timeout)
	for {
		select {
		case <-ch:
		case <-deadline:
			return
		case <-time.After(30 * time.Millisecond):
			return
		}
	}
}

func recv(t *testing.T, ch <-chan SessionView, timeout time.Duration) SessionView {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(timeout):
		t.Fatal("no message received")
		return SessionView{}
	}
}

func TestMergeStateIntoViewSurfacesLastMessageOnlyWhileWaiting(t *testing.T) {
	tests := []struct {
		state hook.State
		want  string
	}{
		{hook.StateWaiting, "msg"},
		{hook.StatePermission, "msg"},
		{hook.StateToolRunning, ""},
		{hook.StateThinking, ""},
		{hook.StateEnded, ""},
	}
	for _, tt := range tests {
		t.Run(string(tt.state), func(t *testing.T) {
			var v SessionView
			mergeStateIntoView(&v, hook.SessionState{State: tt.state, LastMessage: "msg"})
			if v.LastMessage != tt.want {
				t.Errorf("LastMessage = %q, want %q", v.LastMessage, tt.want)
			}
		})
	}
}

func TestMergeStateIntoViewAgent(t *testing.T) {
	tests := []struct {
		name  string
		agent string
		want  string
	}{
		{"engine set", "pi", "pi"},
		{"engine empty defaults to claude", "", "claude"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var v SessionView
			mergeStateIntoView(&v, hook.SessionState{Agent: tt.agent})
			if v.Agent != tt.want {
				t.Errorf("Agent = %q, want %q", v.Agent, tt.want)
			}
		})
	}
}

func TestPruneEnded(t *testing.T) {
	dir := t.TempDir()
	h := NewWithOptions(dir, Options{ClaudeProjectsDir: "-", PruneTTL: time.Hour}, silentLog())

	oldTime := time.Now().Add(-2 * time.Hour)

	writeState(t, dir, hook.SessionState{
		SessionID: "old-ended",
		State:     hook.StateEnded,
		UpdatedAt: oldTime.Unix(),
	})
	// Backdate the on-disk mtime to match production, where hook.Write sets
	// UpdatedAt and the file mtime moments apart. Not load-bearing for the
	// race guard itself (which only compares two stats taken within
	// pruneEnded's own read-to-remove window), but keeps this fixture
	// realistic.
	if err := os.Chtimes(hook.Path(dir, "old-ended"), oldTime, oldTime); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}

	writeState(t, dir, hook.SessionState{
		SessionID: "recent-ended",
		State:     hook.StateEnded,
		UpdatedAt: time.Now().Unix(),
	})

	writeState(t, dir, hook.SessionState{
		SessionID: "old-live",
		State:     hook.StateWaiting,
		UpdatedAt: oldTime.Unix(),
	})
	if err := os.Chtimes(hook.Path(dir, "old-live"), oldTime, oldTime); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}

	writeState(t, dir, hook.SessionState{
		SessionID: "raced-with-resume",
		State:     hook.StateEnded,
		UpdatedAt: oldTime.Unix(),
	})
	if err := os.Chtimes(hook.Path(dir, "raced-with-resume"), oldTime, oldTime); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}

	writeState(t, dir, hook.SessionState{
		SessionID: "raced-via-rename",
		State:     hook.StateEnded,
		UpdatedAt: oldTime.Unix(),
	})
	if err := os.Chtimes(hook.Path(dir, "raced-via-rename"), oldTime, oldTime); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}

	// Simulate a resumed session rewriting the file between pruneEnded's
	// read and its remove: land a real mtime change in that window via the
	// test-only preRemoveStat seam on the Hub, right where the guard's second
	// stat happens.
	racedPath := hook.Path(dir, "raced-with-resume")
	renamePath := hook.Path(dir, "raced-via-rename")
	h.preRemoveStat = func(path string) (os.FileInfo, error) {
		if path == racedPath {
			if err := os.Chtimes(path, time.Now(), time.Now()); err != nil {
				t.Fatalf("Chtimes: %v", err)
			}
		}
		if path == renamePath {
			// Simulate a resumed session writing via rename (hook.Write style)
			// then restoring the original mtime. SameFile catches the inode
			// change even though the mtime is identical.
			if err := hook.Write(path, hook.SessionState{
				SessionID: "raced-via-rename",
				State:     hook.StateEnded,
				UpdatedAt: oldTime.Unix(),
			}); err != nil {
				t.Fatalf("hook.Write: %v", err)
			}
			if err := os.Chtimes(path, oldTime, oldTime); err != nil {
				t.Fatalf("Chtimes: %v", err)
			}
		}
		return os.Stat(path)
	}

	h.pruneEnded(filepath.Join(dir, "claude"))

	if _, err := os.Stat(hook.Path(dir, "old-ended")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("old-ended: want removed, stat err = %v", err)
	}
	if _, err := os.Stat(hook.Path(dir, "recent-ended")); err != nil {
		t.Errorf("recent-ended: want kept, stat err = %v", err)
	}
	if _, err := os.Stat(hook.Path(dir, "old-live")); err != nil {
		t.Errorf("old-live: want kept, stat err = %v", err)
	}
	if _, err := os.Stat(racedPath); err != nil {
		t.Errorf("raced-with-resume: want kept, stat err = %v", err)
	}
	if _, err := os.Stat(renamePath); err != nil {
		t.Errorf("raced-via-rename: want kept, stat err = %v", err)
	}
}

// TestHubAsksFollowsTheLastAssistantText pins when SessionView.Asks is set and
// reset: only the final line of the last assistant text, cleared by any later
// tool or non-assistant activity, untouched by thinking or bookkeeping rows.
func TestHubAsksFollowsTheLastAssistantText(t *testing.T) {
	claude := func(role, block string) string {
		return `{"type":"` + role + `","message":{"role":"` + role + `","content":[` + block + `]}}` + "\n"
	}
	pi := func(role, block string) string {
		return `{"type":"message","message":{"role":"` + role + `","content":[` + block + `]}}` + "\n"
	}
	text := func(s string) string { return `{"type":"text","text":"` + s + `"}` }

	steps := []struct {
		name, line, want string
	}{
		{"assistant question", claude("assistant", text("Ready. Merge it?")), "Ready. Merge it?"},
		{"tool_use resets", claude("assistant", `{"type":"tool_use","id":"tu_1","name":"Bash","input":{"command":"ls"}}`), ""},
		{"tool_result keeps it empty", claude("user", `{"type":"tool_result","tool_use_id":"tu_1","content":"ok"}`), ""},
		{"statement", claude("assistant", text("All green.")), ""},
		{"question again", claude("assistant", text("A or B?")), "A or B?"},
		{"bookkeeping row", `{"type":"system","subtype":"stop_hook_summary"}` + "\n", "A or B?"},
		{"thinking block", claude("assistant", `{"type":"thinking","thinking":"hmm"}`), "A or B?"},
		{"user text resets", claude("user", text("A")), ""},
		{"pi toolCall in the same message resets", pi("assistant", text("Which?")+`,{"type":"toolCall","id":"c1","name":"bash","arguments":{}}`), ""},
		{"pi question", pi("assistant", text("Pick one?")), "Pick one?"},
		{"pi toolResult resets", pi("toolResult", text("ok")), ""},
	}

	path := filepath.Join(t.TempDir(), "t.jsonl")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	h := New(t.TempDir(), nil)
	h.sessions["s1"] = &Session{transcriptPath: path}
	for _, s := range steps {
		f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.WriteString(s.line); err != nil {
			t.Fatal(err)
		}
		_ = f.Close()
		h.refreshTranscript("s1")
		if got := h.sessions["s1"].view.Asks; got != s.want {
			t.Fatalf("after %s: Asks = %q, want %q", s.name, got, s.want)
		}
	}
}

// A tool_result that carries no text changes nothing but the last chip's
// Done/IsError, and must still reach subscribers.
func TestHubBroadcastsTrailChipDoneFlip(t *testing.T) {
	dir := t.TempDir()

	transcript := filepath.Join(t.TempDir(), "t.jsonl")
	toolUse := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"tu_1","name":"Edit","input":{"file_path":"x.go"}}]}}` + "\n"
	if err := os.WriteFile(transcript, []byte(toolUse), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	state := hook.SessionState{
		SessionID:      "flip-1",
		TranscriptPath: transcript,
		State:          hook.StateToolRunning,
		Tool:           "Edit",
		UpdatedAt:      time.Now().Unix(),
	}
	writeState(t, dir, state)

	h := NewWithOptions(dir, Options{ClaudeProjectsDir: "-"}, silentLog())
	startHub(t, h)
	waitForTrail(t, h, "flip-1", 1)

	sub := h.Subscribe()
	defer h.Unsubscribe(sub)

	// Settle the dedupe signature on the view that already holds the chip, so
	// the flip below is judged against it.
	state.Since = 1
	writeState(t, dir, state)
	for got := recv(t, sub, 2*time.Second); len(got.Trail) != 1; got = recv(t, sub, 2*time.Second) {
	}
	drain(sub, 200*time.Millisecond)

	toolResult := `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu_1","content":""}]}}` + "\n"
	f, err := os.OpenFile(transcript, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open transcript: %v", err)
	}
	if _, err := f.WriteString(toolResult); err != nil {
		t.Fatalf("append transcript: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close transcript: %v", err)
	}
	h.refreshTranscript("flip-1")

	got := recv(t, sub, 2*time.Second)
	if len(got.Trail) != 1 || !got.Trail[0].Done {
		t.Fatalf("broadcast trail = %+v, want one done chip", got.Trail)
	}
}

// A permission dialog the user answers "No" fires no hook event; only the
// guarded tool's error tool_result says it is over.
func TestHubHealsDismissedPermissionDialog(t *testing.T) {
	line := func(role, block string) string {
		return `{"type":"` + role + `","message":{"role":"` + role + `","content":[` + block + `]}}` + "\n"
	}
	toolUse := func(id, name string) string {
		return line("assistant", `{"type":"tool_use","id":"`+id+`","name":"`+name+`","input":{}}`)
	}
	rejected := func(id string) string {
		return line("user", `{"type":"tool_result","tool_use_id":"`+id+`","is_error":true,"content":"The user doesn't want to proceed"}`)
	}
	finished := func(id string) string {
		return line("user", `{"type":"tool_result","tool_use_id":"`+id+`","content":"ok"}`)
	}
	interrupted := line("user", `{"type":"text","text":"[Request interrupted by user for tool use]"}`)
	const bashHint = "rm -rf build"
	pre := func(tool string) (string, map[string]any) {
		input := map[string]any{}
		if tool == "Bash" {
			input["command"] = bashHint
		}
		return hook.EventPreToolUse, map[string]any{"tool_name": tool, "tool_input": input}
	}
	notify := map[string]any{"notification_type": "permission_prompt", "message": "Claude needs your permission"}

	type step struct {
		name    string
		event   string         // hook event to dispatch, if any
		payload map[string]any // its payload
		append  string         // transcript lines to append, if any
		file    int            // transcript the line and the event's transcript_path use
		want    hook.State
		tool    string // view.Tool wanted
		hint    string // view.ToolInputHint wanted
		msg     string // view.LastMessage wanted
	}
	const (
		blocked = hook.StatePermission
		idle    = hook.StateWaiting
	)
	preBash, preBashPayload := pre("Bash")
	preRead, preReadPayload := pre("Read")
	preAsk, preAskPayload := pre("AskUserQuestion")
	notifyStep := func(want hook.State, tool, hint, msg string) step {
		return step{name: "notification", event: hook.EventNotification, payload: notify, want: want, tool: tool, hint: hint, msg: msg}
	}
	prompt := step{name: "prompt", event: hook.EventUserPromptSubmit, payload: map[string]any{"prompt": "go on"}, want: hook.StateThinking}
	const asked = "Claude needs your permission"

	cases := []struct {
		name  string
		steps []step
	}{
		{"No after the notification heals", []step{
			{name: "pre", event: preBash, payload: preBashPayload, append: toolUse("tu_1", "Bash"), want: hook.StateToolRunning, tool: "Bash", hint: bashHint},
			notifyStep(blocked, "Bash", bashHint, asked),
			{name: "rejected", append: rejected("tu_1"), want: idle},
			{name: "interrupt text", append: interrupted, want: idle},
		}},
		{"No before the notification heals", []step{
			{name: "pre", event: preBash, payload: preBashPayload, append: toolUse("tu_1", "Bash"), want: hook.StateToolRunning, tool: "Bash", hint: bashHint},
			{name: "rejected", append: rejected("tu_1"), want: hook.StateToolRunning, tool: "Bash", hint: bashHint},
			notifyStep(idle, "", "", ""),
		}},
		{"AskUserQuestion dismissed heals", []step{
			{name: "pre", event: preAsk, payload: preAskPayload, append: toolUse("tu_1", "AskUserQuestion"), want: hook.StateToolRunning, tool: "AskUserQuestion"},
			notifyStep(blocked, "AskUserQuestion", "", asked),
			{name: "rejected", append: rejected("tu_1"), want: idle},
		}},
		{"another open tool keeps it blocked", []step{
			{name: "pre", event: preBash, payload: preBashPayload, append: toolUse("tu_1", "Read") + toolUse("tu_2", "Bash"), want: hook.StateToolRunning, tool: "Bash", hint: bashHint},
			notifyStep(blocked, "Bash", bashHint, asked),
			{name: "rejected", append: rejected("tu_2"), want: blocked, tool: "Bash", hint: bashHint, msg: asked},
			{name: "other finishes", append: finished("tu_1"), want: idle},
		}},
		{"the dialog's tool has no tool_use yet", []step{
			{name: "pre", event: preBash, payload: preBashPayload, append: toolUse("tu_1", "Read"), want: hook.StateToolRunning, tool: "Bash", hint: bashHint},
			notifyStep(blocked, "Bash", bashHint, asked),
			{name: "other result", append: finished("tu_1"), want: blocked, tool: "Bash", hint: bashHint, msg: asked},
		}},
		{"a multi-tool message heals once all are resolved", []step{
			{name: "pre", event: preBash, payload: preBashPayload, append: toolUse("tu_1", "Bash") + toolUse("tu_2", "Read"), want: hook.StateToolRunning, tool: "Bash", hint: bashHint},
			notifyStep(blocked, "Bash", bashHint, asked),
			{name: "both resolved", append: rejected("tu_1") + finished("tu_2"), want: idle},
		}},
		{"same-name siblings heal once all are resolved", []step{
			{name: "pre", event: preBash, payload: preBashPayload, append: toolUse("tu_1", "Bash") + toolUse("tu_2", "Bash"), want: hook.StateToolRunning, tool: "Bash", hint: bashHint},
			notifyStep(blocked, "Bash", bashHint, asked),
			{name: "both resolved", append: rejected("tu_1") + rejected("tu_2"), want: idle},
		}},
		{"same-name siblings stay blocked while one is unresolved", []step{
			{name: "pre", event: preBash, payload: preBashPayload, append: toolUse("tu_1", "Bash") + toolUse("tu_2", "Bash"), want: hook.StateToolRunning, tool: "Bash", hint: bashHint},
			notifyStep(blocked, "Bash", bashHint, asked),
			{name: "one resolved", append: rejected("tu_1"), want: blocked, tool: "Bash", hint: bashHint, msg: asked},
		}},
		{"parallel tools ignore user text", []step{
			{name: "pre", event: preRead, payload: preReadPayload, append: toolUse("tu_1", "Read") + toolUse("tu_2", "Read"), want: hook.StateToolRunning, tool: "Read"},
			notifyStep(blocked, "Read", "", asked),
			{name: "other resolved", append: finished("tu_2"), want: blocked, tool: "Read", msg: asked},
			{name: "user text", append: line("user", `{"type":"text","text":"keep going"}`), want: blocked, tool: "Read", msg: asked},
		}},
		{"an interrupted turn's open tool does not outlive its turn", []step{
			{name: "pre", event: preRead, payload: preReadPayload, append: toolUse("tu_1", "Read"), want: hook.StateToolRunning, tool: "Read"},
			prompt,
			{name: "next pre", event: preBash, payload: preBashPayload, append: toolUse("tu_2", "Bash"), want: hook.StateToolRunning, tool: "Bash", hint: bashHint},
			notifyStep(blocked, "Bash", bashHint, asked),
			{name: "rejected", append: rejected("tu_2"), want: idle},
		}},
		{"a new transcript starts without the old one's tools", []step{
			{name: "pre", event: preBash, payload: preBashPayload, append: toolUse("tu_1", "Bash"), want: hook.StateToolRunning, tool: "Bash", hint: bashHint},
			notifyStep(blocked, "Bash", bashHint, asked),
			{name: "rejected", append: rejected("tu_1"), want: idle},
			{name: "next pre", event: preBash, payload: preBashPayload, file: 1, want: hook.StateToolRunning, tool: "Bash", hint: bashHint},
			{name: "next notification", event: hook.EventNotification, payload: notify, file: 1, want: blocked, tool: "Bash", hint: bashHint, msg: asked},
			{name: "tool_use lands", append: toolUse("tu_2", "Bash"), file: 1, want: blocked, tool: "Bash", hint: bashHint, msg: asked},
			{name: "rejected", append: rejected("tu_2"), file: 1, want: idle},
		}},
		{"notification with no tool behind it", []step{
			{name: "notification", event: hook.EventNotification, payload: notify, append: toolUse("tu_1", "Bash") + rejected("tu_1"), want: blocked, msg: asked},
		}},
		{"no tool_use in the transcript", []step{
			{name: "pre", event: preBash, payload: preBashPayload, append: line("assistant", `{"type":"text","text":"Running it."}`), want: hook.StateToolRunning, tool: "Bash", hint: bashHint},
			notifyStep(blocked, "Bash", bashHint, asked),
		}},
		{"a new dialog after a healed one blocks again", []step{
			{name: "pre", event: preBash, payload: preBashPayload, append: toolUse("tu_1", "Bash"), want: hook.StateToolRunning, tool: "Bash", hint: bashHint},
			notifyStep(blocked, "Bash", bashHint, asked),
			{name: "rejected", append: rejected("tu_1"), want: idle},
			{name: "next pre", event: preRead, payload: preReadPayload, append: toolUse("tu_2", "Read"), want: hook.StateToolRunning, tool: "Read"},
			notifyStep(blocked, "Read", "", asked),
			{name: "same tool again", event: preBash, payload: preBashPayload, append: toolUse("tu_3", "Bash"), want: hook.StateToolRunning, tool: "Bash", hint: bashHint},
			notifyStep(blocked, "Bash", bashHint, asked),
		}},
		{"a later state is left alone", []step{
			{name: "pre", event: preBash, payload: preBashPayload, append: toolUse("tu_1", "Bash"), want: hook.StateToolRunning, tool: "Bash", hint: bashHint},
			notifyStep(blocked, "Bash", bashHint, asked),
			{name: "rejected", append: rejected("tu_1"), want: idle},
			{name: "thinking", event: hook.EventPostToolUse, payload: map[string]any{"tool_name": "Bash"}, want: hook.StateThinking},
			{name: "tool done in transcript", append: line("assistant", `{"type":"text","text":"ok"}`), want: hook.StateThinking},
		}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stateDir := t.TempDir()
			dir := t.TempDir()
			paths := []string{filepath.Join(dir, "t0.jsonl"), filepath.Join(dir, "t1.jsonl")}
			for _, p := range paths {
				if err := os.WriteFile(p, nil, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			h := New(stateDir, nil)
			for _, s := range c.steps {
				path := paths[s.file]
				if s.append != "" {
					f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
					if err != nil {
						t.Fatal(err)
					}
					if _, err := f.WriteString(s.append); err != nil {
						t.Fatal(err)
					}
					_ = f.Close()
				}
				if s.event != "" {
					payload := map[string]any{"session_id": "s1", "transcript_path": path, "hook_event_name": s.event}
					maps.Copy(payload, s.payload)
					b, err := json.Marshal(payload)
					if err != nil {
						t.Fatal(err)
					}
					if err := hook.Dispatch(s.event, stateDir, bytes.NewReader(b)); err != nil {
						t.Fatalf("%s: dispatch: %v", s.name, err)
					}
					h.loadStateFile(hook.Path(stateDir, "s1"))
				} else {
					h.refreshTranscript("s1")
				}
				v := h.sessions["s1"].view
				if v.State != s.want || v.Tool != s.tool || v.ToolInputHint != s.hint || v.LastMessage != s.msg {
					t.Fatalf("after %s: state=%q tool=%q hint=%q message=%q, want %q %q %q %q",
						s.name, v.State, v.Tool, v.ToolInputHint, v.LastMessage, s.want, s.tool, s.hint, s.msg)
				}
			}
		})
	}
}

// The hook file can lag the transcript: a quick "No" fires no Notification, a
// restart folds earlier turns, and a lost PostToolUse leaves tool-running.
func TestHubHealsStaleToolState(t *testing.T) {
	base := time.Now().Add(time.Hour).Truncate(time.Second)
	at := func(sec int) string { return base.Add(time.Duration(sec) * time.Second).Format(time.RFC3339Nano) }
	line := func(sec int, role, block string) string {
		return `{"type":"` + role + `","timestamp":"` + at(sec) + `","message":{"role":"` + role + `","content":[` + block + `]}}` + "\n"
	}
	toolUse := func(sec int, id, name string) string {
		return line(sec, "assistant", `{"type":"tool_use","id":"`+id+`","name":"`+name+`","input":{}}`)
	}
	result := func(sec int, id string, isErr bool) string {
		e := ""
		if isErr {
			e = `"is_error":true,`
		}
		return line(sec, "user", `{"type":"tool_result","tool_use_id":"`+id+`",`+e+`"content":"x"}`)
	}
	prompt := func(sec int) string { return line(sec, "user", `{"type":"text","text":"go on"}`) }
	reply := func(sec int) string { return line(sec, "assistant", `{"type":"text","text":"Done."}`) }

	cases := []struct {
		name       string
		transcript string
		tool       string
		event      string
		restart    bool // a fresh hub reads the transcript from byte 0
		want       hook.State
	}{
		{"answered AskUserQuestion, later turns, turn end", toolUse(1, "a", "AskUserQuestion") + result(2, "a", false) +
			prompt(3) + toolUse(4, "b", "Bash") + result(5, "b", false) + reply(6),
			"AskUserQuestion", hook.EventPreToolUse, false, hook.StateWaiting},
		{"quick No before the notification", toolUse(1, "a", "Bash") + result(2, "a", true),
			"Bash", hook.EventPreToolUse, false, hook.StateWaiting},
		{"Esc on AskUserQuestion before the notification", toolUse(1, "a", "AskUserQuestion") + result(2, "a", true),
			"AskUserQuestion", hook.EventPreToolUse, false, hook.StateWaiting},
		{"restart: earlier turn's unresolved tool", toolUse(1, "old", "Read") + prompt(2) + toolUse(3, "a", "Bash") + result(4, "a", true),
			"Bash", hook.EventNotification, true, hook.StateWaiting},
		{"restart: interrupt text is not a prompt", toolUse(1, "a", "Bash") + result(2, "a", true) +
			line(3, "user", `{"type":"text","text":"[Request interrupted by user for tool use]"}`),
			"Bash", hook.EventNotification, true, hook.StateWaiting},
		{"restart: unresolved tool of the same turn", toolUse(1, "old", "Read") + prompt(2) + toolUse(3, "other", "Read") + toolUse(4, "a", "Bash") + result(5, "a", true),
			"Bash", hook.EventNotification, true, hook.StatePermission},
		{"open permission dialog stays blocked", toolUse(1, "a", "Bash"),
			"Bash", hook.EventNotification, false, hook.StatePermission},
		{"open AskUserQuestion stays blocked", toolUse(1, "a", "AskUserQuestion"),
			"AskUserQuestion", hook.EventPreToolUse, false, hook.StateToolRunning},
		{"a finished tool is not an error", toolUse(1, "a", "Bash") + result(2, "a", false),
			"Bash", hook.EventPreToolUse, false, hook.StateToolRunning},
		{"a result older than the hook state", toolUse(-5, "a", "AskUserQuestion") + result(-4, "a", false) + toolUse(1, "b", "AskUserQuestion"),
			"AskUserQuestion", hook.EventPreToolUse, false, hook.StateToolRunning},
		{"a result older than the hook state, tool_use not yet read", toolUse(-5, "a", "AskUserQuestion") + result(-4, "a", false),
			"AskUserQuestion", hook.EventPreToolUse, false, hook.StateToolRunning},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stateDir := t.TempDir()
			path := filepath.Join(t.TempDir(), "t.jsonl")
			if err := os.WriteFile(path, []byte(c.transcript), 0o644); err != nil {
				t.Fatal(err)
			}
			payload := map[string]any{"session_id": "s1", "transcript_path": path, "hook_event_name": c.event, "tool_name": c.tool, "tool_input": map[string]any{}}
			if c.event == hook.EventNotification {
				payload["notification_type"] = "permission_prompt"
				payload["message"] = "Claude needs your permission"
				// The notification needs a tool behind it.
				pre, _ := json.Marshal(map[string]any{"session_id": "s1", "transcript_path": path, "hook_event_name": hook.EventPreToolUse, "tool_name": c.tool, "tool_input": map[string]any{}})
				if err := hook.Dispatch(hook.EventPreToolUse, stateDir, bytes.NewReader(pre)); err != nil {
					t.Fatal(err)
				}
			}
			b, _ := json.Marshal(payload)
			if err := hook.Dispatch(c.event, stateDir, bytes.NewReader(b)); err != nil {
				t.Fatal(err)
			}
			// The hook state's Since is wall-clock now; the transcript clock is
			// base, an hour ahead for events after it and (negative) before it.
			sp := hook.Path(stateDir, "s1")
			st, err := hook.Read(sp)
			if err != nil {
				t.Fatal(err)
			}
			st.Since = base.Unix()
			if err := hook.Write(sp, st); err != nil {
				t.Fatal(err)
			}
			h := New(stateDir, nil)
			h.loadStateFile(sp)
			if !c.restart {
				// Live: the hook arrived first, the transcript is read afterwards.
				h.refreshTranscript("s1")
			}
			if got := h.sessions["s1"].view.State; got != c.want {
				t.Fatalf("state = %q, want %q", got, c.want)
			}
		})
	}
}
