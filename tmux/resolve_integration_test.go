package tmux

import (
	"errors"
	"math/rand/v2"
	"os"
	"strconv"
	"strings"
	"testing"
)

// newResolveSession creates a detached session named after the test, the pid
// and a random suffix so it cannot collide with anything already running, and
// kills it on cleanup.
func newResolveSession(t *testing.T, c *Client, prefix string) string {
	t.Helper()
	name := prefix + "-" + strconv.Itoa(os.Getpid()) + "-" + strconv.FormatUint(rand.Uint64(), 36)
	if err := c.run("new-session", "-d", "-s", name, "-x", "80", "-y", "24"); err != nil {
		t.Fatalf("new-session %q: %v", name, err)
	}
	t.Cleanup(func() { _ = c.run("kill-session", "-t", name) })
	return name
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
	c := NewClient()
	session := newResolveSession(t, c, "houston-test-resolve")

	// A window 0 / pane 0 regardless of the user's base-index, plus a split
	// pane in another window, so both the bare-session case and a
	// non-default position are resolved.
	if err := c.run("new-window", "-d", "-t", session+":0"); err != nil {
		t.Fatalf("new-window: %v", err)
	}
	if err := c.run("set-option", "-w", "-t", session+":0", "pane-base-index", "0"); err != nil {
		t.Fatalf("pane-base-index: %v", err)
	}
	if err := c.run("new-window", "-d", "-t", session+":3"); err != nil {
		t.Fatalf("new-window: %v", err)
	}
	if err := c.run("set-option", "-w", "-t", session+":3", "pane-base-index", "0"); err != nil {
		t.Fatalf("pane-base-index: %v", err)
	}
	if err := c.run("split-window", "-d", "-t", session+":3"); err != nil {
		t.Fatalf("split-window: %v", err)
	}

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
		want := Pane{ID: id, Session: session, Window: tc.window, Index: tc.index}
		if got != want {
			t.Errorf("ResolvePane(%s) = %+v, want %+v", id, got, want)
		}
	}
}

func TestResolvePaneVanishedIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	p, err := NewClient().ResolvePane("%999999")
	if err == nil {
		t.Fatalf("ResolvePane(%%999999) = %+v, want an error", p)
	}
	if !errors.Is(err, ErrPaneNotFound) {
		t.Errorf("ResolvePane(%%999999) error = %v, want ErrPaneNotFound", err)
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
