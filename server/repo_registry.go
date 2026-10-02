package server

import (
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
)

// registryCap and pickerResultCap are vars only so tests can lower them.
var (
	registryCap     = 500
	pickerResultCap = 200
)

const (
	pickerMaxDepth   = 3
	pickerMaxVisited = 10000
)

// repoRegistry is the houston-persisted half of the known-repo set. Every
// entry is a resolved main checkout inside a configured root; the file keeps
// entries that currently fail that check, so a transient unmount doesn't
// delete them.
type repoRegistry struct {
	mu        sync.Mutex
	file      string
	roots     []string
	repos     []string
	commonDir func(string) (string, error)
}

type repoEntry struct {
	Path  string `json:"path"`
	Name  string `json:"name"`
	Valid bool   `json:"valid"`
}

type repoCandidate struct {
	Path       string `json:"path"`
	Name       string `json:"name"`
	Registered bool   `json:"registered"`
}

// repoError carries the HTTP status the API answers with; msg is shown to the
// user verbatim.
type repoError struct {
	code int
	msg  string
}

func (e *repoError) Error() string { return e.msg }

type repoRegistryFile struct {
	Repos []string `json:"repos"`
}

// resolveRepoRoots makes each root absolute and symlink-free, so containment
// checks compare real paths on both sides.
func resolveRepoRoots(roots []string) []string {
	out := make([]string, 0, len(roots))
	for _, root := range roots {
		abs, err := filepath.Abs(root)
		if err == nil {
			abs, err = filepath.EvalSymlinks(abs)
		}
		if err != nil {
			slog.Warn("repo root unusable, dropping it", "root", root, "error", err)
			continue
		}
		if !slices.Contains(out, abs) {
			out = append(out, abs)
		}
	}
	return out
}

func newRepoRegistry(file string, roots []string, commonDir func(string) (string, error)) *repoRegistry {
	return &repoRegistry{
		file:      file,
		roots:     roots,
		repos:     loadRepoRegistry(file),
		commonDir: commonDir,
	}
}

// loadRepoRegistry never writes: a corrupt file stays as it is until the next
// successful Add or Remove, so a bad read can't silently clobber it.
func loadRepoRegistry(file string) []string {
	data, err := os.ReadFile(file) //nolint:gosec // registry file in houston's status dir, not from a request
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		slog.Warn("repo registry unreadable, starting empty", "file", file, "error", err)
		return nil
	}
	var f repoRegistryFile
	if err := json.Unmarshal(data, &f); err != nil {
		slog.Warn("repo registry corrupt, starting empty", "file", file, "error", err)
		return nil
	}
	return f.Repos
}

func (r *repoRegistry) Roots() []string {
	return append([]string{}, r.roots...)
}

// Add runs the full check, including git, and stores the resolved real path.
func (r *repoRegistry) Add(path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", &repoError{http.StatusBadRequest, "path must be absolute"}
	}
	if len(r.roots) == 0 {
		return "", &repoError{http.StatusUnprocessableEntity, "no repo roots configured"}
	}
	resolved, err := filepath.EvalSymlinks(filepath.Clean(path))
	if err != nil {
		return "", &repoError{http.StatusUnprocessableEntity, "path does not exist"}
	}
	if !r.insideRoot(resolved) {
		return "", &repoError{http.StatusUnprocessableEntity, "path is outside the repo roots"}
	}
	if info, err := os.Stat(resolved); err != nil || !info.IsDir() {
		return "", &repoError{http.StatusUnprocessableEntity, "not a directory"}
	}
	gitDir := filepath.Join(resolved, ".git")
	if !isDirNoFollow(gitDir) {
		return "", &repoError{http.StatusUnprocessableEntity, "not a git main checkout"}
	}
	if dir, err := r.commonDir(resolved); err != nil || filepath.Clean(dir) != gitDir {
		return "", &repoError{http.StatusUnprocessableEntity, "not a git main checkout"}
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if slices.Contains(r.repos, resolved) {
		return resolved, nil
	}
	if len(r.repos) >= registryCap {
		return "", &repoError{http.StatusConflict, "repo registry is full"}
	}
	next := append(slices.Clone(r.repos), resolved)
	if err := r.persist(next); err != nil {
		return "", err
	}
	r.repos = next
	return resolved, nil
}

// Remove matches the stored path exactly; it never touches the repo itself.
func (r *repoRegistry) Remove(path string) error {
	path = filepath.Clean(path)

	r.mu.Lock()
	defer r.mu.Unlock()
	i := slices.Index(r.repos, path)
	if i < 0 {
		return &repoError{http.StatusNotFound, "repo is not registered"}
	}
	next := slices.Delete(slices.Clone(r.repos), i, i+1)
	if err := r.persist(next); err != nil {
		return err
	}
	r.repos = next
	return nil
}

// persist writes atomically (temp file in the same dir, then rename) so a
// crash mid-write never leaves a truncated registry. os.CreateTemp creates
// the file 0600.
func (r *repoRegistry) persist(repos []string) error {
	data, err := json.Marshal(repoRegistryFile{Repos: append([]string{}, repos...)})
	if err != nil {
		return &repoError{http.StatusInternalServerError, "could not save the repo registry"}
	}
	tmp, err := os.CreateTemp(filepath.Dir(r.file), ".repos-*.json")
	if err != nil {
		slog.Error("repo registry save failed", "file", r.file, "error", err)
		return &repoError{http.StatusInternalServerError, "could not save the repo registry"}
	}
	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if err := errors.Join(werr, cerr); err != nil {
		_ = os.Remove(tmp.Name())
		slog.Error("repo registry save failed", "file", r.file, "error", err)
		return &repoError{http.StatusInternalServerError, "could not save the repo registry"}
	}
	if err := os.Rename(tmp.Name(), r.file); err != nil {
		_ = os.Remove(tmp.Name())
		slog.Error("repo registry save failed", "file", r.file, "error", err)
		return &repoError{http.StatusInternalServerError, "could not save the repo registry"}
	}
	return nil
}

// Entries is the raw registry in file order. Valid is the cheap listing
// check — no git — so an options GET never spawns a git per entry.
func (r *repoRegistry) Entries() []repoEntry {
	r.mu.Lock()
	repos := slices.Clone(r.repos)
	r.mu.Unlock()

	out := make([]repoEntry, 0, len(repos))
	for _, p := range repos {
		out = append(out, repoEntry{Path: p, Name: filepath.Base(p), Valid: r.listable(p)})
	}
	return out
}

func (r *repoRegistry) ValidPaths() []string {
	var out []string
	for _, e := range r.Entries() {
		if e.Valid {
			out = append(out, e.Path)
		}
	}
	return out
}

func (r *repoRegistry) Has(resolved string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Contains(r.repos, resolved)
}

// listable re-resolves the stored path because a symlink on it may have been
// retargeted outside the roots since it was added.
func (r *repoRegistry) listable(p string) bool {
	resolved, err := filepath.EvalSymlinks(p)
	if err != nil || !r.insideRoot(resolved) {
		return false
	}
	return isDirNoFollow(filepath.Join(resolved, ".git"))
}

func (r *repoRegistry) insideRoot(resolved string) bool {
	for _, root := range r.roots {
		rel, err := filepath.Rel(root, resolved)
		if err != nil {
			continue
		}
		if rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// isDirNoFollow uses Lstat so a .git symlink never counts as a repo.
func isDirNoFollow(path string) bool {
	info, err := os.Lstat(path)
	if err != nil {
		return false
	}
	return info.IsDir()
}

// Candidates walks every root for directories holding a .git directory,
// matching q case-insensitively against the root-relative path. The walk is
// bounded (depth, directories visited, results) and never follows a symlink,
// so it only ever reports directories really under a root.
func (r *repoRegistry) Candidates(q string) ([]repoCandidate, bool) {
	r.mu.Lock()
	registered := make(map[string]bool, len(r.repos))
	for _, p := range r.repos {
		registered[p] = true
	}
	r.mu.Unlock()

	w := pickerWalk{q: strings.ToLower(q), registered: registered, cands: []repoCandidate{}}
	for _, root := range r.roots {
		if w.stopped {
			break
		}
		w.root = root
		w.walk(root, 0)
	}
	return w.cands, w.truncated
}

type pickerWalk struct {
	root       string
	q          string
	registered map[string]bool
	visited    int
	cands      []repoCandidate
	truncated  bool
	stopped    bool
}

func (w *pickerWalk) walk(dir string, depth int) {
	if w.visited >= pickerMaxVisited {
		w.truncated, w.stopped = true, true
		return
	}
	w.visited++

	if isDirNoFollow(filepath.Join(dir, ".git")) {
		w.report(dir)
		return
	}
	if depth == pickerMaxDepth {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if w.stopped {
			return
		}
		// DirEntry carries lstat type bits, so a symlinked dir is not IsDir.
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		w.walk(filepath.Join(dir, e.Name()), depth+1)
	}
}

func (w *pickerWalk) report(dir string) {
	rel, err := filepath.Rel(w.root, dir)
	if err != nil || rel == "." {
		rel = filepath.Base(dir)
	}
	if !strings.Contains(strings.ToLower(rel), w.q) {
		return
	}
	if len(w.cands) >= pickerResultCap {
		w.truncated, w.stopped = true, true
		return
	}
	w.cands = append(w.cands, repoCandidate{Path: dir, Name: filepath.Base(dir), Registered: w.registered[dir]})
}
