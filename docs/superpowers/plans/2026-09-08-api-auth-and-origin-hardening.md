# API auth and origin hardening — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close the unauthenticated remote-execution surface on houston's API — any web page that can route to the server can currently read every agent session and send arbitrary keystrokes into any tmux pane.

**Architecture:** A bearer token generated on first run into the state dir, delivered to the SPA as an httpOnly cookie when the server serves `index.html`, and required by every `/api/` route. Origin checking (CORS headers and the WebSocket `CheckOrigin`) becomes an explicit allowlist instead of `*`/`true`. The SPA needs no changes at all: every call it makes is a same-origin relative path, so the browser attaches the cookie automatically to `fetch`, `EventSource` and `WebSocket` alike.

**Tech Stack:** Go stdlib only (`crypto/rand`, `crypto/subtle`, `encoding/hex`, `net/http`). `github.com/gorilla/websocket` is already present. No new dependencies, no frontend changes.

**Spec:** `docs/superpowers/specs/2026-09-07-houston-overhaul-design.md` — section "Auth".

## Global Constraints

- **No new dependencies.** This repo has exactly one non-stdlib dependency
  (`github.com/gorilla/websocket`). Add nothing.
- **No frontend changes.** `ui/` is untouched by this plan. Every call site is
  already a same-origin relative path (`ui/src/hooks/useSessionsStream.ts:10`,
  `useAgentsStream.ts:25`, `usePaneSocket.ts:56`,
  `components/MobileInputBar.tsx:45,54,180`).
- **Auth defaults ON.** `-no-auth` exists as an explicit opt-out and must log a
  warning when used.
- **The token file is `0600`** and lives in the state dir. It is never logged at
  info level and never included in an error returned to a client.
- **Tests:** `go test ./server/... -race` from the repo root. NOTE: the server
  package has a PRE-EXISTING data race at `server/agents_test.go:90` over
  `httptest.ResponseRecorder`. It fails under `-race` and predates this work. Run
  `go test ./server/...` WITHOUT `-race` as your gate, and `-race` only with
  `-run` scoped to your own tests.
- **`go build ./...` needs `ui/dist`**, which is gitignored. If it is absent in
  your worktree, build `./server/...` instead — do not run `just ui-build`.

---

## File Structure

| File | Responsibility | Change |
|---|---|---|
| `server/auth.go` | Token load/generate, origin allowlist, auth middleware | **Create** |
| `server/auth_test.go` | Unit tests for all three | **Create** |
| `server/server.go` | Config fields, `New` wiring, `Handler` route mounting, cookie on `index.html` | Modify |
| `server/api.go` | `corsMiddleware` → allowlist-aware | Modify |
| `server/pane_ws.go` | package-level `upgrader` → per-server, real `CheckOrigin` | Modify |
| `main.go` | `-no-auth` flag, allowlist construction, startup logging | Modify |
| `CLAUDE.md` | Security section is now wrong; correct it | Modify |

`auth.go` is a new file rather than additions to `api.go` because it is a
self-contained concern with its own test surface, and `api.go` is already the
grab-bag handler file.

---

### Task 1: Token store

**Files:**
- Create: `server/auth.go`
- Test: `server/auth_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces:
  - `func LoadOrCreateToken(stateDir string) (string, error)`
  - `func tokenMatches(want, got string) bool`

- [ ] **Step 1: Write the failing test**

Create `server/auth_test.go`:

```go
package server

import (
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
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./server/ -run 'TestLoadOrCreateToken|TestTokenMatches' -v`
Expected: FAIL to compile — `undefined: LoadOrCreateToken`.

- [ ] **Step 3: Write the implementation**

Create `server/auth.go`:

```go
package server

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// tokenFile is the state-dir-relative path of the API token.
const tokenFile = "token"

// LoadOrCreateToken returns the API token for this state dir, generating one on
// first run. The file is 0600: anyone who can read it can drive every tmux pane
// this server can reach.
func LoadOrCreateToken(stateDir string) (string, error) {
	path := filepath.Join(stateDir, tokenFile)

	b, err := os.ReadFile(path)
	switch {
	case err == nil:
		tok := strings.TrimSpace(string(b))
		if tok == "" {
			return "", fmt.Errorf("token file %s is empty; delete it to regenerate", path)
		}
		return tok, nil
	case !errors.Is(err, fs.ErrNotExist):
		return "", fmt.Errorf("read token: %w", err)
	}

	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	tok := hex.EncodeToString(raw)

	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return "", fmt.Errorf("create state dir: %w", err)
	}
	if err := os.WriteFile(path, []byte(tok+"\n"), 0o600); err != nil {
		return "", fmt.Errorf("write token: %w", err)
	}
	return tok, nil
}

// tokenMatches compares in constant time. An empty expected token never
// matches, so a misconfiguration fails closed rather than open.
func tokenMatches(want, got string) bool {
	if want == "" || got == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(want), []byte(got)) == 1
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./server/ -run 'TestLoadOrCreateToken|TestTokenMatches' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add server/auth.go server/auth_test.go
git commit -m "feat(server): generate and persist an API token"
```

---

### Task 2: Origin allowlist

Replaces `Access-Control-Allow-Origin: *` (`server/api.go`) and the WebSocket
`CheckOrigin` that returns `true` unconditionally (`server/pane_ws.go`).

A browser always sends `Origin` on a WebSocket handshake and on cross-origin
`fetch`. A request with no `Origin` is therefore not a browser being used as a
confused deputy — it is `curl` or a native client, and the token is what gates
it. So: absent `Origin` is allowed; a present one must be same-origin or
allowlisted.

**Files:**
- Modify: `server/auth.go`
- Test: `server/auth_test.go`

**Interfaces:**
- Consumes: nothing from Task 1.
- Produces: `func originAllowed(r *http.Request, allowed []string) bool`

- [ ] **Step 1: Write the failing test**

Append to `server/auth_test.go` (merge `net/http` and `net/http/httptest` into the
existing import block — do NOT add a second `import` block, it will not compile):

```go
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
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./server/ -run TestOriginAllowed -v`
Expected: FAIL to compile — `undefined: originAllowed`.

- [ ] **Step 3: Write the implementation**

Add to `server/auth.go` (and add `"net/http"` and `"net/url"` to its imports):

```go
// originAllowed reports whether a request's Origin may talk to this server.
//
// A browser always sends Origin on a WebSocket handshake and on a cross-origin
// fetch, so an absent Origin means a non-browser client (curl, a native app) —
// those are gated by the token, not by this check. A present Origin must match
// the request's own host or appear in allowed.
func originAllowed(r *http.Request, allowed []string) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}

	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false // includes the literal "null" a sandboxed frame sends
	}
	if u.Host == r.Host {
		return true
	}
	for _, a := range allowed {
		if a == origin {
			return true
		}
	}
	return false
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./server/ -run TestOriginAllowed -v`
Expected: PASS, all nine subtests.

- [ ] **Step 5: Commit**

```bash
git add server/auth.go server/auth_test.go
git commit -m "feat(server): allowlist-based origin checking"
```

---

### Task 3: Auth middleware and cookie issuance

**Files:**
- Modify: `server/auth.go`
- Test: `server/auth_test.go`

**Interfaces:**
- Consumes: `tokenMatches` (Task 1), `originAllowed` (Task 2).
- Produces:
  - `const authCookie = "houston_token"`
  - `func (a *authGate) middleware(next http.Handler) http.Handler`
  - `func (a *authGate) setCookie(w http.ResponseWriter)`
  - `type authGate struct { token string; allowedOrigins []string; enabled bool }`

- [ ] **Step 1: Write the failing test**

Append to `server/auth_test.go`:

```go
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
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./server/ -run 'TestMiddleware|TestCORS|TestSetCookie' -v`
Expected: FAIL to compile — `undefined: authGate`.

- [ ] **Step 3: Write the implementation**

Add to `server/auth.go`:

```go
// authCookie is the httpOnly cookie the SPA is issued when it loads. Every UI
// call is a same-origin relative path, so the browser attaches it to fetch,
// EventSource and WebSocket alike without any frontend involvement.
const authCookie = "houston_token"

// authGate guards /api/. Disabled, it is a pass-through, which is what
// -no-auth selects.
type authGate struct {
	token          string
	allowedOrigins []string
	enabled        bool
}

// presentedToken pulls the token from the three places a client can put it.
// A browser WebSocket cannot set headers, which is why the query parameter
// exists; same-origin sockets use the cookie and never need it.
func presentedToken(r *http.Request) string {
	if c, err := r.Cookie(authCookie); err == nil && c.Value != "" {
		return c.Value
	}
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimPrefix(h, "Bearer ")
	}
	return r.URL.Query().Get("token")
}

func (a *authGate) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !a.enabled {
			next.ServeHTTP(w, r)
			return
		}

		if !originAllowed(r, a.allowedOrigins) {
			http.Error(w, "forbidden origin", http.StatusForbidden)
			return
		}

		// Echo the origin only once it is known-good, and only when it is
		// genuinely cross-origin; same-origin requests need no CORS headers.
		if origin := r.Header.Get("Origin"); origin != "" && origin != "http://"+r.Host && origin != "https://"+r.Host {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Credentials", "true")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		if !tokenMatches(a.token, presentedToken(r)) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// setCookie issues the token to a browser loading the SPA.
func (a *authGate) setCookie(w http.ResponseWriter) {
	if !a.enabled {
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     authCookie,
		Value:    a.token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./server/ -run 'TestMiddleware|TestCORS|TestSetCookie' -v`
Expected: PASS.

- [ ] **Step 5: Run the whole auth suite**

Run: `go test ./server/ -run 'TestLoadOrCreateToken|TestTokenMatches|TestOriginAllowed|TestMiddleware|TestCORS|TestSetCookie' -race -v`
Expected: PASS. (`-race` is safe here because it is scoped away from the
pre-existing `agents_test.go` race.)

- [ ] **Step 6: Commit**

```bash
git add server/auth.go server/auth_test.go
git commit -m "feat(server): token auth middleware and SPA cookie issuance"
```

---

### Task 4: Wire it in and correct the docs

**Files:**
- Modify: `server/server.go` (`Config`, `Server`, `New`, `Handler`, `SPAHandler`)
- Modify: `server/api.go` (delete `corsMiddleware`)
- Modify: `server/pane_ws.go` (package `upgrader` → per-server)
- Modify: `main.go` (flag, allowlist, startup logging)
- Modify: `CLAUDE.md` (the Security section is now wrong)
- Test: `server/auth_test.go`

**Interfaces:**
- Consumes: `authGate`, `LoadOrCreateToken`, `originAllowed` from Tasks 1-3.
- Produces: `Config.AuthEnabled`, `Config.AllowedOrigins`, `Server.auth`.

- [ ] **Step 1: Write the failing test**

Append to `server/auth_test.go`:

```go
func TestSPAHandlerIssuesCookie(t *testing.T) {
	a := &authGate{token: "secret", enabled: true}
	rec := httptest.NewRecorder()

	SPAHandler(nil, a).ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))

	found := false
	for _, c := range rec.Result().Cookies() {
		if c.Name == authCookie {
			found = true
		}
	}
	if !found {
		t.Fatal("loading the SPA did not issue the auth cookie; the UI could never authenticate")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./server/ -run TestSPAHandlerIssuesCookie -v`
Expected: FAIL to compile — `too many arguments in call to SPAHandler`.

- [ ] **Step 3: Wire the gate into the server**

In `server/server.go`, add to `Config`:

```go
	// AuthEnabled gates /api/ behind the state-dir token. Disabled only by
	// an explicit -no-auth.
	AuthEnabled bool
	// AllowedOrigins are extra origins permitted beyond same-origin, e.g. the
	// Vite dev server.
	AllowedOrigins []string
```

Add to the `Server` struct:

```go
	auth *authGate
```

In `New`, after the struct literal is built, load the token and build the gate:

```go
	gate := &authGate{enabled: cfg.AuthEnabled, allowedOrigins: cfg.AllowedOrigins}
	if cfg.AuthEnabled {
		tok, err := LoadOrCreateToken(cfg.StatusDir)
		if err != nil {
			return nil, fmt.Errorf("api token: %w", err)
		}
		gate.token = tok
	}
	s.auth = gate
```

(Add `"fmt"` to the imports if it is not already there.)

Replace the body of `Handler` with the gate mounted on `/api/` and the SPA
handler taking the gate:

```go
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	if s.uiFS != nil {
		mux.Handle("/", SPAHandler(s.uiFS, s.auth))
	}

	apiMux := http.NewServeMux()
	apiMux.HandleFunc("/api/sessions", s.handleAPISessions)
	apiMux.HandleFunc("/api/pane/", s.handleAPIPane)
	apiMux.HandleFunc("/api/opencode/sessions", s.handleAPIOpenCodeSessions)
	apiMux.HandleFunc("/api/opencode/session/", s.handleAPIOpenCodeSession)
	apiMux.HandleFunc("/api/agents", s.handleAgentsSnapshot)
	apiMux.HandleFunc("/api/agents/stream", s.handleAgentsStream)
	mux.Handle("/api/", s.auth.middleware(apiMux))

	return mux
}
```

Change `SPAHandler` to take the gate and issue the cookie on every SPA response.
It must tolerate a nil `uiFS` so the test above can call it:

```go
// SPAHandler serves the embedded SPA, falling back to index.html for
// client-side routing. Every response issues the auth cookie, which is how the
// browser comes to hold a token it can send on same-origin fetch, EventSource
// and WebSocket calls.
func SPAHandler(uiFS fs.FS, auth *authGate) http.Handler {
	var fileServer http.Handler
	if uiFS != nil {
		fileServer = http.FileServer(http.FS(uiFS))
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth.setCookie(w)
		if fileServer == nil {
			w.WriteHeader(http.StatusOK)
			return
		}

		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" {
			path = "index.html"
		}
		if _, err := fs.Stat(uiFS, path); err == nil {
			fileServer.ServeHTTP(w, r)
			return
		}
		r.URL.Path = "/"
		fileServer.ServeHTTP(w, r)
	})
}
```

- [ ] **Step 4: Delete `corsMiddleware` and fix the WebSocket origin check**

Delete `corsMiddleware` entirely from `server/api.go` — the gate now owns CORS.

In `server/pane_ws.go`, delete the package-level `upgrader` var and give the
server its own:

```go
// wsUpgrader validates Origin against the same allowlist as the HTTP API. A
// browser always sends Origin on a WebSocket handshake, so this is a real
// check, not a formality.
func (s *Server) wsUpgrader() websocket.Upgrader {
	return websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool {
			if !s.auth.enabled {
				return true
			}
			return originAllowed(r, s.auth.allowedOrigins)
		},
	}
}
```

Then replace the single `upgrader.Upgrade(...)` call site with
`up := s.wsUpgrader()` followed by `up.Upgrade(...)`. Find it by symbol, not by
line number.

- [ ] **Step 5: Wire the flag in `main.go`**

In `runServer`, add the flag beside the existing ones:

```go
	noAuth := flag.Bool("no-auth", false, "disable API authentication (NOT recommended)")
```

After `flag.Parse()` and after `*statusDir` is resolved, build the config
additions and log the outcome:

```go
	allowedOrigins := []string{}
	if *debug {
		// The Vite dev server proxies /api here; its Origin is preserved
		// through the proxy, so it has to be allowlisted explicitly.
		allowedOrigins = append(allowedOrigins, "http://localhost:5173")
	}
	if *noAuth {
		slog.Warn("API authentication is DISABLED; any page that can reach this port can drive your tmux panes")
	}
```

Pass them into `server.New`:

```go
		AuthEnabled:    !*noAuth,
		AllowedOrigins: allowedOrigins,
```

And after the server starts, tell the user where the token is — the path, never
the value:

```go
	if !*noAuth {
		fmt.Fprintf(os.Stderr, "api token: %s (open the UI once to authorize this browser)\n",
			filepath.Join(*statusDir, "token"))
	}
```

- [ ] **Step 6: Run the tests**

Run: `go test ./server/...`
Expected: PASS. If `TestAgentsStreamEmitsSnapshotAndUpdate` fails, check whether
you ran with `-race` — that failure is pre-existing and `-race`-only.

Run: `go vet ./server/... && go build ./server/...`
Expected: no output.

- [ ] **Step 7: Verify by hand that the UI still works**

```bash
go build -o /tmp/houston-auth . 2>/dev/null || go build -o /tmp/houston-auth ./...
/tmp/houston-auth -addr 127.0.0.1:9099 &
```

Then, from another shell:

```bash
# No token: refused.
curl -si http://127.0.0.1:9099/api/agents | head -1        # expect 401

# Token from the file: accepted.
curl -si -H "Authorization: Bearer $(cat ~/.local/state/houston/token)" \
     http://127.0.0.1:9099/api/agents | head -1            # expect 200

# A hostile page's origin is refused even with a good token.
curl -si -H "Origin: http://evil.example" \
     -H "Authorization: Bearer $(cat ~/.local/state/houston/token)" \
     http://127.0.0.1:9099/api/agents | head -1            # expect 403

# Loading the SPA issues the cookie.
curl -si http://127.0.0.1:9099/ | grep -i set-cookie       # expect houston_token, HttpOnly
```

Kill the server when done (`kill %1`). Do NOT run `tmux kill-server` or any tmux
command affecting a server or session you did not create.

- [ ] **Step 8: Correct `CLAUDE.md`**

Its Security section currently reads "**No built-in auth** — rely on
network-level security". That is no longer true and was the source of the
vulnerability. Replace that section with the token model: a token generated into
the state dir on first run, issued to the browser as an httpOnly cookie when the
SPA loads, accepted as a cookie, `Authorization: Bearer`, or `?token=` (for
WebSockets, which cannot set headers), an origin allowlist covering both the API
and the WebSocket handshake, and `-no-auth` as an explicit opt-out. Keep the
existing Tailscale/SSH-tunnel advice as defence in depth rather than as the only
defence.

- [ ] **Step 9: Commit**

```bash
git add server/ main.go CLAUDE.md
git commit -m "feat(server): require an API token and check origins"
```

---

## Self-Review

**Spec coverage** — against the spec's "Auth" section:

| Spec requirement | Task |
|---|---|
| `Allow-Origin` becomes an explicit allowlist | 2, 3 |
| Vite dev origin permitted only under `-debug` | 4 |
| `CheckOrigin` validates against the same allowlist | 4 |
| Bearer token generated on first run into the state dir, 0600 | 1 |
| Delivered to the SPA as an httpOnly cookie | 3, 4 |
| Required by every `/api/` route | 3, 4 |
| Reused as the M2 peer credential | out of scope; `LoadOrCreateToken` is the seam |

**Placeholder scan:** none — every step carries the code it needs.

**Type consistency:** `authGate`, its three fields, `middleware`, `setCookie`,
`presentedToken`, `authCookie`, `originAllowed`, `tokenMatches`,
`LoadOrCreateToken`, `Config.AuthEnabled`, `Config.AllowedOrigins` and
`Server.auth` are spelled identically everywhere they appear. `SPAHandler` gains
its second parameter in Task 4, which is the only signature change to an
existing exported function.

**One deliberate asymmetry worth not "fixing":** the middleware checks the origin
*before* the token, so a hostile page with a stolen token gets 403 rather than
401. That ordering is intentional — it means a cross-origin attacker learns
nothing about token validity.
