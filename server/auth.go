package server

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
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

// sameOrigin reports whether the request's Origin is this server's own.
func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return false
	}
	u, err := url.Parse(origin)
	return err == nil && u.Host == r.Host
}

// originAllowed reports whether a request's Origin may talk to this server.
//
// An absent Origin is allowed, but NOT because browsers always send one — they
// do not: <img>, <script>, <link>, top-level navigation and <form method=GET>
// all omit it. It is safe for two independent reasons: every state-changing
// route is POST, and every Origin-less vector above is GET-only; and the auth
// cookie is SameSite=Strict, so no cross-site request carries it at all.
// Weakening either of those — a GET that mutates, or SameSite=Lax — reopens
// this, whatever this comment says.
func originAllowed(r *http.Request, allowed []string) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	if sameOrigin(r) {
		return true
	}

	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false // includes the literal "null" a sandboxed frame sends
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
// -no-auth selects. A nil *authGate is a different case: a Server constructed
// without wiring auth, which is a programming error, not a configuration
// choice — middleware fails closed on it rather than treating it as
// auth-disabled.
type authGate struct {
	token          string
	allowedOrigins []string
	enabled        bool
}

// presentedToken pulls the token from the places a client can put it. The
// query parameter is accepted only on a WebSocket upgrade: a browser socket
// cannot set headers, and the cookie already covers same-origin sockets, so
// the parameter exists solely for the cross-site Vite dev proxy. Accepting it
// on ordinary requests would leak the token into browser history, Referer,
// and proxy logs for no benefit.
func presentedToken(r *http.Request) string {
	if c, err := r.Cookie(authCookie); err == nil && c.Value != "" {
		return c.Value
	}
	if h := r.Header.Get("Authorization"); len(h) > 7 && strings.EqualFold(h[:7], "Bearer ") {
		return h[7:]
	}
	if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		return r.URL.Query().Get("token")
	}
	return ""
}

func (a *authGate) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a == nil {
			// An unwired gate is a programming error, not a configuration
			// choice. Refuse loudly rather than serving /api/ unauthenticated.
			slog.Error("auth gate not configured; refusing request", "path", r.URL.Path)
			http.Error(w, "server misconfigured", http.StatusInternalServerError)
			return
		}

		// Origin checking runs even when auth is disabled: -no-auth removes
		// the token requirement, not cross-origin drivability.
		if !originAllowed(r, a.allowedOrigins) {
			http.Error(w, "forbidden origin", http.StatusForbidden)
			return
		}

		if !a.enabled {
			next.ServeHTTP(w, r)
			return
		}

		// Echo the origin only once it is known-good, and only when it is
		// genuinely cross-origin; same-origin requests need no CORS headers.
		if origin := r.Header.Get("Origin"); origin != "" && !sameOrigin(r) {
			w.Header().Set("Vary", "Origin")
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

// setCookie issues the token to a browser loading the SPA. It has no Secure
// flag: houston serves plaintext over a tailnet or loopback, and Secure would
// break the cookie on both.
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
