package server

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/noamsto/houston/runs"
	"github.com/noamsto/houston/tmux"
)

// termRunID is the ID the registry derives for the "%42" key of termDelta.
const termRunID = "pane-42"

type sentInput struct {
	pane    tmux.Pane
	keys    string
	enter   bool
	special bool
}

// fakeRunPanes stands in for tmux. Its call log is the only way to prove a
// refusal happened before anything reached a pane.
type fakeRunPanes struct {
	mu         sync.Mutex
	resolveErr error
	sendErr    error
	resolved   []string
	sent       []sentInput
}

func (f *fakeRunPanes) ResolvePane(paneID string) (tmux.Pane, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resolved = append(f.resolved, paneID)
	if f.resolveErr != nil {
		return tmux.Pane{}, f.resolveErr
	}
	return tmux.Pane{ID: paneID, Session: "s"}, nil
}

func (f *fakeRunPanes) SendKeys(p tmux.Pane, keys string, enter bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, sentInput{pane: p, keys: keys, enter: enter})
	return f.sendErr
}

func (f *fakeRunPanes) SendSpecialKey(p tmux.Pane, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, sentInput{pane: p, keys: key, special: true})
	return f.sendErr
}

func (f *fakeRunPanes) calls() (resolved int, sent []sentInput) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.resolved), append([]sentInput(nil), f.sent...)
}

// termDelta is a tmux-sourced run, which is what makes Caps.Terminal true.
func termDelta() runs.Delta {
	return runs.Delta{Source: "tmux", Key: "%42", Run: runs.Run{
		Agent: "claude",
		Tmux:  &runs.TmuxRef{Session: "s", Window: 0, PaneID: "%42"},
	}}
}

func newRunTerminalServer(t *testing.T, panes runPaneOps, deltas ...runs.Delta) *Server {
	t.Helper()
	s := newReplyServer(t, nil, deltas...)
	s.runPanes = panes
	return s
}

// runIDWhere finds a seeded run's ID by shape, so tests never reimplement ID
// derivation for the base64 keys.
func runIDWhere(t *testing.T, s *Server, match func(runs.Run) bool) string {
	t.Helper()
	for _, r := range s.runs.Snapshot() {
		if match(r) {
			return r.ID
		}
	}
	t.Fatal("no seeded run matches")
	return ""
}

func inputBody(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

func wsRequest(path string) *http.Request {
	req := replyRequest("GET", path, "")
	setWSUpgradeHeaders(req)
	return req
}

func TestRunTerminalResolution(t *testing.T) {
	hookOnly := runs.Delta{Source: "hooks", Key: "%43", Run: runs.Run{
		Agent: "claude",
		Tmux:  &runs.TmuxRef{Session: "s", PaneID: "%43"},
	}}
	noPaneID := runs.Delta{Source: "tmux", Key: "%44", Run: runs.Run{
		Agent: "claude",
		Tmux:  &runs.TmuxRef{Session: "s"},
	}}

	cases := []struct {
		name        string
		id          func(*Server) string
		resolveErr  error
		want        int
		wantBody    string
		wantResolve int
	}{
		{"unknown run", func(*Server) string { return "pane-999" }, nil, http.StatusNotFound, "no such run", 0},
		{"crew-only run", func(s *Server) string {
			return runIDWhere(t, s, func(r runs.Run) bool { return r.Crew != nil })
		}, nil, http.StatusConflict, "run has no terminal", 0},
		// A hook's cached pane ref outlives the pane; only the tmux layer counts.
		{"stale tmux ref", func(*Server) string { return "pane-43" }, nil, http.StatusConflict, "run has no terminal", 0},
		{"no pane id", func(*Server) string { return "pane-44" }, nil, http.StatusConflict, "run has no terminal", 0},
		{"pane vanished", func(*Server) string { return termRunID }, errors.New("can't find pane: %42"), http.StatusConflict, "terminal pane is gone", 1},
	}

	routes := []struct {
		name string
		req  func(id string) *http.Request
	}{
		{"terminal", func(id string) *http.Request { return wsRequest("/api/runs/" + id + "/terminal") }},
		{"input", func(id string) *http.Request {
			return replyRequest("POST", "/api/runs/"+id+"/input", `{"type":"key","key":"Enter"}`)
		}},
	}

	for _, route := range routes {
		for _, tc := range cases {
			t.Run(route.name+"/"+tc.name, func(t *testing.T) {
				panes := &fakeRunPanes{resolveErr: tc.resolveErr}
				s := newRunTerminalServer(t, panes, termDelta(), crewDelta(nil), hookOnly, noPaneID)

				rec := doReply(t, s, route.req(tc.id(s)))
				// The plain code, not 101: resolution refuses before any upgrade.
				if rec.Code != tc.want {
					t.Fatalf("status %d, want %d (%q)", rec.Code, tc.want, rec.Body.String())
				}
				if got := strings.TrimSpace(rec.Body.String()); got != tc.wantBody {
					t.Errorf("body %q, want %q", got, tc.wantBody)
				}
				resolved, sent := panes.calls()
				if resolved != tc.wantResolve {
					t.Errorf("ResolvePane called %d times, want %d", resolved, tc.wantResolve)
				}
				if len(sent) != 0 {
					t.Errorf("sent %+v to a pane on a refused request", sent)
				}
			})
		}

		t.Run(route.name+"/registry not started", func(t *testing.T) {
			panes := &fakeRunPanes{}
			s := newRunTerminalServer(t, panes)
			s.runs = nil

			rec := doReply(t, s, route.req(termRunID))
			if rec.Code != http.StatusServiceUnavailable {
				t.Fatalf("status %d, want 503", rec.Code)
			}
		})
	}
}

func TestRunInputText(t *testing.T) {
	panes := &fakeRunPanes{}
	s := newRunTerminalServer(t, panes, termDelta())

	body := inputBody(t, map[string]string{"type": "text", "text": "echo hi"})
	rec := doReply(t, s, replyRequest("POST", "/api/runs/"+termRunID+"/input", body))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status %d, want 204 (%q)", rec.Code, rec.Body.String())
	}

	_, sent := panes.calls()
	want := sentInput{pane: tmux.Pane{ID: "%42", Session: "s"}, keys: "echo hi", enter: true}
	if len(sent) != 1 || sent[0] != want {
		t.Fatalf("sent %+v, want [%+v]", sent, want)
	}
}

func TestRunInputKeyAllowlist(t *testing.T) {
	for key := range terminalKeys {
		t.Run("allowed "+key, func(t *testing.T) {
			panes := &fakeRunPanes{}
			s := newRunTerminalServer(t, panes, termDelta())

			body := inputBody(t, map[string]string{"type": "key", "key": key})
			rec := doReply(t, s, replyRequest("POST", "/api/runs/"+termRunID+"/input", body))
			if rec.Code != http.StatusNoContent {
				t.Fatalf("status %d, want 204", rec.Code)
			}
			_, sent := panes.calls()
			want := sentInput{pane: tmux.Pane{ID: "%42", Session: "s"}, keys: key, special: true}
			if len(sent) != 1 || sent[0] != want {
				t.Fatalf("sent %+v, want [%+v]", sent, want)
			}
		})
	}

	for _, key := range []string{"send-keys", "C-b", "10", "", "Escape Enter"} {
		t.Run("refused "+key, func(t *testing.T) {
			panes := &fakeRunPanes{}
			s := newRunTerminalServer(t, panes, termDelta())

			body := inputBody(t, map[string]string{"type": "key", "key": key})
			rec := doReply(t, s, replyRequest("POST", "/api/runs/"+termRunID+"/input", body))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status %d, want 400", rec.Code)
			}
			if _, sent := panes.calls(); len(sent) != 0 {
				t.Fatalf("sent %+v for a disallowed key", sent)
			}
		})
	}
}

func TestRunInputRejectsBadBodies(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"empty text", `{"type":"text","text":""}`},
		{"blank text", `{"type":"text","text":"  \n\t"}`},
		{"unknown type", `{"type":"send-keys","text":"x"}`},
		{"missing type", `{"text":"x"}`},
		{"malformed json", `{"type":"text",`},
		{"no images", `{"type":"image","text":"x","images":[]}`},
		{"bad base64", `{"type":"image","images":[{"name":"a.png","type":"image/png","data":"!!not base64!!"}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			panes := &fakeRunPanes{}
			s := newRunTerminalServer(t, panes, termDelta())

			rec := doReply(t, s, replyRequest("POST", "/api/runs/"+termRunID+"/input", tc.body))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status %d, want 400 (%q)", rec.Code, rec.Body.String())
			}
			if _, sent := panes.calls(); len(sent) != 0 {
				t.Fatalf("sent %+v for a rejected body", sent)
			}
		})
	}
}

func TestRunInputImage(t *testing.T) {
	panes := &fakeRunPanes{}
	s := newRunTerminalServer(t, panes, termDelta())

	data := []byte("\x89PNG not really")
	body := inputBody(t, map[string]any{
		"type": "image",
		"text": "caption",
		"images": []map[string]string{{
			"name": "../../evil.png",
			"type": "image/png",
			"data": base64.StdEncoding.EncodeToString(data),
		}},
	})
	rec := doReply(t, s, replyRequest("POST", "/api/runs/"+termRunID+"/input", body))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status %d, want 204 (%q)", rec.Code, rec.Body.String())
	}

	_, sent := panes.calls()
	if len(sent) != 1 {
		t.Fatalf("sent %+v, want one message", sent)
	}
	msg := sent[0]
	path, _, _ := strings.Cut(msg.keys, " ")
	t.Cleanup(func() { _ = os.Remove(path) })

	if !msg.enter || msg.special {
		t.Errorf("sent %+v, want literal text followed by Enter", msg)
	}
	if !strings.HasSuffix(msg.keys, " caption") {
		t.Errorf("message %q does not end with the caption", msg.keys)
	}
	if filepath.Dir(path) != "/tmp" || !strings.HasSuffix(path, "-evil.png") {
		t.Errorf("image saved at %q, want a basename-only file in /tmp", path)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read saved image: %v", err)
	}
	if string(got) != string(data) {
		t.Errorf("saved image %q, want %q", got, data)
	}
}

// repeatByte is an endless reader, so an oversize body costs no allocation.
type repeatByte byte

func (b repeatByte) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = byte(b)
	}
	return len(p), nil
}

func TestRunInputTooLarge(t *testing.T) {
	panes := &fakeRunPanes{}
	s := newRunTerminalServer(t, panes, termDelta())

	body := io.MultiReader(
		strings.NewReader(`{"type":"text","text":"`),
		io.LimitReader(repeatByte('a'), maxInputBody+1),
	)
	req := httptest.NewRequest("POST", "http://"+replyHost+"/api/runs/"+termRunID+"/input", body)
	req.Host = replyHost
	req.AddCookie(&http.Cookie{Name: authCookie, Value: replyToken})

	rec := doReply(t, s, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status %d, want 413", rec.Code)
	}
	if _, sent := panes.calls(); len(sent) != 0 {
		t.Fatalf("sent %+v for an oversize body", sent)
	}
}

func TestRunInputSendFailure(t *testing.T) {
	panes := &fakeRunPanes{sendErr: errors.New("no server running")}
	s := newRunTerminalServer(t, panes, termDelta())

	rec := doReply(t, s, replyRequest("POST", "/api/runs/"+termRunID+"/input", `{"type":"text","text":"x"}`))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status %d, want 500", rec.Code)
	}
}

// The routes sit behind the same token, Origin and Host gates as the rest of
// /api/. Resolution is made to fail on the cases that pass the gates, so a
// passing request is observed without ever upgrading onto a real pane.
func TestRunTerminalAuthGates(t *testing.T) {
	terminal := "/api/runs/" + termRunID + "/terminal"
	input := "/api/runs/" + termRunID + "/input"

	cases := []struct {
		name    string
		req     func() *http.Request
		want    int
		reached bool
	}{
		{"ws without token", func() *http.Request {
			req := wsRequest(terminal)
			req.Header.Del("Cookie")
			return req
		}, http.StatusUnauthorized, false},
		{"ws with query token", func() *http.Request {
			req := wsRequest(terminal + "?token=" + replyToken)
			req.Header.Del("Cookie")
			return req
		}, http.StatusConflict, true},
		{"query token on plain input", func() *http.Request {
			req := replyRequest("POST", input+"?token="+replyToken, `{"type":"key","key":"Enter"}`)
			req.Header.Del("Cookie")
			return req
		}, http.StatusUnauthorized, false},
		{"ws from foreign origin", func() *http.Request {
			req := wsRequest(terminal)
			req.Header.Set("Origin", "http://evil.example")
			return req
		}, http.StatusForbidden, false},
		{"ws from unknown host", func() *http.Request {
			req := wsRequest(terminal)
			req.Host = "evil.example"
			return req
		}, http.StatusMisdirectedRequest, false},
		{"input from unknown host", func() *http.Request {
			req := replyRequest("POST", input, `{"type":"key","key":"Enter"}`)
			req.Host = "evil.example"
			return req
		}, http.StatusMisdirectedRequest, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			panes := &fakeRunPanes{resolveErr: errors.New("gone")}
			s := newRunTerminalServer(t, panes, termDelta())

			rec := doReply(t, s, tc.req())
			if rec.Code != tc.want {
				t.Fatalf("status %d, want %d (%q)", rec.Code, tc.want, rec.Body.String())
			}
			if resolved, _ := panes.calls(); (resolved > 0) != tc.reached {
				t.Errorf("reached resolution = %v, want %v", resolved > 0, tc.reached)
			}
		})
	}

	t.Run("input with cookie and good origin", func(t *testing.T) {
		panes := &fakeRunPanes{}
		s := newRunTerminalServer(t, panes, termDelta())

		req := replyRequest("POST", input, `{"type":"key","key":"Enter"}`)
		req.Header.Set("Origin", replyOrigin)
		rec := doReply(t, s, req)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("status %d, want 204 (%q)", rec.Code, rec.Body.String())
		}
	})
}
