package opencode

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// TestManagerCloseAbortsServerSubscription proves Manager.Close aborts the
// in-flight SSE request opened by SubscribeToServer, so the client reader and
// the manager forwarding goroutine stop rather than lingering on a live
// connection gated only by a derived context.
func TestManagerCloseAbortsServerSubscription(t *testing.T) {
	events := make(chan Event, 4)
	requestDone := make(chan struct{})

	sseSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher.Flush()
		defer close(requestDone)
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
	// LIFO: subCancel (registered next) runs before sseSrv.Close, so an unfixed
	// run's still-open reader cannot hang sseSrv.Close.
	t.Cleanup(sseSrv.Close)
	subCtx, subCancel := context.WithCancel(context.Background())
	t.Cleanup(subCancel)

	m := NewManager(NewDiscovery(WithStaticURL("http://127.0.0.1:1")))

	var mu sync.Mutex
	var received int
	err := m.SubscribeToServer(subCtx, sseSrv.URL, func(Event) {
		mu.Lock()
		received++
		mu.Unlock()
	})
	if err != nil {
		t.Fatalf("SubscribeToServer: %v", err)
	}

	events <- Event{Type: "test.one"}
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

	m.Close()

	select {
	case <-requestDone:
	case <-time.After(2 * time.Second):
		t.Fatal("SSE request still open 2s after Manager.Close: reader goroutine was not aborted")
	}

	events <- Event{Type: "test.two"}
	time.Sleep(200 * time.Millisecond)

	mu.Lock()
	n := received
	mu.Unlock()
	if n != 1 {
		t.Fatalf("handler received %d events after Close, want 1: forwarding did not stop", n)
	}
}
