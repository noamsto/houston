package runs

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// commonDirTimeout bounds the git call below so a hung git cannot park a
// caller forever.
const commonDirTimeout = 5 * time.Second

// projectRetryAfter is how long a failed resolution stays empty before git is
// asked again.
const projectRetryAfter = 30 * time.Second

// GitCommonDir resolves root's absolute git-common-dir: the main repo's .git
// (or the bare repo itself), shared by every linked worktree.
func GitCommonDir(root string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), commonDirTimeout)
	defer cancel()

	out, err := exec.CommandContext(ctx, "git", "-C", root, "rev-parse", "--path-format=absolute", "--git-common-dir").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// ProjectFromCommonDir names the main repo a git common dir belongs to: the
// parent directory of a ".git" dir, or the bare repo's own name minus ".git".
func ProjectFromCommonDir(dir string) string {
	dir = filepath.Clean(dir)
	if filepath.Base(dir) == ".git" {
		return filepath.Base(filepath.Dir(dir))
	}
	return strings.TrimSuffix(filepath.Base(dir), ".git")
}

type projectEntry struct {
	name string
	ok   bool
	at   time.Time
}

// projectResolver maps a checkout root to its main repo's name. Successes are
// cached for the process lifetime; a failure answers "" and is retried only
// after projectRetryAfter, so a vanished root does not cost a git exec on every
// poll. The root's base name is never a fallback: for a linked worktree it is
// the worktree's leaf, not the project.
type projectResolver struct {
	mu    sync.Mutex
	cache map[string]projectEntry

	commonDir func(root string) (string, error)
	now       func() time.Time
}

func newProjectResolver() *projectResolver {
	return &projectResolver{
		cache:     make(map[string]projectEntry),
		commonDir: GitCommonDir,
		now:       time.Now,
	}
}

// project is "" unless git actually named the main repo.
func (p *projectResolver) project(root string) string {
	if root == "" {
		return ""
	}
	return p.lookup(root).name
}

func (p *projectResolver) lookup(root string) projectEntry {
	p.mu.Lock()
	defer p.mu.Unlock()

	if e, ok := p.cache[root]; ok && (e.ok || p.now().Sub(e.at) < projectRetryAfter) {
		return e
	}

	e := projectEntry{at: p.now()}
	if dir, err := p.commonDir(root); err == nil {
		e.name, e.ok = ProjectFromCommonDir(dir), true
	}
	p.cache[root] = e
	return e
}
