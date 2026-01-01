// status/watcher_test.go
package status

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadStatusFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test-session")

	err := os.WriteFile(path, []byte("needs_attention"), 0644)
	if err != nil {
		t.Fatal(err)
	}

	status, err := readStatusFile(path)
	if err != nil {
		t.Fatal(err)
	}

	if status != StatusNeedsAttention {
		t.Errorf("expected StatusNeedsAttention, got %v", status)
	}
}

func TestWatcherGetAll(t *testing.T) {
	dir := t.TempDir()

	os.WriteFile(filepath.Join(dir, "session1"), []byte("needs_attention"), 0644)
	os.WriteFile(filepath.Join(dir, "session2"), []byte("idle"), 0644)

	w := NewWatcher(dir)
	statuses := w.GetAll()

	if len(statuses) != 2 {
		t.Errorf("expected 2 statuses, got %d", len(statuses))
	}
}
