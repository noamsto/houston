package opencode

import (
	"context"
	"testing"
	"time"
)

// TestStartBackgroundRefreshClosesDoneOnCancel proves StartBackgroundRefresh's
// returned channel tracks the goroutine's actual exit rather than closing
// eagerly, so a caller can safely join it.
func TestStartBackgroundRefreshClosesDoneOnCancel(t *testing.T) {
	m := NewManager(NewDiscovery(WithStaticURL("http://127.0.0.1:1")))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := m.StartBackgroundRefresh(ctx, time.Hour)

	select {
	case <-done:
		t.Fatal("done closed before ctx was cancelled")
	case <-time.After(50 * time.Millisecond):
	}

	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("done not closed within 2s of cancellation")
	}
}
