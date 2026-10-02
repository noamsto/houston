package server

import (
	"bytes"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
)

// realTempDir resolves t.TempDir() because /tmp is a symlink on some systems,
// and the registry stores and compares resolved paths only.
func realTempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
}

func mkdirAll(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func gitInit(t *testing.T, dir string) string {
	t.Helper()
	mkdirAll(t, dir)
	runGit(t, dir, "init", "-q", "-b", "main")
	return dir
}

// fakeRepo makes a directory the picker treats as a repo — the walk only
// looks for a .git directory, it never runs git.
func fakeRepo(t *testing.T, dir string) string {
	t.Helper()
	mkdirAll(t, filepath.Join(dir, ".git"))
	return dir
}

func symlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

func setRegistryCap(t *testing.T, n int) {
	t.Helper()
	old := registryCap
	registryCap = n
	t.Cleanup(func() { registryCap = old })
}

func setPickerResultCap(t *testing.T, n int) {
	t.Helper()
	old := pickerResultCap
	pickerResultCap = n
	t.Cleanup(func() { pickerResultCap = old })
}

type registryFixture struct {
	base, root, file string
}

func newRegistryFixture(t *testing.T) registryFixture {
	t.Helper()
	base := realTempDir(t)
	return registryFixture{
		base: base,
		root: mkdirAll(t, filepath.Join(base, "root")),
		file: filepath.Join(realTempDir(t), "repos.json"),
	}
}

func (f registryFixture) open() *repoRegistry {
	return newRepoRegistry(f.file, []string{f.root}, gitCommonDir)
}

func wantRepoError(t *testing.T, err error, code int) {
	t.Helper()
	var re *repoError
	if !errors.As(err, &re) {
		t.Fatalf("err = %v (%T), want *repoError with code %d", err, err, code)
	}
	if re.code != code {
		t.Fatalf("err code = %d (%q), want %d", re.code, re.msg, code)
	}
}

func TestRepoRegistryAddPersists(t *testing.T) {
	requireGit(t)
	f := newRegistryFixture(t)
	repo := gitInit(t, filepath.Join(f.root, "proj"))

	got, err := f.open().Add(repo + "/")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if got != repo {
		t.Fatalf("Add returned %q, want %q", got, repo)
	}

	info, err := os.Stat(f.file)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("registry file mode = %o, want 600", perm)
	}

	reopened := f.open()
	if !reopened.Has(repo) {
		t.Errorf("a new registry from the same file does not have %q", repo)
	}
	want := []repoEntry{{Path: repo, Name: "proj", Valid: true}}
	if got := reopened.Entries(); !slices.Equal(got, want) {
		t.Errorf("Entries() = %+v, want %+v", got, want)
	}
}

func TestRepoRegistryAddIdempotent(t *testing.T) {
	requireGit(t)
	f := newRegistryFixture(t)
	repo := gitInit(t, filepath.Join(f.root, "proj"))
	reg := f.open()

	for range 2 {
		if _, err := reg.Add(repo); err != nil {
			t.Fatalf("Add: %v", err)
		}
	}
	if got := reg.Entries(); len(got) != 1 {
		t.Errorf("Entries() = %+v, want one entry", got)
	}
}

func TestRepoRegistryAddRootItself(t *testing.T) {
	requireGit(t)
	f := newRegistryFixture(t)
	gitInit(t, f.root)

	if _, err := f.open().Add(f.root); err != nil {
		t.Fatalf("Add(root): %v, want the root itself to count as inside", err)
	}
}

func TestRepoRegistryAddRefusals(t *testing.T) {
	requireGit(t)
	f := newRegistryFixture(t)
	outside := gitInit(t, filepath.Join(f.base, "outside"))

	escape := filepath.Join(f.root, "escape")
	symlink(t, outside, escape)

	plain := mkdirAll(t, filepath.Join(f.root, "plain"))

	repo := gitInit(t, filepath.Join(f.root, "proj"))
	runGit(t, repo, "commit", "-q", "--allow-empty", "-m", "init")
	worktree := filepath.Join(f.root, "wt")
	runGit(t, repo, "worktree", "add", "-q", "-b", "feature", worktree)

	gitFile := mkdirAll(t, filepath.Join(f.root, "gitfile"))
	if err := os.WriteFile(filepath.Join(gitFile, ".git"), []byte("gitdir: "+filepath.Join(repo, ".git")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// An empty .git dir isn't a git dir: git walks up to proj, so the common
	// dir is proj's and this is not a main checkout of its own.
	hollow := fakeRepo(t, filepath.Join(repo, "hollow"))

	file := filepath.Join(f.root, "file")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name, path string
		code       int
	}{
		{"empty", "", http.StatusBadRequest},
		{"relative", "root/proj", http.StatusBadRequest},
		{"outside root", outside, http.StatusUnprocessableEntity},
		{"symlink escape", escape, http.StatusUnprocessableEntity},
		{"dotdot escape", f.root + "/../outside", http.StatusUnprocessableEntity},
		{"missing", filepath.Join(f.root, "nope"), http.StatusUnprocessableEntity},
		{"not a directory", file, http.StatusUnprocessableEntity},
		{"not git", plain, http.StatusUnprocessableEntity},
		{"linked worktree", worktree, http.StatusUnprocessableEntity},
		{".git file", gitFile, http.StatusUnprocessableEntity},
		{"hollow .git dir", hollow, http.StatusUnprocessableEntity},
	}
	reg := f.open()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := reg.Add(c.path)
			wantRepoError(t, err, c.code)
		})
	}
	if got := reg.Entries(); len(got) != 0 {
		t.Errorf("Entries() = %+v after only refusals, want none", got)
	}
	if _, err := os.Stat(f.file); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("registry file written after only refusals (stat err %v)", err)
	}
}

func TestRepoRegistryAddNoRoots(t *testing.T) {
	requireGit(t)
	base := realTempDir(t)
	repo := gitInit(t, filepath.Join(base, "proj"))
	reg := newRepoRegistry(filepath.Join(base, "repos.json"), nil, gitCommonDir)

	_, err := reg.Add(repo)
	wantRepoError(t, err, http.StatusUnprocessableEntity)
	if got := reg.Roots(); got == nil || len(got) != 0 {
		t.Errorf("Roots() = %#v, want an empty non-nil slice", got)
	}
}

func TestRepoRegistryFull(t *testing.T) {
	requireGit(t)
	setRegistryCap(t, 2)
	f := newRegistryFixture(t)
	reg := f.open()
	for _, name := range []string{"a", "b"} {
		if _, err := reg.Add(gitInit(t, filepath.Join(f.root, name))); err != nil {
			t.Fatalf("Add(%s): %v", name, err)
		}
	}

	_, err := reg.Add(gitInit(t, filepath.Join(f.root, "c")))
	wantRepoError(t, err, http.StatusConflict)

	if _, err := reg.Add(filepath.Join(f.root, "a")); err != nil {
		t.Errorf("re-adding a registered repo when full: %v, want idempotent success", err)
	}
}

func TestRepoRegistryRemove(t *testing.T) {
	requireGit(t)
	f := newRegistryFixture(t)
	repo := gitInit(t, filepath.Join(f.root, "proj"))
	reg := f.open()
	if _, err := reg.Add(repo); err != nil {
		t.Fatal(err)
	}

	if err := reg.Remove(repo); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if reg.Has(repo) {
		t.Error("Has after Remove = true")
	}
	if f.open().Has(repo) {
		t.Error("removal not persisted: a new registry still has the repo")
	}
	if _, err := os.Stat(repo); err != nil {
		t.Errorf("Remove touched the repo itself: %v", err)
	}

	wantRepoError(t, reg.Remove(repo), http.StatusNotFound)
}

func TestRepoRegistryCorruptFileUntouched(t *testing.T) {
	f := newRegistryFixture(t)
	corrupt := []byte(`{"repos": [`)
	if err := os.WriteFile(f.file, corrupt, 0o600); err != nil {
		t.Fatal(err)
	}

	reg := f.open()
	if got := reg.Entries(); len(got) != 0 {
		t.Errorf("Entries() = %+v from a corrupt file, want none", got)
	}
	got, err := os.ReadFile(f.file)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, corrupt) {
		t.Errorf("corrupt file rewritten on load: %q", got)
	}
}

func TestRepoRegistryMissingFileIsEmpty(t *testing.T) {
	f := newRegistryFixture(t)
	if got := f.open().Entries(); len(got) != 0 {
		t.Errorf("Entries() = %+v with no file, want none", got)
	}
}

func TestRepoRegistryEntriesListingCheck(t *testing.T) {
	requireGit(t)
	f := newRegistryFixture(t)
	gone := gitInit(t, filepath.Join(f.root, "gone"))
	kept := gitInit(t, filepath.Join(f.root, "kept"))
	reg := f.open()
	for _, p := range []string{gone, kept} {
		if _, err := reg.Add(p); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.RemoveAll(gone); err != nil {
		t.Fatal(err)
	}

	want := []repoEntry{
		{Path: gone, Name: "gone", Valid: false},
		{Path: kept, Name: "kept", Valid: true},
	}
	if got := reg.Entries(); !slices.Equal(got, want) {
		t.Errorf("Entries() = %+v, want %+v", got, want)
	}
	if got := reg.ValidPaths(); !slices.Equal(got, []string{kept}) {
		t.Errorf("ValidPaths() = %v, want [%s]", got, kept)
	}
	if !f.open().Has(gone) {
		t.Error("an invalid entry was dropped from the file; it must be kept")
	}
}

func TestRepoRegistryEntriesRootRemoved(t *testing.T) {
	requireGit(t)
	f := newRegistryFixture(t)
	repo := gitInit(t, filepath.Join(f.root, "proj"))
	if _, err := f.open().Add(repo); err != nil {
		t.Fatal(err)
	}

	other := mkdirAll(t, filepath.Join(f.base, "other"))
	reg := newRepoRegistry(f.file, []string{other}, gitCommonDir)
	if got := reg.Entries(); len(got) != 1 || got[0].Valid {
		t.Errorf("Entries() = %+v, want the entry flagged invalid once outside every root", got)
	}
}

func TestRepoRegistryResolveRoots(t *testing.T) {
	base := realTempDir(t)
	realDir := mkdirAll(t, filepath.Join(base, "real"))
	second := mkdirAll(t, filepath.Join(base, "second"))
	link := filepath.Join(base, "link")
	symlink(t, realDir, link)

	got := resolveRepoRoots([]string{
		link,
		filepath.Join(base, "missing"),
		realDir + "/",
		second,
	})
	if want := []string{realDir, second}; !slices.Equal(got, want) {
		t.Errorf("resolveRepoRoots = %v, want %v", got, want)
	}
}

// pickerFixture lays out a root with repos at several depths plus every shape
// the walk must not report.
func pickerFixture(t *testing.T) (root, outside string) {
	t.Helper()
	base := realTempDir(t)
	root = mkdirAll(t, filepath.Join(base, "root"))
	outside = fakeRepo(t, filepath.Join(base, "outside"))

	fakeRepo(t, filepath.Join(root, "One"))
	fakeRepo(t, filepath.Join(root, "One", "sub", "nested"))
	fakeRepo(t, filepath.Join(root, "a", "b", "three"))
	fakeRepo(t, filepath.Join(root, "a", "b", "c", "four"))
	fakeRepo(t, filepath.Join(root, ".hidden", "secret"))
	symlink(t, outside, filepath.Join(root, "linked"))
	symlink(t, filepath.Dir(outside), filepath.Join(root, "linkeddir"))

	gitLink := mkdirAll(t, filepath.Join(root, "gitlink"))
	symlink(t, filepath.Join(outside, ".git"), filepath.Join(gitLink, ".git"))
	return root, outside
}

func candidatePaths(cands []repoCandidate) []string {
	out := make([]string, 0, len(cands))
	for _, c := range cands {
		out = append(out, c.Path)
	}
	return out
}

func TestRepoPickerWalk(t *testing.T) {
	root, _ := pickerFixture(t)
	reg := newRepoRegistry(filepath.Join(realTempDir(t), "repos.json"), []string{root}, gitCommonDir)

	cands, truncated := reg.Candidates("")
	if truncated {
		t.Error("truncated = true, want false")
	}
	want := []string{
		filepath.Join(root, "One"),
		filepath.Join(root, "a", "b", "three"),
	}
	got := candidatePaths(cands)
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("candidates = %v, want %v", got, want)
	}
	for _, c := range cands {
		if c.Name != filepath.Base(c.Path) {
			t.Errorf("candidate %q Name = %q, want its basename", c.Path, c.Name)
		}
	}
}

func TestRepoPickerQueryCaseInsensitive(t *testing.T) {
	root, _ := pickerFixture(t)
	reg := newRepoRegistry(filepath.Join(realTempDir(t), "repos.json"), []string{root}, gitCommonDir)

	cands, _ := reg.Candidates("A/B")
	if got, want := candidatePaths(cands), []string{filepath.Join(root, "a", "b", "three")}; !slices.Equal(got, want) {
		t.Errorf("Candidates(A/B) = %v, want %v", got, want)
	}

	// The match is on the path relative to the root, not the absolute path.
	cands, _ = reg.Candidates("root")
	if len(cands) != 0 {
		t.Errorf("Candidates(root) = %v, want none: the root's own path is not matched", candidatePaths(cands))
	}
}

func TestRepoPickerResultCap(t *testing.T) {
	setPickerResultCap(t, 1)
	root, _ := pickerFixture(t)
	reg := newRepoRegistry(filepath.Join(realTempDir(t), "repos.json"), []string{root}, gitCommonDir)

	cands, truncated := reg.Candidates("")
	if len(cands) != 1 || !truncated {
		t.Errorf("Candidates = %v, truncated %v; want 1 result and truncated", candidatePaths(cands), truncated)
	}
}

func TestRepoPickerRegisteredFlag(t *testing.T) {
	root, _ := pickerFixture(t)
	one := filepath.Join(root, "One")
	file := filepath.Join(realTempDir(t), "repos.json")
	if err := os.WriteFile(file, []byte(`{"repos":["`+one+`"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	reg := newRepoRegistry(file, []string{root}, gitCommonDir)

	cands, _ := reg.Candidates("")
	for _, c := range cands {
		if want := c.Path == one; c.Registered != want {
			t.Errorf("candidate %q Registered = %v, want %v", c.Path, c.Registered, want)
		}
	}
}

func TestRepoPickerNoRoots(t *testing.T) {
	reg := newRepoRegistry(filepath.Join(realTempDir(t), "repos.json"), nil, gitCommonDir)
	if cands, truncated := reg.Candidates(""); len(cands) != 0 || truncated {
		t.Errorf("Candidates with no roots = %v, %v; want none", cands, truncated)
	}
}
