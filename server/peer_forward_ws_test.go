package server

import (
	"encoding/json"
	"errors"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/noamsto/houston/agents"
	"github.com/noamsto/houston/agents/generic"
	"github.com/noamsto/houston/runs"
	"github.com/noamsto/houston/tmux"
)

// dialForwardedTerminal opens the browser hop: A's terminal route for a peer
// run, with the cookie and Origin a browser sends.
func dialForwardedTerminal(t *testing.T, s *Server, runID string) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	srv := startForwardServer(t, s)
	header := http.Header{}
	header.Set("Origin", replyOrigin)
	header.Set("Cookie", authCookie+"="+replyToken)
	conn, res, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/api/runs/"+runID+"/terminal", header)
	if conn != nil {
		t.Cleanup(func() { _ = conn.Close() })
	}
	return conn, res, err
}

func mustDialForwarded(t *testing.T, s *Server) *websocket.Conn {
	t.Helper()
	conn, res, err := dialForwardedTerminal(t, s, fwdPeerRunID)
	if res != nil {
		_ = res.Body.Close()
	}
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	return conn
}

// stubPeerTerminal is a peer that upgrades every request and hands the
// socket to serve.
func stubPeerTerminal(t *testing.T, serve func(*websocket.Conn)) *peerRecorder {
	t.Helper()
	up := websocket.Upgrader{}
	p := newPeerRecorder(t, func(w http.ResponseWriter, r *http.Request) {
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = c.Close() }()
		serve(c)
	})
	return p
}

func TestPeerTerminalRelaysFramesBothWays(t *testing.T) {
	p := stubPeerTerminal(t, func(c *websocket.Conn) {
		_ = c.WriteMessage(websocket.BinaryMessage, []byte{0, 1, 2})
		for {
			mt, data, err := c.ReadMessage()
			if err != nil {
				return
			}
			_ = c.WriteMessage(mt, data)
		}
	})
	browser := mustDialForwarded(t, newForwardServer(t, p.srv.URL))
	_ = browser.SetReadDeadline(time.Now().Add(5 * time.Second))

	if mt, data, err := browser.ReadMessage(); err != nil || mt != websocket.BinaryMessage || string(data) != "\x00\x01\x02" {
		t.Fatalf("first frame %d %q %v, want the peer's binary frame", mt, data, err)
	}
	if err := browser.WriteMessage(websocket.TextMessage, []byte(`{"type":"input","data":{"data":"x"}}`)); err != nil {
		t.Fatal(err)
	}
	if mt, data, err := browser.ReadMessage(); err != nil || mt != websocket.TextMessage || string(data) != `{"type":"input","data":{"data":"x"}}` {
		t.Fatalf("echo %d %q %v, want the text frame back", mt, data, err)
	}

	up := p.requests()[0]
	if up.path != "/api/runs/pane-2/terminal" {
		t.Errorf("upstream path %q", up.path)
	}
	if a := up.header.Values("Authorization"); len(a) != 1 || a[0] != "Bearer "+fwdPeerToken {
		t.Errorf("upstream Authorization %q, want only the peer token", a)
	}
	for _, h := range []string{"Cookie", "Origin"} {
		if v := up.header.Get(h); v != "" {
			t.Errorf("upstream carried %s: %q", h, v)
		}
	}
}

func TestPeerTerminalRelaysBrowserClose(t *testing.T) {
	got := make(chan error, 1)
	p := stubPeerTerminal(t, func(c *websocket.Conn) {
		_, _, err := c.ReadMessage()
		got <- err
	})
	browser := mustDialForwarded(t, newForwardServer(t, p.srv.URL))

	if err := browser.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(4001, "bye")); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-got:
		var ce *websocket.CloseError
		if !errors.As(err, &ce) || ce.Code != 4001 || ce.Text != "bye" {
			t.Fatalf("peer read %v, want close 4001 bye", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the peer never saw the browser's close")
	}
}

func TestPeerTerminalRelaysPeerClose(t *testing.T) {
	p := stubPeerTerminal(t, func(c *websocket.Conn) {
		_ = c.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseInternalServerErr, "could not verify tmux server"))
		_, _, _ = c.ReadMessage()
	})
	browser := mustDialForwarded(t, newForwardServer(t, p.srv.URL))

	_ = browser.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, _, err := browser.ReadMessage()
	var ce *websocket.CloseError
	if !errors.As(err, &ce) || ce.Code != websocket.CloseInternalServerErr || ce.Text != "could not verify tmux server" {
		t.Fatalf("browser read %v, want close 1011 with the peer's text", err)
	}
}

func TestPeerTerminalPeerDropIsAbnormal(t *testing.T) {
	p := stubPeerTerminal(t, func(c *websocket.Conn) {
		_ = c.NetConn().Close()
	})
	browser := mustDialForwarded(t, newForwardServer(t, p.srv.URL))

	_ = browser.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, _, err := browser.ReadMessage()
	var ce *websocket.CloseError
	if !errors.As(err, &ce) || ce.Code != websocket.CloseAbnormalClosure {
		t.Fatalf("browser read %v, want an abnormal close (no close frame relayed)", err)
	}
}

func TestPeerTerminalPreUpgradeRefusalIsRelayed(t *testing.T) {
	p := newPeerRecorder(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Set-Cookie", "x=y")
		http.Error(w, "terminal pane is gone", http.StatusConflict)
	})
	_, res, err := dialForwardedTerminal(t, newForwardServer(t, p.srv.URL), fwdPeerRunID)
	if !errors.Is(err, websocket.ErrBadHandshake) || res == nil {
		t.Fatalf("dial %v, want a refused handshake", err)
	}
	defer func() { _ = res.Body.Close() }()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusConflict || string(body) != "terminal pane is gone\n" {
		t.Fatalf("refusal %d %q, want the peer's 409", res.StatusCode, body)
	}
	if res.Header.Get("Set-Cookie") != "" {
		t.Error("peer Set-Cookie relayed")
	}
}

func TestPeerTerminalPreUpgradeRefusalCannotRender(t *testing.T) {
	p := newPeerRecorder(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusConflict)
		_, _ = io.WriteString(w, "<script>alert(1)</script>")
	})
	_, res, err := dialForwardedTerminal(t, newForwardServer(t, p.srv.URL), fwdPeerRunID)
	if !errors.Is(err, websocket.ErrBadHandshake) || res == nil {
		t.Fatalf("dial %v, want a refused handshake", err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("status %d, want the peer's 409", res.StatusCode)
	}
	wantInertHeaders(t, res.Header, "text/plain; charset=utf-8")
}

func TestPeerTerminalUnupgradedAnswerIs502(t *testing.T) {
	for _, code := range []int{http.StatusOK, http.StatusNoContent} {
		t.Run(strconv.Itoa(code), func(t *testing.T) {
			p := newPeerRecorder(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(code)
			})
			_, res, err := dialForwardedTerminal(t, newForwardServer(t, p.srv.URL), fwdPeerRunID)
			if !errors.Is(err, websocket.ErrBadHandshake) || res == nil {
				t.Fatalf("dial %v, want a refused handshake", err)
			}
			defer func() { _ = res.Body.Close() }()
			body, _ := io.ReadAll(res.Body)
			if res.StatusCode != http.StatusBadGateway || string(body) != "peer refused the request\n" {
				t.Fatalf("refusal %d %q, want 502 peer refused the request", res.StatusCode, body)
			}
		})
	}
}

func TestPeerTerminalPlainRequestIsNotDialled(t *testing.T) {
	p := stubPeerTerminal(t, func(*websocket.Conn) {})
	s := newForwardServer(t, p.srv.URL)

	wantRefusal(t, doReply(t, s, browserRequest("GET", "/api/runs/"+fwdPeerRunID+"/terminal", "")), http.StatusBadRequest, "not a websocket request")
	if n := len(p.requests()); n != 0 {
		t.Fatalf("peer saw %d requests, want 0", n)
	}
}

func TestPeerTerminalCraftedTargetsAreNotDialled(t *testing.T) {
	p := stubPeerTerminal(t, func(*websocket.Conn) {})
	s := newForwardServer(t, p.srv.URL)

	for _, id := range append(slices.Clone(craftedPeerIDs), "peer-b.pane-2/terminal/extra", "peer-b.pane-2/../pane-2") {
		t.Run(id, func(t *testing.T) {
			_, res, err := dialForwardedTerminal(t, s, id)
			if res != nil {
				_ = res.Body.Close()
			}
			if err == nil {
				t.Fatal("dial succeeded")
			}
		})
	}
	for _, got := range p.requests() {
		t.Errorf("peer saw %s %s", got.method, got.path)
	}
}

func TestPeerTerminalDownPeerIs502(t *testing.T) {
	down := httptest.NewServer(http.NotFoundHandler())
	downURL := down.URL
	down.Close()

	_, res, err := dialForwardedTerminal(t, newForwardServer(t, downURL), fwdPeerRunID)
	if !errors.Is(err, websocket.ErrBadHandshake) || res == nil {
		t.Fatalf("dial %v, want a refused handshake", err)
	}
	defer func() { _ = res.Body.Close() }()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusBadGateway || string(body) != "peer unreachable\n" {
		t.Fatalf("refusal %d %q, want 502 peer unreachable", res.StatusCode, body)
	}
}

func TestPeerTerminalUnlistedRunIsNotDialled(t *testing.T) {
	p := stubPeerTerminal(t, func(*websocket.Conn) {})
	_, res, err := dialForwardedTerminal(t, newForwardServer(t, p.srv.URL), "peer-b.pane-9")
	if !errors.Is(err, websocket.ErrBadHandshake) || res == nil || res.StatusCode != http.StatusNotFound {
		t.Fatalf("dial %v, want 404", err)
	}
	_ = res.Body.Close()
	if n := len(p.requests()); n != 0 {
		t.Fatalf("peer saw %d requests, want 0", n)
	}
}

// pingingPeer is a peer terminal that pings the relay every interval, as
// servePane does, counts the pongs it gets back and closes gone when its
// socket ends.
func pingingPeer(t *testing.T, interval time.Duration, pongs *atomic.Int32, gone chan struct{}) *peerRecorder {
	t.Helper()
	return stubPeerTerminal(t, func(c *websocket.Conn) {
		c.SetPongHandler(func(string) error { pongs.Add(1); return nil })
		stop := make(chan struct{})
		defer close(stop)
		go func() {
			tick := time.NewTicker(interval)
			defer tick.Stop()
			for {
				select {
				case <-stop:
					return
				case <-tick.C:
					_ = c.WriteControl(websocket.PingMessage, nil, time.Now().Add(time.Second))
				}
			}
		}()
		for {
			if _, _, err := c.ReadMessage(); err != nil {
				close(gone)
				return
			}
		}
	})
}

// TestPeerTerminalLivenessTearsDownBothHops: a browser that stops answering
// pings (it never reads, so gorilla never sends a pong) loses both hops once
// the read deadline passes, though the peer stays live.
func TestPeerTerminalLivenessTearsDownBothHops(t *testing.T) {
	peerGone := make(chan struct{})
	var pongs atomic.Int32
	p := pingingPeer(t, 20*time.Millisecond, &pongs, peerGone)
	s := newForwardServer(t, p.srv.URL)
	s.peerWSPing = 20 * time.Millisecond
	s.peerWSPongWait = 60 * time.Millisecond
	s.peerWSWriteWait = 50 * time.Millisecond
	mustDialForwarded(t, s)

	select {
	case <-peerGone:
	case <-time.After(2 * time.Second):
		t.Fatal("the peer hop outlived a browser that stopped answering pings")
	}
}

// TestPeerTerminalAnsweredPingsKeepTheRelay: a browser that answers the
// relay's pings and a peer that pings the relay keep both hops open through
// several idle read deadlines, and the relay answers the peer's pings.
func TestPeerTerminalAnsweredPingsKeepTheRelay(t *testing.T) {
	peerGone := make(chan struct{})
	var pongs atomic.Int32
	p := pingingPeer(t, 20*time.Millisecond, &pongs, peerGone)
	s := newForwardServer(t, p.srv.URL)
	s.peerWSPing = 20 * time.Millisecond
	s.peerWSPongWait = 60 * time.Millisecond
	browser := mustDialForwarded(t, s)

	// Reading answers the relay's pings; idle for several deadlines.
	idle := make(chan error, 1)
	go func() {
		_ = browser.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
		_, _, err := browser.ReadMessage()
		idle <- err
	}()
	if err := <-idle; err == nil || !strings.Contains(err.Error(), "timeout") {
		t.Fatalf("idle read ended with %v, want only our own deadline", err)
	}
	select {
	case <-peerGone:
		t.Fatal("the peer hop closed while the peer was pinging")
	default:
	}
	if n := pongs.Load(); n < 3 {
		t.Errorf("peer got %d pongs, want the relay to answer its pings", n)
	}
}

// TestPeerTerminalSilentPeerTearsDownBothHops: a peer that upgrades and then
// sends nothing, not even pings, loses both hops once the peer hop's read
// deadline passes, though the browser keeps answering pings.
func TestPeerTerminalSilentPeerTearsDownBothHops(t *testing.T) {
	peerGone := make(chan struct{})
	p := stubPeerTerminal(t, func(c *websocket.Conn) {
		for {
			if _, _, err := c.ReadMessage(); err != nil {
				close(peerGone)
				return
			}
		}
	})
	s := newForwardServer(t, p.srv.URL)
	s.peerWSPing = 20 * time.Millisecond
	s.peerWSPongWait = 60 * time.Millisecond
	browser := mustDialForwarded(t, s)

	// Reading answers the relay's pings, so only the peer hop is silent.
	_ = browser.SetReadDeadline(time.Now().Add(2 * time.Second))
	// The drop reads as 1006 or a reset, never as our own deadline.
	_, _, err := browser.ReadMessage()
	var ne net.Error
	if err == nil || errors.As(err, &ne) && ne.Timeout() {
		t.Fatalf("browser read %v, want the relay to drop the browser hop", err)
	}
	select {
	case <-peerGone:
	case <-time.After(2 * time.Second):
		t.Fatal("the peer hop outlived its own silence")
	}
}

// fakePeerHouston is houston B's terminal route over the pane-WS harness:
// servePane against fake tmux and a fake control client, so B's real
// open-time server check runs without a tmux server.
func fakePeerHouston(t *testing.T, ft *fakeTmux, cm controlManagerOps) string {
	t.Helper()
	mux := http.NewServeMux()
	reg := agents.NewRegistry(generic.New())
	mux.HandleFunc("GET /api/runs/{id}/terminal", func(w http.ResponseWriter, r *http.Request) {
		up := websocket.Upgrader{}
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		servePane(c, ft, cm, reg, serverPane, 10*time.Millisecond)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestPeerTerminalRelaysServerChangedClose(t *testing.T) {
	ft := newFakeTmux()
	ft.paneID = "%1"
	ft.setResolve(tmux.Pane{Server: "200"}, nil) // serverPane is on "100"
	cm := newFakeControlManager(newFakeControlClient())

	browser := mustDialForwarded(t, newForwardServer(t, fakePeerHouston(t, ft, cm)))
	expectClose(t, browser, wsCloseServerChanged)
}

func TestPeerTerminalInputAndOutputCrossThePeer(t *testing.T) {
	ft := newFakeTmux()
	ft.paneID = "%1"
	ft.setResolve(tmux.Pane{Server: "100"}, nil)
	cc := newFakeControlClient()
	cm := newFakeControlManager(cc)

	browser := mustDialForwarded(t, newForwardServer(t, fakePeerHouston(t, ft, cm)))
	readUntilType(t, browser, "seed", 5*time.Second)

	writeInput(t, browser, "peer-input")
	waitForSendCall(t, cc, ft.paneID, "peer-input")

	cc.dispatchAll(ft.paneID, []byte("peer-output"))
	env := readUntilType(t, browser, "output", 5*time.Second)
	var out WSOutput
	if err := json.Unmarshal(env.Data, &out); err != nil || out.Data != "peer-output" {
		t.Fatalf("output %s (%v), want peer-output", env.Data, err)
	}
}

// TestPeerTerminalE2E types through houston A into a real pane houston B
// serves, and reads the echo back through A.
func TestPeerTerminalE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	_, runPaneID, _ := newTrapSession(t)

	regB := runs.NewRegistry(runs.DefaultOrder)
	regB.Apply(runs.Delta{Source: "tmux", Key: runPaneID, Run: runs.Run{
		Agent: "claude",
		Tmux:  &runs.TmuxRef{PaneID: runPaneID},
	}})
	snap := regB.Snapshot()
	if len(snap) != 1 {
		t.Fatalf("B lists %d runs, want 1", len(snap))
	}
	bRunID := snap[0].ID

	controlMgr := tmux.NewControlManager()
	t.Cleanup(controlMgr.Close)
	b := &Server{
		auth:       &authGate{token: fwdPeerToken, enabled: true},
		hosts:      deriveHosts(nil, nil),
		runs:       regB,
		runPanes:   tmux.NewClient(),
		tmux:       tmux.NewClient(),
		controlMgr: controlMgr,
		registry:   agents.NewRegistry(generic.New()),
	}
	srvB := httptest.NewServer(b.Handler())
	t.Cleanup(srvB.Close)

	a := newForwardServer(t, srvB.URL, peerRunDelta("b", bRunID))
	conn, res, err := dialForwardedTerminal(t, a, "peer-b."+bRunID)
	if res != nil {
		_ = res.Body.Close()
	}
	if err != nil {
		status := 0
		if res != nil {
			status = res.StatusCode
		}
		t.Fatalf("dial: %v (status %d)", err, status)
	}
	readUntilType(t, conn, "seed", 5*time.Second)

	// The '' keeps the echoed command line from containing the marker whole.
	marker := strconv.FormatUint(rand.Uint64(), 36)
	writeInput(t, conn, "echo peer-ws-''"+marker+"\r")
	waitForPaneText(t, runPaneID, "peer-ws-"+marker)

	var text strings.Builder
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(text.String(), "peer-ws-"+marker) {
		env := readUntilType(t, conn, "output", time.Until(deadline))
		var out WSOutput
		if err := json.Unmarshal(env.Data, &out); err != nil {
			t.Fatalf("decode %s: %v", env.Data, err)
		}
		text.WriteString(out.Data)
	}
}
