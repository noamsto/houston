package server

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/noamsto/houston/tmux"
)

// readUntilType reads messages off conn until one of type want arrives,
// skipping meta/output frames that can interleave with it (metaPollLoop and
// a live pane both race the message under test).
func readUntilType(t *testing.T, conn *websocket.Conn, want string, timeout time.Duration) wsEnvelope {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for a %q message", want)
		}
		_ = conn.SetReadDeadline(time.Now().Add(timeout))
		_, raw, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read message: %v", err)
		}
		var env wsEnvelope
		if err := json.Unmarshal(raw, &env); err != nil {
			t.Fatalf("unmarshal envelope: %v", err)
		}
		if env.Type == want {
			return env
		}
	}
}

// expectClose reads until the connection closes and asserts the close code.
func expectClose(t *testing.T, conn *websocket.Conn, code int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for close code %d", code)
		}
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		if _, _, err := conn.ReadMessage(); err != nil {
			var closeErr *websocket.CloseError
			if errors.As(err, &closeErr) {
				if closeErr.Code != code {
					t.Fatalf("close code = %d, want %d", closeErr.Code, code)
				}
				return
			}
			t.Fatalf("read error is not a close error: %v", err)
		}
	}
}

// expectCloseWithoutSeed is expectClose, but also fails the moment a "seed"
// frame is seen before the close arrives.
func expectCloseWithoutSeed(t *testing.T, conn *websocket.Conn, code int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for close code %d", code)
		}
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		_, raw, err := conn.ReadMessage()
		if err != nil {
			var closeErr *websocket.CloseError
			if errors.As(err, &closeErr) {
				if closeErr.Code != code {
					t.Fatalf("close code = %d, want %d", closeErr.Code, code)
				}
				return
			}
			t.Fatalf("read error is not a close error: %v", err)
		}
		var env wsEnvelope
		if err := json.Unmarshal(raw, &env); err != nil {
			t.Fatalf("unmarshal envelope: %v", err)
		}
		if env.Type == "seed" {
			t.Fatal("a seed frame reached the client before the close: a foreign seed was sent")
		}
	}
}

// waitForRelease polls cm's release-client calls until at least one has
// happened, which only occurs once servePane's cleanup defer has run.
func waitForRelease(t *testing.T, cm *fakeControlManager) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if len(cm.releaseClientCalls()) > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for the server to release the control client")
		}
		time.Sleep(time.Millisecond)
	}
}

func writeInput(t *testing.T, conn *websocket.Conn, data string) {
	t.Helper()
	msg, err := json.Marshal(WSMessage{Type: "input", Data: mustMarshal(t, WSInput{Data: data})})
	if err != nil {
		t.Fatalf("marshal input message: %v", err)
	}
	if err := conn.WriteMessage(websocket.TextMessage, msg); err != nil {
		t.Fatalf("write input message: %v", err)
	}
}

func mustMarshal(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

// TestPaneWSClosesWhenServerChanges: once the generation is bumped it
// never becomes verified again (the resolve now disagrees), so input read
// after the bump is dropped regardless of exactly when it arrives relative
// to the Dirty event that triggers the check.
func TestPaneWSClosesWhenServerChanges(t *testing.T) {
	fakeTmux := newFakeTmux()
	fakeTmux.paneID = "%1"
	fakeTmux.setResolve(tmux.Pane{Server: "100"}, nil)

	fakeCC := newFakeControlClient()
	cm := newFakeControlManager(fakeCC)

	conn, cleanup := startPaneWSWithPane(t, fakeTmux, cm, serverPane)
	defer cleanup()

	readUntilType(t, conn, "seed", 5*time.Second)

	fakeTmux.setResolve(tmux.Pane{Server: "200"}, nil)
	fakeCC.bumpGeneration()

	writeInput(t, conn, "a")

	// Prove "a" was actually presented to SendKeys and refused for its stale
	// generation, rather than the close simply winning the race before the
	// read loop got to it — which would let removing the gate go unnoticed.
	waitForRefusedCall(t, fakeCC, fakeTmux.paneID, "a")

	fakeCC.markDirty(fakeTmux.paneID)

	expectClose(t, conn, wsCloseServerChanged)

	waitForRelease(t, cm)
	if slices.Contains(fakeCC.sendCallsFor(fakeTmux.paneID), "a") {
		t.Fatal("input sent after the generation changed reached SendKeys")
	}
}

// waitForRefusedCall polls cc.refusedCallsFor(paneID) until it contains want,
// failing the test if it does not show up within 5s.
func waitForRefusedCall(t *testing.T, cc *fakeControlClient, paneID, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if slices.Contains(cc.refusedCallsFor(paneID), want) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for a refused SendKeys(%q, %q); got %v", paneID, want, cc.refusedCallsFor(paneID))
		}
		time.Sleep(time.Millisecond)
	}
}

// TestPaneWSSurvivesReconnectToSameServer: a reconnect to the same
// server re-verifies and keeps streaming.
func TestPaneWSSurvivesReconnectToSameServer(t *testing.T) {
	fakeTmux := newFakeTmux()
	fakeTmux.paneID = "%1"
	fakeTmux.setResolve(tmux.Pane{Server: "100"}, nil)

	fakeCC := newFakeControlClient()
	cm := newFakeControlManager(fakeCC)

	conn, cleanup := startPaneWSWithPane(t, fakeTmux, cm, serverPane)
	defer cleanup()

	readUntilType(t, conn, "seed", 5*time.Second)

	fakeCC.reconnect(fakeTmux.paneID)

	// Its arrival proves verification completed: the write loop stores the
	// generation before capturing.
	readUntilType(t, conn, "seed", 5*time.Second)

	writeInput(t, conn, "c")
	waitForSendCall(t, fakeCC, fakeTmux.paneID, "c")

	fakeCC.dispatchAll(fakeTmux.paneID, []byte("x"))
	env := readUntilType(t, conn, "output", 5*time.Second)
	var out struct{ Data string }
	if err := json.Unmarshal(env.Data, &out); err != nil {
		t.Fatalf("unmarshal output payload: %v", err)
	}
	if out.Data != "x" {
		t.Fatalf("output data = %q, want %q", out.Data, "x")
	}
}

// TestPaneWSOrdinaryReseedDoesNotResolve: a Dirty event with no
// generation change re-seeds without touching tmux again.
func TestPaneWSOrdinaryReseedDoesNotResolve(t *testing.T) {
	fakeTmux := newFakeTmux()
	fakeTmux.paneID = "%1"
	fakeTmux.setResolve(tmux.Pane{Server: "100"}, nil)

	fakeCC := newFakeControlClient()
	cm := newFakeControlManager(fakeCC)

	conn, cleanup := startPaneWSWithPane(t, fakeTmux, cm, serverPane)
	defer cleanup()

	readUntilType(t, conn, "seed", 5*time.Second)

	fakeCC.markDirty(fakeTmux.paneID) // no generation bump

	readUntilType(t, conn, "seed", 5*time.Second)

	if got := fakeTmux.resolveCalls(); got != 1 {
		t.Fatalf("resolveCalls() = %d, want 1 (the open check only)", got)
	}

	writeInput(t, conn, "k")
	waitForSendCall(t, fakeCC, fakeTmux.paneID, "k")
}

// TestPaneWSOpenTimeServerMismatchCloses covers the open-time check itself:
// a pane already on a different server never gets past it.
func TestPaneWSOpenTimeServerMismatchCloses(t *testing.T) {
	fakeTmux := newFakeTmux()
	fakeTmux.paneID = "%1"
	fakeTmux.setResolve(tmux.Pane{Server: "200"}, nil)

	fakeCC := newFakeControlClient()
	cm := newFakeControlManager(fakeCC)

	conn, cleanup := startPaneWSWithPane(t, fakeTmux, cm, serverPane)
	defer cleanup()

	expectClose(t, conn, wsCloseServerChanged)
}

// TestPaneWSPaneGoneClosesWithServerChanged: a pane gone by the time of a
// reconnect's verify closes the same way a server mismatch does.
func TestPaneWSPaneGoneClosesWithServerChanged(t *testing.T) {
	fakeTmux := newFakeTmux()
	fakeTmux.paneID = "%1"
	fakeTmux.setResolve(tmux.Pane{Server: "100"}, nil)

	fakeCC := newFakeControlClient()
	cm := newFakeControlManager(fakeCC)

	conn, cleanup := startPaneWSWithPane(t, fakeTmux, cm, serverPane)
	defer cleanup()

	readUntilType(t, conn, "seed", 5*time.Second)

	fakeTmux.setResolve(tmux.Pane{}, tmux.ErrPaneNotFound)
	fakeCC.reconnect(fakeTmux.paneID)

	expectClose(t, conn, wsCloseServerChanged)
}

// TestPaneWSUnverifiableServerCloses1011: a resolve error that isn't
// ErrPaneNotFound means the check itself is untrustworthy, not that the pane
// moved — closes 1011 so the client's normal retry goes back through runPane.
func TestPaneWSUnverifiableServerCloses1011(t *testing.T) {
	fakeTmux := newFakeTmux()
	fakeTmux.paneID = "%1"
	fakeTmux.setResolve(tmux.Pane{Server: "100"}, nil)

	fakeCC := newFakeControlClient()
	cm := newFakeControlManager(fakeCC)

	conn, cleanup := startPaneWSWithPane(t, fakeTmux, cm, serverPane)
	defer cleanup()

	readUntilType(t, conn, "seed", 5*time.Second)

	fakeTmux.setResolve(tmux.Pane{}, errors.New("boom"))
	fakeCC.reconnect(fakeTmux.paneID)

	expectClose(t, conn, websocket.CloseInternalServerErr)
}

// TestPaneWSUnknownServerNeverResolves pins the legacy pane-address route:
// pane.Server == "" means nothing is ever checked, reconnect included.
func TestPaneWSUnknownServerNeverResolves(t *testing.T) {
	fakeTmux := newFakeTmux()
	fakeTmux.paneID = "%1"

	fakeCC := newFakeControlClient()
	cm := newFakeControlManager(fakeCC)

	conn, cleanup := startPaneWSWithPane(t, fakeTmux, cm, harnessPane)
	defer cleanup()

	readUntilType(t, conn, "seed", 5*time.Second)

	fakeCC.reconnect(fakeTmux.paneID)

	readUntilType(t, conn, "seed", 5*time.Second)

	if got := fakeTmux.resolveCalls(); got != 0 {
		t.Fatalf("resolveCalls() = %d, want 0 (pane.Server is unknown)", got)
	}

	writeInput(t, conn, "z")
	waitForSendCall(t, fakeCC, fakeTmux.paneID, "z")
}

// TestPaneWSOpenTimeMismatchNeverZooms: the open-time server check
// must run before auto-zoom, so a pane already on a different server at open
// never has its window zoomed on our behalf.
func TestPaneWSOpenTimeMismatchNeverZooms(t *testing.T) {
	fakeTmux := newFakeTmux()
	fakeTmux.paneID = "%1"
	fakeTmux.setWindowPaneCount(2)
	fakeTmux.setResolve(tmux.Pane{Server: "200"}, nil)

	fakeCC := newFakeControlClient()
	cm := newFakeControlManager(fakeCC)

	conn, cleanup := startPaneWSWithPane(t, fakeTmux, cm, serverPane)
	defer cleanup()

	expectClose(t, conn, wsCloseServerChanged)

	waitForRelease(t, cm)
	if got := fakeTmux.zoomPaneCalls(); got != 0 {
		t.Fatalf("zoomPaneCalls() = %d, want 0 (open-time mismatch must precede auto-zoom)", got)
	}
}

// TestPaneWSServerChangeSkipsZoomRestore: a socket that closes
// because the pane's server changed must not un-zoom on the way out — that
// zoom toggle would hit an unrelated window on the new server.
func TestPaneWSServerChangeSkipsZoomRestore(t *testing.T) {
	fakeTmux := newFakeTmux()
	fakeTmux.paneID = "%1"
	fakeTmux.setWindowPaneCount(2)
	fakeTmux.setResolve(tmux.Pane{Server: "100"}, nil)

	fakeCC := newFakeControlClient()
	cm := newFakeControlManager(fakeCC)

	conn, cleanup := startPaneWSWithPane(t, fakeTmux, cm, serverPane)
	defer cleanup()

	readUntilType(t, conn, "seed", 5*time.Second)

	if got := fakeTmux.zoomPaneCalls(); got != 1 {
		t.Fatalf("zoomPaneCalls() after connect = %d, want 1 (auto-zoom on connect)", got)
	}

	fakeTmux.setResolve(tmux.Pane{Server: "200"}, nil)
	fakeCC.reconnect(fakeTmux.paneID)

	expectClose(t, conn, wsCloseServerChanged)

	waitForRelease(t, cm)
	if got := fakeTmux.zoomPaneCalls(); got != 1 {
		t.Fatalf("zoomPaneCalls() after server-change close = %d, want 1 (zoom restore must be skipped)", got)
	}
}

// TestPaneWSReconnectDuringReseedIsNotLost: a reconnect landing mid-capture
// must not be lost behind AckReseed clearing the dirty flag, and the seed
// captured under the stale generation must never reach the client.
func TestPaneWSReconnectDuringReseedIsNotLost(t *testing.T) {
	fakeTmux := newFakeTmux()
	fakeTmux.paneID = "%1"
	fakeTmux.setResolve(tmux.Pane{Server: "100"}, nil)

	fakeCC := newFakeControlClient()
	cm := newFakeControlManager(fakeCC)

	conn, cleanup := startPaneWSWithPane(t, fakeTmux, cm, serverPane)
	defer cleanup()

	readUntilType(t, conn, "seed", 5*time.Second)

	fakeTmux.setResolve(tmux.Pane{Server: "200"}, nil)

	var armed sync.Once
	fakeTmux.setOnCapture(func() {
		// A no-op while the sub is already dirty from the markDirty below.
		armed.Do(func() { fakeCC.reconnect(fakeTmux.paneID) })
	})

	fakeCC.markDirty(fakeTmux.paneID)

	deadline := time.Now().Add(2 * time.Second)
	for {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for the close")
		}
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		_, raw, err := conn.ReadMessage()
		if err != nil {
			var closeErr *websocket.CloseError
			if errors.As(err, &closeErr) {
				if closeErr.Code != wsCloseServerChanged {
					t.Fatalf("close code = %d, want %d", closeErr.Code, wsCloseServerChanged)
				}
				return
			}
			t.Fatalf("read error is not a close error: %v", err)
		}
		var env wsEnvelope
		if err := json.Unmarshal(raw, &env); err != nil {
			t.Fatalf("unmarshal envelope: %v", err)
		}
		if env.Type == "seed" {
			t.Fatal("a second seed frame reached the client before the close: the foreign capture was sent")
		}
	}
}

// TestPaneWSReconnectBeforeSubscribeToNewServerClosesWithoutSeed: a reconnect
// landing between the open-time check and Subscribe must be verified before
// the seed goes out, so a seed captured on the new server never reaches the
// client.
func TestPaneWSReconnectBeforeSubscribeToNewServerClosesWithoutSeed(t *testing.T) {
	fakeTmux := newFakeTmux()
	fakeTmux.paneID = "%1"
	fakeTmux.setResolve(tmux.Pane{Server: "100"}, nil)

	fakeCC := newFakeControlClient()
	cm := newFakeControlManager(fakeCC)

	fakeCC.setOnRun(func(cmd string) {
		if strings.HasSuffix(cmd, ":pause") {
			fakeTmux.setResolve(tmux.Pane{Server: "200"}, nil)
			fakeCC.bumpGeneration()
		}
	})

	conn, cleanup := startPaneWSWithPane(t, fakeTmux, cm, serverPane)
	defer cleanup()

	expectCloseWithoutSeed(t, conn, wsCloseServerChanged)

	waitForRelease(t, cm)
}

// TestPaneWSReconnectDuringInitialCaptureClosesWithoutSeed: a reconnect
// landing during the initial capture, after the pre-dims check passed, must
// still be verified before that seed is written.
func TestPaneWSReconnectDuringInitialCaptureClosesWithoutSeed(t *testing.T) {
	fakeTmux := newFakeTmux()
	fakeTmux.paneID = "%1"
	fakeTmux.setResolve(tmux.Pane{Server: "100"}, nil)

	fakeCC := newFakeControlClient()
	cm := newFakeControlManager(fakeCC)

	// onCapture also fires on later captures (e.g. the write loop's
	// re-seed), so arm it to act only once.
	var armed sync.Once
	fakeTmux.setOnCapture(func() {
		armed.Do(func() {
			fakeTmux.setResolve(tmux.Pane{Server: "200"}, nil)
			fakeCC.reconnect(fakeTmux.paneID)
		})
	})

	conn, cleanup := startPaneWSWithPane(t, fakeTmux, cm, serverPane)
	defer cleanup()

	expectCloseWithoutSeed(t, conn, wsCloseServerChanged)

	waitForRelease(t, cm)
}

// TestPaneWSReconnectBeforeSubscribeToSameServerKeepsInput: the same
// generation bump with no server change behind it must not disrupt the
// connection — the seed still ships and input still reaches SendKeys.
func TestPaneWSReconnectBeforeSubscribeToSameServerKeepsInput(t *testing.T) {
	fakeTmux := newFakeTmux()
	fakeTmux.paneID = "%1"
	fakeTmux.setResolve(tmux.Pane{Server: "100"}, nil)

	fakeCC := newFakeControlClient()
	cm := newFakeControlManager(fakeCC)

	fakeCC.setOnRun(func(cmd string) {
		if strings.HasSuffix(cmd, ":pause") {
			fakeCC.bumpGeneration()
		}
	})

	conn, cleanup := startPaneWSWithPane(t, fakeTmux, cm, serverPane)
	defer cleanup()

	readUntilType(t, conn, "seed", 5*time.Second)

	writeInput(t, conn, "z")
	waitForSendCall(t, fakeCC, fakeTmux.paneID, "z")
}
