package server

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// readMetaUntil returns the first meta frame accepted by want.
func readMetaUntil(t *testing.T, conn *websocket.Conn, want func(WSMeta) bool) WSMeta {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		_, raw, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		var frame wsEnvelope
		if err := json.Unmarshal(raw, &frame); err != nil || frame.Type != "meta" {
			continue
		}
		var m WSMeta
		if err := json.Unmarshal(frame.Data, &m); err != nil {
			t.Fatalf("unmarshal meta: %v", err)
		}
		if want(m) {
			return m
		}
	}
	t.Fatal("timed out waiting for the expected meta frame")
	return WSMeta{}
}

// TestPaneWSMetaCarriesTerminalMode: the alternate-screen and mouse flags
// reach the client, and a change is re-sent without a reconnect.
func TestPaneWSMetaCarriesTerminalMode(t *testing.T) {
	fakeTmux := newFakeTmux()
	fakeTmux.paneID = "%1"
	fakeTmux.setTermMode(true, true)
	cm := newFakeControlManager(newFakeControlClient())

	conn, cleanup := startPaneWS(t, fakeTmux, cm)
	defer cleanup()

	readMetaUntil(t, conn, func(m WSMeta) bool { return m.AlternateOn && m.MouseOn })

	fakeTmux.setTermMode(false, false)
	readMetaUntil(t, conn, func(m WSMeta) bool { return !m.AlternateOn && !m.MouseOn })
}
