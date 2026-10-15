package server

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/noamsto/houston/tmux"
)

// A pane resized after the socket opened (another client attaching, a zoom
// toggled elsewhere) must reach the browser: TUIs such as Claude Code redraw
// their bottom rows relative to the screen height, so an xterm left at the old
// size scrolls them instead of repainting in place and piles up copies of the
// prompt box and status bar.
func TestPaneWSSendsDimsAndReseedsWhenPaneResizes(t *testing.T) {
	fakeTmux := newFakeTmux()
	fakeTmux.paneID = "%1"
	fakeTmux.paneWidth = 80
	fakeTmux.paneHeight = 24

	fakeCC := newFakeControlClient()
	conn, cleanup := startPaneWS(t, fakeTmux, newFakeControlManager(fakeCC))
	defer cleanup()

	readUntilType(t, conn, "dims", 5*time.Second)
	readUntilType(t, conn, "seed", 5*time.Second)

	fakeTmux.mu.Lock()
	fakeTmux.seed = "resized-seed"
	fakeTmux.mu.Unlock()
	fakeTmux.setPaneSize(120, 40)

	env := readUntilType(t, conn, "dims", 5*time.Second)
	var dims struct{ Cols, Rows int }
	if err := json.Unmarshal(env.Data, &dims); err != nil {
		t.Fatalf("unmarshal dims payload: %v", err)
	}
	if dims.Cols != 120 || dims.Rows != 40 {
		t.Fatalf("dims after resize = %+v, want {120 40}", dims)
	}

	env = readUntilType(t, conn, "seed", 5*time.Second)
	var seed struct{ Data string }
	if err := json.Unmarshal(env.Data, &seed); err != nil {
		t.Fatalf("unmarshal seed payload: %v", err)
	}
	if seed.Data != "resized-seed" {
		t.Fatalf("seed after resize = %q, want a fresh capture %q", seed.Data, "resized-seed")
	}
}

// The seed is written sequentially, which leaves xterm's cursor at the end of
// the last captured line. A shell echoes typed text without positioning it, so
// without the pane's real cursor in the seed the text lands far from the prompt.
func TestPaneWSSeedCarriesPaneCursor(t *testing.T) {
	fakeTmux := newFakeTmux()
	fakeTmux.paneID = "%1"
	fakeTmux.paneWidth = 80
	fakeTmux.paneHeight = 24
	fakeTmux.cursorX, fakeTmux.cursorY = 6, 23

	conn, cleanup := startPaneWS(t, fakeTmux, newFakeControlManager(newFakeControlClient()))
	defer cleanup()

	env := readUntilType(t, conn, "seed", 5*time.Second)
	var seed struct {
		Data   string
		Cursor *struct{ X, Y int }
	}
	if err := json.Unmarshal(env.Data, &seed); err != nil {
		t.Fatalf("unmarshal seed payload: %v", err)
	}
	if seed.Cursor == nil || seed.Cursor.X != 6 || seed.Cursor.Y != 23 {
		t.Fatalf("seed cursor = %+v, want {6 23}", seed.Cursor)
	}
}

// The size comes from a tmux CLI read addressed by session:window.index, which
// after a tmux server restart can name another session's pane while the control
// client is still backing off (no generation bump). That size must not reach
// the browser, nor seed it with the other pane's history.
func TestPaneWSResizeOnForeignServerClosesWithoutDimsOrSeed(t *testing.T) {
	fakeTmux := newFakeTmux()
	fakeTmux.paneID = "%1"
	fakeTmux.paneWidth = 80
	fakeTmux.paneHeight = 24
	fakeTmux.setResolve(tmux.Pane{Server: "100"}, nil)

	conn, cleanup := startPaneWSWithPane(t, fakeTmux, newFakeControlManager(newFakeControlClient()), serverPane)
	defer cleanup()

	readUntilType(t, conn, "dims", 5*time.Second)
	readUntilType(t, conn, "seed", 5*time.Second)

	fakeTmux.setResolve(tmux.Pane{Server: "200"}, nil)
	fakeTmux.setPaneSize(120, 40)

	expectCloseWithoutFrame(t, conn, wsCloseServerChanged, "dims")
}

// An unchanged size must not retrigger: every seed clears the browser's
// scrollback and resets a detached view, so a reseed per meta tick would wreck
// the view while every other test stayed green.
func TestPaneWSSteadySizeSendsOneDimsAndOneSeed(t *testing.T) {
	fakeTmux := newFakeTmux()
	fakeTmux.paneID = "%1"
	fakeTmux.paneWidth = 80
	fakeTmux.paneHeight = 24

	conn, cleanup := startPaneWS(t, fakeTmux, newFakeControlManager(newFakeControlClient()))
	defer cleanup()

	counts := map[string]int{}
	deadline := time.Now().Add(300 * time.Millisecond) // ~30 meta ticks at the harness's 10ms
	for time.Now().Before(deadline) {
		_ = conn.SetReadDeadline(deadline)
		_, raw, err := conn.ReadMessage()
		if err != nil {
			break
		}
		var msg wsEnvelope
		if err := json.Unmarshal(raw, &msg); err != nil {
			t.Fatalf("unmarshal envelope: %v", err)
		}
		counts[msg.Type]++
	}
	if counts["dims"] != 1 || counts["seed"] != 1 {
		t.Fatalf("frames over a steady pane = %v, want exactly one dims and one seed", counts)
	}
}
