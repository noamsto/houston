// server/server_test.go
package server

import (
	"testing"

	"github.com/noamsto/houston/tmux"
)

// TestParsePaneTarget_DoubleDecode guards the double-decode addressing
// scheme: net/http decodes r.URL.Path once before parsePaneTarget sees it,
// and parsePaneTarget decodes again via url.PathUnescape (after stripping
// the "/pane/" prefix and "/ws"-style suffix itself). A client that wants
// the final target to be the literal tmux pane id "%307" must send
// "%2525307" on the wire, which arrives here (post net/http decode) as
// "/pane/%25307/ws". The second case shows what happens if a client instead
// sends the single-encoded form "%307": parsePaneTarget's own decode mangles
// it down to "07" ("%30" decodes to '0', leaving the literal "7").
func TestParsePaneTarget_DoubleDecode(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		want       tmux.Pane
		wantTarget string
	}{
		{"double-encoded wire form round-trips", "/pane/%25307/ws", tmux.Pane{Session: "%307", Window: 0, Index: 0}, "%307"},
		{"single-encoded wire form is mangled", "/pane/%307/ws", tmux.Pane{Session: "07", Window: 0, Index: 0}, "07"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pane, err := parsePaneTarget(tt.path)
			if err != nil {
				t.Fatalf("parsePaneTarget(%q) returned error: %v", tt.path, err)
			}
			if pane != tt.want {
				t.Errorf("parsePaneTarget(%q) = %+v, want %+v", tt.path, pane, tt.want)
			}
			if got := pane.Target(); got != tt.wantTarget {
				t.Errorf("pane.Target() = %q, want %q", got, tt.wantTarget)
			}
		})
	}
}
