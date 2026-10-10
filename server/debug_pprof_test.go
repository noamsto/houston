package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/noamsto/houston/mode"
)

func pprofRequest(t *testing.T, s *Server, path, host string, withToken bool) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("GET", "http://"+host+path, nil)
	req.Host = host
	if withToken {
		req.Header.Set("Authorization", "Bearer "+s.auth.token)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

func newPprofServer(t *testing.T, debug bool) *Server {
	t.Helper()
	return newFullServer(t, Config{
		StatusDir:   t.TempDir(),
		AuthEnabled: true,
		UIFS:        fstest.MapFS{},
		Mode:        mode.Dispatcher,
		Debug:       debug,
	})
}

func TestPprofNotRegisteredWithoutDebug(t *testing.T) {
	s := newPprofServer(t, false)
	if rec := pprofRequest(t, s, "/api/debug/pprof/", "localhost", true); rec.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404", rec.Code)
	}
}

func TestPprofRequiresToken(t *testing.T) {
	s := newPprofServer(t, true)
	if rec := pprofRequest(t, s, "/api/debug/pprof/", "localhost", false); rec.Code != http.StatusUnauthorized {
		t.Fatalf("status %d, want 401", rec.Code)
	}
}

// A named profile only resolves when StripPrefix leaves pprof.Index the
// "/debug/pprof/<name>" path it expects.
func TestPprofServesNamedProfile(t *testing.T) {
	s := newPprofServer(t, true)
	rec := pprofRequest(t, s, "/api/debug/pprof/heap?debug=1", "localhost", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "heap profile") {
		t.Fatalf("body does not look like a heap profile: %.200s", rec.Body.String())
	}
}

func TestPprofRefusesUnknownHost(t *testing.T) {
	s := newPprofServer(t, true)
	if rec := pprofRequest(t, s, "/api/debug/pprof/", "evil.example", true); rec.Code != http.StatusMisdirectedRequest {
		t.Fatalf("status %d, want 421", rec.Code)
	}
}
