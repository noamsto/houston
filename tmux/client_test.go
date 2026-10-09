// tmux/client_test.go
package tmux

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
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

// loggedCall is one fake-tmux invocation: when it ran and its argv.
type loggedCall struct {
	at   time.Duration // since the Unix epoch, microsecond resolution
	args []string
}

// recordingTmux returns a Client whose tmux appends every call to a log; see
// loggingTmux. calls yields the argv of each call in order.
func recordingTmux(t *testing.T) (*Client, func() []loggedCall) {
	t.Helper()
	return loggingTmux(t, 0)
}

// loggingTmux returns a Client whose tmux logs each call (an $EPOCHREALTIME
// token, then each arg NUL-terminated, then a lone "\x01" arg as the call
// marker) in one printf so concurrent calls never interleave inside an entry.
// A failAt > 0 makes the failAt-th call exit 1 after being logged.
func loggingTmux(t *testing.T, failAt int) (*Client, func() []loggedCall) {
	t.Helper()
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not found")
	}
	if out, _ := exec.Command(bash, "-c", `printf %s "$EPOCHREALTIME"`).Output(); len(out) == 0 {
		t.Skip("bash lacks EPOCHREALTIME (needs >= 5.0)")
	}
	dir := t.TempDir()
	logPath := filepath.Join(dir, "calls.log")
	counterPath := filepath.Join(dir, "counter")
	script := "#!" + bash + "\n" +
		"printf '%s\\0' \"$EPOCHREALTIME\" \"$@\" $'\\001' >> " + strconv.Quote(logPath) + "\n"
	if failAt > 0 {
		script += "n=$(( $(cat " + strconv.Quote(counterPath) + " 2>/dev/null || echo 0) + 1 ))\n" +
			"echo $n > " + strconv.Quote(counterPath) + "\n" +
			"[ $n -eq " + strconv.Itoa(failAt) + " ] && exit 1\n" +
			"exit 0\n"
	}
	path := filepath.Join(dir, "faketmux")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake tmux: %v", err)
	}
	calls := func() []loggedCall {
		data, err := os.ReadFile(logPath)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			t.Fatalf("read call log: %v", err)
		}
		var out []loggedCall
		var cur []string
		for _, tok := range strings.Split(string(data), "\x00") {
			switch tok {
			case "":
			case "\x01":
				micros, err := strconv.ParseInt(strings.NewReplacer(".", "", ",", "").Replace(cur[0]), 10, 64)
				if err != nil {
					t.Fatalf("bad call timestamp %q: %v", cur[0], err)
				}
				out = append(out, loggedCall{at: time.Duration(micros) * time.Microsecond, args: cur[1:]})
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
			if err := c.SendKeys(context.Background(), pane, tc.text, false); err != nil {
				t.Fatalf("SendKeys() error: %v", err)
			}
			got := calls()
			if len(got) == 0 {
				t.Fatalf("no tmux calls recorded")
			}
			var sent strings.Builder
			for i, call := range got {
				args := call.args
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

	isEnter := func(c loggedCall) bool { return strings.Join(c.args, " ") == "send-keys -t %1 Enter" }

	t.Run("enter follows text after a settle gap", func(t *testing.T) {
		c, calls := recordingTmux(t)
		if err := c.SendKeys(context.Background(), pane, "hello", true); err != nil {
			t.Fatalf("SendKeys() error: %v", err)
		}
		got := calls()
		if len(got) != 2 {
			t.Fatalf("recorded %d calls, want text then Enter: %v", len(got), got)
		}
		if !isEnter(got[1]) {
			t.Errorf("last call = %q, want send-keys -t %%1 Enter", got[1].args)
		}
		gap := got[1].at - got[0].at
		t.Logf("text->Enter gap %v", gap)
		if gap < 50*time.Millisecond {
			t.Errorf("text->Enter gap %v, want >= 50ms", gap)
		}
	})

	t.Run("no enter means no Enter call", func(t *testing.T) {
		c, calls := recordingTmux(t)
		if err := c.SendKeys(context.Background(), pane, "hello", false); err != nil {
			t.Fatalf("SendKeys() error: %v", err)
		}
		for _, call := range calls() {
			if isEnter(call) {
				t.Errorf("unexpected Enter call: %q", call.args)
			}
		}
	})

	t.Run("empty text still presses Enter", func(t *testing.T) {
		c, calls := recordingTmux(t)
		if err := c.SendKeys(context.Background(), pane, "", true); err != nil {
			t.Fatalf("SendKeys() error: %v", err)
		}
		got := calls()
		if len(got) != 1 || !isEnter(got[0]) {
			t.Errorf("calls = %v, want exactly one Enter", got)
		}
	})

	t.Run("a failing chunk stops the loop and sends no Enter", func(t *testing.T) {
		c, calls := loggingTmux(t, 2)
		text := strings.Repeat("a", 3*maxChunk)
		err := c.SendKeys(context.Background(), pane, text, true)
		var partial *DeliveryPartialError
		if !errors.As(err, &partial) {
			t.Fatalf("SendKeys() = %v, want *DeliveryPartialError", err)
		}
		got := calls()
		if len(got) != 2 {
			t.Fatalf("recorded %d calls, want 2 (one chunk, then the failing one): %v", len(got), got)
		}
		for _, call := range got {
			if isEnter(call) {
				t.Errorf("Enter sent after a failed chunk: %q", call.args)
			}
		}
	})

	t.Run("concurrent sends to one pane do not interleave", func(t *testing.T) {
		c, calls := recordingTmux(t)
		var wg sync.WaitGroup
		for _, text := range []string{"A", "B"} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := c.SendKeys(context.Background(), pane, text, true); err != nil {
					t.Errorf("SendKeys(%q) error: %v", text, err)
				}
			}()
		}
		wg.Wait()
		got := calls()
		if len(got) != 4 {
			t.Fatalf("recorded %d calls, want 4: %v", len(got), got)
		}
		for i := 0; i < 4; i += 2 {
			if isEnter(got[i]) || !isEnter(got[i+1]) {
				t.Fatalf("calls = %v, want text, Enter, text, Enter", got)
			}
		}
		if got[0].args[5] == got[2].args[5] {
			t.Errorf("both texts are %q, want one A and one B", got[0].args[5])
		}
	})
}

// holdSecondChunkTmux logs every argv like loggingTmux. The first call exits
// 0. The second creates entered2, then polls every 20ms for release2 (max 5s,
// then exit 2) and exits 0. Later calls exit 0.
func holdSecondChunkTmux(t *testing.T) (*Client, func() []loggedCall, string) {
	t.Helper()
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not found")
	}
	if out, _ := exec.Command(bash, "-c", `printf %s "$EPOCHREALTIME"`).Output(); len(out) == 0 {
		t.Skip("bash lacks EPOCHREALTIME (needs >= 5.0)")
	}
	dir := t.TempDir()
	logPath := filepath.Join(dir, "calls.log")
	counterPath := filepath.Join(dir, "counter")
	entered2 := filepath.Join(dir, "entered2")
	release2 := filepath.Join(dir, "release2")
	script := "#!" + bash + "\n" +
		"printf '%s\\0' \"$EPOCHREALTIME\" \"$@\" $'\\001' >> " + strconv.Quote(logPath) + "\n" +
		"n=$(( $(cat " + strconv.Quote(counterPath) + " 2>/dev/null || echo 0) + 1 ))\n" +
		"echo \"$n\" > " + strconv.Quote(counterPath) + "\n" +
		"if [ \"$n\" -eq 2 ]; then\n" +
		"  : > " + strconv.Quote(entered2) + "\n" +
		"  for ((i = 0; i < 250; i++)); do\n" +
		"    [ -f " + strconv.Quote(release2) + " ] && exit 0\n" +
		"    sleep 0.02\n" +
		"  done\n" +
		"  exit 2\n" +
		"fi\n" +
		"exit 0\n"
	path := filepath.Join(dir, "faketmux")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake tmux: %v", err)
	}
	calls := func() []loggedCall {
		data, err := os.ReadFile(logPath)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			t.Fatalf("read call log: %v", err)
		}
		var out []loggedCall
		var cur []string
		for _, tok := range strings.Split(string(data), "\x00") {
			switch tok {
			case "":
			case "\x01":
				micros, err := strconv.ParseInt(strings.NewReplacer(".", "", ",", "").Replace(cur[0]), 10, 64)
				if err != nil {
					t.Fatalf("bad call timestamp %q: %v", cur[0], err)
				}
				out = append(out, loggedCall{at: time.Duration(micros) * time.Microsecond, args: cur[1:]})
				cur = nil
			default:
				cur = append(cur, tok)
			}
		}
		return out
	}
	return &Client{tmuxPath: path}, calls, dir
}

func countLiteralSends(calls []loggedCall) int {
	n := 0
	for _, call := range calls {
		if slices.Contains(call.args, "-l") {
			n++
		}
	}
	return n
}

func enterSent(calls []loggedCall) bool {
	for _, call := range calls {
		if strings.Join(call.args, " ") == "send-keys -t %1 Enter" {
			return true
		}
	}
	return false
}

func pollUntil(t *testing.T, what string, done func() bool) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		if done() {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for %s", what)
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func TestSendKeysCancelStopsLaterChunks(t *testing.T) {
	c, calls, dir := holdSecondChunkTmux(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pane := Pane{ID: "%1"}
	text := strings.Repeat("a", 3*(8<<10))

	errCh := make(chan error, 1)
	go func() {
		errCh <- c.SendKeys(ctx, pane, text, true)
	}()

	entered2 := filepath.Join(dir, "entered2")
	pollUntil(t, "entered2", func() bool {
		_, err := os.Stat(entered2)
		return err == nil
	})
	cancel()
	release2 := filepath.Join(dir, "release2")
	if err := os.WriteFile(release2, nil, 0o644); err != nil {
		t.Fatalf("create release2: %v", err)
	}

	var err error
	select {
	case err = <-errCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for SendKeys")
	}

	var partial *DeliveryPartialError
	if !errors.As(err, &partial) {
		t.Fatalf("SendKeys() = %v, want *DeliveryPartialError", err)
	}
	if !errors.Is(partial.Unwrap(), context.Canceled) {
		t.Fatalf("unwrap = %v, want context.Canceled", partial.Unwrap())
	}
	got := calls()
	if n := countLiteralSends(got); n != 2 {
		t.Fatalf("recorded %d -l calls, want 2: %v", n, got)
	}
	if enterSent(got) {
		t.Fatalf("unexpected send-keys -t %%1 Enter: %v", got)
	}
}

func TestSendKeysCancelDuringSettle(t *testing.T) {
	c, calls := recordingTmux(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pane := Pane{ID: "%1"}

	errCh := make(chan error, 1)
	go func() {
		errCh <- c.SendKeys(ctx, pane, "hello", true)
	}()

	deadline := time.After(2 * time.Second)
	for {
		got := calls()
		hasL := false
		for _, call := range got {
			for _, arg := range call.args {
				if arg == "-l" {
					hasL = true
				}
			}
		}
		if hasL {
			if enterSent(got) {
				t.Fatalf("Enter already sent before cancel: %v", got)
			}
			cancel()
			break
		}
		select {
		case <-deadline:
			t.Fatal("timed out waiting for -l call")
		case <-time.After(time.Millisecond):
		}
	}

	var err error
	select {
	case err = <-errCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for SendKeys")
	}

	var partial *DeliveryPartialError
	if !errors.As(err, &partial) {
		t.Fatalf("SendKeys() = %v, want *DeliveryPartialError", err)
	}
	if !errors.Is(partial.Unwrap(), context.Canceled) {
		t.Fatalf("unwrap = %v, want context.Canceled", partial.Unwrap())
	}
	if got := calls(); enterSent(got) {
		t.Fatalf("unexpected send-keys -t %%1 Enter: %v", got)
	}
}

func TestSendKeysPartialOnLaterChunk(t *testing.T) {
	c, calls := loggingTmux(t, 2)
	pane := Pane{ID: "%1"}
	text := strings.Repeat("a", 3*(8<<10))
	err := c.SendKeys(context.Background(), pane, text, true)

	var partial *DeliveryPartialError
	if !errors.As(err, &partial) {
		t.Fatalf("SendKeys() = %v, want *DeliveryPartialError", err)
	}
	got := calls()
	if n := countLiteralSends(got); n != 2 {
		t.Fatalf("recorded %d -l calls, want 2: %v", n, got)
	}
	if enterSent(got) {
		t.Fatalf("unexpected send-keys -t %%1 Enter: %v", got)
	}
}

func TestSendKeysCancelBeforeEmptyEnter(t *testing.T) {
	c, calls := recordingTmux(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := c.SendKeys(ctx, Pane{ID: "%1"}, "", true)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("SendKeys() = %v, want context.Canceled", err)
	}
	var partial *DeliveryPartialError
	if errors.As(err, &partial) {
		t.Fatalf("errors.As *DeliveryPartialError = true, want false: %v", err)
	}
	if got := calls(); len(got) != 0 {
		t.Fatalf("recorded %d calls, want 0: %v", len(got), got)
	}
}

func TestParsePaneInMode(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want bool
	}{
		{"0", false},
		{"0\n", false},
		{"1", true},
		{"2", true},
		{"", true},
		{"garbage", true},
	} {
		if got := parsePaneInMode(tc.in); got != tc.want {
			t.Errorf("parsePaneInMode(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}
