package server

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
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

// authCookie is the httpOnly cookie the SPA is issued when it loads. Every UI
// call is a same-origin relative path, so the browser attaches it to fetch,
// EventSource and WebSocket alike without any frontend involvement.
const authCookie = "houston_token"

// authGate guards /api/. Disabled, it is a pass-through, which is what
// -no-auth selects. A nil *authGate behaves the same way: agents_test.go
// constructs bare &Server{} values that never set auth.
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
		if a == nil || !a.enabled {
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
	if a == nil || !a.enabled {
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
