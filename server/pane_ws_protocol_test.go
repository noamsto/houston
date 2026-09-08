package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/noamsto/houston/agents"
	"github.com/noamsto/houston/agents/generic"
	"github.com/noamsto/houston/tmux"
)

// wsEnvelope mirrors WSMessage's JSON shape. Defined locally, rather than
// reusing WSMessage, so these tests exercise the actual wire format instead
// of trivially agreeing with whatever pane_ws.go's struct says today.
type wsEnvelope struct {
	Type string
	Data json.RawMessage
}

// TestPaneWSServerToClientProtocol asserts the REAL JSON envelope protocol —
// {"type":"...","data":{...}} with types dims/seed/output/meta — that
// pane_ws.go actually speaks. This is NOT the output:/meta:/resize-done
// string-prefix protocol documented in CLAUDE.md: that doc describes a
// pre-control-mode wire format and is stale. Do not "fix" this test to match
// it; fix the doc instead.
func TestPaneWSServerToClientProtocol(t *testing.T) {
	fakeTmux := newFakeTmux()
	fakeTmux.paneID = "%1"
	fakeTmux.paneWidth = 80
	fakeTmux.paneHeight = 24

	fakeCC := newFakeControlClient()
	cm := newFakeControlManager(fakeCC)

	conn, cleanup := startPaneWS(t, fakeTmux, cm)
	defer cleanup()

	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, raw, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read dims message: %v", err)
	}
	var env wsEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal dims envelope: %v", err)
	}
	if env.Type != "dims" {
		t.Fatalf("first message type = %q, want %q", env.Type, "dims")
	}
	var dims struct{ Cols, Rows int }
	if err := json.Unmarshal(env.Data, &dims); err != nil {
		t.Fatalf("unmarshal dims payload: %v", err)
	}
	if dims.Cols != fakeTmux.paneWidth || dims.Rows != fakeTmux.paneHeight {
		t.Fatalf("dims = %+v, want {%d %d}", dims, fakeTmux.paneWidth, fakeTmux.paneHeight)
	}

	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, raw, err = conn.ReadMessage()
	if err != nil {
		t.Fatalf("read seed message: %v", err)
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal seed envelope: %v", err)
	}
	if env.Type != "seed" {
		t.Fatalf("second message type = %q, want %q", env.Type, "seed")
	}
	var seed struct{ Data string }
	if err := json.Unmarshal(env.Data, &seed); err != nil {
		t.Fatalf("unmarshal seed payload: %v", err)
	}
	if seed.Data != fakeTmux.seed {
		t.Fatalf("seed data = %q, want %q", seed.Data, fakeTmux.seed)
	}

	stopPing := pingPane(fakeCC, fakeTmux.paneID, 10*time.Millisecond)
	t.Cleanup(stopPing)

	// meta may interleave (it's polled every second and can differ from its
	// zero-value on the first tick), so skip past it to find the output
	// message pingPane's ticks produce.
	deadline := time.Now().Add(5 * time.Second)
	for {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for an output message")
		}
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		_, raw, err = conn.ReadMessage()
		if err != nil {
			t.Fatalf("read output message: %v", err)
		}
		if err := json.Unmarshal(raw, &env); err != nil {
			t.Fatalf("unmarshal envelope: %v", err)
		}
		if env.Type == "meta" {
			continue
		}
		break
	}
	if env.Type != "output" {
		t.Fatalf("message type = %q, want %q", env.Type, "output")
	}
	var out struct{ Data string }
	if err := json.Unmarshal(env.Data, &out); err != nil {
		t.Fatalf("unmarshal output payload: %v", err)
	}
	if out.Data != "tick" {
		t.Fatalf("output data = %q, want %q", out.Data, "tick")
	}
}

// TestPaneWSClientToServerInput confirms an "input" message read off the
// client conn reaches tmux via SendKeys with the pane ID resolved at connect.
func TestPaneWSClientToServerInput(t *testing.T) {
	fakeTmux := newFakeTmux()
	fakeTmux.paneID = "%42"

	fakeCC := newFakeControlClient()
	cm := newFakeControlManager(fakeCC)

	conn, cleanup := startPaneWS(t, fakeTmux, cm)
	defer cleanup()

	if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"input","data":{"data":"echo hi"}}`)); err != nil {
		t.Fatalf("write input message: %v", err)
	}

	waitForSendCall(t, fakeCC, fakeTmux.paneID, "echo hi")
}

// waitForSendCall polls cc.sendCallsFor(paneID) until it contains want,
// failing the test if it doesn't show up within 2s. Polling is the only
// reliable way to observe the read loop's async effect on the fake.
func waitForSendCall(t *testing.T, cc *fakeControlClient, paneID, want string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if slices.Contains(cc.sendCallsFor(paneID), want) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for SendKeys(%q, %q); got %v", paneID, want, cc.sendCallsFor(paneID))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestWSUpgraderCheckOrigin unit-tests (*Server).wsUpgrader().CheckOrigin
// directly, against the same originAllowed semantics the HTTP auth
// middleware uses (see auth.go).
func TestWSUpgraderCheckOrigin(t *testing.T) {
	gated := &Server{auth: &authGate{enabled: true, allowedOrigins: []string{"http://good.example"}}}

	tests := []struct {
		name   string
		server *Server
		origin string // "" omits the header entirely
		want   bool
	}{
		{
			name:   "same-origin request allowed",
			server: gated,
			origin: "http://this-host.local",
			want:   true,
		},
		{
			name:   "foreign origin refused",
			server: gated,
			origin: "http://evil.example",
			want:   false,
		},
		{
			// originAllowed's comment in auth.go: an absent Origin is safe
			// because every Origin-less request vector (<img>, <script>,
			// <link>, top-level navigation, <form method=GET>) is GET-only
			// and every state-changing route is POST, and because the auth
			// cookie is SameSite=Strict so no cross-site request carries it.
			name:   "absent origin allowed",
			server: gated,
			origin: "",
			want:   true,
		},
		{
			name:   "unwired auth gate fails closed regardless of origin",
			server: &Server{},
			origin: "http://this-host.local",
			want:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "http://this-host.local/api/pane/x/ws", nil)
			if tt.origin != "" {
				req.Header.Set("Origin", tt.origin)
			}
			if got := tt.server.wsUpgrader().CheckOrigin(req); got != tt.want {
				t.Errorf("CheckOrigin() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestPaneWSNeverResizesToClientDimensions covers the invariant that
// pane_ws.go never resizes a pane to a size the client chose — it does call
// into tmux on connect (ForceRedraw, and ZoomPane when auto-zoom applies),
// just never with client-supplied dimensions. windowPaneCount is set to 1 so
// auto-zoom's own precondition never fires, keeping this test's only subject
// the resize invariant.
//
// This covers pane_ws.go's own calls into tmux. The other half of the
// invariant — refresh-client -f ignore-size, re-asserted on every reconnect
// inside tmux.ControlClient.attach — lives in tmux/control_client_test.go, a
// different package.
func TestPaneWSNeverResizesToClientDimensions(t *testing.T) {
	const paneID = "%77"

	fakeTmux := newFakeTmux()
	fakeTmux.paneID = paneID
	fakeTmux.paneWidth = 100
	fakeTmux.paneHeight = 40
	fakeTmux.windowPaneCount = 1

	fakeCC := newFakeControlClient()
	cm := newFakeControlManager(fakeCC)

	conn, cleanup := startPaneWS(t, fakeTmux, cm)
	defer cleanup()

	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, raw, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read dims message: %v", err)
	}
	var env wsEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal dims envelope: %v", err)
	}
	if env.Type != "dims" {
		t.Fatalf("first message type = %q, want %q", env.Type, "dims")
	}
	var dims struct{ Cols, Rows int }
	if err := json.Unmarshal(env.Data, &dims); err != nil {
		t.Fatalf("unmarshal dims payload: %v", err)
	}
	if dims.Cols != 100 || dims.Rows != 40 {
		t.Fatalf("dims = %+v, want houston to report the pane's actual size {100 40}", dims)
	}

	if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"resize","data":{"cols":1,"rows":1}}`)); err != nil {
		t.Fatalf("write resize message: %v", err)
	}
	if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"input","data":{"data":"sync"}}`)); err != nil {
		t.Fatalf("write input message: %v", err)
	}

	// The read loop has no ack for "resize"; this poll returning is the only
	// reliable evidence it consumed both frames, since it processes them in
	// order off one connection.
	waitForSendCall(t, fakeCC, paneID, "sync")

	if got := fakeTmux.forceRedrawCalls(); got != 1 {
		t.Errorf("forceRedrawCalls() = %d, want 1 (connect-time setup ran once, client resize never reached it)", got)
	}

	want := []string{
		fmt.Sprintf("refresh-client -A %s:pause", paneID),
		fmt.Sprintf("refresh-client -A %s:continue", paneID),
	}
	if got := fakeCC.runCalls(); !slices.Equal(got, want) {
		t.Errorf("runCalls() = %v, want %v", got, want)
	}
}

// gatingWriter is a wsWriter that blocks a "seed" write until the test
// releases it, signaling once it's blocked so the test doesn't have to guess
// timing. It gates on the message's Type field, not call order, because the
// bug under test is about ordering relative to a *specific* write.
type gatingWriter struct {
	blockCh   chan struct{}
	started   chan struct{}
	startOnce sync.Once
}

func (w *gatingWriter) WriteMessage(messageType int, data []byte) error {
	var msg struct{ Type string }
	_ = json.Unmarshal(data, &msg)
	if msg.Type == "seed" {
		w.startOnce.Do(func() { close(w.started) })
		<-w.blockCh
	}
	return nil
}

// TestPaneWSAckBeforeWrite is the regression test for the historical
// ack-ordering bug (see git show 9c2577f0 / 27a9734): AckReseed must run
// before writeSeed, not after. Before the fix, output arriving while the
// seed write was still in flight found the subscriber still marked dirty and
// was silently dropped.
//
// This drives paneWSWriteLoop directly, bypassing servePane, so it owns
// connDone itself.
func TestPaneWSAckBeforeWrite(t *testing.T) {
	fakeTmux := newFakeTmux() // default non-empty seed matters here
	fakeCC := newFakeControlClient()
	sub := fakeCC.Subscribe(fakeTmux.paneID).(*fakePaneSub)
	// Mirror tmux.ControlClient.markDirtyLocked: a real dirty subscriber has
	// both the Dirty event queued AND its dirty flag set, which is what
	// gates fakeControlClient.deliverLocked. Pushing the event alone (as the
	// production code's caller sees it) doesn't set the fake's dirty flag,
	// so tryDispatch below would trivially succeed regardless of ordering.
	sub.dirty = true
	sub.ch <- tmux.PaneEvent{Dirty: true}

	connDone := make(chan struct{})
	t.Cleanup(func() { close(connDone) })
	t.Cleanup(fakeCC.closeDone)

	gw := &gatingWriter{
		blockCh: make(chan struct{}),
		started: make(chan struct{}),
	}

	go paneWSWriteLoop(gw, fakeTmux, fakeCC, agents.NewRegistry(generic.New()), harnessPane, sub, connDone)

	<-gw.started

	delivered := fakeCC.tryDispatch(sub, []byte("late output"))
	if !delivered {
		t.Fatal("output dispatched while the write loop was blocked inside writeSeed was dropped: the subscriber was still marked dirty, meaning AckReseed did not run before writeSeed")
	}

	close(gw.blockCh)
}
