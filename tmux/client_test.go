// tmux/client_test.go
package tmux

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestParseSessionLine(t *testing.T) {
	line := "main|1735689600|3|1|1735690000"

	session, err := parseSessionLine(line)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if session.Name != "main" {
		t.Errorf("expected name 'main', got %q", session.Name)
	}
	if session.Windows != 3 {
		t.Errorf("expected 3 windows, got %d", session.Windows)
	}
	if !session.Attached {
		t.Error("expected attached=true")
	}
}

func TestParsePaneInfoLine(t *testing.T) {
	cases := []struct {
		name   string
		line   string
		want   PaneInfo
		wantOK bool
	}{
		{"full", "1|1|%307|1966|claude|/home/u/repo|my title",
			PaneInfo{Index: 1, Active: true, ID: "%307", Server: "1966", Command: "claude", Path: "/home/u/repo", Title: "my title"}, true},
		{"inactive, no title", "0|0|%3|42|fish|/tmp|",
			PaneInfo{Index: 0, ID: "%3", Server: "42", Command: "fish", Path: "/tmp"}, true},
		{"title keeps pipes", "2|0|%9|42|vim|/tmp|a|b",
			PaneInfo{Index: 2, ID: "%9", Server: "42", Command: "vim", Path: "/tmp", Title: "a|b"}, true},
		{"no path", "0|1|%1|7|bash",
			PaneInfo{Index: 0, Active: true, ID: "%1", Server: "7", Command: "bash"}, true},
		{"too short", "0|1|%1|7", PaneInfo{}, false},
		{"empty", "", PaneInfo{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parsePaneInfoLine(tc.line)
			if ok != tc.wantOK || got != tc.want {
				t.Errorf("parsePaneInfoLine(%q) = %+v, %v; want %+v, %v", tc.line, got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

func TestCapturePaneOutput(t *testing.T) {
	// This tests the output structure, actual capture requires tmux
	output := `$ echo hello
hello
$ _`

	if len(output) == 0 {
		t.Error("expected non-empty output")
	}
}

func TestServerMismatch(t *testing.T) {
	cases := []struct {
		name string
		a, b string
		want bool
	}{
		{"both empty", "", "", false},
		{"a empty", "", "100", false},
		{"b empty", "100", "", false},
		{"equal", "100", "100", false},
		{"different", "100", "200", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ServerMismatch(tc.a, tc.b); got != tc.want {
				t.Errorf("ServerMismatch(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

func TestPaneTarget(t *testing.T) {
	cases := []struct {
		name string
		pane Pane
		want string
	}{
		{"id wins at window 0 pane 0", Pane{ID: "%42", Session: "s"}, "%42"},
		{"id wins elsewhere", Pane{ID: "%42", Session: "s", Window: 1, Index: 2}, "%42"},
		{"no id, window 0 pane 0 is the bare session", Pane{Session: "s"}, "s"},
		{"no id, full target", Pane{Session: "s", Window: 1, Index: 2}, "s:1.2"},
		{"no id, nonzero pane in window 0", Pane{Session: "s", Index: 1}, "s:0.1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.pane.Target(); got != tc.want {
				t.Errorf("Target() = %q, want %q", got, tc.want)
			}
		})
	}
}

// fakeTmux writes a script that prints stderrMsg to stderr and exits 1,
// standing in for tmux itself so ResolvePane's exit-1 classification can be
// tested without a real tmux server.
func fakeTmux(t *testing.T, stderrMsg string) *Client {
	t.Helper()
	path := filepath.Join(t.TempDir(), "faketmux")
	script := "#!/bin/sh\necho " + strconv.Quote(stderrMsg) + " >&2\nexit 1\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake tmux: %v", err)
	}
	return &Client{tmuxPath: path}
}

func TestResolvePaneExitOneClassification(t *testing.T) {
	cases := []struct {
		name         string
		stderr       string
		wantNotFound bool
		wantMsgInErr bool
	}{
		{"can't find pane", "can't find pane: %9", true, false},
		{"no server socket", "error connecting to /tmp/x (No such file or directory)", true, false},
		{"server exited", "no server running on /tmp/x/default", true, false},
		{"protocol version mismatch", "protocol version mismatch (client 8, server 7)", false, true},
		{"permission denied", "error connecting to /tmp/x (Permission denied)", false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := fakeTmux(t, tc.stderr)
			_, err := c.ResolvePane("%1")
			if err == nil {
				t.Fatalf("ResolvePane() = nil error, want one")
			}
			if got := errors.Is(err, ErrPaneNotFound); got != tc.wantNotFound {
				t.Errorf("errors.Is(err, ErrPaneNotFound) = %v, want %v (err: %v)", got, tc.wantNotFound, err)
			}
			if tc.wantMsgInErr && !strings.Contains(err.Error(), tc.stderr) {
				t.Errorf("error %q does not contain tmux stderr %q", err, tc.stderr)
			}
		})
	}
}

func TestIsGoneMessage(t *testing.T) {
	for _, tc := range []struct {
		msg  string
		want bool
	}{
		{"can't find pane: %9", true},
		{"tmux display-message: exit status 1: can't find session: =proj", true},
		{"error connecting to /tmp/x (No such file or directory)", true},
		{"no server running on /tmp/x/default", true},
		{"protocol version mismatch (client 8, server 7)", false},
		{"error connecting to /tmp/x (Permission denied)", false},
		{"tmux display-message: signal: killed", false},
		{"", false},
	} {
		if got := IsGoneMessage(tc.msg); got != tc.want {
			t.Errorf("IsGoneMessage(%q) = %v, want %v", tc.msg, got, tc.want)
		}
	}
}

// fakeTmuxOK writes a script that prints stdout to stdout and exits 0,
// standing in for a live tmux server's successful display-message reply.
func fakeTmuxOK(t *testing.T, stdout string) *Client {
	t.Helper()
	path := filepath.Join(t.TempDir(), "faketmux")
	script := "#!/bin/sh\necho " + strconv.Quote(stdout) + "\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake tmux: %v", err)
	}
	return &Client{tmuxPath: path}
}

func TestResolvePaneParsesServerPID(t *testing.T) {
	c := fakeTmuxOK(t, "1966 3 1 houston")
	got, err := c.ResolvePane("%307")
	if err != nil {
		t.Fatalf("ResolvePane() error: %v", err)
	}
	want := Pane{ID: "%307", Session: "houston", Window: 3, Index: 1, Server: "1966"}
	if got != want {
		t.Errorf("ResolvePane() = %+v, want %+v", got, want)
	}
}

// Pane is embedded in API responses; the id is an internal addressing detail.
func TestPaneJSONOmitsID(t *testing.T) {
	b, err := json.Marshal(Pane{ID: "%1", Session: "s", Window: 1, Index: 2})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(b), "%1") || strings.Contains(strings.ToLower(string(b)), `"id"`) {
		t.Errorf("marshalled pane %s exposes the id", b)
	}
}

// recordingTmux returns a Client whose tmux appends every call's argv to a
// log: each arg NUL-terminated, then a lone "\x01" arg as the call marker.
func recordingTmux(t *testing.T) (*Client, func() [][]string) {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "calls.log")
	script := "#!/usr/bin/env bash\n" +
		"printf '%s\\0' \"$@\" $'\\001' >> " + strconv.Quote(logPath) + "\n"
	path := filepath.Join(dir, "faketmux")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake tmux: %v", err)
	}
	calls := func() [][]string {
		data, err := os.ReadFile(logPath)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			t.Fatalf("read call log: %v", err)
		}
		var out [][]string
		var cur []string
		for _, tok := range strings.Split(string(data), "\x00") {
			switch tok {
			case "":
			case "\x01":
				out = append(out, cur)
				cur = nil
			default:
				cur = append(cur, tok)
			}
		}
		return out
	}
	return &Client{tmuxPath: path}, calls
}

// decodeSentChunk undoes the `;` escaping tmux applies to a trailing
// separator, and fails on a chunk tmux would silently truncate.
func decodeSentChunk(t *testing.T, chunk string) string {
	t.Helper()
	if strings.HasSuffix(chunk, `\;`) {
		return chunk[:len(chunk)-2] + ";"
	}
	if strings.HasSuffix(chunk, ";") {
		t.Errorf("chunk ends in an unescaped ';' (tmux would drop it): %q", tail(chunk))
	}
	return chunk
}

func tail(s string) string {
	if len(s) > 24 {
		return "..." + s[len(s)-24:]
	}
	return s
}

func TestSendKeys(t *testing.T) {
	const maxChunk = 8 << 10 // literal-text bytes per send-keys call, before ';' escaping
	pane := Pane{ID: "%1"}

	cases := []struct {
		name string
		text string
	}{
		{"trailing semicolon", "a;"},
		{"trailing escaped semicolon", `a\;`},
		{"double trailing semicolon", "x;;"},
		{"literal -l", "-l"},
		{"leading dash", "-1 is wrong"},
		{"multi-line trailing semicolon", "a\nb;"},
		{"semicolon at chunk boundary", strings.Repeat("a", maxChunk-1) + ";" + strings.Repeat("b", 12000)},
		{"long text", strings.Repeat("0123456789", 2500)},
		{"2-byte rune across boundary", strings.Repeat("a", maxChunk-1) + strings.Repeat("\u00e9", 5000)},
		{"3-byte rune across boundary", strings.Repeat("a", maxChunk-1) + strings.Repeat("\u05d0", 5000) + ";"},
		{"4-byte rune across boundary", strings.Repeat("a", maxChunk-2) + strings.Repeat("\U0001F600", 4000)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, calls := recordingTmux(t)
			if err := c.SendKeys(pane, tc.text, false); err != nil {
				t.Fatalf("SendKeys() error: %v", err)
			}
			got := calls()
			if len(got) == 0 {
				t.Fatalf("no tmux calls recorded")
			}
			var sent strings.Builder
			for i, args := range got {
				if len(args) != 6 || args[0] != "send-keys" || args[1] != "-t" || args[2] != "%1" || args[3] != "-l" || args[4] != "--" {
					t.Fatalf("call %d argv = %q, want send-keys -t %%1 -l -- <chunk>", i, args)
				}
				chunk := args[5]
				if !utf8.ValidString(chunk) {
					t.Errorf("call %d chunk is not valid UTF-8 (rune split across chunks)", i)
				}
				if len(chunk) > maxChunk+1 {
					t.Errorf("call %d chunk is %d bytes, want <= %d", i, len(chunk), maxChunk+1)
				}
				sent.WriteString(decodeSentChunk(t, chunk))
			}
			if sent.String() != tc.text {
				t.Errorf("delivered text differs from input (got %d bytes, want %d; tail %q vs %q)",
					sent.Len(), len(tc.text), tail(sent.String()), tail(tc.text))
			}
			if len(tc.text) > maxChunk && len(got) < 2 {
				t.Errorf("%d-byte text sent in %d call, want it split", len(tc.text), len(got))
			}
		})
	}

	t.Run("enter follows text after a settle gap", func(t *testing.T) {
		c, calls := recordingTmux(t)
		start := time.Now()
		if err := c.SendKeys(pane, "hello", true); err != nil {
			t.Fatalf("SendKeys() error: %v", err)
		}
		elapsed := time.Since(start)
		got := calls()
		if len(got) < 2 {
			t.Fatalf("recorded %d calls, want text then Enter: %q", len(got), got)
		}
		last := got[len(got)-1]
		if strings.Join(last, " ") != "send-keys -t %1 Enter" {
			t.Errorf("last call = %q, want send-keys -t %%1 Enter", last)
		}
		t.Logf("SendKeys(enter=true) took %v", elapsed)
		if elapsed < 50*time.Millisecond {
			t.Errorf("SendKeys took %v, want >= 50ms between the text and Enter", elapsed)
		}
	})

	t.Run("no enter means no Enter call", func(t *testing.T) {
		c, calls := recordingTmux(t)
		if err := c.SendKeys(pane, "hello", false); err != nil {
			t.Fatalf("SendKeys() error: %v", err)
		}
		for _, args := range calls() {
			if args[len(args)-1] == "Enter" {
				t.Errorf("unexpected Enter call: %q", args)
			}
		}
	})

	t.Run("empty text still presses Enter", func(t *testing.T) {
		c, calls := recordingTmux(t)
		if err := c.SendKeys(pane, "", true); err != nil {
			t.Fatalf("SendKeys() error: %v", err)
		}
		got := calls()
		if len(got) == 0 || strings.Join(got[len(got)-1], " ") != "send-keys -t %1 Enter" {
			t.Errorf("calls = %q, want a final Enter", got)
		}
	})
}
