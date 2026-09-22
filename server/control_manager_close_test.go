package server

import "testing"

// TestClose_ClosesControlManager proves Server.Close invokes controlMgr.Close,
// closing every tmux -C control-client child process it opened.
func TestClose_ClosesControlManager(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	session, _, _ := newTrapSession(t)

	s, err := New(Config{StatusDir: t.TempDir()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if _, err := s.controlMgr.GetClient(session); err != nil {
		t.Fatalf("GetClient: %v", err)
	}
	if !s.controlMgr.SessionStates()[session] {
		t.Fatal("session not connected after GetClient")
	}

	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if states := s.controlMgr.SessionStates(); len(states) != 0 {
		t.Fatalf("controlMgr still tracking sessions after Close: %v", states)
	}
}
