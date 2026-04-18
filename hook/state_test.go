package hook

import (
	"path/filepath"
	"testing"
	"time"
)

func TestWriteReadRoundtrip(t *testing.T) {
	dir := t.TempDir()
	path := Path(dir, "sess-1")

	in := SessionState{
		SessionID:      "sess-1",
		TranscriptPath: "/tmp/a.jsonl",
		CWD:            "/tmp",
		State:          StateToolRunning,
		Tool:           "Edit",
		ToolInputHint:  "ui/src/App.tsx",
		Turn:           3,
		Since:          time.Now().Unix(),
	}

	if err := Write(path, in); err != nil {
		t.Fatalf("Write: %v", err)
	}

	got, err := Read(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got.SessionID != in.SessionID || got.State != in.State || got.Tool != in.Tool {
		t.Fatalf("roundtrip mismatch: got %+v, want %+v", got, in)
	}
	if got.Version != StateSchemaVersion {
		t.Fatalf("Version not set on write: got %d, want %d", got.Version, StateSchemaVersion)
	}
	if got.UpdatedAt == 0 {
		t.Fatalf("UpdatedAt should be auto-set when zero")
	}
}

func TestReadMissingReturnsZero(t *testing.T) {
	dir := t.TempDir()
	s, err := Read(filepath.Join(dir, "nope.json"))
	if err != nil {
		t.Fatalf("expected nil err on missing file, got %v", err)
	}
	if s.SessionID != "" {
		t.Fatalf("expected zero value, got %+v", s)
	}
}

func TestWriteCreatesParentDir(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "does", "not", "exist", "sess.json")
	if err := Write(path, SessionState{SessionID: "x", State: StateWaiting}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if _, err := Read(path); err != nil {
		t.Fatalf("Read after Write with missing parent: %v", err)
	}
}

func TestPathUnderClaudeSubdir(t *testing.T) {
	got := Path("/state", "abc-def")
	want := "/state/claude/abc-def.json"
	if got != want {
		t.Fatalf("Path = %q, want %q", got, want)
	}
}
