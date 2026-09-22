package server

import (
	"encoding/json"
	"runtime"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// readUntilOutput reads messages off conn, skipping the "dims"/"seed"
// messages sent on connect, until it receives one "output" message.
//
// Receiving that message is the happens-after the teardown tests below rely
// on: paneWSWriteLoop starts `go paneWSReadLoop(...)` and, inside itself,
// `go metaPollLoop(...)` strictly before entering the select loop that can
// produce an "output" message. So by the time this returns, both goroutines
// are already running.
func readUntilOutput(t *testing.T, conn *websocket.Conn) {
	t.Helper()
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read message: %v", err)
		}
		var msg WSMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			t.Fatalf("unmarshal message: %v", err)
		}
		if msg.Type == "output" {
			return
		}
	}
}

// TestPaneWSTeardownOnClientDisconnect verifies that closing the client side
// of a pane connection tears down every goroutine servePane started for it,
// and releases its control-client reference exactly once.
func TestPaneWSTeardownOnClientDisconnect(t *testing.T) {
	fakeTmux := newFakeTmux()
	fakeTmux.paneID = "%1"
	fakeCC := newFakeControlClient()
	cm := newFakeControlManager(fakeCC)
	conn, cleanup := startPaneWS(t, fakeTmux, cm)
	defer cleanup()

	stopPing := pingPane(fakeCC, fakeTmux.paneID, 10*time.Millisecond)
	t.Cleanup(stopPing)

	// See readUntilOutput's doc: this is the required happens-after, not an
	// optional nicety. Snapshotting NumGoroutine any earlier would race the
	// creation of paneWSReadLoop/metaPollLoop.
	readUntilOutput(t, conn)
	n0 := runtime.NumGoroutine()

	_ = conn.Close()

	// Three goroutines should exit: paneWSReadLoop, paneWSWriteLoop itself
	// (running synchronously inside the httptest handler goroutine), and
	// metaPollLoop. The write loop only notices the dead connection on its
	// next write attempt, which is pingPane's next 10ms tick — so the
	// window here is generous relative to that interval.
	//
	// NumGoroutine is process-wide, so an unrelated goroutine exiting could
	// satisfy the count before the teardown has run; wait for the release too.
	want := n0 - 3
	deadline := time.Now().Add(3 * time.Second)
	for runtime.NumGoroutine() > want || len(cm.releaseClientCalls()) == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("teardown did not settle: want <= %d goroutines, got %d; releases = %v",
				want, runtime.NumGoroutine(), cm.releaseClientCalls())
		}
		time.Sleep(2 * time.Millisecond)
	}

	releases := cm.releaseClientCalls()
	if len(releases) != 1 || releases[0] != harnessPane.Session {
		t.Fatalf("releaseClientCalls = %v, want exactly one entry %q", releases, harnessPane.Session)
	}
}

// TestPaneWSMetaPollerDoesNotOutliveItsConnection guards against a
// regression where metaPollLoop was parked on the control client's shared,
// ref-counted Done channel instead of a per-connection done channel: with
// two connections open on the same session, closing one left its
// metaPollLoop running for as long as the other connection stayed open,
// because the shared Done channel only closes when the last reference is
// released.
//
// Each connection gets its own fakeTmux so captureModeCalls() attributes
// polls to one connection's poller; the control manager stays shared because
// its ref-counting is the point. A capture already in flight when A's done
// closed lands in the first window below, so the second window must see none
// from A. B's poller ticking is what proves time advanced.
func TestPaneWSMetaPollerDoesNotOutliveItsConnection(t *testing.T) {
	tmA := newFakeTmux()
	tmA.paneID = "%1"
	tmB := newFakeTmux()
	tmB.paneID = "%1"
	fakeCC := newFakeControlClient()
	cm := newFakeControlManager(fakeCC) // shared by both connections: ref-counting matters here

	connA, cleanupA := startPaneWS(t, tmA, cm)
	t.Cleanup(cleanupA)
	connB, cleanupB := startPaneWS(t, tmB, cm)
	t.Cleanup(cleanupB)

	stopPing := pingPane(fakeCC, tmA.paneID, 10*time.Millisecond)
	t.Cleanup(stopPing)

	// Per-connection happens-after: prove both connections' goroutine sets
	// exist before measuring poll counts.
	readUntilOutput(t, connA)
	readUntilOutput(t, connB)

	waitFor := func(what string, cond func() bool) {
		t.Helper()
		for deadline := time.Now().Add(5 * time.Second); !cond(); time.Sleep(time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s", what)
			}
		}
	}

	waitFor("both pollers to tick", func() bool {
		return tmA.captureModeCalls() >= 2 && tmB.captureModeCalls() >= 2
	})

	releasesBefore := len(cm.releaseClientCalls())
	_ = connA.Close() // close the client side directly; cleanupA still runs at test end and tolerates a second Close

	// The next pingPane tick after the close makes A's write loop notice the
	// dead connection and return, which runs servePane's defer and calls
	// ReleaseClient.
	waitFor("A's ReleaseClient", func() bool {
		return len(cm.releaseClientCalls()) == releasesBefore+1
	})

	b1 := tmB.captureModeCalls()
	waitFor("B's poller to tick after A's release", func() bool { return tmB.captureModeCalls() >= b1+5 })
	a1 := tmA.captureModeCalls()
	b2 := tmB.captureModeCalls()
	waitFor("B's poller to tick again", func() bool { return tmB.captureModeCalls() >= b2+5 })

	if got := tmA.captureModeCalls(); got != a1 {
		t.Fatalf("A's metaPollLoop kept polling after its connection closed: %d -> %d captures while B's poller ticked", a1, got)
	}
}

// TestPaneWSAutoZoomsOnConnectAndUnzoomsOnTeardown covers servePane's
// auto-zoom branch (weZoomed): with multiple panes in the window, it zooms
// the target pane on connect and un-zooms it in the teardown defer.
// TestPaneWSNeverResizesToClientDimensions deliberately sets windowPaneCount
// to 1 to keep auto-zoom out of its way, so this behavior needs its own test.
func TestPaneWSAutoZoomsOnConnectAndUnzoomsOnTeardown(t *testing.T) {
	fakeTmux := newFakeTmux()
	fakeTmux.paneID = "%1"
	fakeTmux.paneWidth = 80
	fakeTmux.paneHeight = 24       // non-zero so the "dims" message is actually sent (pane_ws.go only sends it when w,h > 0)
	fakeTmux.setWindowPaneCount(2) // >1 pane in the window is auto-zoom's precondition
	fakeCC := newFakeControlClient()
	cm := newFakeControlManager(fakeCC)

	conn, cleanup := startPaneWS(t, fakeTmux, cm)
	defer cleanup()

	// ZoomPane (if it fires) runs before the dims/seed sequence in servePane,
	// so the first message being "dims" is sufficient evidence auto-zoom
	// already ran one way or the other.
	_, data, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read first message: %v", err)
	}
	var msg WSMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		t.Fatalf("unmarshal first message: %v", err)
	}
	if msg.Type != "dims" {
		t.Fatalf("first message type = %q, want %q", msg.Type, "dims")
	}

	if got := fakeTmux.zoomPaneCalls(); got != 1 {
		t.Fatalf("zoomPaneCalls() after connect = %d, want 1 (auto-zoom on connect)", got)
	}

	if calls := cm.getClientCalls(); len(calls) != 1 || calls[0] != harnessPane.Session {
		t.Fatalf("getClientCalls() = %v, want exactly one entry %q", calls, harnessPane.Session)
	}

	stopPing := pingPane(fakeCC, fakeTmux.paneID, 10*time.Millisecond)
	t.Cleanup(stopPing)
	readUntilOutput(t, conn) // same happens-after as the other teardown tests, before closing

	_ = conn.Close()

	deadline := time.Now().Add(3 * time.Second)
	for fakeTmux.zoomPaneCalls() < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("zoomPaneCalls() did not reach 2 (un-zoom on teardown) within deadline, got %d", fakeTmux.zoomPaneCalls())
		}
		time.Sleep(2 * time.Millisecond)
	}
}
