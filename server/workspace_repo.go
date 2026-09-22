package server

import (
	"log/slog"
	"path/filepath"
	"sync"

	"github.com/noamsto/houston/runs"
)

// mainCheckoutChecker resolves a @git_root's main-checkout status and
// project name, both derived from the same git-common-dir lookup. Narrow
// enough to fake in tests.
type mainCheckoutChecker interface {
	isMainCheckout(root string) bool
	project(root string) string
}

// repoInfo is what one git-common-dir lookup answers for a @git_root.
type repoInfo struct {
	isMain  bool
	project string
}

// repoClassifier answers isMainCheckout/project by shelling out to git once
// per distinct root and caching the result for the process lifetime — the
// same "cache negative results too" trade-off runs/crewsource.go's crewDir
// already accepted, but keyed and owned by the HTTP handler rather than a
// ticking source.
type repoClassifier struct {
	mu    sync.Mutex
	cache map[string]repoInfo

	// commonDir resolves root's git-common-dir. A field, not a direct
	// exec.CommandContext call, so a test can wrap it to count invocations
	// and prove the cache actually prevents a second shell-out.
	commonDir func(root string) (string, error)
}

func newRepoClassifier() *repoClassifier {
	return &repoClassifier{cache: make(map[string]repoInfo), commonDir: gitCommonDir}
}

func gitCommonDir(root string) (string, error) { return runs.GitCommonDir(root) }

// isMainCheckout is true iff root's own .git is a direct child of root — a
// linked worktree's common dir resolves to the original repo's .git,
// somewhere else entirely. An error (git missing, root no longer exists)
// returns false WITHOUT caching it, so a transient failure gets retried on
// the next call instead of wedging root into the wrong bucket forever.
func (c *repoClassifier) isMainCheckout(root string) bool {
	return c.lookup(root).isMain
}

// project names root's main repo, falling back to root's own basename when
// the git lookup failed — mirroring runs.projectResolver's own fallback so a
// group always has a label even when git fails.
func (c *repoClassifier) project(root string) string {
	if info := c.lookup(root); info.project != "" {
		return info.project
	}
	return filepath.Base(root)
}

func (c *repoClassifier) lookup(root string) repoInfo {
	c.mu.Lock()
	if v, ok := c.cache[root]; ok {
		c.mu.Unlock()
		return v
	}
	c.mu.Unlock()

	v, err := c.classify(root)
	if err != nil {
		return repoInfo{}
	}

	c.mu.Lock()
	c.cache[root] = v
	c.mu.Unlock()

	return v
}

func (c *repoClassifier) classify(root string) (repoInfo, error) {
	commonDir, err := c.commonDir(root)
	if err != nil {
		slog.Warn("workspace: git classify failed", "root", root, "error", err)
		return repoInfo{}, err
	}

	return repoInfo{
		isMain:  filepath.Dir(commonDir) == root,
		project: runs.ProjectFromCommonDir(commonDir),
	}, nil
}
