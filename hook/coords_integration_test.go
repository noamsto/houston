package hook

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestTmuxCoordsIntegration pins the hook's tmux coordinate parse against a
// real tmux server under a non-UTF-8 locale. tmux rewrites control characters
// in -F/format output to '_' unless the client is UTF-8, so without -u the
// tab in "#S\t#I" comes back '_', strings.Split finds no separator, and the
// session/window coordinates mis-parse. tmux forces UTF-8 whenever $TMUX is
// set, so the wrapper unsets it — otherwise this test could not reproduce the
// Nix build sandbox from inside a tmux pane.
func TestTmuxCoordsIntegration(t *testing.T) {
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
	sock := filepath.Join(sockDir, "s")

	t.Setenv("LC_ALL", "C")
	bin := t.TempDir()
	wrapper := "#!" + bash + "\nunset TMUX\nexec '" + realTmux + "' -S '" + sock + "' -f /dev/null \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "tmux"), []byte(wrapper), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	tmuxReal := func(args ...string) {
		t.Helper()
		full := append([]string{"-S", sock, "-f", "/dev/null"}, args...)
		if out, err := exec.Command(realTmux, full...).CombinedOutput(); err != nil {
			t.Fatalf("tmux %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
		}
	}
	tmuxReal("new-session", "-d", "-s", "proj", "--", "sleep", "600")
	t.Cleanup(func() { _ = exec.Command(realTmux, "-S", sock, "-f", "/dev/null", "kill-server").Run() })

	out, err := exec.Command(realTmux, "-S", sock, "-f", "/dev/null", "list-panes", "-t", "proj", "-F", "#{pane_id}").Output()
	if err != nil {
		t.Fatalf("list panes: %v", err)
	}
	pane := strings.TrimSpace(string(out))
	t.Setenv("TMUX_PANE", pane)

	session, window, gotPane := tmuxCoords()

	if session != "proj" || window != "0" || gotPane != pane {
		t.Errorf("tmuxCoords() = (%q, %q, %q), want (%q, %q, %q) — a non-UTF-8 client returns '_' for the tab separator",
			session, window, gotPane, "proj", "0", pane)
	}
}
