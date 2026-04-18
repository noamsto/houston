package hub

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/noamsto/houston/hook"
)

// Utility: build a fake projects dir with one project containing N transcripts.
func fakeProjectsDir(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	projectDir := filepath.Join(root, "-home-me-project")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(projectDir, name), []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return root
}

func TestDiscoveryFindsWaitingSession(t *testing.T) {
	body := `{"type":"user","message":{"role":"user","content":"hi"}}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"What do you want me to do?"}]}}
`
	root := fakeProjectsDir(t, map[string]string{"sess-aaa.jsonl": body})

	got, err := DiscoverClaudeSessions(root, time.Hour)
	if err != nil {
		t.Fatalf("DiscoverClaudeSessions: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d sessions, want 1", len(got))
	}
	s := got[0]
	if s.SessionID != "sess-aaa" {
		t.Errorf("SessionID = %q, want sess-aaa", s.SessionID)
	}
	if s.State != hook.StateWaiting {
		t.Errorf("State = %q, want waiting (assistant text, no pending tool)", s.State)
	}
	if s.TranscriptPath == "" {
		t.Errorf("TranscriptPath empty")
	}
	if s.CWD == "" {
		t.Errorf("CWD empty (should be derived from project dir name)")
	}
}

func TestDiscoveryInfersToolRunningWhenRecent(t *testing.T) {
	body := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"tu_1","name":"Bash","input":{"command":"sleep 60"}}]}}
`
	root := fakeProjectsDir(t, map[string]string{"sess-running.jsonl": body})

	got, err := DiscoverClaudeSessions(root, time.Hour)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d, want 1", len(got))
	}
	if got[0].State != hook.StateToolRunning {
		t.Errorf("State = %q, want tool-running", got[0].State)
	}
	if got[0].Tool != "Bash" {
		t.Errorf("Tool = %q, want Bash", got[0].Tool)
	}
}

func TestDiscoveryMarksOldUnmatchedToolAsEnded(t *testing.T) {
	body := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"tu_1","name":"Bash","input":{"command":"x"}}]}}
`
	root := fakeProjectsDir(t, map[string]string{"sess-old.jsonl": body})

	// Backdate the file so the "running" heuristic doesn't apply.
	path := filepath.Join(root, "-home-me-project", "sess-old.jsonl")
	old := time.Now().Add(-5 * time.Minute)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	got, err := DiscoverClaudeSessions(root, 24*time.Hour)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d", len(got))
	}
	if got[0].State != hook.StateEnded {
		t.Errorf("State = %q, want ended (stale unmatched tool_use)", got[0].State)
	}
}

func TestDiscoverySkipsTranscriptsOutsideWindow(t *testing.T) {
	body := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"done"}]}}`
	root := fakeProjectsDir(t, map[string]string{"sess-ancient.jsonl": body})
	path := filepath.Join(root, "-home-me-project", "sess-ancient.jsonl")
	ancient := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(path, ancient, ancient); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	got, _ := DiscoverClaudeSessions(root, 24*time.Hour)
	if len(got) != 0 {
		t.Errorf("got %d sessions for transcript older than window, want 0", len(got))
	}
}

func TestDiscoveryMissingDirIsNotError(t *testing.T) {
	got, err := DiscoverClaudeSessions(filepath.Join(t.TempDir(), "nope"), time.Hour)
	if err != nil {
		t.Errorf("missing dir → err %v, want nil", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d, want 0", len(got))
	}
}

func TestDecodeProjectDirName(t *testing.T) {
	cases := map[string]string{
		"-home-noams-git-noamsto-houston": "/home/noams/git/noamsto/houston",
		"no-leading-dash":                 "no-leading-dash",
	}
	for in, want := range cases {
		if got := decodeProjectDirName(in); got != want {
			t.Errorf("decodeProjectDirName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestHubSeedsDiscoveredSessionOnStart(t *testing.T) {
	body := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Ready."}]}}`
	projects := fakeProjectsDir(t, map[string]string{"sess-dsc.jsonl": body})

	stateDir := t.TempDir()
	h := NewWithOptions(stateDir, Options{ClaudeProjectsDir: projects}, slog.Default())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = h.Run(ctx) }()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(h.Snapshot()) > 0 {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	snap := h.Snapshot()
	if len(snap) != 1 {
		t.Fatalf("snapshot has %d sessions, want 1", len(snap))
	}
	if snap[0].SessionID != "sess-dsc" {
		t.Errorf("SessionID = %q", snap[0].SessionID)
	}
	if snap[0].State != hook.StateWaiting {
		t.Errorf("State = %q, want waiting", snap[0].State)
	}
}

func TestHubHookStateWinsOverDiscovery(t *testing.T) {
	// Discovery would mark this as waiting (no pending tool). Then a hook
	// fires with tool-running state — the hook's state must win.
	body := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"hi"}]}}`
	projects := fakeProjectsDir(t, map[string]string{"dual.jsonl": body})

	stateDir := t.TempDir()
	// Seed a hook state file before starting — simulates a hook firing between
	// discovery and hub pickup. (In practice it'd be fsnotify after startup.)
	if err := hook.Write(hook.Path(stateDir, "dual"), hook.SessionState{
		SessionID: "dual",
		State:     hook.StateToolRunning,
		Tool:      "Edit",
		UpdatedAt: time.Now().Unix(),
	}); err != nil {
		t.Fatalf("seed hook: %v", err)
	}

	h := NewWithOptions(stateDir, Options{ClaudeProjectsDir: projects}, slog.Default())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = h.Run(ctx) }()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(h.Snapshot()) > 0 {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	snap := h.Snapshot()
	if len(snap) != 1 || snap[0].State != hook.StateToolRunning {
		t.Fatalf("hook state should win: %+v", snap)
	}
}
