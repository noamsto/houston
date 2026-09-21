package server

import (
	"log/slog"
	"path/filepath"
	"sync"

	"github.com/noamsto/houston/runs"
)

// mainCheckoutChecker answers whether a @git_root is a repo's main checkout,
// as opposed to a linked worktree. Narrow enough to fake in tests.
type mainCheckoutChecker interface {
	isMainCheckout(root string) bool
}

// repoClassifier answers isMainCheckout by shelling out to git once per
// distinct root and caching the result for the process lifetime — the same
// "cache negative results too" trade-off runs/crewsource.go's crewDir
// already accepted, but keyed and owned by the HTTP handler rather than a
// ticking source.
type repoClassifier struct {
	mu    sync.Mutex
	cache map[string]bool

	// commonDir resolves root's git-common-dir. A field, not a direct
	// exec.CommandContext call, so a test can wrap it to count invocations
	// and prove the cache actually prevents a second shell-out.
	commonDir func(root string) (string, error)
}

func newRepoClassifier() *repoClassifier {
	return &repoClassifier{cache: make(map[string]bool), commonDir: gitCommonDir}
}

func gitCommonDir(root string) (string, error) { return runs.GitCommonDir(root) }

// isMainCheckout is true iff root's own .git is a direct child of root — a
// linked worktree's common dir resolves to the original repo's .git,
// somewhere else entirely. An error (git missing, root no longer exists)
// returns false WITHOUT caching it, so a transient failure gets retried on
// the next call instead of wedging root into the wrong bucket forever.
func (c *repoClassifier) isMainCheckout(root string) bool {
	c.mu.Lock()
	if v, ok := c.cache[root]; ok {
		c.mu.Unlock()
		return v
	}
	c.mu.Unlock()

	v, err := c.classify(root)
	if err != nil {
		return false
	}

	c.mu.Lock()
	c.cache[root] = v
	c.mu.Unlock()

	return v
}

func (c *repoClassifier) classify(root string) (bool, error) {
	commonDir, err := c.commonDir(root)
	if err != nil {
		slog.Warn("workspace: git classify failed", "root", root, "error", err)
		return false, err
	}

	return filepath.Dir(commonDir) == root, nil
}
