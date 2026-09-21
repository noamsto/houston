package tmux

import (
	"os"
	"strconv"
	"testing"
	"time"
)

func TestControlManagerGetClient(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	client := NewClient()
	session := "houston-test-mgr-" + strconv.Itoa(os.Getpid())
	err := client.run("new-session", "-d", "-s", session, "-x", "80", "-y", "24")
	if err != nil {
		t.Fatalf("failed to create test session: %v", err)
	}
	defer func() { _ = client.run("kill-session", "-t", session) }()

	mgr := NewControlManager()
	defer mgr.Close()

	// First get creates a new client
	cc1, err := mgr.GetClient(session)
	if err != nil {
		t.Fatalf("GetClient failed: %v", err)
	}

	// Second get returns the same client
	cc2, err := mgr.GetClient(session)
	if err != nil {
		t.Fatalf("second GetClient failed: %v", err)
	}
	if cc1 != cc2 {
		t.Error("expected same client instance")
	}

	// Release twice (ref count drops to 0) — client should be closed
	mgr.ReleaseClient(session)
	mgr.ReleaseClient(session)

	// Wait for the close goroutine to finish
	<-cc1.Done()

	// Next get creates a new client
	cc3, err := mgr.GetClient(session)
	if err != nil {
		t.Fatalf("third GetClient failed: %v", err)
	}
	if cc3 == cc1 {
		t.Error("expected new client after release")
	}
	mgr.ReleaseClient(session)
}

// TestControlManagerSessionStatesAndChanges drives a real ControlClient through
// a drop and a reconnect and asserts the manager-wide aggregation reflects it,
// without a tmux binary. This is the producer-path coverage the runs-level
// regression builds on.
func TestControlManagerSessionStatesAndChanges(t *testing.T) {
	release := make(chan struct{})
	d := &recordingDialer{onDial: func(i int, _ *recordedConn) {
		if i > 0 {
			<-release
		}
	}}
	m := NewControlManager()
	m.newClient = func(session string) *ControlClient {
		cc := NewControlClient(session)
		cc.dial = d.dial
		cc.backoff = time.Millisecond
		return cc
	}
	defer m.Close()

	if _, err := m.GetClient("s"); err != nil {
		t.Fatalf("GetClient: %v", err)
	}
	if !m.SessionStates()["s"] {
		t.Fatal("session not connected after GetClient")
	}

	// The client watcher forwards exactly one attach transition; consume it so
	// the next read is the disconnect edge.
	select {
	case <-m.Changes():
	case <-time.After(2 * time.Second):
		t.Fatal("no Changes signal after GetClient")
	}

	conn0 := d.connAt(t, 0)
	_ = conn0.pw.Close()

	select {
	case <-m.Changes():
	case <-time.After(2 * time.Second):
		t.Fatal("no Changes signal after the connection dropped")
	}
	if m.SessionStates()["s"] {
		t.Fatal("SessionStates()[s] = true while the re-dial is parked")
	}

	close(release)

	select {
	case <-m.Changes():
	case <-time.After(2 * time.Second):
		t.Fatal("no Changes signal after the re-attach")
	}
	if !m.SessionStates()["s"] {
		t.Fatal("session not connected after re-attach")
	}
}
