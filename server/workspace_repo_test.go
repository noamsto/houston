package server

import (
	"os/exec"
	"testing"
)

// runGit runs a git command in dir and fails the test on error, mirroring
// the setup shape tmux/client_test.go's own worktree fixtures use.
func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestRepoClassifier_MainCheckoutAndWorktree(t *testing.T) {
	mainDir := t.TempDir()
	runGit(t, mainDir, "init", "-q", "-b", "main")
	runGit(t, mainDir, "commit", "-q", "--allow-empty", "-m", "init")

	wtDir := t.TempDir()
	runGit(t, mainDir, "worktree", "add", "-q", "-b", "feature", wtDir)

	c := newRepoClassifier()

	if !c.isMainCheckout(mainDir) {
		t.Errorf("isMainCheckout(%q) = false, want true for the main checkout", mainDir)
	}
	if c.isMainCheckout(wtDir) {
		t.Errorf("isMainCheckout(%q) = true, want false for a linked worktree", wtDir)
	}
}

func TestRepoClassifier_NonexistentPath(t *testing.T) {
	c := newRepoClassifier()

	if c.isMainCheckout("/nonexistent/path/does/not/exist") {
		t.Error("isMainCheckout on a nonexistent path = true, want false")
	}
}

func TestRepoClassifier_CachesAfterFirstCall(t *testing.T) {
	mainDir := t.TempDir()
	runGit(t, mainDir, "init", "-q", "-b", "main")
	runGit(t, mainDir, "commit", "-q", "--allow-empty", "-m", "init")

	c := newRepoClassifier()
	calls := 0
	real := c.commonDir
	c.commonDir = func(root string) (string, error) {
		calls++
		return real(root)
	}

	if !c.isMainCheckout(mainDir) {
		t.Fatalf("first call: isMainCheckout(%q) = false, want true", mainDir)
	}
	if calls != 1 {
		t.Fatalf("calls after first isMainCheckout = %d, want 1", calls)
	}

	if !c.isMainCheckout(mainDir) {
		t.Fatalf("second call: isMainCheckout(%q) = false, want true", mainDir)
	}
	if calls != 1 {
		t.Errorf("calls after second isMainCheckout = %d, want 1 (cached, no re-shell-out)", calls)
	}
}
