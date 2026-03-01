// status/watcher_test.go
package status

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestReadStatusFilePlainText(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test-session")

	// Test old plain text format
	err := os.WriteFile(path, []byte("needs_attention"), 0644)
	if err != nil {
		t.Fatal(err)
	}

	status, err := readStatusFile(path)
	if err != nil {
		t.Fatal(err)
	}

	// "needs_attention" maps to StatusWaiting in new format
	if status.Status != StatusWaiting {
		t.Errorf("expected StatusWaiting, got %v", status.Status)
	}
}

func TestReadStatusFileJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test-session.json")

	// Test new JSON format
	data := statusFile{
		TmuxSession: "test-session",
		Status:      "permission",
		Message:     "Allow Bash?",
		Timestamp:   time.Now().Unix(),
	}
	jsonData, _ := json.Marshal(data)
	err := os.WriteFile(path, jsonData, 0644)
	if err != nil {
		t.Fatal(err)
	}

	status, err := readStatusFile(path)
	if err != nil {
		t.Fatal(err)
	}

	if status.Status != StatusPermission {
		t.Errorf("expected StatusPermission, got %v", status.Status)
	}
	if status.Message != "Allow Bash?" {
		t.Errorf("expected 'Allow Bash?', got %v", status.Message)
	}
	if status.Session != "test-session" {
		t.Errorf("expected 'test-session', got %v", status.Session)
	}
}

func TestStatusNeedsAttention(t *testing.T) {
	tests := []struct {
		status Status
		wants  bool
	}{
		{StatusUnknown, false},
		{StatusIdle, false},
		{StatusWorking, false},
		{StatusWaiting, true},
		{StatusPermission, true},
	}

	for _, tc := range tests {
		if got := tc.status.NeedsAttention(); got != tc.wants {
			t.Errorf("Status(%v).NeedsAttention() = %v, want %v", tc.status, got, tc.wants)
		}
	}
}

func TestSessionStatusIsFresh(t *testing.T) {
	fresh := SessionStatus{UpdatedAt: time.Now()}
	stale := SessionStatus{UpdatedAt: time.Now().Add(-1 * time.Minute)}

	if !fresh.IsFresh(30 * time.Second) {
		t.Error("expected fresh status to be fresh")
	}
	if stale.IsFresh(30 * time.Second) {
		t.Error("expected stale status to not be fresh")
	}
}

func TestFilenameConversion(t *testing.T) {
	tests := []struct {
		session  string
		filename string
	}{
		{"main", "main.json"},
		{"mono/main", "mono%main.json"},
		{"mono/feature/test", "mono%feature%test.json"},
	}

	for _, tc := range tests {
		got := sessionToFilename(tc.session)
		if got != tc.filename {
			t.Errorf("sessionToFilename(%q) = %q, want %q", tc.session, got, tc.filename)
		}

		back := filenameToSession(tc.filename)
		if back != tc.session {
			t.Errorf("filenameToSession(%q) = %q, want %q", tc.filename, back, tc.session)
		}
	}
}

func TestWatcherGetAll(t *testing.T) {
	dir := t.TempDir()

	// Write old format
	_ = os.WriteFile(filepath.Join(dir, "session1"), []byte("idle"), 0644)

	// Write new JSON format
	data := statusFile{
		TmuxSession: "session2",
		Status:      "working",
		Timestamp:   time.Now().Unix(),
	}
	jsonData, _ := json.Marshal(data)
	_ = os.WriteFile(filepath.Join(dir, "session2.json"), jsonData, 0644)

	w := NewWatcher(dir)
	statuses := w.GetAll()

	if len(statuses) != 2 {
		t.Errorf("expected 2 statuses, got %d", len(statuses))
	}

	if statuses["session1"].Status != StatusIdle {
		t.Errorf("expected session1 to be idle")
	}
	if statuses["session2"].Status != StatusWorking {
		t.Errorf("expected session2 to be working")
	}
}
