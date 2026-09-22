package opencode

import (
	"context"
	"testing"
	"time"
)

// TestStartBackgroundScanClosesDoneOnCancel proves StartBackgroundScan's
// returned channel tracks the goroutine's actual exit rather than closing
// eagerly, so a caller can safely join it.
func TestStartBackgroundScanClosesDoneOnCancel(t *testing.T) {
	d := NewDiscovery(WithStaticURL("http://127.0.0.1:1"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := d.StartBackgroundScan(ctx, time.Hour)

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
