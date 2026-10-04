package server

import (
	"net/http"
	"testing"
)

// TestRemovedLegacyRoutes pins the classic-view routes as deleted: they must
// 404 through the real Handler, while the run-addressed routes that replaced
// them stay live.
func TestRemovedLegacyRoutes(t *testing.T) {
	s := newReplyServer(t, nil)

	statusOf := func(method, path string) int {
		return doReply(t, s, replyRequest(method, path, "")).Code
	}

	removed := []struct {
		method string
		path   string
	}{
		{"GET", "/api/sessions"},
		{"GET", "/api/sessions?stream=1"},
		{"GET", "/api/agents"},
		{"GET", "/api/agents/stream"},
		{"GET", "/api/pane/x/ws"},
		{"POST", "/api/pane/x/send"},
		{"POST", "/api/pane/x/send-with-images"},
		{"POST", "/api/pane/s:0.0/kill"},
		{"POST", "/api/pane/s:0.0/respawn"},
		{"POST", "/api/pane/s:0.0/kill-window"},
		{"POST", "/api/pane/s:0.0/zoom"},
		{"GET", "/api/pane/s:0.0"},
		{"GET", "/api/pane/s:0.0/send"},
	}
	for _, tc := range removed {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			if code := statusOf(tc.method, tc.path); code != http.StatusNotFound {
				t.Errorf("status %d, want 404", code)
			}
		})
	}

	if code := statusOf("GET", "/api/runs"); code != http.StatusOK {
		t.Errorf("GET /api/runs status %d, want 200", code)
	}
}
