package tmux

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestListOptionsIntegration pins the option listing against a real tmux
// server under a non-UTF-8 locale. tmux rewrites control characters in -F
// output to '_' unless the client is UTF-8, so without -u every separator
// comes back '_', the field count is wrong, and the parser silently drops
// every window and pane. tmux forces UTF-8 whenever $TMUX is set, so the
// wrapper unsets it — otherwise this test could not reproduce the Nix build
// sandbox from inside a tmux pane.
func TestListOptionsIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	realTmux, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux not found")
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not found")
	}
	// A socket path is capped near 100 bytes, too short for t.TempDir().
	sockDir, err := os.MkdirTemp("", "hx")
	if err != nil {
		t.Skip("no short temp dir for a tmux socket")
	}
	t.Cleanup(func() { _ = os.RemoveAll(sockDir) })

	t.Setenv("LC_ALL", "C")
	bin := t.TempDir()
	wrapper := "#!" + bash + "\nunset TMUX\nexec '" + realTmux + "' -S '" + sockDir + "/s' -f /dev/null \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "tmux"), []byte(wrapper), 0o755); err != nil {
		t.Fatal(err)
	}
	c := &Client{tmuxPath: filepath.Join(bin, "tmux")}

	if err := c.run("new-session", "-d", "-s", "proj", "--", "sleep", "600"); err != nil {
		t.Fatalf("start private tmux: %v", err)
	}
	t.Cleanup(func() { _ = c.run("kill-server") })

	for _, tc := range []struct{ name, target, value string }{
		{"@claude_status", "proj:0.0", "processing 1790000000 "},
		{"@claude_task", "proj:0.0", "fix #205 | locale"},
		{"@window_task", "proj:0", "fix #205 | locale"},
	} {
		scope := "-p"
		if tc.name == "@window_task" {
			scope = "-w"
		}
		if err := c.run("set-option", scope, "-t", tc.target, tc.name, tc.value); err != nil {
			t.Fatalf("set %s: %v", tc.name, err)
		}
	}

	panes, err := c.ListPaneOptions()
	if err != nil {
		t.Fatalf("ListPaneOptions: %v", err)
	}
	if len(panes) != 1 {
		t.Fatalf("%d panes, want 1 — a non-UTF-8 client mangled the separator and the line was dropped", len(panes))
	}
	if panes[0].Target != "proj:0" || panes[0].ClaudeStatus != "processing 1790000000 " || panes[0].ClaudeTask != "fix #205 | locale" {
		t.Errorf("pane = %+v, want target proj:0 and both free-text fields intact", panes[0])
	}

	wins, err := c.ListWindowOptions()
	if err != nil {
		t.Fatalf("ListWindowOptions: %v", err)
	}
	if len(wins) != 1 {
		t.Fatalf("%d windows, want 1 — a non-UTF-8 client mangled the separator and the line was dropped", len(wins))
	}
	if wins[0].Target() != "proj:0" || wins[0].Task != "fix #205 | locale" {
		t.Errorf("window = %+v, want target proj:0 and the free-text task intact", wins[0])
	}
}
