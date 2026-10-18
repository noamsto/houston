package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/noamsto/houston/mode"
	"github.com/noamsto/houston/peer"
)

// hostRoutes is every host-addressed route, with the path the peer must see
// and a body that would pass for POST and DELETE.
var hostRoutes = []struct{ method, path, upstream, body string }{
	{"GET", "dispatch/options", "/api/dispatch/options", ""},
	{"POST", "dispatch", "/api/dispatch", `{"repo":"/r","title":"t"}`},
	{"POST", "dispatch/dispatcher", "/api/dispatch/dispatcher", `{"repo":"/r","engine":"claude"}`},
	{"GET", "repos", "/api/repos", ""},
	{"POST", "repos", "/api/repos", `{"path":"/r"}`},
	{"DELETE", "repos", "/api/repos", `{"path":"/r"}`},
	{"GET", "repos/candidates", "/api/repos/candidates", ""},
}

func TestHostForwardSendsOnlyAllowlistedRequestParts(t *testing.T) {
	for _, rt := range hostRoutes {
		t.Run(rt.method+" "+rt.path, func(t *testing.T) {
			p := newPeerRecorder(t, peerConflict)
			s := newForwardServer(t, p.srv.URL)

			query, wantQuery := "?junk=1&token=x", url.Values{}
			if rt.path == "repos/candidates" {
				query, wantQuery = "?q=abc&junk=1&token=x", url.Values{"q": {"abc"}}
			}
			rec := doReply(t, s, browserRequest(rt.method, "/api/hosts/b/"+rt.path+query, rt.body))

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
			if up.method != rt.method || up.path != rt.upstream {
				t.Errorf("upstream %s %s, want %s %s", up.method, up.path, rt.method, rt.upstream)
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
			if len(q) != len(wantQuery) || !queryEqual(q, wantQuery) {
				t.Errorf("upstream query %v, want %v", q, wantQuery)
			}
			if string(up.body) != rt.body {
				t.Errorf("upstream body %q, want %q", up.body, rt.body)
			}
			wantCT := ""
			if rt.method != "GET" {
				wantCT = "application/json"
			}
			if ct := up.header.Get("Content-Type"); ct != wantCT {
				t.Errorf("upstream Content-Type %q, want %q", ct, wantCT)
			}
		})
	}
}

func TestHostForwardRelaysStatusAndBody(t *testing.T) {
	const options = `{"repos":[{"path":"/r","registered":true}], "tiers":["standard"]}`
	p := newPeerRecorder(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, options)
	})
	s := newForwardServer(t, p.srv.URL)

	rec := doReply(t, s, browserRequest("GET", "/api/hosts/b/dispatch/options", ""))
	if rec.Code != http.StatusOK || rec.Body.String() != options {
		t.Fatalf("options %d %q, want the peer's 200 body", rec.Code, rec.Body.String())
	}
	wantInertHeaders(t, rec.Header(), "application/json")

	rec = doReply(t, s, browserRequest("DELETE", "/api/hosts/b/repos", `{"path":"/r"}`))
	if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
		t.Fatalf("delete %d %q, want an empty 204", rec.Code, rec.Body.String())
	}
}

func TestHostForwardMapsLaunchRunID(t *testing.T) {
	const launch = `{"crew":"1-2","session":"s","window":"@1","pane":"%3","run_id":"pane-3"}`
	serve := func(contentType, body string) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", contentType)
			_, _ = io.WriteString(w, body)
		}
	}
	decode := func(t *testing.T, b []byte) map[string]any {
		t.Helper()
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatalf("local body %q: %v", b, err)
		}
		return m
	}
	wantMapped := func(t *testing.T, rec *httptest.ResponseRecorder) {
		t.Helper()
		got := decode(t, rec.Body.Bytes())
		want := decode(t, []byte(launch))
		want["run_id"] = "peer-b.pane-3"
		for k, v := range want {
			if got[k] != v {
				t.Errorf("%s = %v, want %v", k, got[k], v)
			}
		}
		if len(got) != len(want) {
			t.Errorf("local body %v, want %v", got, want)
		}
	}

	t.Run("mapped", func(t *testing.T) {
		p := newPeerRecorder(t, serve("application/json", launch))
		s := newForwardServer(t, p.srv.URL)
		rec := doReply(t, s, browserRequest("POST", "/api/hosts/b/dispatch/dispatcher", `{}`))
		if rec.Code != http.StatusOK {
			t.Fatalf("local answer %d %q", rec.Code, rec.Body.String())
		}
		wantMapped(t, rec)
	})

	t.Run("mapped whatever the content type", func(t *testing.T) {
		p := newPeerRecorder(t, serve("text/plain", launch))
		s := newForwardServer(t, p.srv.URL)
		wantMapped(t, doReply(t, s, browserRequest("POST", "/api/hosts/b/dispatch/dispatcher", `{}`)))
	})

	for _, id := range []string{`"pane/../x"`, `""`, `7`, `null`} {
		t.Run("dropped "+id, func(t *testing.T) {
			p := newPeerRecorder(t, serve("application/json", `{"crew":"1-2","run_id":`+id+`}`))
			s := newForwardServer(t, p.srv.URL)
			rec := doReply(t, s, browserRequest("POST", "/api/hosts/b/dispatch/dispatcher", `{}`))
			got := decode(t, rec.Body.Bytes())
			if v, ok := got["run_id"]; ok {
				t.Errorf("run_id %v kept, want it dropped", v)
			}
			if got["crew"] != "1-2" {
				t.Errorf("crew %v, want 1-2", got["crew"])
			}
		})
	}

	t.Run("bom", func(t *testing.T) {
		p := newPeerRecorder(t, serve("application/json", "\xEF\xBB\xBF"+`{"run_id":"pane-5"}`))
		s := newForwardServer(t, p.srv.URL)
		rec := doReply(t, s, browserRequest("POST", "/api/hosts/b/dispatch/dispatcher", `{}`))
		if got := rec.Body.String(); got != `{"run_id":"peer-b.pane-5"}` {
			t.Errorf("local body %q, want the mapped id and no BOM", got)
		}
	})

	for _, tc := range []struct{ name, path, body string }{
		{"no run_id", "dispatch/dispatcher", `{"error": "dispatcher exited immediately", "crew": "1-2"}`},
		{"worker dispatch", "dispatch", `{"crew": "1-2", "run_id": "pane-3"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := newPeerRecorder(t, serve("application/json", tc.body))
			s := newForwardServer(t, p.srv.URL)
			rec := doReply(t, s, browserRequest("POST", "/api/hosts/b/"+tc.path, `{}`))
			if rec.Body.String() != tc.body {
				t.Errorf("local body %q, want the peer's %q unchanged", rec.Body.String(), tc.body)
			}
		})
	}

	for _, tc := range []struct{ name, contentType, body string }{
		{"not json", "application/json", `dispatcher exited`},
		{"json array", "application/json", `["pane-5"]`},
		{"array of objects", "application/json", `[{"run_id":"pane-5"}]`},
		{"null", "application/json", `null`},
		{"too deep", "application/json", deepRunIDBody},
		{"too deep as text", "text/plain", deepRunIDBody},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := newPeerRecorder(t, serve(tc.contentType, tc.body))
			s := newForwardServer(t, p.srv.URL)
			rec := doReply(t, s, browserRequest("POST", "/api/hosts/b/dispatch/dispatcher", `{}`))
			if got := rec.Body.String(); got != `{"error":"unreadable peer response"}` {
				t.Errorf("local body %.80q, want it replaced", got)
			}
			if rec.Code != http.StatusOK {
				t.Errorf("status %d, want the peer's 200", rec.Code)
			}
		})
	}
}

// deepRunIDBody nests past Go's JSON depth limit, which JSON.parse accepts,
// so the UI would find its run_id.
var deepRunIDBody = `{"x":` + strings.Repeat("[", 10001) + strings.Repeat("]", 10001) + `,"run_id":"pane-5"}`

func TestHostForwardRefusesUnknownHostsAndPaths(t *testing.T) {
	p := newPeerRecorder(t, peerConflict)
	s := newForwardServer(t, p.srv.URL)

	for _, mp := range []string{
		"GET /api/hosts/c/dispatch/options",
		"GET /api/hosts/local/dispatch/options",
		"GET /api/hosts/B/dispatch/options",
		"GET /api/hosts/b/dispatch/x",
		"GET /api/hosts/b/runs",
		"GET /api/hosts/b%2Fx/dispatch/options",
		"GET /api/hosts/b/..%2Fruns/dispatch/options",
		"GET /api/hosts/b/dispatch/options/x",
		"GET /api/hosts/b/../runs",
		"PUT /api/hosts/b/repos",
		"GET /api/hosts/b/dispatch",
	} {
		t.Run(mp, func(t *testing.T) {
			method, path, _ := strings.Cut(mp, " ")
			rec := doReply(t, s, browserRequest(method, path, `{}`))
			if rec.Code < 300 {
				t.Fatalf("status %d, want a refusal", rec.Code)
			}
		})
	}
	for _, got := range p.requests() {
		t.Errorf("peer saw %s %s", got.method, got.path)
	}
	wantRefusal(t, doReply(t, s, browserRequest("GET", "/api/hosts/c/dispatch/options", "")), http.StatusNotFound, "no such host")
}

func TestHostForwardWithoutAuthIsRefused(t *testing.T) {
	p := newPeerRecorder(t, peerConflict)
	s := newForwardServer(t, p.srv.URL)
	s.auth.enabled = false

	for _, rt := range hostRoutes {
		rec := doReply(t, s, browserRequest(rt.method, "/api/hosts/b/"+rt.path, rt.body))
		if rec.Code != http.StatusForbidden || strings.TrimSpace(rec.Body.String()) != "peer actions need auth" {
			t.Errorf("%s %s: got %d %q, want 403 peer actions need auth", rt.method, rt.path, rec.Code, rec.Body.String())
		}
	}
	if n := len(p.requests()); n != 0 {
		t.Fatalf("peer saw %d requests, want 0", n)
	}
}

func TestHostForwardOversizedBodyIsRefusedLocally(t *testing.T) {
	p := newPeerRecorder(t, peerConflict)
	s := newForwardServer(t, p.srv.URL)

	for _, tc := range []struct {
		path string
		size int
	}{
		{"dispatch", maxDispatchBody + 1},
		{"dispatch/dispatcher", maxDispatchBody + 1},
		{"repos", maxRepoBody + 1},
	} {
		rec := doReply(t, s, browserRequest("POST", "/api/hosts/b/"+tc.path, strings.Repeat("a", tc.size)))
		wantRefusal(t, rec, http.StatusRequestEntityTooLarge, "request body too large")
	}
	if n := len(p.requests()); n != 0 {
		t.Fatalf("peer saw %d requests, want 0", n)
	}
}

// TestHostForwardTmuxModePeerIs404: a tmux-mode peer has no dispatch or repo
// routes; its own 404 reaches the browser.
func TestHostForwardTmuxModePeerIs404(t *testing.T) {
	peerSrv := newReplyServer(t, nil)
	peerSrv.mode = mode.Tmux
	ts := httptest.NewServer(peerSrv.Handler())
	t.Cleanup(ts.Close)

	s := newForwardServer(t, ts.URL)
	s.peers = []*peer.Source{peer.New(
		peer.Config{Name: "b", URL: ts.URL, TokenFile: writeTokenFile(t, replyToken)},
		peer.Options{Caps: forwardCaps(mode.Dispatcher, true)},
	)}

	for _, rt := range []struct{ method, path, body string }{
		{"GET", "dispatch/options", ""},
		{"POST", "dispatch", `{}`},
		{"GET", "repos", ""},
	} {
		rec := doReply(t, s, browserRequest(rt.method, "/api/hosts/b/"+rt.path, rt.body))
		if rec.Code != http.StatusNotFound || strings.Contains(rec.Body.String(), "no such host") {
			t.Errorf("%s %s: got %d %q, want the peer's 404", rt.method, rt.path, rec.Code, rec.Body.String())
		}
	}
}

func TestHostRoutesAbsentInTmuxMode(t *testing.T) {
	p := newPeerRecorder(t, peerConflict)
	s := newForwardServer(t, p.srv.URL)
	s.mode = mode.Tmux

	for _, rt := range hostRoutes {
		if rec := doReply(t, s, browserRequest(rt.method, "/api/hosts/b/"+rt.path, rt.body)); rec.Code != http.StatusNotFound {
			t.Errorf("%s %s status %d, want 404", rt.method, rt.path, rec.Code)
		}
	}
	if n := len(p.requests()); n != 0 {
		t.Fatalf("peer saw %d requests, want 0", n)
	}
}

func TestHostForwardDownPeerIsUnreachable(t *testing.T) {
	down := httptest.NewServer(http.NotFoundHandler())
	downURL := down.URL
	down.Close()
	s := newForwardServer(t, downURL)

	wantRefusal(t, doReply(t, s, browserRequest("GET", "/api/hosts/b/dispatch/options", "")), http.StatusBadGateway, "peer unreachable")
}

func TestHostForwardRoutesWait(t *testing.T) {
	if worst := dispatchTimeout + dispatchGrace + tmuxTimeout + 2*dispatchEnginesTimeout; fwdHostDispatch.wait <= worst {
		t.Errorf("dispatch wait %v, want more than the peer's worst case %v", fwdHostDispatch.wait, worst)
	}
	for name, rt := range map[string]peerRoute{"options": fwdHostOptions, "dispatcher": fwdHostDispatcher, "restart": fwdRestart} {
		if rt.wait <= 15*time.Second {
			t.Errorf("%s wait %v, want more than the 15 s header timeout", name, rt.wait)
		}
	}
}

func TestHostForwardPastItsWaitIs504(t *testing.T) {
	release := make(chan struct{})
	p := newPeerRecorder(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	})
	defer close(release)
	s := newForwardServer(t, p.srv.URL)

	rt := fwdHostDispatch
	rt.wait = 50 * time.Millisecond
	req := browserRequest("POST", "/api/hosts/b/dispatch", `{}`)
	req.SetPathValue("host", "b")
	rec := httptest.NewRecorder()
	s.forwardHost(rt)(rec, req)
	wantRefusal(t, rec, http.StatusGatewayTimeout, "peer did not answer in time — it may still be running; check Fleet before retrying")
}
