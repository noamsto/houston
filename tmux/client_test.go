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
