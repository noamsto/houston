package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/noamsto/houston/agents"
	"github.com/noamsto/houston/agents/generic"
	"github.com/noamsto/houston/tmux"
)

// fakeTmux implements tmuxOps with fixed, configurable responses. A single
// mutex guards every field touched after construction: production code reads
// these from servePane's and metaPollLoop's goroutines while tests read the
// call counters from the test goroutine.
type fakeTmux struct {
	mu sync.Mutex

	paneID          string
	paneWidth       int
	paneHeight      int
	windowPaneCount int
	zoomed          bool

	// seed defaults to a non-empty string: captureSeed treats "" as "nothing
	// to send," which would silently skip the write loop's reseed branch.
	seed string

	captureModeOutput string

	panes   []tmux.PaneInfo
	windows []tmux.Window

	forceRedrawN int
	zoomPaneN    int
	captureModeN int
}

func newFakeTmux() *fakeTmux {
	return &fakeTmux{
		seed: "seed-output",
	}
}

func (f *fakeTmux) GetPaneID(p tmux.Pane) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.paneID, nil
}

func (f *fakeTmux) WindowPaneCount(p tmux.Pane) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.windowPaneCount, nil
}

func (f *fakeTmux) IsZoomed(p tmux.Pane) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.zoomed, nil
}

func (f *fakeTmux) ZoomPane(p tmux.Pane) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.zoomPaneN++
	return nil
}

func (f *fakeTmux) GetPaneSize(p tmux.Pane) (width, height int, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.paneWidth, f.paneHeight, nil
}

// CapturePane is deliberately independent of CapturePaneWithMode: the real
// tmux.Client delegates one to the other, but doing that here would let the
// one-time seed capture pollute captureModeCalls(), which a later test uses
// to measure per-second polling rate.
func (f *fakeTmux) CapturePane(p tmux.Pane, lines int) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.seed, nil
}

func (f *fakeTmux) CapturePaneWithMode(p tmux.Pane, lines int) (tmux.CaptureResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.captureModeN++
	return tmux.CaptureResult{Output: f.captureModeOutput}, nil
}

func (f *fakeTmux) ForceRedraw(p tmux.Pane) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.forceRedrawN++
	return nil
}

func (f *fakeTmux) ListPanes(session string, window int) ([]tmux.PaneInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.panes, nil
}

func (f *fakeTmux) ListWindows(session string) ([]tmux.Window, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.windows, nil
}

func (f *fakeTmux) forceRedrawCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.forceRedrawN
}

func (f *fakeTmux) zoomPaneCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.zoomPaneN
}

func (f *fakeTmux) captureModeCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.captureModeN
}

func (f *fakeTmux) setWindowPaneCount(n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.windowPaneCount = n
}

// fakePaneSub implements paneSub. dirty is intentionally NOT guarded by its
// own lock — it's guarded by whichever *fakeControlClient owns it, mirroring
// the real tmux.PaneSub/tmux.ControlClient.mu relationship: a sub's dirty
// flag is meaningless without its owning client's lock.
type fakePaneSub struct {
	ch    chan tmux.PaneEvent
	dirty bool
}

func (s *fakePaneSub) C() <-chan tmux.PaneEvent { return s.ch }

// fakeControlClient implements controlClientOps. One mutex guards all
// mutable state, including the dirty flag on every fakePaneSub it owns.
type fakeControlClient struct {
	mu sync.Mutex

	subs map[string][]*fakePaneSub

	runCallsList []string

	sendCallsList []struct {
		paneID string
		data   string
	}

	done     chan struct{}
	closeOne sync.Once
}

func newFakeControlClient() *fakeControlClient {
	return &fakeControlClient{
		subs: make(map[string][]*fakePaneSub),
		done: make(chan struct{}),
	}
}

func (c *fakeControlClient) RunCommand(command string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.runCallsList = append(c.runCallsList, command)
	return "", nil
}

func (c *fakeControlClient) Subscribe(paneID string) paneSub {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := &fakePaneSub{ch: make(chan tmux.PaneEvent, 64)}
	c.subs[paneID] = append(c.subs[paneID], s)
	return s
}

func (c *fakeControlClient) Unsubscribe(paneID string, s paneSub) {
	c.mu.Lock()
	defer c.mu.Unlock()
	fs := s.(*fakePaneSub)
	subs := c.subs[paneID]
	for i, sub := range subs {
		if sub == fs {
			c.subs[paneID] = append(subs[:i], subs[i+1:]...)
			break
		}
	}
}

func (c *fakeControlClient) AckReseed(s paneSub) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s.(*fakePaneSub).dirty = false
}

func (c *fakeControlClient) MarkPendingReseed(s paneSub) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s.(*fakePaneSub).dirty = true
}

func (c *fakeControlClient) Done() <-chan struct{} {
	return c.done
}

func (c *fakeControlClient) SendKeys(paneID, text string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sendCallsList = append(c.sendCallsList, struct {
		paneID string
		data   string
	}{paneID, text})
	return nil
}

func (c *fakeControlClient) runCalls() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.runCallsList...)
}

func (c *fakeControlClient) sendCallsFor(paneID string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []string
	for _, call := range c.sendCallsList {
		if call.paneID == paneID {
			out = append(out, call.data)
		}
	}
	return out
}

// deliverLocked attempts a non-blocking delivery of data to sub, honoring
// the dirty gate. Callers must hold c.mu.
func (c *fakeControlClient) deliverLocked(sub *fakePaneSub, data []byte) bool {
	if sub.dirty {
		return false
	}
	select {
	case sub.ch <- tmux.PaneEvent{Data: data}:
		return true
	default:
		return false
	}
}

// tryDispatch is a test-only helper, not part of controlClientOps.
func (c *fakeControlClient) tryDispatch(sub *fakePaneSub, data []byte) (delivered bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.deliverLocked(sub, data)
}

func (c *fakeControlClient) dispatchAll(paneID string, data []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, sub := range c.subs[paneID] {
		c.deliverLocked(sub, data)
	}
}

func (c *fakeControlClient) closeDone() {
	c.closeOne.Do(func() {
		close(c.done)
	})
}

// pingPane starts a goroutine that dispatches a "tick" output event to
// paneID every interval until stop is called. Used by tests that need a
// steady stream of output events so the production write loop keeps
// attempting WriteMessage regardless of what metaPollLoop is doing —
// deterministic disconnect detection.
func pingPane(cc *fakeControlClient, paneID string, interval time.Duration) (stop func()) {
	stopCh := make(chan struct{})
	var stopOnce sync.Once

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				cc.dispatchAll(paneID, []byte("tick"))
			case <-stopCh:
				return
			}
		}
	}()

	return func() {
		stopOnce.Do(func() {
			close(stopCh)
		})
	}
}

// fakeControlManager implements controlManagerOps with real ref-counting:
// the client's Done channel only closes once every acquired reference has
// been released. This is load-bearing for a later two-connection test — do
// not simplify to "always close on first release."
type fakeControlManager struct {
	mu sync.Mutex

	client   *fakeControlClient
	refCount int

	getCallsList     []string
	releaseCallsList []string
}

func newFakeControlManager(client *fakeControlClient) *fakeControlManager {
	return &fakeControlManager{client: client}
}

func (m *fakeControlManager) GetClient(session string) (controlClientOps, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.refCount++
	m.getCallsList = append(m.getCallsList, session)
	return m.client, nil
}

func (m *fakeControlManager) ReleaseClient(session string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.releaseCallsList = append(m.releaseCallsList, session)
	m.refCount--
	if m.refCount <= 0 {
		m.client.closeDone()
	}
}

func (m *fakeControlManager) getClientCalls() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.getCallsList...)
}

func (m *fakeControlManager) releaseClientCalls() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.releaseCallsList...)
}

// harnessPane is the fixed pane target used by every startPaneWS call.
var harnessPane = tmux.Pane{Session: "harness-session", Window: 0, Index: 0}

// startPaneWS upgrades a fresh httptest connection into servePane, running
// the real production loop against fake dependencies. The handler performs
// no origin/auth checking — that surface is tested elsewhere against the
// real Server.
func startPaneWS(t *testing.T, tm tmuxOps, cm controlManagerOps) (conn *websocket.Conn, cleanup func()) {
	t.Helper()

	harnessRegistry := agents.NewRegistry(generic.New())

	upgrader := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		servePane(c, tm, cm, harnessRegistry, harnessPane)
	}))

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	clientConn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		srv.Close()
		t.Fatalf("dial pane ws: %v", err)
	}

	cleanup = func() {
		_ = clientConn.Close()
		srv.Close()
	}
	return clientConn, cleanup
}
