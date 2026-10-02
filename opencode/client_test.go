package opencode

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"
)

func TestClient_Health(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/global/health" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(HealthResponse{
			Healthy: true,
			Version: "1.0.0",
		})
	}))
	defer server.Close()

	client := NewClient(server.URL)
	health, err := client.Health(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !health.Healthy {
		t.Error("expected healthy=true")
	}
	if health.Version != "1.0.0" {
		t.Errorf("expected version=1.0.0, got %s", health.Version)
	}
}

func TestClient_ListSessions(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/session" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode([]Session{
			{
				ID:        "sess-1",
				Title:     "Test Session",
				CreatedAt: time.Now(),
			},
			{
				ID:        "sess-2",
				Title:     "Another Session",
				CreatedAt: time.Now(),
			},
		})
	}))
	defer server.Close()

	client := NewClient(server.URL)
	sessions, err := client.ListSessions(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(sessions) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(sessions))
	}
	if sessions[0].ID != "sess-1" {
		t.Errorf("expected session ID=sess-1, got %s", sessions[0].ID)
	}
}

func TestClient_GetSessionStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/session/status" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]SessionStatus{
			"sess-1": {Status: "idle", SessionID: "sess-1"},
			"sess-2": {Status: "busy", SessionID: "sess-2"},
		})
	}))
	defer server.Close()

	client := NewClient(server.URL)
	statuses, err := client.GetSessionStatus(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(statuses) != 2 {
		t.Errorf("expected 2 statuses, got %d", len(statuses))
	}
	if statuses["sess-1"].Status != "idle" {
		t.Errorf("expected status=idle, got %s", statuses["sess-1"].Status)
	}
	if statuses["sess-2"].Status != "busy" {
		t.Errorf("expected status=busy, got %s", statuses["sess-2"].Status)
	}
}

func TestClient_SendPromptAsync(t *testing.T) {
	var receivedBody PromptRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/session/test-session/prompt_async" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		_ = json.NewDecoder(r.Body).Decode(&receivedBody)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	client := NewClient(server.URL)
	err := client.SendPromptAsync(context.Background(), "test-session", PromptRequest{
		Parts: []PromptPart{
			{Type: "text", Text: "Hello, world!"},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(receivedBody.Parts) != 1 {
		t.Errorf("expected 1 part, got %d", len(receivedBody.Parts))
	}
	if receivedBody.Parts[0].Text != "Hello, world!" {
		t.Errorf("expected text='Hello, world!', got %s", receivedBody.Parts[0].Text)
	}
}

func TestIsAvailable(t *testing.T) {
	// Test available server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(HealthResponse{Healthy: true, Version: "1.0.0"})
	}))
	defer server.Close()

	if !IsAvailable(context.Background(), server.URL) {
		t.Error("expected server to be available")
	}

	// Test unavailable server
	if IsAvailable(context.Background(), "http://localhost:99999") {
		t.Error("expected server to be unavailable")
	}
}

func TestClient_SessionIDIsEscapedIntoOnePathSegment(t *testing.T) {
	id := "../../config?x=#"
	var rawPath, rawQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rawPath, rawQuery = r.URL.EscapedPath(), r.URL.RawQuery
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	if _, err := NewClient(server.URL).GetSession(context.Background(), id); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := "/session/" + url.PathEscape(id); rawPath != want {
		t.Errorf("path = %q, want %q", rawPath, want)
	}
	if rawQuery != "" {
		t.Errorf("query = %q, want empty", rawQuery)
	}
}

func TestClient_RejectsDotSessionIDs(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
	}))
	defer server.Close()
	c := NewClient(server.URL)
	ctx := context.Background()

	for _, id := range []string{"", ".", ".."} {
		calls := map[string]error{
			"GetSession":      func() error { _, err := c.GetSession(ctx, id); return err }(),
			"GetMessages":     func() error { _, err := c.GetMessages(ctx, id, 10); return err }(),
			"GetTodos":        func() error { _, err := c.GetTodos(ctx, id); return err }(),
			"SendPrompt":      func() error { _, err := c.SendPrompt(ctx, id, PromptRequest{}); return err }(),
			"SendPromptAsync": c.SendPromptAsync(ctx, id, PromptRequest{}),
			"AbortSession":    c.AbortSession(ctx, id),
			"DeleteSession":   c.DeleteSession(ctx, id),
		}
		for name, err := range calls {
			if !errors.Is(err, ErrInvalidSessionID) {
				t.Errorf("%s(%q) err = %v, want ErrInvalidSessionID", name, id, err)
			}
		}
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("server received %d requests, want 0", n)
	}
}
