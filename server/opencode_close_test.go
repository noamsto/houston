package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/noamsto/houston/opencode"
)

// TestClose_JoinsOpenCodeScanGoroutine proves Close waits for the OpenCode
// background scan goroutine to actually exit before returning, rather than
// merely cancelling its context and moving on. The gate closes only from a
// test-owned timer, so this can't pass by coincidence.
func TestClose_JoinsOpenCodeScanGoroutine(t *testing.T) {
	s, err := New(Config{StatusDir: t.TempDir()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	var joined atomic.Bool
	gate := make(chan struct{})
	s.ocScanDone = gate
	closedCh := make(chan struct{})
	close(closedCh)
	s.ocRefreshDone = closedCh

	go func() {
		time.Sleep(200 * time.Millisecond)
		joined.Store(true)
		close(gate)
	}()

	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if !joined.Load() {
		t.Fatal("Close returned before the OpenCode scan goroutine's done channel closed")
	}
}

// TestClose_ClosesOpenCodeManager proves Server.Close invokes ocManager.Close.
// SubscribeToServer/Close have no production caller today, so this is a
// white-box proof of the wiring, not a production-path regression test.
func TestClose_ClosesOpenCodeManager(t *testing.T) {
	events := make(chan opencode.Event, 4)
	sseSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher.Flush()
		for {
			select {
			case ev := <-events:
				data, _ := json.Marshal(ev)
				fmt.Fprintf(w, "data: %s\n\n", data)
				flusher.Flush()
			case <-r.Context().Done():
				return
			}
		}
	}))
	t.Cleanup(sseSrv.Close)

	// subCancel must run before sseSrv.Close (t.Cleanup is LIFO), or the SSE
	// reader in opencode/client.go blocks forever and sseSrv.Close hangs.
	subCtx, subCancel := context.WithCancel(context.Background())
	t.Cleanup(subCancel)

	s, err := New(Config{
		StatusDir:       t.TempDir(),
		OpenCodeEnabled: true,
		OpenCodeURL:     "http://127.0.0.1:1", // unreachable; the periodic loops don't matter here
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	var mu sync.Mutex
	var received int
	err = s.ocManager.SubscribeToServer(subCtx, sseSrv.URL, func(opencode.Event) {
		mu.Lock()
		received++
		mu.Unlock()
	})
	if err != nil {
		t.Fatalf("SubscribeToServer: %v", err)
	}

	events <- opencode.Event{Type: "test.one"}
	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		n := received
		mu.Unlock()
		if n == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("handler never received the first event")
		}
		time.Sleep(10 * time.Millisecond)
	}

	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	events <- opencode.Event{Type: "test.two"}
	time.Sleep(200 * time.Millisecond)

	mu.Lock()
	n := received
	mu.Unlock()
	if n != 1 {
		t.Fatalf("handler received %d events after Close, want 1: ocManager.Close did not stop delivery", n)
	}
}
