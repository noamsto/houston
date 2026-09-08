package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"testing/fstest"
)

// TestUnknownHostGetsNoCookieAndIsRefused is the A0 regression test. If it
// fails, a rebound page can obtain the real token by fetching "/".
func TestUnknownHostGetsNoCookieAndIsRefused(t *testing.T) {
	g := deriveHosts(nil, nil)
	a := &authGate{token: "secret", enabled: true}

	handler := g.middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		a.setCookie(w)
		w.WriteHeader(http.StatusOK)
	}))

	r := httptest.NewRequest("GET", "http://evil.example/", nil)
	r.Host = "evil.example"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, r)

	if rec.Code != http.StatusMisdirectedRequest {
		t.Fatalf("status %d, want %d", rec.Code, http.StatusMisdirectedRequest)
	}
	if cookies := rec.Result().Cookies(); len(cookies) != 0 {
		t.Fatalf("%d cookies set for an unrecognized host, want 0 — the real token must never reach a rebound page", len(cookies))
	}
}

// TestHandlerRefusesUnknownHostEndToEnd is the A0 regression test proper: it
// exercises Server.Handler(), because A0 was a wiring defect — the SPA
// handler sat outside the host gate and issued the real token to any Host. A
// test that builds its own middleware chain (above) cannot catch that coming
// back.
func TestHandlerRefusesUnknownHostEndToEnd(t *testing.T) {
	dir := t.TempDir()
	s, err := New(Config{StatusDir: dir, AuthEnabled: true, UIFS: fstest.MapFS{}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	req := httptest.NewRequest("GET", "http://evil.example/", nil)
	req.Host = "evil.example"
	rec := httptest.NewRecorder()

	s.Handler().ServeHTTP(rec, req)

	if n := len(rec.Result().Cookies()); n != 0 {
		t.Fatalf("%d cookies issued to an unrecognized Host — the token is being handed to a rebound attacker", n)
	}
	if rec.Code != http.StatusMisdirectedRequest {
		t.Fatalf("status %d, want 421", rec.Code)
	}
}

func TestDeriveHostsIncludesSelfKnowledge(t *testing.T) {
	g := deriveHosts(nil, nil)

	for _, want := range []string{"localhost", "127.0.0.1", "::1"} {
		if !g.allows(want) {
			t.Errorf("deriveHosts did not include loopback literal %q", want)
		}
	}

	hn, err := os.Hostname()
	if err != nil {
		t.Fatalf("os.Hostname: %v", err)
	}
	if !g.allows(hn) {
		t.Errorf("deriveHosts did not include os.Hostname() %q", hn)
	}
}

func TestAllowsStripsPortAndIsCaseInsensitive(t *testing.T) {
	g := deriveHosts(nil, nil)

	if !g.allows("127.0.0.1:9090") {
		t.Error("allows(\"127.0.0.1:9090\") = false, want true — the port must be stripped")
	}
	if !g.allows("LOCALHOST") {
		t.Error("allows(\"LOCALHOST\") = false, want true — the comparison must be case-insensitive")
	}
	if !g.allows("[::1]:9090") {
		t.Error("allows(\"[::1]:9090\") = false, want true — a bracketed IPv6 host:port must be recognized")
	}
}
