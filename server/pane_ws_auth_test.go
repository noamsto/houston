package server

import (
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/noamsto/houston/agents"
	"github.com/noamsto/houston/agents/generic"
	"github.com/noamsto/houston/tmux"
)

// newAuthTestServer builds a real *Server wired the way New would wire it,
// but without touching disk or spawning tmux — these tests only need the
// auth/host gates and enough of the rest for Handler() and handlePaneWS to
// run without panicking.
func newAuthTestServer(token string) *Server {
	allowedOrigins := []string{"http://good.example"}
	return &Server{
		auth:       &authGate{token: token, enabled: true, allowedOrigins: allowedOrigins},
		hosts:      deriveHosts(nil, allowedOrigins), // mirrors New(): deriveHosts also allowlists each origin's hostname
		tmux:       tmux.NewClient(),
		controlMgr: tmux.NewControlManager(),
		registry:   agents.NewRegistry(generic.New()),
	}
}

// testPaneTarget returns a pane WS path that can't collide with a real tmux
// session on the machine running these tests. parsePaneTarget doesn't treat
// "/" as part of a session name, hence the ReplaceAll on t.Name() (which
// contains one for subtests).
func testPaneTarget(t *testing.T) string {
	name := strings.ReplaceAll(t.Name(), "/", "-")
	return "/api/pane/" + name + "-" + strconv.FormatUint(rand.Uint64(), 36) + ":0.0/ws"
}

func setWSUpgradeHeaders(req *http.Request) {
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Sec-WebSocket-Version", "13")
	req.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
}

func TestPaneWSUpgradeRefusedWithoutToken(t *testing.T) {
	s := newAuthTestServer("secret")

	req := httptest.NewRequest("GET", "http://127.0.0.1:9090"+testPaneTarget(t), nil)
	req.Host = "127.0.0.1:9090"
	setWSUpgradeHeaders(req)

	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status %d, want 401", rec.Code)
	}
}

func TestPaneWSUpgradeSucceedsWithQueryToken(t *testing.T) {
	s := newAuthTestServer("secret")

	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)

	wsURL := "ws://" + strings.TrimPrefix(srv.URL, "http://") + testPaneTarget(t) + "?token=secret"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
}

func TestPaneWSQueryTokenRejectedOnPlainRequest(t *testing.T) {
	s := newAuthTestServer("secret")

	req := httptest.NewRequest("GET", "http://127.0.0.1:9090"+testPaneTarget(t)+"?token=secret", nil)
	req.Host = "127.0.0.1:9090"

	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)

	// Together with TestPaneWSUpgradeSucceedsWithQueryToken, this asserts
	// both halves: the query param must work on the upgrade path and be
	// rejected everywhere else. Unlike auth_test.go's
	// TestMiddlewareRejectsQueryTokenOnNonUpgradeRequest, this exercises the
	// actual pane WS route rather than the middleware in isolation.
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status %d, want 401", rec.Code)
	}
}

func TestPaneWSUpgradeRefusedForeignOrigin(t *testing.T) {
	s := newAuthTestServer("secret")

	req := httptest.NewRequest("GET", "http://127.0.0.1:9090"+testPaneTarget(t), nil)
	req.Host = "127.0.0.1:9090"
	req.AddCookie(&http.Cookie{Name: authCookie, Value: "secret"})
	req.Header.Set("Origin", "http://evil.example")
	setWSUpgradeHeaders(req)

	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status %d, want 403", rec.Code)
	}
}

func TestPaneWSUpgradeRefusedUnrecognisedHost(t *testing.T) {
	s := newAuthTestServer("secret")

	req := httptest.NewRequest("GET", "http://evil.example"+testPaneTarget(t), nil)
	req.Host = "evil.example"
	req.AddCookie(&http.Cookie{Name: authCookie, Value: "secret"})
	setWSUpgradeHeaders(req)

	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusMisdirectedRequest {
		t.Fatalf("status %d, want 421", rec.Code)
	}
}
