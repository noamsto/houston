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

	stopPing := pingPane(fakeCC, fakeTmux.paneID, 100*time.Millisecond)
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
	// next write attempt, which is pingPane's next 100ms tick — so the
	// window here is generous relative to that interval.
	want := n0 - 3
	deadline := time.Now().Add(3 * time.Second)
	for runtime.NumGoroutine() > want {
		if time.Now().After(deadline) {
			t.Fatalf("goroutine count did not settle: want <= %d, got %d", want, runtime.NumGoroutine())
		}
		time.Sleep(50 * time.Millisecond)
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
func TestPaneWSMetaPollerDoesNotOutliveItsConnection(t *testing.T) {
	fakeTmux := newFakeTmux()
	fakeTmux.paneID = "%1"
	fakeCC := newFakeControlClient()
	cm := newFakeControlManager(fakeCC) // shared by both connections: ref-counting matters here

	connA, cleanupA := startPaneWS(t, fakeTmux, cm)
	t.Cleanup(cleanupA)
	connB, cleanupB := startPaneWS(t, fakeTmux, cm)
	t.Cleanup(cleanupB)

	stopPing := pingPane(fakeCC, fakeTmux.paneID, 100*time.Millisecond)
	t.Cleanup(stopPing)

	// Per-connection happens-after: prove both connections' goroutine sets
	// exist before measuring poll rates.
	readUntilOutput(t, connA)
	readUntilOutput(t, connB)

	before := fakeTmux.captureModeCalls()
	time.Sleep(1200 * time.Millisecond) // >1 tick of each 1s metaPollLoop ticker
	duringBoth := fakeTmux.captureModeCalls() - before
	if duringBoth < 2 {
		t.Fatalf("duringBoth = %d, want >= 2 (expected roughly one tick from each of 2 live pollers before closing anything)", duringBoth)
	}

	releasesBefore := len(cm.releaseClientCalls())
	_ = connA.Close() // close the client side directly; cleanupA still runs at test end and tolerates a second Close

	// Wait for A's teardown to land: the next pingPane tick after the close
	// makes A's write loop notice the dead connection and return, which runs
	// servePane's defer and calls ReleaseClient.
	deadline := time.Now().Add(3 * time.Second)
	for len(cm.releaseClientCalls()) != releasesBefore+1 {
		if time.Now().After(deadline) {
			t.Fatalf("releaseClientCalls did not grow by exactly one within deadline: got %v", cm.releaseClientCalls())
		}
		time.Sleep(50 * time.Millisecond)
	}

	beforeSolo := fakeTmux.captureModeCalls()
	time.Sleep(2200 * time.Millisecond) // comfortably more than 2 ticks of B's still-live 1s ticker
	duringSolo := fakeTmux.captureModeCalls() - beforeSolo

	// One live poller ticking every 1s over a ~2.2s window produces roughly
	// 2 calls; two live pollers (A's zombie plus B's, the regression this
	// test guards against) would produce roughly 4. The [1,3] band is wide
	// enough to absorb ticker jitter while staying well clear of the
	// two-poller regime.
	if duringSolo < 1 || duringSolo > 3 {
		t.Fatalf("duringSolo = %d, want in [1,3] (consistent with exactly one live metaPollLoop, not two)", duringSolo)
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

	stopPing := pingPane(fakeCC, fakeTmux.paneID, 100*time.Millisecond)
	t.Cleanup(stopPing)
	readUntilOutput(t, conn) // same happens-after as the other teardown tests, before closing

	_ = conn.Close()

	deadline := time.Now().Add(3 * time.Second)
	for fakeTmux.zoomPaneCalls() < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("zoomPaneCalls() did not reach 2 (un-zoom on teardown) within deadline, got %d", fakeTmux.zoomPaneCalls())
		}
		time.Sleep(50 * time.Millisecond)
	}
}
