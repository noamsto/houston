package server

import (
	"bytes"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/noamsto/houston/tmux"
)

// serverLogCapture collects slog output. The buffer carries its own lock:
// servePane's read and write loops log from their own goroutines.
type serverLogCapture struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *serverLogCapture) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *serverLogCapture) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

// captureServerLogs routes slog to a buffer for the rest of the test.
// slog.SetDefault is process-global, so a test using it must not call
// t.Parallel(). Debug is enabled so a quiet path can be proven to have run.
func captureServerLogs(t *testing.T) *serverLogCapture {
	t.Helper()
	lc := &serverLogCapture{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(lc, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return lc
}

// TestPaneWSPartialInputLogsWarn covers #156's read-loop half: when SendKeys
// reports a partially written paste, the read loop must warn with the segment
// counts rather than quietly debug-log it like a full drop.
func TestPaneWSPartialInputLogsWarn(t *testing.T) {
	fakeTmux := newFakeTmux()
	fakeTmux.paneID = "%1"

	fakeCC := newFakeControlClient()
	cm := newFakeControlManager(fakeCC)

	conn, cleanup := startPaneWS(t, fakeTmux, cm)
	defer cleanup()

	readUntilType(t, conn, "seed", 5*time.Second)

	fakeCC.setSendErr(&tmux.PartialSendError{Sent: 1, Total: 3})

	logs := captureServerLogs(t)

	writeInput(t, conn, "a\nb")

	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(logs.String(), "partial input delivery pending tmux server check") {
		if time.Now().After(deadline) {
			t.Fatalf("no partial-delivery warning logged; got %q", logs.String())
		}
		time.Sleep(time.Millisecond)
	}

	got := logs.String()
	if !strings.Contains(got, "level=WARN") {
		t.Fatalf("partial delivery was not logged at WARN: %q", got)
	}
	if !strings.Contains(got, "sent=1") || !strings.Contains(got, "total=3") {
		t.Fatalf("warning did not carry the segment counts: %q", got)
	}
}
