package server

import (
	"errors"
	"os"
	"os/exec"
	"testing"
)

// runGit runs a git command in dir and fails the test on error, mirroring
// the setup shape tmux/client_test.go's own worktree fixtures use. HOME and
// XDG_CONFIG_HOME are pinned to an empty per-test directory (git falls back
// to $XDG_CONFIG_HOME/git/config for global config when it's set, bypassing
// a HOME override alone) and the author/committer identity is injected via
// env, so the commit doesn't depend on the runner having a global git
// identity — or any other global gitconfig, e.g. commit.gpgsign — configured.
// See ec46684 for the same fix applied to the host allowlist.
func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	home := t.TempDir()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"HOME="+home,
		"XDG_CONFIG_HOME="+home,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=test",
		"GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test",
		"GIT_COMMITTER_EMAIL=test@example.com",
	)
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

func TestRepoClassifier_DoesNotCacheOnClassifyError(t *testing.T) {
	c := newRepoClassifier()
	calls := 0
	c.commonDir = func(root string) (string, error) {
		calls++
		return "", errors.New("git not found")
	}

	if c.isMainCheckout("/some/root") {
		t.Fatalf("first call: isMainCheckout = true, want false on classify error")
	}
	if calls != 1 {
		t.Fatalf("calls after first isMainCheckout = %d, want 1", calls)
	}

	if c.isMainCheckout("/some/root") {
		t.Fatalf("second call: isMainCheckout = true, want false on classify error")
	}
	if calls != 2 {
		t.Errorf("calls after second isMainCheckout = %d, want 2 (not cached, retries on error)", calls)
	}
}
