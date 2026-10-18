package server

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/noamsto/houston/mode"
	"github.com/noamsto/houston/peer"
	"github.com/noamsto/houston/runs"
)

const (
	fwdPeerToken = "token-b"
	fwdPeerRunID = "peer-b.pane-2"
)

type peerRequest struct {
	method, path, rawQuery, host string
	header                       http.Header
	body                         []byte
}

// peerRecorder is an httptest peer that records every request it is sent and
// answers with handler.
type peerRecorder struct {
	srv *httptest.Server
	mu  sync.Mutex
	got []peerRequest
}

func newPeerRecorder(t *testing.T, handler http.HandlerFunc) *peerRecorder {
	t.Helper()
	p := &peerRecorder{}
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		p.mu.Lock()
		p.got = append(p.got, peerRequest{
			method: r.Method, path: r.URL.Path, rawQuery: r.URL.RawQuery, host: r.Host,
			header: r.Header.Clone(), body: body,
		})
		p.mu.Unlock()
		handler(w, r)
	}))
	t.Cleanup(p.srv.Close)
	return p
}

func (p *peerRecorder) requests() []peerRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.got)
}

func peerRunDelta(host, id string) runs.Delta {
	return runs.Delta{Source: runs.PeerLayer, Key: runs.PeerKey(host, id), Run: runs.Run{
		Agent: "claude", State: runs.StateIdle, Host: host,
		Caps: runs.Caps{Terminal: true, Chat: true, Reply: true},
	}}
}

// newForwardServer is a dispatcher-mode local houston listing peer-b.pane-2
// (and peer-c.pane-2, from a host it has no source for), with peer b at
// peerURL.
func newForwardServer(t *testing.T, peerURL string, deltas ...runs.Delta) *Server {
	t.Helper()
	s := newReplyServer(t, nil, append([]runs.Delta{peerRunDelta("b", "pane-2"), peerRunDelta("c", "pane-2")}, deltas...)...)
	s.mode = mode.Dispatcher
	s.peers = []*peer.Source{peer.New(
		peer.Config{Name: "b", URL: peerURL, TokenFile: writeTokenFile(t, fwdPeerToken)},
		peer.Options{Caps: forwardCaps(mode.Dispatcher, true)},
	)}
	return s
}

// browserRequest carries everything a browser could attach that must never
// reach the peer: the cookie, its own bearer token, Origin, ?token= and stray
// headers.
func browserRequest(method, path, body string) *http.Request {
	req := replyRequest(method, path, body)
	req.Header.Set("Authorization", "Bearer "+replyToken)
	req.Header.Set("Origin", replyOrigin)
	req.Header.Set("X-Extra", "1")
	req.Header.Set("Last-Event-ID", "e.1")
	return req
}

func peerConflict(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Set-Cookie", "x=y")
	w.Header().Set("X-Peer", "1")
	w.WriteHeader(http.StatusConflict)
	_, _ = io.WriteString(w, `{"partial":true}`)
}

func TestPeerForwardSendsOnlyAllowlistedRequestParts(t *testing.T) {
	for _, tc := range []struct {
		name, method, path, body string
		upstream                 string
		query                    url.Values
	}{
		{"input", "POST", "/input", `{"type":"text","text":"hi"}`, "/api/runs/pane-2/input", nil},
		{"answer", "POST", "/answer", `{"kind":"choice","ordinal":1}`, "/api/runs/pane-2/answer", nil},
		{"reply", "POST", "/reply", `{"text":"yes"}`, "/api/runs/pane-2/reply", nil},
		{"prompt", "GET", "/prompt", "", "/api/runs/pane-2/prompt", nil},
		{"chat", "GET", "/chat?before=7&limit=10&junk=1&token=x", "", "/api/runs/pane-2/chat", url.Values{"before": {"7"}, "limit": {"10"}}},
		{"chat tool", "GET", "/chat/tool/call-1?token=x", "", "/api/runs/pane-2/chat/tool/call-1", nil},
		{"crew feed", "GET", "/crew/feed?before=e.3&limit=5&from=e&token=x", "", "/api/runs/pane-2/crew/feed", url.Values{"before": {"e.3"}, "limit": {"5"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := newPeerRecorder(t, peerConflict)
			s := newForwardServer(t, p.srv.URL)

			rec := doReply(t, s, browserRequest(tc.method, "/api/runs/"+fwdPeerRunID+tc.path, tc.body))

			if rec.Code != http.StatusConflict || rec.Body.String() != `{"partial":true}` {
				t.Fatalf("local answer %d %q, want the peer's 409", rec.Code, rec.Body.String())
			}
			if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
				t.Errorf("Content-Type %q, want the peer's application/json", ct)
			}
			for _, h := range []string{"Set-Cookie", "X-Peer"} {
				if v := rec.Header().Get(h); v != "" {
					t.Errorf("peer header %s relayed: %q", h, v)
				}
			}

			got := p.requests()
			if len(got) != 1 {
				t.Fatalf("peer saw %d requests, want 1", len(got))
			}
			up := got[0]
			if up.method != tc.method || up.path != tc.upstream {
				t.Errorf("upstream %s %s, want %s %s", up.method, up.path, tc.method, tc.upstream)
			}
			if a := up.header.Values("Authorization"); !slices.Equal(a, []string{"Bearer " + fwdPeerToken}) {
				t.Errorf("upstream Authorization %q, want only the peer token", a)
			}
			for _, h := range []string{"Cookie", "Origin", "X-Extra", "Last-Event-Id"} {
				if v := up.header.Get(h); v != "" {
					t.Errorf("upstream carried %s: %q", h, v)
				}
			}
			if want := strings.TrimPrefix(p.srv.URL, "http://"); up.host != want {
				t.Errorf("upstream Host %q, want the peer's %q", up.host, want)
			}
			q, err := url.ParseQuery(up.rawQuery)
			if err != nil {
				t.Fatalf("upstream query %q: %v", up.rawQuery, err)
			}
			if len(q) != len(tc.query) || (len(q) > 0 && !queryEqual(q, tc.query)) {
				t.Errorf("upstream query %v, want %v", q, tc.query)
			}
			if string(up.body) != tc.body {
				t.Errorf("upstream body %q, want %q", up.body, tc.body)
			}
			wantCT := ""
			if tc.method == "POST" {
				wantCT = "application/json"
			}
			if ct := up.header.Get("Content-Type"); ct != wantCT {
				t.Errorf("upstream Content-Type %q, want %q", ct, wantCT)
			}
		})
	}
}

func queryEqual(a, b url.Values) bool {
	for k, v := range a {
		if !slices.Equal(v, b[k]) {
			return false
		}
	}
	return true
}

func TestPeerForwardRelaysNoContent(t *testing.T) {
	p := newPeerRecorder(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	s := newForwardServer(t, p.srv.URL)

	rec := doReply(t, s, browserRequest("POST", "/api/runs/"+fwdPeerRunID+"/input", `{"type":"key","key":"Enter"}`))
	if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
		t.Fatalf("local answer %d %q, want an empty 204", rec.Code, rec.Body.String())
	}
}

func TestPeerForwardEscapesToolCallID(t *testing.T) {
	p := newPeerRecorder(t, peerConflict)
	s := newForwardServer(t, p.srv.URL)

	doReply(t, s, browserRequest("GET", "/api/runs/"+fwdPeerRunID+"/chat/tool/a%3Fb%23c", ""))
	got := p.requests()
	if len(got) != 1 || got[0].path != "/api/runs/pane-2/chat/tool/a?b#c" || got[0].rawQuery != "" {
		t.Fatalf("peer saw %+v, want one request for call id a?b#c with no query", got)
	}
}

// TestPeerForwardResponseCannotRenderOnThisOrigin: a peer's body is served on
// this origin, so only a JSON or plain-text type survives, never sniffed.
func TestPeerForwardResponseCannotRenderOnThisOrigin(t *testing.T) {
	for _, tc := range []struct{ peerCT, want string }{
		{"text/html", "text/plain; charset=utf-8"},
		{"image/svg+xml", "text/plain; charset=utf-8"},
		{"application/json-seq", "text/plain; charset=utf-8"},
		{"not a type", "text/plain; charset=utf-8"},
		{"", "text/plain; charset=utf-8"},
		{"application/json", "application/json"},
		{"text/plain; charset=iso-8859-1", "text/plain; charset=iso-8859-1"},
	} {
		t.Run(tc.peerCT, func(t *testing.T) {
			p := newPeerRecorder(t, func(w http.ResponseWriter, _ *http.Request) {
				if tc.peerCT == "" {
					w.Header()["Content-Type"] = nil // no sniffed type either
				} else {
					w.Header().Set("Content-Type", tc.peerCT)
				}
				_, _ = io.WriteString(w, "<script>alert(1)</script>")
			})
			s := newForwardServer(t, p.srv.URL)

			rec := doReply(t, s, browserRequest("GET", "/api/runs/"+fwdPeerRunID+"/prompt", ""))
			if rec.Code != http.StatusOK || rec.Body.String() != "<script>alert(1)</script>" {
				t.Fatalf("local answer %d %q, want the peer's 200 body", rec.Code, rec.Body.String())
			}
			wantInertHeaders(t, rec.Header(), tc.want)
		})
	}
}

func wantInertHeaders(t *testing.T, h http.Header, contentType string) {
	t.Helper()
	if ct := h.Get("Content-Type"); ct != contentType {
		t.Errorf("Content-Type %q, want %q", ct, contentType)
	}
	if v := h.Get("X-Content-Type-Options"); v != "nosniff" {
		t.Errorf("X-Content-Type-Options %q, want nosniff", v)
	}
	if v := h.Get("Content-Security-Policy"); v != "default-src 'none'; sandbox" {
		t.Errorf("Content-Security-Policy %q", v)
	}
}

// forwardedRoutes is every HTTP route a peer run id is forwarded on, with a
// body that would pass for POST.
var forwardedRoutes = []struct{ method, suffix, body string }{
	{"POST", "input", `{"type":"key","key":"Enter"}`},
	{"POST", "answer", `{"kind":"choice","ordinal":1}`},
	{"GET", "prompt", ""},
	{"GET", "chat", ""},
	{"GET", "chat/stream", ""},
	{"GET", "chat/tool/call-1", ""},
	{"POST", "reply", `{"text":"yes"}`},
	{"GET", "crew/feed", ""},
	{"GET", "crew/feed/stream", ""},
}

func TestPeerForwardWithoutAuthIsRefused(t *testing.T) {
	p := newPeerRecorder(t, peerConflict)
	s := newForwardServer(t, p.srv.URL, runs.Delta{Source: "tmux", Key: "%1", Run: runs.Run{Agent: "claude", State: runs.StateIdle}})
	s.auth.enabled = false

	for _, rt := range append(forwardedRoutes, struct{ method, suffix, body string }{"GET", "terminal", ""}) {
		rec := doReply(t, s, browserRequest(rt.method, "/api/runs/"+fwdPeerRunID+"/"+rt.suffix, rt.body))
		if rec.Code != http.StatusForbidden || strings.TrimSpace(rec.Body.String()) != "peer actions need auth" {
			t.Errorf("%s %s: got %d %q, want 403 peer actions need auth", rt.method, rt.suffix, rec.Code, rec.Body.String())
		}
	}
	if n := len(p.requests()); n != 0 {
		t.Fatalf("peer saw %d requests, want 0", n)
	}
	wantRefusal(t, doReply(t, s, browserRequest("GET", "/api/runs/pane-1/chat", "")), http.StatusNotFound, "no chat")

	for _, m := range []mode.Mode{mode.Dispatcher, mode.Tmux} {
		if c := forwardCaps(m, false); c != (runs.Caps{}) {
			t.Errorf("%s-mode caps without auth %+v, want none", m, c)
		}
	}
}

func TestPeerForwardOversizedBodyIsRefusedLocally(t *testing.T) {
	p := newPeerRecorder(t, peerConflict)
	s := newForwardServer(t, p.srv.URL)

	body := strings.Repeat("a", maxAnswerBody+1)
	rec := doReply(t, s, browserRequest("POST", "/api/runs/"+fwdPeerRunID+"/answer", body))
	wantRefusal(t, rec, http.StatusRequestEntityTooLarge, "request body too large")
	if n := len(p.requests()); n != 0 {
		t.Fatalf("peer saw %d requests, want 0", n)
	}
}

// craftedPeerIDs are run ids that must never reach a peer: malformed, for a
// host with no source (c), for an unknown peer name (z), or not listed.
var craftedPeerIDs = []string{
	"peer-b.pane-2%2F..%2Fx",
	"peer-b.%2e%2e",
	"peer-b",
	"peer-.pane-2",
	"peer-b.",
	"peer-c.pane-2",
	"peer-z.pane-2",
	"peer-b.pane$2",
	"peer-b.pane-9",
}

// TestPeerForwardRefusesCraftedTargets: every crafted id or path is refused
// here on every forwarded route, and nothing but a fixed route path ever
// reaches the peer.
func TestPeerForwardRefusesCraftedTargets(t *testing.T) {
	p := newPeerRecorder(t, peerConflict)
	s := newForwardServer(t, p.srv.URL)

	var paths []string
	for _, rt := range forwardedRoutes {
		for _, id := range craftedPeerIDs {
			paths = append(paths, rt.method+" /api/runs/"+id+"/"+rt.suffix)
		}
		paths = append(paths,
			rt.method+" /api/runs/peer-b.pane-2/"+rt.suffix+"/extra",
			rt.method+" /api/runs/peer-b.pane-2/../pane-2/"+rt.suffix)
	}
	for _, path := range []string{
		"/api/runs/peer-b.pane-2/chat/tool/..",
		"/api/runs/peer-b.pane-2/chat/tool/%2e%2e",
		"/api/runs/peer-b.pane-2/chat/tool/a%2Fb",
		"/api/runs/peer-b.pane-2/chat/tool/a%5Cb",
		"/api/runs/peer-b.pane-2/chat/tool/a%25b",
		"/api/runs/peer-b.pane-2/chat/tool/a%0Ab",
		"/api/runs/peer-b.pane-2/chat/tool/" + strings.Repeat("a", 1025),
	} {
		paths = append(paths, "GET "+path)
	}
	for _, mp := range paths {
		t.Run(mp, func(t *testing.T) {
			method, path, _ := strings.Cut(mp, " ")
			rec := doReply(t, s, browserRequest(method, path, `{}`))
			// 301 is ServeMux cleaning a dot segment; the browser would
			// follow it to a path that is itself checked.
			if rec.Code != http.StatusNotFound && rec.Code != http.StatusMovedPermanently {
				t.Fatalf("status %d, want 404 or ServeMux's 301", rec.Code)
			}
		})
	}
	for _, got := range p.requests() {
		t.Errorf("peer saw %s %s", got.method, got.path)
	}
}

func TestPeerForwardRefusalBodies(t *testing.T) {
	p := newPeerRecorder(t, peerConflict)
	s := newForwardServer(t, p.srv.URL)

	for _, tc := range []struct{ path, detail string }{
		{"/api/runs/peer-c.pane-2/chat", "no such run"},
		{"/api/runs/peer-b.pane-9/chat", "no such run"},
		{"/api/runs/peer-b.pane$2/chat", "no such run"},
		{"/api/runs/peer-b.pane-2/chat/tool/a%2Fb", "no such tool call"},
	} {
		wantRefusal(t, doReply(t, s, browserRequest("GET", tc.path, "")), http.StatusNotFound, tc.detail)
	}
	if n := len(p.requests()); n != 0 {
		t.Fatalf("peer saw %d requests, want 0", n)
	}
}

func TestPeerForwardNeedsTheRegistry(t *testing.T) {
	p := newPeerRecorder(t, peerConflict)
	withPeers := newForwardServer(t, p.srv.URL)
	s := &Server{auth: withPeers.auth, hosts: withPeers.hosts, peers: withPeers.peers}

	wantRefusal(t, doReply(t, s, browserRequest("GET", "/api/runs/"+fwdPeerRunID+"/chat", "")), http.StatusServiceUnavailable, "run registry not started")
}

func TestPeerRestartIsForwarded(t *testing.T) {
	p := newPeerRecorder(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"crew":"1-2","run_id":"pane-9","old_ended":true}`)
	})
	s := newForwardServer(t, p.srv.URL)

	rec := doReply(t, s, browserRequest("POST", "/api/runs/"+fwdPeerRunID+"/restart", `{"force":true}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("local answer %d %q, want 200", rec.Code, rec.Body.String())
	}
	var got struct {
		Crew     string `json:"crew"`
		RunID    string `json:"run_id"`
		OldEnded bool   `json:"old_ended"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("local body %q: %v", rec.Body.String(), err)
	}
	if got.RunID != "peer-b.pane-9" || !got.OldEnded || got.Crew != "1-2" {
		t.Errorf("local body %+v, want run_id peer-b.pane-9, old_ended, crew 1-2", got)
	}

	reqs := p.requests()
	if len(reqs) != 1 {
		t.Fatalf("peer saw %d requests, want 1", len(reqs))
	}
	up := reqs[0]
	if up.method != "POST" || up.path != "/api/runs/pane-2/restart" {
		t.Errorf("upstream %s %s, want POST /api/runs/pane-2/restart", up.method, up.path)
	}
	if a := up.header.Values("Authorization"); !slices.Equal(a, []string{"Bearer " + fwdPeerToken}) {
		t.Errorf("upstream Authorization %q, want only the peer token", a)
	}
	if string(up.body) != `{"force":true}` {
		t.Errorf("upstream body %q", up.body)
	}
	if ct := up.header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("upstream Content-Type %q", ct)
	}
	for _, h := range []string{"Cookie", "Origin"} {
		if v := up.header.Get(h); v != "" {
			t.Errorf("upstream carried %s: %q", h, v)
		}
	}

	t.Run("unlisted", func(t *testing.T) {
		p := newPeerRecorder(t, peerConflict)
		s := newForwardServer(t, p.srv.URL)

		wantRefusal(t, doReply(t, s, browserRequest("POST", "/api/runs/peer-b.pane-404/restart", `{}`)), http.StatusNotFound, "no such run")
		if n := len(p.requests()); n != 0 {
			t.Fatalf("peer saw %d requests, want 0", n)
		}
	})

	t.Run("busy", func(t *testing.T) {
		const busy = `{"error":"busy","busy":true}`
		p := newPeerRecorder(t, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			_, _ = io.WriteString(w, busy)
		})
		s := newForwardServer(t, p.srv.URL)

		rec := doReply(t, s, browserRequest("POST", "/api/runs/"+fwdPeerRunID+"/restart", `{}`))
		if rec.Code != http.StatusConflict || rec.Body.String() != busy {
			t.Fatalf("local answer %d %q, want the peer's 409 %s", rec.Code, rec.Body.String(), busy)
		}
	})
}

func TestPeerForwardLeavesLocalIDsLocal(t *testing.T) {
	p := newPeerRecorder(t, peerConflict)
	s := newForwardServer(t, p.srv.URL, runs.Delta{Source: "tmux", Key: "%1", Run: runs.Run{Agent: "claude", State: runs.StateIdle}})

	wantRefusal(t, doReply(t, s, browserRequest("GET", "/api/runs/pane-1/chat", "")), http.StatusNotFound, "no chat")
	if n := len(p.requests()); n != 0 {
		t.Fatalf("peer saw %d requests, want 0", n)
	}
}

func TestPeerForwardTmuxModeRegistersNoReplyOrFeed(t *testing.T) {
	p := newPeerRecorder(t, peerConflict)
	s := newForwardServer(t, p.srv.URL)
	s.mode = mode.Tmux

	for _, rt := range []struct{ method, path string }{
		{"POST", "/reply"},
		{"GET", "/crew/feed"},
		{"GET", "/crew/feed/stream"},
	} {
		if rec := doReply(t, s, browserRequest(rt.method, "/api/runs/"+fwdPeerRunID+rt.path, `{}`)); rec.Code != http.StatusNotFound {
			t.Errorf("%s %s status %d, want 404", rt.method, rt.path, rec.Code)
		}
	}
	if n := len(p.requests()); n != 0 {
		t.Fatalf("peer saw %d requests, want 0", n)
	}

	if c := forwardCaps(mode.Tmux, true); c != (runs.Caps{Terminal: true, Chat: true}) {
		t.Errorf("tmux-mode forwardable caps %+v, want terminal and chat only", c)
	}
	if c := forwardCaps(mode.Dispatcher, true); c != (runs.Caps{Terminal: true, Chat: true, Reply: true}) {
		t.Errorf("dispatcher-mode forwardable caps %+v, want terminal, chat and reply", c)
	}
}

func TestPeerForwardDownPeerIsUnreachable(t *testing.T) {
	down := httptest.NewServer(http.NotFoundHandler())
	downURL := down.URL
	down.Close()
	s := newForwardServer(t, downURL)

	rec := doReply(t, s, browserRequest("POST", "/api/runs/"+fwdPeerRunID+"/input", `{"type":"key","key":"Enter"}`))
	if rec.Code != http.StatusBadGateway || rec.Body.String() != "peer unreachable\n" {
		t.Fatalf("local answer %d %q, want 502 peer unreachable", rec.Code, rec.Body.String())
	}
	u, err := url.Parse(downURL)
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{u.Host, u.Port(), fwdPeerToken, "refused", "dial"} {
		if strings.Contains(rec.Body.String(), leak) {
			t.Errorf("body leaks %q", leak)
		}
	}
}

func TestPeerForwardUnreadableTokenIsUnreachable(t *testing.T) {
	p := newPeerRecorder(t, peerConflict)
	s := newForwardServer(t, p.srv.URL)
	s.peers = []*peer.Source{peer.New(
		peer.Config{Name: "b", URL: p.srv.URL, TokenFile: t.TempDir() + "/missing.token"},
		peer.Options{Caps: forwardCaps(mode.Dispatcher, true)},
	)}

	rec := doReply(t, s, browserRequest("GET", "/api/runs/"+fwdPeerRunID+"/prompt", ""))
	wantRefusal(t, rec, http.StatusBadGateway, "peer unreachable")
	if n := len(p.requests()); n != 0 {
		t.Fatalf("peer saw %d requests, want 0", n)
	}
}

func TestPeerForwardRefusalsAreGatewayErrors(t *testing.T) {
	target := newPeerRecorder(t, peerConflict)
	for _, tc := range []struct {
		name    string
		handler http.HandlerFunc
	}{
		{"401", func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "unauthorized", http.StatusUnauthorized) }},
		{"403", func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "forbidden origin", http.StatusForbidden) }},
		{"302", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, target.srv.URL+"/api/runs/pane-2/prompt", http.StatusFound)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := newPeerRecorder(t, tc.handler)
			s := newForwardServer(t, p.srv.URL)

			rec := doReply(t, s, browserRequest("GET", "/api/runs/"+fwdPeerRunID+"/prompt", ""))
			wantRefusal(t, rec, http.StatusBadGateway, "peer refused the request")
			if loc := rec.Header().Get("Location"); loc != "" {
				t.Errorf("Location relayed: %q", loc)
			}
		})
	}
	if n := len(target.requests()); n != 0 {
		t.Fatalf("redirect target saw %d requests, want 0", n)
	}
}

func TestPeerForwardOversizedPeerAnswerIs502(t *testing.T) {
	p := newPeerRecorder(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(make([]byte, maxForwardBody+1))
	})
	s := newForwardServer(t, p.srv.URL)

	wantRefusal(t, doReply(t, s, browserRequest("GET", "/api/runs/"+fwdPeerRunID+"/chat", "")), http.StatusBadGateway, "peer response too large")
}

// startForwardServer serves s over a real listener, for streaming.
func startForwardServer(t *testing.T, s *Server) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv
}

func streamRequest(t *testing.T, ctx context.Context, base, path, lastID string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, "GET", base+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.AddCookie(&http.Cookie{Name: authCookie, Value: replyToken})
	req.Header.Set("Last-Event-ID", lastID)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	return res
}

// readEvent reads one SSE event (up to its blank line) as raw text.
func readEvent(t *testing.T, br *bufio.Reader) string {
	t.Helper()
	var b strings.Builder
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("read event: %v (so far %q)", err, b.String())
		}
		if line == "\n" {
			return b.String()
		}
		b.WriteString(line)
	}
}

func TestPeerChatStreamResumesAndEndsWithThePeer(t *testing.T) {
	second := make(chan struct{})
	lastIDs := make(chan string, 1)
	p := newPeerRecorder(t, func(w http.ResponseWriter, r *http.Request) {
		lastIDs <- r.Header.Get("Last-Event-ID")
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Set-Cookie", "x=y")
		_, _ = io.WriteString(w, "id: e.6\nevent: updates\ndata: []\n\n")
		w.(http.Flusher).Flush()
		<-second
		_, _ = io.WriteString(w, "id: e.7\nevent: updates\ndata: []\n\n")
	})
	srv := startForwardServer(t, newForwardServer(t, p.srv.URL))

	res := streamRequest(t, context.Background(), srv.URL, "/api/runs/"+fwdPeerRunID+"/chat/stream?after=e.2&junk=1", "e.5")
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status %d, want 200", res.StatusCode)
	}
	if ct := res.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type %q", ct)
	}
	if res.Header.Get("Set-Cookie") != "" {
		t.Error("peer Set-Cookie relayed")
	}
	if got := <-lastIDs; got != "e.5" {
		t.Errorf("peer saw Last-Event-ID %q, want e.5", got)
	}
	up := p.requests()[0]
	if up.path != "/api/runs/pane-2/chat/stream" || up.rawQuery != "after=e.2" || up.header.Get("Accept") != "text/event-stream" {
		t.Errorf("upstream %s ?%s Accept %q", up.path, up.rawQuery, up.header.Get("Accept"))
	}

	br := bufio.NewReader(res.Body)
	// The first event arrives while the peer still holds the stream open.
	if ev := readEvent(t, br); ev != "id: e.6\nevent: updates\ndata: []\n" {
		t.Fatalf("first event %q", ev)
	}
	close(second)
	if ev := readEvent(t, br); ev != "id: e.7\nevent: updates\ndata: []\n" {
		t.Fatalf("second event %q", ev)
	}
	if rest, err := io.ReadAll(br); err != nil || len(rest) != 0 {
		t.Fatalf("after the peer ended: %q, %v; want EOF", rest, err)
	}
}

func TestPeerStreamEndsUpstreamWhenTheBrowserGoes(t *testing.T) {
	gone := make(chan struct{})
	p := newPeerRecorder(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: ping\ndata: \n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(gone)
	})
	srv := startForwardServer(t, newForwardServer(t, p.srv.URL))

	ctx, cancel := context.WithCancel(context.Background())
	res := streamRequest(t, ctx, srv.URL, "/api/runs/"+fwdPeerRunID+"/crew/feed/stream?from=e&after=e.1", "e.1")
	defer func() { _ = res.Body.Close() }()
	readEvent(t, bufio.NewReader(res.Body))
	cancel()

	select {
	case <-gone:
	case <-time.After(2 * time.Second):
		t.Fatal("the peer's stream outlived the browser's")
	}
	up := p.requests()[0]
	if q, _ := url.ParseQuery(up.rawQuery); !queryEqual(q, url.Values{"from": {"e"}, "after": {"e.1"}}) || len(q) != 2 {
		t.Errorf("upstream query %q", up.rawQuery)
	}
}

func TestPeerStreamNonEventStreamIs502(t *testing.T) {
	p := newPeerRecorder(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, "<html>")
	})
	s := newForwardServer(t, p.srv.URL)

	wantRefusal(t, doReply(t, s, browserRequest("GET", "/api/runs/"+fwdPeerRunID+"/chat/stream", "")), http.StatusBadGateway, "peer refused the request")
}

func TestPeerStreamRefusalIsRelayed(t *testing.T) {
	p := newPeerRecorder(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_, _ = io.WriteString(w, `{"reset":true}`)
	})
	s := newForwardServer(t, p.srv.URL)

	rec := doReply(t, s, browserRequest("GET", "/api/runs/"+fwdPeerRunID+"/crew/feed/stream", ""))
	if rec.Code != http.StatusConflict || rec.Body.String() != `{"reset":true}` {
		t.Fatalf("local answer %d %q, want the peer's 409", rec.Code, rec.Body.String())
	}
}

// TestPeerStreamsSendOnlyAllowlistedRequestParts: the stream routes carry the
// peer token, Last-Event-ID and their own query keys, and nothing else the
// browser sent.
func TestPeerStreamsSendOnlyAllowlistedRequestParts(t *testing.T) {
	for _, tc := range []struct {
		suffix string
		query  url.Values
	}{
		{"chat/stream", url.Values{"after": {"e.2"}}},
		{"crew/feed/stream", url.Values{"after": {"e.2"}, "from": {"e"}}},
	} {
		t.Run(tc.suffix, func(t *testing.T) {
			p := newPeerRecorder(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "event: reset\ndata: \n\n")
			})
			s := newForwardServer(t, p.srv.URL)

			rec := doReply(t, s, browserRequest("GET", "/api/runs/"+fwdPeerRunID+"/"+tc.suffix+"?after=e.2&from=e&before=e.1&limit=3&token=x&junk=1", ""))
			if rec.Code != http.StatusOK || rec.Body.String() != "event: reset\ndata: \n\n" {
				t.Fatalf("local answer %d %q, want the peer's stream", rec.Code, rec.Body.String())
			}
			if v := rec.Header().Get("X-Content-Type-Options"); v != "nosniff" {
				t.Errorf("X-Content-Type-Options %q, want nosniff", v)
			}

			got := p.requests()
			if len(got) != 1 {
				t.Fatalf("peer saw %d requests, want 1", len(got))
			}
			up := got[0]
			if up.path != "/api/runs/pane-2/"+tc.suffix {
				t.Errorf("upstream path %q", up.path)
			}
			if a := up.header.Values("Authorization"); !slices.Equal(a, []string{"Bearer " + fwdPeerToken}) {
				t.Errorf("upstream Authorization %q, want only the peer token", a)
			}
			for _, h := range []string{"Cookie", "Origin", "X-Extra"} {
				if v := up.header.Get(h); v != "" {
					t.Errorf("upstream carried %s: %q", h, v)
				}
			}
			if v := up.header.Get("Last-Event-ID"); v != "e.1" {
				t.Errorf("upstream Last-Event-ID %q, want e.1", v)
			}
			q, err := url.ParseQuery(up.rawQuery)
			if err != nil {
				t.Fatalf("upstream query %q: %v", up.rawQuery, err)
			}
			if len(q) != len(tc.query) || !queryEqual(q, tc.query) {
				t.Errorf("upstream query %v, want %v", q, tc.query)
			}
		})
	}
}
