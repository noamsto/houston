package peer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

var errBadID = errors.New("invalid peer run id")

// Mode is how long a forwarded request may wait for the peer and what a late
// failure means.
type Mode int

const (
	// Fast keeps the transport's response-header timeout.
	Fast Mode = iota
	// Long has no header timeout; the caller's context and Options.LongCeiling
	// bound the request, body read included.
	Long
	// Detached is Long for a request the peer finishes on its own once it has
	// it: a failure after the request was written is ErrOutcomeUnknown.
	Detached
)

// ErrOutcomeUnknown wraps a Detached request's failure once the request was
// written: the peer may have acted on it.
var ErrOutcomeUnknown = errors.New("peer outcome unknown")

var errCeiling = errors.New("peer request ceiling reached")

// Do sends one request to the peer's /api/runs/<peerID>/<suffix> with this
// source's bearer token. Only the given header is sent; the client never
// follows a redirect or uses a proxy, so the token goes nowhere else. The
// caller builds suffix from constants and escaped parts.
func (s *Source) Do(ctx context.Context, method, peerID, suffix string, query url.Values, header http.Header, body []byte, mode Mode) (*http.Response, error) {
	if !ValidID(peerID) {
		return nil, errBadID
	}
	return s.send(ctx, s.runURL(peerID, suffix), method, query, header, body, mode)
}

// apiPaths are the host-addressed routes DoAPI may reach.
var apiPaths = map[string]bool{
	"dispatch/options":    true,
	"dispatch":            true,
	"dispatch/dispatcher": true,
	"repos":               true,
	"repos/candidates":    true,
}

var errBadPath = errors.New("path not forwardable")

// DoAPI sends one request to the peer's /api/<path>, one of the host-addressed
// dispatch and repo routes; no other path can be built.
func (s *Source) DoAPI(ctx context.Context, method, path string, query url.Values, header http.Header, body []byte, mode Mode) (*http.Response, error) {
	if !apiPaths[path] {
		return nil, errBadPath
	}
	return s.send(ctx, s.cfg.URL+"/api/"+path, method, query, header, body, mode)
}

func (s *Source) send(ctx context.Context, u, method string, query url.Values, header http.Header, body []byte, mode Mode) (*http.Response, error) {
	token, err := readToken(s.cfg.TokenFile)
	if err != nil {
		return nil, err
	}
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	var rd io.Reader // not a nil *bytes.Reader, which would read as a body
	if body != nil {
		rd = bytes.NewReader(body)
	}
	client, cancel := s.client, context.CancelFunc(func() {})
	if mode != Fast {
		client = s.slow
		ctx, cancel = context.WithTimeoutCause(ctx, s.opts.LongCeiling, errCeiling)
	}
	var written atomic.Bool
	if mode == Detached {
		ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
			WroteRequest: func(info httptrace.WroteRequestInfo) {
				if info.Err == nil {
					written.Store(true)
				}
			},
		})
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		cancel()
		return nil, err
	}
	for k, vs := range header {
		req.Header[k] = append([]string(nil), vs...)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		cancel()
		if written.Load() {
			return nil, fmt.Errorf("%w: %w", ErrOutcomeUnknown, err)
		}
		return nil, err
	}
	resp.Body = &cancelOnClose{ReadCloser: resp.Body, cancel: cancel, detached: mode == Detached}
	return resp, nil
}

// cancelOnClose keeps the request's deadline alive while the caller reads the
// body, and releases it on Close. A Detached body that fails mid-read is an
// unknown outcome: the answer saying what the peer did never arrived whole.
type cancelOnClose struct {
	io.ReadCloser
	cancel   context.CancelFunc
	detached bool
}

func (c *cancelOnClose) Read(p []byte) (int, error) {
	n, err := c.ReadCloser.Read(p)
	if err != nil && !errors.Is(err, io.EOF) && c.detached {
		err = fmt.Errorf("%w: %w", ErrOutcomeUnknown, err)
	}
	return n, err
}

func (c *cancelOnClose) Close() error {
	err := c.ReadCloser.Close()
	c.cancel()
	return err
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
