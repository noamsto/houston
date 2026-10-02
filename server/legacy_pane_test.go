package server

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/noamsto/houston/tmux"
)

func legacySendRequest(path string) *http.Request {
	req := replyRequest("POST", path, "input=Enter&special=true")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return req
}

var legacyPaneRoutes = []struct {
	name string
	req  func(query string) *http.Request
}{
	{"ws", func(q string) *http.Request { return wsRequest("/api/pane/other:3.1/ws" + q) }},
	{"send", func(q string) *http.Request { return legacySendRequest("/api/pane/other:3.1/send" + q) }},
	{"send-with-images", func(q string) *http.Request {
		return replyRequest("POST", "/api/pane/other:3.1/send-with-images"+q, `{"text":"hi","images":[]}`)
	}},
}

func TestLegacyPaneRefusals(t *testing.T) {
	cases := []struct {
		name          string
		query         string
		resolveErr    error
		resolveServer string
		want          int
		wantBody      string
		wantResolve   int
	}{
		{"no identity", "", nil, "", http.StatusBadRequest, "pane identity required", 0},
		{"missing pane_id", "?server=1111", nil, "", http.StatusBadRequest, "pane identity required", 0},
		{"missing server", "?pane_id=%2542", nil, "", http.StatusBadRequest, "pane identity required", 0},
		{"malformed pane_id", "?pane_id=sess:0.0&server=1111", nil, "", http.StatusBadRequest, "pane identity required", 0},
		{"pane gone", "?pane_id=%2542&server=1111", fmt.Errorf("%w: %%42", tmux.ErrPaneNotFound), "", http.StatusConflict, "terminal pane is gone", 1},
		{"tmux unavailable", "?pane_id=%2542&server=1111", errors.New("no server running"), "", http.StatusServiceUnavailable, "tmux unavailable", 1},
		{"server mismatch", "?pane_id=%2542&server=1111", nil, "2222", http.StatusConflict, "terminal pane belongs to a different tmux server", 1},
	}

	for _, route := range legacyPaneRoutes {
		for _, tc := range cases {
			t.Run(route.name+"/"+tc.name, func(t *testing.T) {
				panes := &fakeRunPanes{resolveErr: tc.resolveErr, resolveServer: tc.resolveServer}
				s := newRunTerminalServer(t, panes)

				rec := doReply(t, s, route.req(tc.query))
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
	}
}

// The URL coordinate is only routing: the pane acted on is the one the
// presented pane id resolves to.
func TestLegacyPaneActsOnResolvedPane(t *testing.T) {
	const query = "?pane_id=%2542&server=1111"

	t.Run("send", func(t *testing.T) {
		panes := &fakeRunPanes{resolveServer: "1111"}
		s := newRunTerminalServer(t, panes)

		rec := doReply(t, s, legacySendRequest("/api/pane/other:3.1/send"+query))
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d, want 200 (%q)", rec.Code, rec.Body.String())
		}
		_, sent := panes.calls()
		want := sentInput{pane: tmux.Pane{ID: "%42", Session: "s", Server: "1111"}, keys: "Enter", special: true}
		if len(sent) != 1 || sent[0] != want {
			t.Fatalf("sent %+v, want [%+v]", sent, want)
		}
	})

	t.Run("send-with-images", func(t *testing.T) {
		panes := &fakeRunPanes{resolveServer: "1111"}
		s := newRunTerminalServer(t, panes)

		body := `{"text":"hi","images":[{"name":"a.png","type":"image/png","data":"aGk="}]}`
		rec := doReply(t, s, replyRequest("POST", "/api/pane/other:3.1/send-with-images"+query, body))
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d, want 200 (%q)", rec.Code, rec.Body.String())
		}
		_, sent := panes.calls()
		if len(sent) != 1 || sent[0].pane != (tmux.Pane{ID: "%42", Session: "s", Server: "1111"}) || !sent[0].enter {
			t.Fatalf("sent %+v, want one send with Enter to the resolved pane", sent)
		}
		if !strings.HasSuffix(sent[0].keys, " hi") {
			t.Errorf("keys %q, want image path followed by the text", sent[0].keys)
		}
	})

	t.Run("unknown resolved server is trusted", func(t *testing.T) {
		panes := &fakeRunPanes{resolveServer: ""}
		s := newRunTerminalServer(t, panes)

		rec := doReply(t, s, legacySendRequest("/api/pane/other:3.1/send"+query))
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d, want 200 (%q)", rec.Code, rec.Body.String())
		}
		if _, sent := panes.calls(); len(sent) != 1 {
			t.Fatalf("sent %+v, want one send", sent)
		}
	})

	t.Run("ws", func(t *testing.T) {
		panes := &fakeRunPanes{resolveServer: "1111"}
		s := newRunTerminalServer(t, panes)

		// A recorder can't be hijacked, so getting past resolution shows up
		// as the upgrade's own 500.
		rec := doReply(t, s, wsRequest("/api/pane/other:3.1/ws"+query))
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status %d, want 500 (%q)", rec.Code, rec.Body.String())
		}
		panes.mu.Lock()
		defer panes.mu.Unlock()
		if len(panes.resolved) != 1 || panes.resolved[0] != "%42" {
			t.Fatalf("resolved %v, want [%%42]", panes.resolved)
		}
	})
}

func TestLegacyPaneRemovedRoutes(t *testing.T) {
	cases := []struct {
		method string
		path   string
	}{
		{"POST", "/api/pane/s:0.0/kill"},
		{"POST", "/api/pane/s:0.0/respawn"},
		{"POST", "/api/pane/s:0.0/kill-window"},
		{"POST", "/api/pane/s:0.0/zoom"},
		{"GET", "/api/pane/s:0.0"},
		{"GET", "/api/pane/s:0.0/send"},
	}
	for _, tc := range cases {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			panes := &fakeRunPanes{resolveServer: "1111"}
			s := newRunTerminalServer(t, panes)

			rec := doReply(t, s, replyRequest(tc.method, tc.path+"?pane_id=%2542&server=1111", ""))
			if rec.Code != http.StatusNotFound {
				t.Fatalf("status %d, want 404 (%q)", rec.Code, rec.Body.String())
			}
			if resolved, sent := panes.calls(); resolved != 0 || len(sent) != 0 {
				t.Errorf("resolved %d, sent %+v on a removed route", resolved, sent)
			}
		})
	}
}
