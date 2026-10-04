package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/noamsto/houston/agents"
	"github.com/noamsto/houston/agents/generic"
	"github.com/noamsto/houston/runs"
	"github.com/noamsto/houston/tmux"
)

// newAuthTestServer builds a real *Server wired the way New would wire it,
// but without touching disk or spawning tmux — these tests only need the
// auth/host gates and enough of the rest for Handler() and the run terminal
// route to run without panicking.
func newAuthTestServer(token string) *Server {
	allowedOrigins := []string{"http://good.example"}
	reg := runs.NewRegistry(runs.DefaultOrder)
	reg.Apply(termDelta())
	return &Server{
		auth:       &authGate{token: token, enabled: true, allowedOrigins: allowedOrigins},
		hosts:      deriveHosts(nil, allowedOrigins), // mirrors New(): deriveHosts also allowlists each origin's hostname
		tmux:       tmux.NewClient(),
		controlMgr: tmux.NewControlManager(),
		registry:   agents.NewRegistry(generic.New()),
		runs:       reg,
		runPanes:   &fakeRunPanes{resolveServer: "1"},
	}
}

// testPaneTarget returns the run terminal path for the run termDelta seeds.
func testPaneTarget() string {
	return "/api/runs/pane-42/terminal"
}

func setWSUpgradeHeaders(req *http.Request) {
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Sec-WebSocket-Version", "13")
	req.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
}

func TestPaneWSUpgradeRefusedWithoutToken(t *testing.T) {
	s := newAuthTestServer("secret")

	req := httptest.NewRequest("GET", "http://127.0.0.1:9090"+testPaneTarget(), nil)
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

	wsURL := "ws://" + strings.TrimPrefix(srv.URL, "http://") + testPaneTarget() + "?token=secret"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil) //nolint:bodyclose // gorilla: a successful upgrade needs no body close
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
}

func TestPaneWSQueryTokenRejectedOnPlainRequest(t *testing.T) {
	s := newAuthTestServer("secret")

	req := httptest.NewRequest("GET", "http://127.0.0.1:9090"+testPaneTarget()+"?token=secret", nil)
	req.Host = "127.0.0.1:9090"

	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)

	// Together with TestPaneWSUpgradeSucceedsWithQueryToken, this asserts
	// both halves: the query param must work on the upgrade path and be
	// rejected everywhere else. Unlike auth_test.go's
	// TestMiddlewareRejectsQueryTokenOnNonUpgradeRequest, this exercises the
	// actual run terminal route rather than the middleware in isolation.
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status %d, want 401", rec.Code)
	}
}

func TestPaneWSUpgradeRefusedForeignOrigin(t *testing.T) {
	s := newAuthTestServer("secret")

	req := httptest.NewRequest("GET", "http://127.0.0.1:9090"+testPaneTarget(), nil)
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

	req := httptest.NewRequest("GET", "http://evil.example"+testPaneTarget(), nil)
	req.Host = "evil.example"
	req.AddCookie(&http.Cookie{Name: authCookie, Value: "secret"})
	setWSUpgradeHeaders(req)

	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusMisdirectedRequest {
		t.Fatalf("status %d, want 421", rec.Code)
	}
}
