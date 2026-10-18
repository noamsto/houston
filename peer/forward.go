package peer

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

var errBadID = errors.New("invalid peer run id")

// Do sends one request to the peer's /api/runs/<peerID>/<suffix> with this
// source's bearer token. Only the given header is sent; the client never
// follows a redirect or uses a proxy, so the token goes nowhere else. The
// caller builds suffix from constants and escaped parts.
func (s *Source) Do(ctx context.Context, method, peerID, suffix string, query url.Values, header http.Header, body []byte) (*http.Response, error) {
	if !ValidID(peerID) {
		return nil, errBadID
	}
	token, err := readToken(s.cfg.TokenFile)
	if err != nil {
		return nil, err
	}
	u := s.runURL(peerID, suffix)
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	var rd io.Reader // not a nil *bytes.Reader, which would read as a body
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return nil, err
	}
	for k, vs := range header {
		req.Header[k] = append([]string(nil), vs...)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	return s.client.Do(req)
}

// DialTerminal opens the peer run's terminal WebSocket through the same dial
// as the runs stream, so an http peer gets the same cleartext-hop check. No
// Origin is sent: the peer's origin check lets a request without one through.
func (s *Source) DialTerminal(ctx context.Context, peerID string) (*websocket.Conn, *http.Response, error) {
	if !ValidID(peerID) {
		return nil, nil, errBadID
	}
	token, err := readToken(s.cfg.TokenFile)
	if err != nil {
		return nil, nil, err
	}
	u := s.runURL(peerID, "terminal")
	if rest, ok := strings.CutPrefix(u, "https:"); ok {
		u = "wss:" + rest
	} else {
		u = "ws:" + strings.TrimPrefix(u, "http:")
	}
	d := websocket.Dialer{NetDialContext: s.dial, Proxy: nil, HandshakeTimeout: 10 * time.Second}
	return d.DialContext(ctx, u, http.Header{"Authorization": {"Bearer " + token}})
}

func (s *Source) runURL(peerID, suffix string) string {
	return s.cfg.URL + "/api/runs/" + peerID + "/" + suffix
}
