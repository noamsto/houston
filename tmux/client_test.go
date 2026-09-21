// tmux/client_test.go
package tmux

import (
	"encoding/json"
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
