package hub

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
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

func TestHubIngestsTranscriptTrailAndPreview(t *testing.T) {
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
	if got := findSession(h, "tr-2"); len(got.Trail) < 3 {
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

	// Simulate a resumed session rewriting the file between pruneEnded's
	// read and its remove: land a real mtime change in that window via the
	// test-only preRemoveStat seam, right where the guard's second stat
	// happens.
	racedPath := hook.Path(dir, "raced-with-resume")
	prevPreRemoveStat := preRemoveStat
	preRemoveStat = func(path string) (os.FileInfo, error) {
		if path == racedPath {
			if err := os.Chtimes(path, time.Now(), time.Now()); err != nil {
				t.Fatalf("Chtimes: %v", err)
			}
		}
		return os.Stat(path)
	}
	defer func() { preRemoveStat = prevPreRemoveStat }()

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
}
