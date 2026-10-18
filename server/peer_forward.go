package server

import (
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/gorilla/websocket"
	"github.com/noamsto/houston/mode"
	"github.com/noamsto/houston/peer"
	"github.com/noamsto/houston/runs"
)

// maxForwardBody bounds a peer's non-stream answer held in memory.
const maxForwardBody = 16 << 20

const (
	peerWSPing      = 30 * time.Second
	peerWSPongWait  = 45 * time.Second
	peerWSWriteWait = 10 * time.Second

	// Read limits only bound memory; the local pane socket sets none.
	peerWSBrowserLimit = 8 << 20
	peerWSPeerLimit    = 32 << 20
)

type forwardKind int

const (
	forwardPlain forwardKind = iota
	forwardSSE
	forwardWS
)

// peerRoute is how one run-addressed route crosses to a peer: everything the
// upstream request carries is named here, nothing is copied from the browser
// request wholesale.
type peerRoute struct {
	suffix   string // upstream path after /api/runs/<peer id>/
	method   string
	query    []string
	headers  []string
	limit    int64 // POST body cap; 0 sends no body
	kind     forwardKind
	toolCall bool // suffix is followed by the escaped {callId}
}

var (
	fwdTerminal       = peerRoute{suffix: "terminal", method: http.MethodGet, kind: forwardWS}
	fwdInput          = peerRoute{suffix: "input", method: http.MethodPost, headers: []string{"Content-Type"}, limit: maxInputBody}
	fwdAnswer         = peerRoute{suffix: "answer", method: http.MethodPost, headers: []string{"Content-Type"}, limit: maxAnswerBody}
	fwdPrompt         = peerRoute{suffix: "prompt", method: http.MethodGet}
	fwdChat           = peerRoute{suffix: "chat", method: http.MethodGet, query: []string{"before", "limit"}}
	fwdChatStream     = peerRoute{suffix: "chat/stream", method: http.MethodGet, query: []string{"after"}, headers: []string{"Last-Event-ID"}, kind: forwardSSE}
	fwdChatTool       = peerRoute{suffix: "chat/tool", method: http.MethodGet, toolCall: true}
	fwdReply          = peerRoute{suffix: "reply", method: http.MethodPost, headers: []string{"Content-Type"}, limit: maxReplyBody}
	fwdCrewFeed       = peerRoute{suffix: "crew/feed", method: http.MethodGet, query: []string{"before", "limit"}}
	fwdCrewFeedStream = peerRoute{suffix: "crew/feed/stream", method: http.MethodGet, query: []string{"after", "from"}, headers: []string{"Last-Event-ID"}, kind: forwardSSE}
)

// forwardCaps is what this houston can forward to a peer in mode m: reply
// only where the reply route is registered, and no kill route exists. Without
// auth nothing is forwarded: anyone reaching this houston would act on the
// peer with the peer's token.
func forwardCaps(m mode.Mode, authEnabled bool) runs.Caps {
	if !authEnabled {
		return runs.Caps{}
	}
	return runs.Caps{Terminal: true, Chat: true, Reply: m == mode.Dispatcher}
}

// peerOr sends a peer run id to its peer and every other id to local. Local
// ids never start "peer-" (runs.idFor), so local never sees a peer run.
func (s *Server) peerOr(rt peerRoute, local http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.PathValue("id"), "peer-") {
			if s.auth == nil || !s.auth.enabled {
				http.Error(w, "peer actions need auth", http.StatusForbidden)
				return
			}
			s.forwardPeer(w, r, rt)
			return
		}
		local(w, r)
	}
}

// peerTarget resolves peer-NAME.<peer id> to its source, refusing before any
// dial unless the registry lists that run from that peer, so only ids the
// peer itself published ever reach it.
func (s *Server) peerTarget(w http.ResponseWriter, r *http.Request) (*peer.Source, string, bool) {
	if s.runs == nil {
		http.Error(w, "run registry not started", http.StatusServiceUnavailable)
		return nil, "", false
	}
	id := r.PathValue("id")
	name, peerID, ok := strings.Cut(strings.TrimPrefix(id, "peer-"), ".")
	if ok && name != "" && peer.ValidID(peerID) && s.peerRunListed(id, name) {
		for _, src := range s.peers {
			if src.Host() == name {
				return src, peerID, true
			}
		}
	}
	http.Error(w, "no such run", http.StatusNotFound)
	return nil, "", false
}

func (s *Server) peerRunListed(id, host string) bool {
	for _, run := range s.runs.Snapshot() {
		if run.ID == id && run.Host == host {
			return true
		}
	}
	return false
}

// validCallID keeps a tool call id one plain path segment once escaped.
func validCallID(id string) bool {
	if id == "" || len(id) > 1024 || id == "." || id == ".." {
		return false
	}
	return !strings.ContainsFunc(id, func(c rune) bool {
		return c == '/' || c == '\\' || c == '%' || unicode.IsControl(c)
	})
}

func (s *Server) forwardPeer(w http.ResponseWriter, r *http.Request, rt peerRoute) {
	src, peerID, ok := s.peerTarget(w, r)
	if !ok {
		return
	}
	if rt.kind == forwardWS {
		s.forwardTerminal(w, r, src, peerID)
		return
	}
	suffix := rt.suffix
	if rt.toolCall {
		callID := r.PathValue("callId")
		if !validCallID(callID) {
			http.Error(w, "no such tool call", http.StatusNotFound)
			return
		}
		suffix += "/" + url.PathEscape(callID)
	}

	var body []byte
	if rt.limit > 0 {
		b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, rt.limit))
		if err != nil {
			var tooBig *http.MaxBytesError
			if errors.As(err, &tooBig) {
				http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
				return
			}
			http.Error(w, "malformed request body", http.StatusBadRequest)
			return
		}
		body = b
	}
	query := url.Values{}
	in := r.URL.Query()
	for _, k := range rt.query {
		if vs, ok := in[k]; ok {
			query[k] = vs
		}
	}
	header := http.Header{}
	for _, k := range rt.headers {
		for _, v := range r.Header.Values(k) {
			header.Add(k, v)
		}
	}
	if rt.kind == forwardSSE {
		header.Set("Accept", "text/event-stream")
	}

	level := slog.LevelDebug
	if rt.method == http.MethodPost {
		level = slog.LevelInfo
	}
	resp, err := src.Do(r.Context(), rt.method, peerID, suffix, query, header, body)
	if err != nil {
		slog.Debug("peer forward failed", "peer", src.Host(), "route", rt.suffix, "err", err)
		http.Error(w, "peer unreachable", http.StatusBadGateway)
		slog.Log(r.Context(), level, "peer forward", "peer", src.Host(), "route", rt.suffix, "status", http.StatusBadGateway)
		return
	}
	defer func() { _ = resp.Body.Close() }()

	if rt.kind == forwardSSE && resp.StatusCode == http.StatusOK {
		if mt, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type")); mt == "text/event-stream" {
			slog.Debug("peer stream open", "peer", src.Host(), "route", rt.suffix)
			relayPeerStream(w, resp.Body)
			slog.Debug("peer stream closed", "peer", src.Host(), "route", rt.suffix)
			return
		}
		http.Error(w, "peer refused the request", http.StatusBadGateway)
		slog.Debug("peer forward", "peer", src.Host(), "route", rt.suffix, "status", http.StatusBadGateway)
		return
	}
	code := relayPeerResponse(w, resp)
	slog.Log(r.Context(), level, "peer forward", "peer", src.Host(), "route", rt.suffix, "status", code)
}

// relayPeerResponse writes the peer's status, Content-Type and body, and no
// other peer header (no Set-Cookie, no Location). A redirect or auth refusal
// becomes 502: relaying 401 would read as the browser's own token failing.
// The body is served on this origin, so only a JSON or plain-text type is
// relayed and the browser may neither sniff nor render it as a document.
func relayPeerResponse(w http.ResponseWriter, resp *http.Response) int {
	if resp.StatusCode >= 300 && resp.StatusCode < 400 ||
		resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		http.Error(w, "peer refused the request", http.StatusBadGateway)
		return http.StatusBadGateway
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxForwardBody+1))
	if err != nil {
		http.Error(w, "peer unreachable", http.StatusBadGateway)
		return http.StatusBadGateway
	}
	if len(b) > maxForwardBody {
		http.Error(w, "peer response too large", http.StatusBadGateway)
		return http.StatusBadGateway
	}
	h := w.Header()
	h.Set("Content-Type", relayContentType(resp.Header.Get("Content-Type")))
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(b)
	return resp.StatusCode
}

func relayContentType(ct string) string {
	if mt, _, err := mime.ParseMediaType(ct); err == nil && (mt == "application/json" || mt == "text/plain") {
		return ct
	}
	return "text/plain; charset=utf-8"
}

// relayPeerStream copies an event stream as it arrives. The peer's ids,
// resets and pings pass through, so Last-Event-ID resume and the UI's stream
// liveness work unchanged; the request context ends the upstream read when
// the browser goes.
func relayPeerStream(w http.ResponseWriter, body io.Reader) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	h.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	buf := make([]byte, 32<<10)
	for {
		n, err := body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			flusher.Flush()
		}
		if err != nil {
			return
		}
	}
}

// forwardTerminal relays a run's terminal socket to the peer's. The peer is
// dialled before the browser is upgraded, so its pre-upgrade refusals (409,
// 503) reach the browser as the same plain HTTP errors the local route gives.
func (s *Server) forwardTerminal(w http.ResponseWriter, r *http.Request, src *peer.Source, peerID string) {
	if !websocket.IsWebSocketUpgrade(r) {
		http.Error(w, "not a websocket request", http.StatusBadRequest)
		return
	}
	up, resp, err := src.DialTerminal(r.Context(), peerID)
	if resp != nil {
		defer func() { _ = resp.Body.Close() }()
	}
	if err != nil {
		code := http.StatusBadGateway
		switch {
		case errors.Is(err, websocket.ErrBadHandshake) && resp != nil && resp.StatusCode < 300:
			// Relayed, a 1xx/2xx without an upgrade would read as success.
			http.Error(w, "peer refused the request", http.StatusBadGateway)
		case errors.Is(err, websocket.ErrBadHandshake) && resp != nil:
			code = relayPeerResponse(w, resp)
		default:
			slog.Debug("peer terminal dial failed", "peer", src.Host(), "err", err)
			http.Error(w, "peer unreachable", http.StatusBadGateway)
		}
		slog.Info("peer terminal", "peer", src.Host(), "status", code)
		return
	}

	up.SetReadLimit(peerWSPeerLimit)
	upgrader := s.wsUpgrader()
	browser, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		_ = up.Close()
		slog.Debug("peer terminal upgrade failed", "peer", src.Host(), "err", err)
		return
	}
	browser.SetReadLimit(peerWSBrowserLimit)

	code := relayWS(browser, up,
		orDefault(s.peerWSPing, peerWSPing),
		orDefault(s.peerWSPongWait, peerWSPongWait),
		orDefault(s.peerWSWriteWait, peerWSWriteWait))
	outcome := "dropped"
	if code != 0 {
		outcome = "closed " + strconv.Itoa(code)
	}
	slog.Info("peer terminal", "peer", src.Host(), "outcome", outcome)
}

// wsHop serialises writes to one connection: gorilla allows one writer, and
// the pump, the ping ticker and the close relay all write.
type wsHop struct {
	conn *websocket.Conn
	mu   sync.Mutex
	wait time.Duration
}

func (h *wsHop) write(mt int, data []byte) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	_ = h.conn.SetWriteDeadline(time.Now().Add(h.wait))
	return h.conn.WriteMessage(mt, data)
}

func (h *wsHop) control(mt int, data []byte) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.conn.WriteControl(mt, data, time.Now().Add(h.wait))
}

// relayWS pumps frames both ways until either side ends, then closes both and
// returns the close code it relayed first (0 for a drop). The browser hop is
// pinged and read under a deadline, as servePane does, so a phone socket left
// half-open never keeps a peer pane socket alive. The peer hop is read under
// the same deadline, kept fresh by the pings the peer's servePane sends, so a
// peer that went half-open never keeps the browser's socket alive either.
func relayWS(browserConn, upConn *websocket.Conn, ping, pongWait, writeWait time.Duration) int {
	browser := &wsHop{conn: browserConn, wait: writeWait}
	up := &wsHop{conn: upConn, wait: writeWait}

	refresh := func() { _ = browserConn.SetReadDeadline(time.Now().Add(pongWait)) }
	refresh()
	browserConn.SetPongHandler(func(string) error { refresh(); return nil })

	upRefresh := func() { _ = upConn.SetReadDeadline(time.Now().Add(pongWait)) }
	upRefresh()
	// Replacing gorilla's default handler means answering the pong here, through
	// the hop's lock like every other write to the peer.
	upConn.SetPingHandler(func(appData string) error {
		upRefresh()
		if err := up.control(websocket.PongMessage, []byte(appData)); err != nil && !errors.Is(err, websocket.ErrCloseSent) {
			return err
		}
		return nil
	})

	done := make(chan struct{})
	var once sync.Once
	closeBoth := func() {
		once.Do(func() {
			close(done)
			_ = browserConn.Close()
			_ = upConn.Close()
		})
	}

	codes := make(chan int, 2)
	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		codes <- pumpWS(up, browser, upRefresh)
		closeBoth()
	}()
	go func() {
		defer wg.Done()
		codes <- pumpWS(browser, up, refresh)
		closeBoth()
	}()
	go func() {
		defer wg.Done()
		t := time.NewTicker(ping)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				if err := browser.control(websocket.PingMessage, nil); err != nil {
					closeBoth()
					return
				}
			}
		}
	}()
	wg.Wait()
	if first := <-codes; first != 0 {
		return first
	}
	return <-codes
}

// pumpWS copies frames from one hop to the other, type preserved. A close
// frame with a code that may be sent is passed on (so 4409 and 1011 reach the
// browser as the peer sent them); any other read error sends nothing, and the
// other side sees an abnormal close.
func pumpWS(from, to *wsHop, onRead func()) int {
	for {
		mt, data, err := from.conn.ReadMessage()
		if err != nil {
			var ce *websocket.CloseError
			if errors.As(err, &ce) && sendableClose(ce.Code) {
				_ = to.control(websocket.CloseMessage, websocket.FormatCloseMessage(ce.Code, ce.Text))
				return ce.Code
			}
			return 0
		}
		onRead()
		if err := to.write(mt, data); err != nil {
			return 0
		}
	}
}

func sendableClose(code int) bool {
	return code != websocket.CloseNoStatusReceived && code != websocket.CloseAbnormalClosure && code != websocket.CloseTLSHandshake
}
