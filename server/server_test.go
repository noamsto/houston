// server/server_test.go
package server

import (
	"testing"

	"github.com/noamsto/houston/tmux"
)

// TestParsePaneTarget_DoubleDecode guards the double-decode addressing
// scheme: net/http decodes r.URL.Path once before parsePaneTarget sees it,
// and parsePaneTarget decodes again via url.PathUnescape. A client that
// wants the final target to be the literal tmux pane id "%307" must send
// "%2525307" on the wire, which arrives here (post net/http decode, post
// prefix/suffix stripping by the caller) as "%25307".
func TestParsePaneTarget_DoubleDecode(t *testing.T) {
	pane, err := parsePaneTarget("/pane/%25307/ws")
	if err != nil {
		t.Fatalf("parsePaneTarget returned error: %v", err)
	}

	want := tmux.Pane{Session: "%307", Window: 0, Index: 0}
	if pane != want {
		t.Errorf("parsePaneTarget(%q) = %+v, want %+v", "/pane/%25307/ws", pane, want)
	}
	if got := pane.Target(); got != "%307" {
		t.Errorf("pane.Target() = %q, want %q", got, "%307")
	}
}
