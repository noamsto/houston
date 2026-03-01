package tmux

import (
	"os"
	"strconv"
	"testing"
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
	defer client.run("kill-session", "-t", session)

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
