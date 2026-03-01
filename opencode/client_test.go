package opencode

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
		t.Errorf("expected 2 sessions, got %d", len(sessions))
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
