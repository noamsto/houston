package server

import "testing"

// newFullServer builds a full Server via New and registers Close as cleanup. t.Cleanup is
// LIFO, so call it after t.TempDir(): Close then runs before the directory is
// removed and no background goroutine is still writing into it.
func newFullServer(t *testing.T, cfg Config) *Server {
	t.Helper()
	s, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return s
}

func TestCloseStopsBackgroundGoroutines(t *testing.T) {
	s, err := New(Config{StatusDir: t.TempDir()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	select {
	case <-s.pumpDone:
	default:
		t.Fatal("deltas pump still running after Close")
	}
}
