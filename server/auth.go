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
