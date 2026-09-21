package runs

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestProjectFromCommonDir(t *testing.T) {
	tests := []struct {
		name, dir, want string
	}{
		{"main checkout", "/x/houston/.git", "houston"},
		{"linked worktree resolves to the main repo", "/x/houston/.git", "houston"},
		{"trailing slash", "/x/houston/.git/", "houston"},
		{"bare repo", "/x/foo.git", "foo"},
		{"bare repo trailing slash", "/x/foo.git/", "foo"},
		{"bare repo without suffix", "/x/foo", "foo"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ProjectFromCommonDir(tt.dir); got != tt.want {
				t.Errorf("ProjectFromCommonDir(%q) = %q, want %q", tt.dir, got, tt.want)
			}
		})
	}
}

type fakeCommonDir struct {
	calls int
	dir   string
	err   error
}

func (f *fakeCommonDir) resolve(string) (string, error) {
	f.calls++
	return f.dir, f.err
}

func newFakeResolver(f *fakeCommonDir, clock *time.Time) *projectResolver {
	return &projectResolver{
		cache:     map[string]projectEntry{},
		commonDir: f.resolve,
		now:       func() time.Time { return *clock },
	}
}

func TestProjectResolverEmptyRoot(t *testing.T) {
	f := &fakeCommonDir{dir: "/x/houston/.git"}
	clock := time.Unix(1000, 0)
	if got := newFakeResolver(f, &clock).project(""); got != "" || f.calls != 0 {
		t.Errorf("project(\"\") = %q with %d calls, want \"\" and none", got, f.calls)
	}
}

func TestProjectResolverCachesSuccessForever(t *testing.T) {
	f := &fakeCommonDir{dir: "/x/houston/.git"}
	clock := time.Unix(1000, 0)
	p := newFakeResolver(f, &clock)

	for range 3 {
		if got := p.project("/wt/feat"); got != "houston" {
			t.Fatalf("project = %q, want houston", got)
		}
		clock = clock.Add(24 * time.Hour)
	}
	if f.calls != 1 {
		t.Errorf("commonDir called %d times, want 1", f.calls)
	}
}

func TestProjectResolverFailureFallsBackAndRetriesAfterWindow(t *testing.T) {
	f := &fakeCommonDir{err: errors.New("not a repo")}
	clock := time.Unix(1000, 0)
	p := newFakeResolver(f, &clock)

	if got := p.project("/wt/feat"); got != "feat" {
		t.Fatalf("project = %q, want Base(root) fallback", got)
	}
	clock = clock.Add(projectRetryAfter - time.Second)
	if got := p.project("/wt/feat"); got != "feat" {
		t.Fatalf("project = %q, want fallback within the window", got)
	}
	if f.calls != 1 {
		t.Fatalf("commonDir called %d times within the window, want 1", f.calls)
	}

	f.err, f.dir = nil, "/x/houston/.git"
	clock = clock.Add(2 * time.Second)
	if got := p.project("/wt/feat"); got != "houston" {
		t.Errorf("project = %q after the window, want houston", got)
	}
	if f.calls != 2 {
		t.Errorf("commonDir called %d times, want a retry after the window", f.calls)
	}

	p.project("/wt/feat")
	if f.calls != 2 {
		t.Errorf("commonDir called %d times, want the success cached", f.calls)
	}
}

// runGit runs git in dir with global and system config isolated, so the test
// depends on no ambient gitconfig.
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

func TestGitCommonDirResolvesWorktreeToMainRepo(t *testing.T) {
	mainDir := filepath.Join(t.TempDir(), "myrepo")
	if err := os.Mkdir(mainDir, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, mainDir, "init", "-q", "-b", "main")
	runGit(t, mainDir, "commit", "-q", "--allow-empty", "-m", "init")

	wtDir := filepath.Join(t.TempDir(), "feat-branch")
	runGit(t, mainDir, "worktree", "add", "-q", "-b", "feature", wtDir)

	p := newProjectResolver()
	for _, root := range []string{mainDir, wtDir} {
		if got := p.project(root); got != "myrepo" {
			t.Errorf("project(%q) = %q, want myrepo", root, got)
		}
	}
}

func TestProjectResolverResolvedNeverReturnsTheFallback(t *testing.T) {
	f := &fakeCommonDir{err: errors.New("not a repo")}
	clock := time.Unix(1000, 0)
	p := newFakeResolver(f, &clock)

	if got := p.resolved("/wt/feat"); got != "" {
		t.Fatalf("resolved = %q on a git failure, want \"\"", got)
	}
	// The second call is a cache hit on the failed entry.
	if got := p.resolved("/wt/feat"); got != "" {
		t.Fatalf("resolved = %q on a cached failure, want \"\"", got)
	}
	if f.calls != 1 {
		t.Fatalf("commonDir called %d times within the window, want 1", f.calls)
	}

	f.err, f.dir = nil, "/x/houston/.git"
	clock = clock.Add(projectRetryAfter + time.Second)
	if got := p.resolved("/wt/feat"); got != "houston" {
		t.Errorf("resolved = %q after the window, want houston", got)
	}
	if got := p.resolved(""); got != "" {
		t.Errorf("resolved(\"\") = %q, want \"\"", got)
	}
}
