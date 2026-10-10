package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/noamsto/houston/hub"
	"github.com/noamsto/houston/mode"
	"github.com/noamsto/houston/tmux"
)

func TestNewRejectsUnresolvedMode(t *testing.T) {
	for _, m := range []mode.Mode{"", mode.Auto} {
		t.Run(string(m), func(t *testing.T) {
			s, err := New(Config{StatusDir: t.TempDir(), Mode: m})
			if s != nil {
				_ = s.Close()
				t.Fatal("New returned a server for an unresolved mode")
			}
			if err == nil {
				t.Fatal("New accepted an unresolved mode")
			}
		})
	}
}

func modeRequest(t *testing.T, h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, "http://127.0.0.1"+path, strings.NewReader(body))
	req.Host = "127.0.0.1"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func decodeMode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/mode status %d, want 200 — body %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type %q, want application/json", ct)
	}
	var got struct {
		Mode string `json:"mode"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v — body %s", err, rec.Body.String())
	}
	return got.Mode
}

func TestTmuxModeUnregistersDispatcherRoutes(t *testing.T) {
	h := newFullServer(t, Config{StatusDir: t.TempDir(), Mode: mode.Tmux}).Handler()

	for _, r := range []struct{ method, path string }{
		{"POST", "/api/dispatch"},
		{"GET", "/api/dispatch/options"},
		{"POST", "/api/dispatch/dispatcher"},
		{"GET", "/api/repos"},
		{"POST", "/api/repos"},
		{"DELETE", "/api/repos"},
		{"GET", "/api/repos/candidates"},
		{"POST", "/api/runs/x/reply"},
	} {
		t.Run(r.method+" "+r.path, func(t *testing.T) {
			if rec := modeRequest(t, h, r.method, r.path, "{}"); rec.Code != http.StatusNotFound {
				t.Fatalf("status %d, want 404 — body %s", rec.Code, rec.Body.String())
			}
		})
	}

	if rec := modeRequest(t, h, "GET", "/api/runs", ""); rec.Code != http.StatusOK {
		t.Fatalf("GET /api/runs status %d, want 200", rec.Code)
	}
	if got := decodeMode(t, modeRequest(t, h, "GET", "/api/mode", "")); got != "tmux" {
		t.Fatalf("mode %q, want tmux", got)
	}
}

func TestTmuxModeNeverRunsDispatchModels(t *testing.T) {
	s := newFullServer(t, Config{StatusDir: t.TempDir(), Mode: mode.Tmux})
	s.dispatchModels = func(context.Context, string) (dispatchModelSet, error) {
		t.Error("dispatch --models ran in tmux mode")
		return dispatchModelSet{}, nil
	}
	h := s.Handler()
	for _, r := range []struct{ method, path string }{
		{"GET", "/api/dispatch/options"},
		{"POST", "/api/dispatch"},
		{"POST", "/api/dispatch/dispatcher"},
	} {
		modeRequest(t, h, r.method, r.path, "{}")
	}
}

func TestDispatcherModeKeepsRoutes(t *testing.T) {
	h := newFullServer(t, Config{StatusDir: t.TempDir(), Mode: mode.Dispatcher}).Handler()

	if got := decodeMode(t, modeRequest(t, h, "GET", "/api/mode", "")); got != "dispatcher" {
		t.Fatalf("mode %q, want dispatcher", got)
	}
	if rec := modeRequest(t, h, "GET", "/api/repos", ""); rec.Code != http.StatusOK {
		t.Fatalf("GET /api/repos status %d, want 200 — body %s", rec.Code, rec.Body.String())
	}
	if rec := modeRequest(t, h, "POST", "/api/dispatch", "{}"); rec.Code == http.StatusNotFound {
		t.Fatalf("POST /api/dispatch is 404 in dispatcher mode — body %s", rec.Body.String())
	}
}

func TestCrewFeedOnlyInDispatcherMode(t *testing.T) {
	for _, m := range []mode.Mode{mode.Dispatcher, mode.Tmux} {
		t.Run(string(m), func(t *testing.T) {
			s := newFullServer(t, Config{StatusDir: t.TempDir(), Mode: m})
			if got, want := s.crewFeed != nil, m == mode.Dispatcher; got != want {
				t.Fatalf("crew feed present = %v, want %v", got, want)
			}
		})
	}
}

func TestRunSourcesByMode(t *testing.T) {
	cm := tmux.NewControlManager()
	t.Cleanup(cm.Close)
	h := hub.New(t.TempDir(), slog.Default())

	for _, tc := range []struct {
		mode mode.Mode
		want []string
	}{
		{mode.Tmux, []string{"hooks", "tmux", "conn"}},
		{mode.Dispatcher, []string{"hooks", "tmux", "crew", "conn"}},
	} {
		t.Run(string(tc.mode), func(t *testing.T) {
			var got []string
			for _, src := range runSources(tc.mode, h, tmux.NewClient(), cm, nil) {
				got = append(got, src.Name())
			}
			if !slices.Equal(got, tc.want) {
				t.Fatalf("sources %v, want %v", got, tc.want)
			}
		})
	}
}

func TestModeEndpoint(t *testing.T) {
	for _, tc := range []struct {
		mode mode.Mode
		want string
	}{
		{"", "dispatcher"},
		{mode.Dispatcher, "dispatcher"},
		{mode.Tmux, "tmux"},
	} {
		t.Run(string(tc.mode), func(t *testing.T) {
			s := &Server{mode: tc.mode}
			rec := httptest.NewRecorder()
			s.handleMode(rec, httptest.NewRequest("GET", "/api/mode", nil))
			if got := decodeMode(t, rec); got != tc.want {
				t.Fatalf("mode %q, want %q", got, tc.want)
			}
		})
	}
}
