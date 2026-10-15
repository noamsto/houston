package server

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func writeScroll(t *testing.T, conn *websocket.Conn, mode, data string) {
	t.Helper()
	msg, err := json.Marshal(WSMessage{Type: "scroll", Data: mustMarshal(t, WSScroll{Mode: mode, Data: data})})
	if err != nil {
		t.Fatalf("marshal scroll message: %v", err)
	}
	if err := conn.WriteMessage(websocket.TextMessage, msg); err != nil {
		t.Fatalf("write scroll message: %v", err)
	}
}

// TestPaneWSScrollGate covers #250: scroll the UI forwarded from a stale
// meta sample must not reach a pane that has since left the alternate screen
// or entered a mode.
func TestPaneWSScrollGate(t *testing.T) {
	const wheel = "\x1b[<65;3;4M"
	const page = "\x1b[6~"
	tests := []struct {
		name               string
		alt, mouse, inMode bool
		stateErr           bool
		mode, data         string
		wantSent           bool
	}{
		{"alt screen wheel with mouse", true, true, false, false, "wheel", wheel, true},
		{"alt screen page without mouse", true, false, false, false, "page", page, true},
		{"app quit, wheel", false, false, false, false, "wheel", wheel, false},
		{"app quit, page", false, false, false, false, "page", page, false},
		{"copy mode", true, true, true, false, "wheel", wheel, false},
		{"copy mode, page", true, false, true, false, "page", page, false},
		{"wheel to pane no longer tracking the mouse", true, false, false, false, "wheel", wheel, false},
		{"unknown mode", true, true, false, false, "bogus", wheel, false},
		{"state unreadable", true, true, false, true, "wheel", wheel, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fakeTmux := newFakeTmux()
			fakeTmux.paneID = "%7"
			fakeTmux.altScreen, fakeTmux.mouseSGR, fakeTmux.inMode = tt.alt, tt.mouse, tt.inMode
			if tt.stateErr {
				fakeTmux.scrollStateErr = errors.New("tmux unavailable")
			}
			fakeCC := newFakeControlClient()
			conn, cleanup := startPaneWS(t, fakeTmux, newFakeControlManager(fakeCC))
			defer cleanup()
			readUntilType(t, conn, "seed", 5*time.Second)

			writeScroll(t, conn, tt.mode, tt.data)
			// A typed marker after the scroll proves the read loop got past it.
			writeInput(t, conn, "marker")
			waitForSendCall(t, fakeCC, "%7", "marker")

			calls := fakeCC.sendCallsFor("%7")
			sent := false
			for _, c := range calls {
				if c == tt.data {
					sent = true
				}
			}
			if sent != tt.wantSent {
				t.Fatalf("scroll sent = %v, want %v (calls %q)", sent, tt.wantSent, calls)
			}
		})
	}
}

// TestPaneWSInputIgnoresPaneMode pins that typed input is not gated by the
// scroll state: typing into a copy-mode pane is the user's own navigation.
func TestPaneWSInputIgnoresPaneMode(t *testing.T) {
	fakeTmux := newFakeTmux()
	fakeTmux.paneID = "%7"
	fakeTmux.inMode = true
	fakeCC := newFakeControlClient()
	conn, cleanup := startPaneWS(t, fakeTmux, newFakeControlManager(fakeCC))
	defer cleanup()
	readUntilType(t, conn, "seed", 5*time.Second)

	writeInput(t, conn, "q")
	waitForSendCall(t, fakeCC, "%7", "q")
}
