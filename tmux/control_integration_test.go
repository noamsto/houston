package tmux

import (
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestControlClientIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	client := NewClient()
	session := "houston-test-ctrl-" + strconv.Itoa(os.Getpid())
	err := client.run("new-session", "-d", "-s", session, "-x", "80", "-y", "24")
	if err != nil {
		t.Fatalf("failed to create test session: %v", err)
	}
	defer func() { _ = client.run("kill-session", "-t", session) }()

	// Get the pane ID
	out, err := client.output("display-message", "-t", session, "-p", "#{pane_id}")
	if err != nil {
		t.Fatalf("failed to get pane ID: %v", err)
	}
	paneID := strings.TrimSpace(string(out))

	// Connect control client
	cc := NewControlClient(session)
	if err := cc.Start(); err != nil {
		t.Fatalf("failed to start control client: %v", err)
	}
	defer func() { _ = cc.Close() }()

	// Subscribe to pane output
	ch := cc.Subscribe(paneID)
	defer cc.Unsubscribe(paneID, ch)

	// Send keys via control client
	if err := cc.SendKeys(paneID, "echo hello-control-mode"); err != nil {
		t.Fatalf("SendKeys failed: %v", err)
	}
	if err := cc.SendSpecialKey(paneID, "Enter"); err != nil {
		t.Fatalf("SendSpecialKey Enter failed: %v", err)
	}

	// Wait for output containing our text
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	var received string
	for {
		select {
		case data := <-ch:
			received += string(data)
			if strings.Contains(received, "hello-control-mode") {
				return // success
			}
		case <-timer.C:
			t.Fatalf("timeout waiting for output, received so far: %q", received)
		}
	}
}
