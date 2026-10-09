package tmux

import (
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestMatchSGRMouse(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"\x1b[<64;12;3M", 11},
		{"\x1b[<65;1;1M\x1b[<65;1;1M", 10},
		{"\x1b[<0;5;5m", 9},
		{"\x1b[<64;12;3", 0},
		{"\x1b[<64;12M", 0},
		{"\x1b[<;1;1M", 0},
		{"\x1b[A", 0},
		{"\x1b[<64;1;1;1M", 0},
	}
	for _, c := range cases {
		if got := matchSGRMouse(c.in); got != c.want {
			t.Errorf("matchSGRMouse(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestSplitKeysKeepsMouseReportsWhole(t *testing.T) {
	got := splitKeys("\x1b[<65;3;4M\x1b[<65;3;4Mx")
	want := []keysSegment{{raw: "\x1b[<65;3;4M"}, {raw: "\x1b[<65;3;4M"}, {literal: "x"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("splitKeys = %#v, want %#v", got, want)
	}
}

// TestSendKeysDeliversMouseReportUnchanged proves, against a real tmux, that a
// wheel report reaches the pane's program as the exact bytes sent.
func TestSendKeysDeliversMouseReportUnchanged(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	client := NewClient()
	session := "houston-test-mouse-" + strconv.Itoa(os.Getpid())
	if err := client.run("new-session", "-d", "-s", session, "-x", "80", "-y", "24", "cat -v"); err != nil {
		t.Fatalf("new-session: %v", err)
	}
	defer func() { _ = client.run("kill-session", "-t", session) }()
	out, err := client.output("display-message", "-t", session, "-p", "#{pane_id}")
	if err != nil {
		t.Fatalf("pane id: %v", err)
	}
	paneID := strings.TrimSpace(string(out))

	cc := NewControlClient(session)
	if err := cc.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() { _ = cc.Close() }()

	if err := cc.SendKeys(cc.Generation(), paneID, "\x1b[<65;12;3M\x1b[<64;12;3M"); err != nil {
		t.Fatalf("SendKeys: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		shown, _ := client.CapturePane(Pane{Session: session}, 24)
		if strings.Contains(shown, "^[[<65;12;3M^[[<64;12;3M") {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("mouse reports did not reach the pane intact")
}

// TestCapturePaneWithModeReportsTerminalMode checks, against a real tmux, the
// alternate-screen and SGR-mouse flags a TUI like pi turns on.
func TestCapturePaneWithModeReportsTerminalMode(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	client := NewClient()
	session := "houston-test-mode-" + strconv.Itoa(os.Getpid())
	script := `printf '\033[?1049h\033[?1000h\033[?1006h'; sleep 30`
	if err := client.run("new-session", "-d", "-s", session, "-x", "80", "-y", "24", "sh", "-c", script); err != nil {
		t.Fatalf("new-session: %v", err)
	}
	defer func() { _ = client.run("kill-session", "-t", session) }()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		res, err := client.CapturePaneWithMode(Pane{Session: session}, 24)
		if err != nil {
			t.Fatalf("capture: %v", err)
		}
		if res.AltScreen && res.MouseSGR {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("alternate screen and SGR mouse never reported")
}
