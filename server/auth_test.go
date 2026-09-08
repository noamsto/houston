package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadOrCreateTokenGeneratesAndPersists(t *testing.T) {
	dir := t.TempDir()

	first, err := LoadOrCreateToken(dir)
	if err != nil {
		t.Fatalf("LoadOrCreateToken: %v", err)
	}
	if len(first) != 64 {
		t.Fatalf("token length %d, want 64 hex chars", len(first))
	}
	if strings.TrimSpace(first) != first {
		t.Fatalf("token has surrounding whitespace: %q", first)
	}

	second, err := LoadOrCreateToken(dir)
	if err != nil {
		t.Fatalf("second LoadOrCreateToken: %v", err)
	}
	if second != first {
		t.Fatalf("token not persisted: %q then %q", first, second)
	}
}

func TestLoadOrCreateTokenFileIsPrivate(t *testing.T) {
	dir := t.TempDir()
	if _, err := LoadOrCreateToken(dir); err != nil {
		t.Fatalf("LoadOrCreateToken: %v", err)
	}

	info, err := os.Stat(filepath.Join(dir, "token"))
	if err != nil {
		t.Fatalf("stat token: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("token file mode %o, want 600", perm)
	}
}

func TestLoadOrCreateTokenToleratesTrailingNewline(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "token"), []byte("deadbeef\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := LoadOrCreateToken(dir)
	if err != nil {
		t.Fatalf("LoadOrCreateToken: %v", err)
	}
	if got != "deadbeef" {
		t.Fatalf("got %q, want %q — an editor-added newline must not change the token", got, "deadbeef")
	}
}

func TestTokenMatches(t *testing.T) {
	if !tokenMatches("abc123", "abc123") {
		t.Error("identical tokens did not match")
	}
	if tokenMatches("abc123", "abc124") {
		t.Error("different tokens matched")
	}
	if tokenMatches("abc123", "") {
		t.Error("empty presented token matched")
	}
	if tokenMatches("", "abc123") {
		t.Error("empty expected token matched — that would disable auth")
	}
}

func reqWithOrigin(host, origin string) *http.Request {
	r := httptest.NewRequest("GET", "http://"+host+"/api/runs", nil)
	r.Host = host
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	return r
}

func TestOriginAllowed(t *testing.T) {
	allowed := []string{"http://localhost:5173"}

	tests := []struct {
		name   string
		host   string
		origin string
		want   bool
	}{
		{"no origin is a non-browser client", "halo:9090", "", true},
		{"same origin", "halo:9090", "http://halo:9090", true},
		{"same origin over https", "halo:9090", "https://halo:9090", true},
		{"explicitly allowlisted", "halo:9090", "http://localhost:5173", true},
		{"different host", "halo:9090", "http://evil.example", false},
		{"different port on same host", "halo:9090", "http://halo:9091", false},
		{"port-less origin against ported host", "halo:9090", "http://halo", false},
		{"null origin from a sandboxed frame", "halo:9090", "null", false},
		{"prefix of an allowed origin", "halo:9090", "http://localhost:51739", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := originAllowed(reqWithOrigin(tc.host, tc.origin), allowed); got != tc.want {
				t.Fatalf("originAllowed(host=%q, origin=%q) = %v, want %v",
					tc.host, tc.origin, got, tc.want)
			}
		})
	}
}

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
}

func TestMiddlewareRejectsWithoutToken(t *testing.T) {
	a := &authGate{token: "secret", enabled: true}
	rec := httptest.NewRecorder()

	a.middleware(okHandler()).ServeHTTP(rec, reqWithOrigin("halo:9090", ""))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status %d, want 401", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "secret") {
		t.Fatal("the response body leaked the token")
	}
}

func TestMiddlewareAcceptsCookieBearerAndQuery(t *testing.T) {
	a := &authGate{token: "secret", enabled: true}

	cookieReq := reqWithOrigin("halo:9090", "")
	cookieReq.AddCookie(&http.Cookie{Name: authCookie, Value: "secret"})

	bearerReq := reqWithOrigin("halo:9090", "")
	bearerReq.Header.Set("Authorization", "Bearer secret")

	queryReq := httptest.NewRequest("GET", "http://halo:9090/api/runs?token=secret", nil)
	queryReq.Host = "halo:9090"

	for name, r := range map[string]*http.Request{
		"cookie": cookieReq,
		"bearer": bearerReq,
		"query":  queryReq,
	} {
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			a.middleware(okHandler()).ServeHTTP(rec, r)
			if rec.Code != http.StatusOK {
				t.Fatalf("status %d, want 200", rec.Code)
			}
		})
	}
}

func TestMiddlewareRejectsBadOriginEvenWithValidToken(t *testing.T) {
	a := &authGate{token: "secret", enabled: true}
	r := reqWithOrigin("halo:9090", "http://evil.example")
	r.AddCookie(&http.Cookie{Name: authCookie, Value: "secret"})

	rec := httptest.NewRecorder()
	a.middleware(okHandler()).ServeHTTP(rec, r)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status %d, want 403 — a valid token from a hostile page must still be refused", rec.Code)
	}
}

func TestMiddlewareDisabledAllowsEverything(t *testing.T) {
	a := &authGate{enabled: false}
	rec := httptest.NewRecorder()

	a.middleware(okHandler()).ServeHTTP(rec, reqWithOrigin("halo:9090", "http://evil.example"))

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200 when auth is disabled", rec.Code)
	}
}

func TestCORSHeadersOnlyForAllowedOrigin(t *testing.T) {
	a := &authGate{token: "secret", enabled: true, allowedOrigins: []string{"http://localhost:5173"}}

	good := reqWithOrigin("halo:9090", "http://localhost:5173")
	good.AddCookie(&http.Cookie{Name: authCookie, Value: "secret"})
	goodRec := httptest.NewRecorder()
	a.middleware(okHandler()).ServeHTTP(goodRec, good)

	if got := goodRec.Header().Get("Access-Control-Allow-Origin"); got != "http://localhost:5173" {
		t.Fatalf("Allow-Origin = %q, want the echoed allowed origin", got)
	}
	if got := goodRec.Header().Get("Access-Control-Allow-Credentials"); got != "true" {
		t.Fatalf("Allow-Credentials = %q, want true — the cookie must be sent cross-origin in dev", got)
	}

	bad := reqWithOrigin("halo:9090", "http://evil.example")
	badRec := httptest.NewRecorder()
	a.middleware(okHandler()).ServeHTTP(badRec, bad)

	if got := badRec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("Allow-Origin = %q for a rejected origin, want empty", got)
	}
}

func TestSetCookieIsHttpOnly(t *testing.T) {
	a := &authGate{token: "secret", enabled: true}
	rec := httptest.NewRecorder()

	a.setCookie(rec)

	cookies := rec.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("%d cookies set, want 1", len(cookies))
	}
	c := cookies[0]
	if c.Name != authCookie || c.Value != "secret" {
		t.Fatalf("cookie = %s=%s, want %s=secret", c.Name, c.Value, authCookie)
	}
	if !c.HttpOnly {
		t.Error("cookie is not HttpOnly — script on the page could read the token")
	}
	if c.SameSite != http.SameSiteStrictMode {
		t.Error("cookie is not SameSite=Strict")
	}
	if c.Path != "/" {
		t.Errorf("cookie path %q, want /", c.Path)
	}
}
