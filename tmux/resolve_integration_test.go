package tmux

import (
	"errors"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// newResolveSession creates a detached session named after the test, the pid
// and a random suffix so it cannot collide with anything already running, and
// kills it on cleanup.
func newResolveSession(t *testing.T, c *Client, prefix string) string {
	t.Helper()
	name := prefix + "-" + strconv.Itoa(os.Getpid()) + "-" + strconv.FormatUint(rand.Uint64(), 36)
	tmuxRun(t, c, "new-session", "-d", "-s", name, "-x", "80", "-y", "24")
	t.Cleanup(func() { _ = c.run("kill-session", "-t", name) })
	return name
}

// isolateTmux points tmux at a fresh private server directory for the test.
// On a machine with no other tmux server, killing the last session tears the
// default server down asynchronously, so the next test's new-session can race
// that exit and fail with "server exited unexpectedly". A server per test
// removes the shared-teardown race, and keeps the user's real server out of
// it. Unix socket paths are capped near 100 bytes, hence /tmp, not t.TempDir().
func isolateTmux(t *testing.T) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "hx")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_TMPDIR", dir)
}

// tmuxRun runs a tmux command on the client's server and reports tmux's own
// stderr on failure, which Client.run discards.
func tmuxRun(t *testing.T, c *Client, args ...string) {
	t.Helper()
	if out, err := exec.Command(c.tmuxPath, args...).CombinedOutput(); err != nil {
		t.Fatalf("tmux %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
}

func paneIDOf(t *testing.T, c *Client, target string) string {
	t.Helper()
	out, err := c.output("display-message", "-t", target, "-p", "#{pane_id}")
	if err != nil {
		t.Fatalf("pane id of %q: %v", target, err)
	}
	return strings.TrimSpace(string(out))
}

func TestResolvePaneIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	isolateTmux(t)
	c := NewClient()
	session := newResolveSession(t, c, "houston-test-resolve")

	// A window 0 / pane 0 regardless of the user's base-index (-k replaces
	// window 0 when base-index is 0 and it already exists), plus a split
	// pane in another window, so both the bare-session case and a
	// non-default position are resolved.
	tmuxRun(t, c, "new-window", "-d", "-k", "-t", session+":0")
	tmuxRun(t, c, "set-option", "-w", "-t", session+":0", "pane-base-index", "0")
	tmuxRun(t, c, "new-window", "-d", "-t", session+":3")
	tmuxRun(t, c, "set-option", "-w", "-t", session+":3", "pane-base-index", "0")
	tmuxRun(t, c, "split-window", "-d", "-t", session+":3")

	cases := []struct {
		target        string
		window, index int
	}{
		{session + ":0.0", 0, 0},
		{session + ":3.1", 3, 1},
	}
	for _, tc := range cases {
		id := paneIDOf(t, c, tc.target)
		got, err := c.ResolvePane(id)
		if err != nil {
			t.Fatalf("ResolvePane(%s): %v", id, err)
		}
		if got.Server == "" {
			t.Errorf("ResolvePane(%s).Server is empty, want the live server's pid", id)
		}
		got.Server = ""
		want := Pane{ID: id, Session: session, Window: tc.window, Index: tc.index}
		if got != want {
			t.Errorf("ResolvePane(%s) = %+v, want %+v", id, got, want)
		}
	}
}

// TestResolvePaneVanishedIntegration covers the "server up, pane gone"
// branch of ResolvePane: with a live session present, tmux resolves -t for
// an unknown pane id by exiting 0 with every requested field blank instead
// of erroring. A session is created first so this branch runs regardless of
// what else is on the machine.
func TestResolvePaneVanishedIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	isolateTmux(t)
	c := NewClient()
	newResolveSession(t, c, "houston-test-resolve-vanished")

	p, err := c.ResolvePane("%999999")
	if err == nil {
		t.Fatalf("ResolvePane(%%999999) = %+v, want an error", p)
	}
	if !errors.Is(err, ErrPaneNotFound) {
		t.Errorf("ResolvePane(%%999999) error = %v, want ErrPaneNotFound", err)
	}
}

// TestResolvePaneNoServerIntegration covers the "no server running" branch
// of ResolvePane: display-message exits 1 when there is no tmux server at
// all. TMUX_TMPDIR is pointed at a fresh, empty directory (and TMUX cleared)
// so tmux can't find or attach to any server, including the user's real one.
// Unix socket paths are capped around 100 bytes, so this uses a short
// os.MkdirTemp("/tmp", ...) directory rather than t.TempDir(), whose path is
// usually too long.
func TestResolvePaneNoServerIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	dir, err := os.MkdirTemp("/tmp", "hx")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	t.Setenv("TMUX", "")
	t.Setenv("TMUX_TMPDIR", dir)

	p, err := NewClient().ResolvePane("%999999")
	if err == nil {
		t.Fatalf("ResolvePane(%%999999) = %+v, want an error", p)
	}
	if !errors.Is(err, ErrPaneNotFound) {
		t.Errorf("ResolvePane(%%999999) error = %v, want ErrPaneNotFound", err)
	}
}

// TestResolvePaneServerExitedIntegration covers the "stale socket, server
// exited" branch of ResolvePane: unlike TestResolvePaneNoServerIntegration
// (socket path never existed), this starts a real server on a private
// socket, kills it, and confirms the socket file survives the kill while
// tmux itself reports "no server running" for it. Never touches the user's
// default tmux server: TMUX_TMPDIR points at a fresh, private directory.
func TestResolvePaneServerExitedIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	dir, err := os.MkdirTemp("/tmp", "hx")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	t.Setenv("TMUX", "")
	t.Setenv("TMUX_TMPDIR", dir)

	c := NewClient()
	session := newResolveSession(t, c, "houston-test-resolve-exited")
	id := paneIDOf(t, c, session)

	socket := filepath.Join(dir, "tmux-"+strconv.Itoa(os.Getuid()), "default")
	if _, err := os.Stat(socket); err != nil {
		t.Fatalf("socket %s not present before kill-server: %v", socket, err)
	}

	_ = c.run("kill-server")

	// Immediately after kill-server, a display-message racing the server's
	// own teardown can observe the transient "server exited unexpectedly"
	// rather than the settled "no server running" — poll past that instead
	// of asserting on whichever one lands first.
	deadline := time.Now().Add(2 * time.Second)
	var resolveErr error
	for {
		if _, statErr := os.Stat(socket); statErr != nil {
			t.Fatalf("socket %s disappeared after kill-server: %v", socket, statErr)
		}
		_, resolveErr = c.ResolvePane(id)
		if resolveErr != nil && !strings.Contains(resolveErr.Error(), "server exited unexpectedly") {
			break
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if resolveErr == nil {
		t.Fatalf("ResolvePane(%s) = nil error after kill-server, want an error", id)
	}
	if !errors.Is(resolveErr, ErrPaneNotFound) {
		t.Errorf("ResolvePane(%s) error = %v, want ErrPaneNotFound", id, resolveErr)
	}
}

func TestResolvePaneTmuxUnreachableIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	c := &Client{tmuxPath: "/nonexistent/tmux"}
	p, err := c.ResolvePane("%1")
	if err == nil {
		t.Fatalf("ResolvePane(%%1) = %+v, want an error", p)
	}
	if errors.Is(err, ErrPaneNotFound) {
		t.Errorf("ResolvePane(%%1) error = %v, want NOT ErrPaneNotFound", err)
	}
}

func TestResolvePaneSessionWithSpaceIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	isolateTmux(t)
	c := NewClient()
	session := newResolveSession(t, c, "houston test resolve")

	id := paneIDOf(t, c, session)
	got, err := c.ResolvePane(id)
	if err != nil {
		t.Fatalf("ResolvePane(%s): %v", id, err)
	}
	if got.Session != session || got.ID != id {
		t.Errorf("ResolvePane(%s) = %+v, want session %q", id, got, session)
	}
}
