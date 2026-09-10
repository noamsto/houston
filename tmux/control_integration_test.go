package tmux

import (
	"fmt"
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
	sub := cc.Subscribe(paneID)
	defer cc.Unsubscribe(paneID, sub)

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
		case ev := <-sub.C():
			received += string(ev.Data)
			if strings.Contains(received, "hello-control-mode") {
				return // success
			}
		case <-timer.C:
			t.Fatalf("timeout waiting for output, received so far: %q", received)
		}
	}
}

// drainPane discards anything already queued on a subscriber without
// blocking, so a later assertion isn't tripped by output left over from an
// earlier step.
func drainPane(sub *PaneSub) {
	for {
		select {
		case <-sub.C():
		default:
			return
		}
	}
}

// TestGapDeadlineResumeActuallyResumesThePane proves the gap deadline's
// resume command actually unpauses the pane, not just that it parses. tmux's
// command lexer rejects a bare %-prefixed token containing ':', which is why
// refresh-client -A %0:continue used to come back %error while looking
// like a no-op in anything that only checks for success.
func TestGapDeadlineResumeActuallyResumesThePane(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	client := NewClient()
	session := "houston-test-gapresume-" + strconv.Itoa(os.Getpid())
	err := client.run("new-session", "-d", "-s", session, "-x", "80", "-y", "24")
	if err != nil {
		t.Fatalf("failed to create test session: %v", err)
	}
	defer func() { _ = client.run("kill-session", "-t", session) }()

	out, err := client.output("display-message", "-t", session, "-p", "#{pane_id}")
	if err != nil {
		t.Fatalf("failed to get pane ID: %v", err)
	}
	paneID := strings.TrimSpace(string(out))

	cc := NewControlClient(session)
	// Out of reach, and expireGap driven by hand below — the same
	// construction TestGapDeadlineResumesAndMarksEverySubscriber uses to keep
	// the paused arm untimed instead of racing a clock. The timer path itself
	// is already covered by the fake-dialer tests.
	cc.gapDeadline = time.Hour
	if err := cc.Start(); err != nil {
		t.Fatalf("failed to start control client: %v", err)
	}
	defer func() { _ = cc.Close() }()

	sub := cc.Subscribe(paneID)
	defer cc.Unsubscribe(paneID, sub)

	if _, err := cc.RunCommand(fmt.Sprintf("refresh-client -A '%s:pause'", paneID)); err != nil {
		t.Fatalf("pause command failed: %v", err)
	}

	var g *gap
	for deadline := time.Now().Add(5 * time.Second); g == nil && time.Now().Before(deadline); {
		g = cc.gapFor(paneID)
		if g == nil {
			time.Sleep(10 * time.Millisecond)
		}
	}
	if g == nil {
		t.Fatalf("no gap open after pausing %s", paneID)
	}

	drainPane(sub)

	// The paused arm below reads a silence, and a dirty subscriber is silent
	// for an unrelated reason — dispatch withholds everything until it acks.
	// %pause deliberately marks nobody, so this must hold here; without the
	// check the arm could pass while proving nothing about the pane.
	if cc.isDirty(sub) {
		t.Fatal("subscriber is dirty before the paused arm, so its silence would prove nothing")
	}

	pid := strconv.Itoa(os.Getpid())
	pausedMarker := "houston-paused-marker-" + pid
	if err := cc.SendKeys(paneID, "echo "+pausedMarker); err != nil {
		t.Fatalf("SendKeys failed: %v", err)
	}
	if err := cc.SendSpecialKey(paneID, "Enter"); err != nil {
		t.Fatalf("SendSpecialKey Enter failed: %v", err)
	}

	var receivedWhilePaused string
	idle := time.NewTimer(2 * time.Second)
	defer idle.Stop()
paused:
	for {
		select {
		case ev := <-sub.C():
			receivedWhilePaused += string(ev.Data)
			if strings.Contains(receivedWhilePaused, pausedMarker) {
				t.Fatalf("marker reached the subscriber while the pane was paused: %q", receivedWhilePaused)
			}
		case <-idle.C:
			break paused
		}
	}

	go cc.expireGap(paneID, g)

	dirtyDeadline := time.NewTimer(5 * time.Second)
	defer dirtyDeadline.Stop()
	for gotDirty := false; !gotDirty; {
		select {
		case ev := <-sub.C():
			gotDirty = ev.Dirty
		case <-dirtyDeadline.C:
			t.Fatalf("timed out waiting for the Dirty event after expireGap")
		}
	}
	// dispatch withholds all data from a dirty subscriber until it acks, so
	// without this the resumed arm below can't pass even against correct code.
	cc.AckReseed(sub)

	resumedMarker := "houston-resumed-marker-" + pid
	if err := cc.SendKeys(paneID, "echo "+resumedMarker); err != nil {
		t.Fatalf("SendKeys failed: %v", err)
	}
	if err := cc.SendSpecialKey(paneID, "Enter"); err != nil {
		t.Fatalf("SendSpecialKey Enter failed: %v", err)
	}

	var receivedAfterResume string
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		select {
		case ev := <-sub.C():
			receivedAfterResume += string(ev.Data)
			if strings.Contains(receivedAfterResume, resumedMarker) {
				return
			}
		case <-timer.C:
			t.Fatalf("resumed output never reached the subscriber, received so far: %q", receivedAfterResume)
		}
	}
}
