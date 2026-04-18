package hub

import (
	"context"
	"encoding/json"
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

func silentLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.NewFile(0, os.DevNull), &slog.HandlerOptions{Level: slog.LevelError}))
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
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = h.Run(ctx) }()

	waitForSnapshot(t, h, 1, time.Second)

	snap := h.Snapshot()
	if snap[0].SessionID != "s1" || snap[0].State != hook.StateThinking || snap[0].Turn != 3 {
		t.Errorf("snapshot mismatch: %+v", snap[0])
	}
}

func TestHubPicksUpNewSessionAfterStart(t *testing.T) {
	dir := t.TempDir()
	h := NewWithOptions(dir, Options{ClaudeProjectsDir: "-"}, silentLog())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = h.Run(ctx) }()

	// give Run a moment to set up fsnotify
	time.Sleep(50 * time.Millisecond)

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
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = h.Run(ctx) }()

	waitForTrail(t, h, "tr-1", 1, 2*time.Second)

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

func TestHubBroadcastsOnStateRewrite(t *testing.T) {
	dir := t.TempDir()
	h := NewWithOptions(dir, Options{ClaudeProjectsDir: "-"}, silentLog())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = h.Run(ctx) }()

	time.Sleep(50 * time.Millisecond)
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

func waitForSnapshot(t *testing.T, h *Hub, want int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if len(h.Snapshot()) >= want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("snapshot never reached %d entries", want)
}

func waitForTrail(t *testing.T, h *Hub, sessionID string, wantLen int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if s := findSession(h, sessionID); s != nil && len(s.Trail) >= wantLen {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("trail never reached %d entries for %s", wantLen, sessionID)
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
