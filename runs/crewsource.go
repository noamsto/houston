package runs

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/noamsto/houston/tmux"
)

// CrewSource reads dispatcher's git-backed bus. It contributes the one thing
// no other source can express: a worker blocked on a question addressed to you.
//
// It derives its own repo roots from the same ListWindowOptions call tmuxsource
// uses, rather than taking them from the caller — that keeps construction free
// of I/O and self-contained.
type CrewSource struct {
	client *tmux.Client
	every  time.Duration

	// crewDirs caches root -> bus directory, keyed by lazytmux's @git_root.
	// scan() runs only on this source's own goroutine, so a plain map needs no
	// mutex — do not add one, and do not read this from another goroutine.
	crewDirs map[string]string
}

func NewCrewSource(c *tmux.Client, every time.Duration) *CrewSource {
	if every <= 0 {
		every = 3 * time.Second
	}
	return &CrewSource{client: c, every: every, crewDirs: map[string]string{}}
}

func (s *CrewSource) Name() string { return "crew" }

func (s *CrewSource) Run(ctx context.Context, out chan<- Delta) error {
	t := time.NewTicker(s.every)
	defer t.Stop()

	for {
		for branch, r := range s.scan() {
			select {
			case out <- Delta{Source: s.Name(), Key: "branch/" + branch, Run: r}:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
}

func (s *CrewSource) scan() map[string]Run {
	wins, err := s.client.ListWindowOptions()
	if err != nil {
		slog.Debug("crew source: list window options", "error", err)
		return nil
	}

	roots := map[string]bool{}
	for _, w := range wins {
		if w.GitRoot != "" {
			roots[w.GitRoot] = true
		}
	}

	rootList := make([]string, 0, len(roots))
	for repo := range roots {
		rootList = append(rootList, repo)
	}
	return s.scanRoots(rootList)
}

// scanRoots reads every root's crew bus and folds it into the latest state per
// branch. Split out from scan() so the root list can be injected in tests
// without going through tmux.
func (s *CrewSource) scanRoots(roots []string) map[string]Run {
	merged := map[string]Run{}
	for _, repo := range roots {
		dir := s.crewDir(repo)
		if dir == "" {
			continue
		}
		logs, err := filepath.Glob(filepath.Join(dir, "*.jsonl"))
		if err != nil {
			continue
		}
		for _, path := range logs {
			f, err := os.Open(path)
			if err != nil {
				slog.Debug("crew log", "path", path, "error", err)
				continue
			}
			for branch, r := range deltasFromCrewLog(f) {
				merged[branch] = r
			}
			_ = f.Close()
		}
	}
	return merged
}

// crewDirTimeout bounds the git call below so a hung git cannot park this
// source's goroutine forever.
const crewDirTimeout = 5 * time.Second

// crewDir resolves where dispatcher writes its bus for a checkout.
// lazytmux's @git_root is the worktree's top level, but dispatcher writes under
// the COMMON git dir — and in a worktree <root>/.git is a file, not a
// directory, so joining ".git/crew" there finds nothing. Worktree-per-branch is
// the normal case here, not an edge case.
func (s *CrewSource) crewDir(root string) string {
	if dir, ok := s.crewDirs[root]; ok {
		return dir
	}
	ctx, cancel := context.WithTimeout(context.Background(), crewDirTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", "-C", root, "rev-parse", "--git-common-dir").Output()
	if err != nil {
		s.crewDirs[root] = ""
		return ""
	}
	dir := strings.TrimSpace(string(out))
	if !filepath.IsAbs(dir) {
		// Whether this comes back relative or absolute depends on whether root
		// is a worktree or the main checkout, not on git's version: verified on
		// git 2.55, a worktree answers absolute and the main checkout answers
		// relative (".git"). Resolve the relative case against root.
		dir = filepath.Join(root, dir)
	}
	dir = filepath.Join(dir, "crew")
	s.crewDirs[root] = dir
	return dir
}

type crewRecord struct {
	TS     int64  `json:"ts"`
	CrewID string `json:"crew_id"`
	From   string `json:"from"`
	Kind   string `json:"kind"`
	Branch string `json:"branch"`
	Title  string `json:"title"`
	Tier   string `json:"tier"`
	Engine string `json:"engine"`
	Model  string `json:"model"`
	// Session is a tmux session name, present on the dispatch record. It is
	// useful for the later branch->pane join but is not used yet.
	Session string `json:"session"`
	Body    struct {
		State  string `json:"state"`
		Detail string `json:"detail"`
		PRURL  string `json:"pr_url"`
	} `json:"body"`
}

// deltasFromCrewLog folds one bus log into the latest state per branch. The bus
// is append-only, so later records win.
func deltasFromCrewLog(rd io.Reader) map[string]Run {
	out := map[string]Run{}
	sc := bufio.NewScanner(rd)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for sc.Scan() {
		var rec crewRecord
		if err := json.Unmarshal(sc.Bytes(), &rec); err != nil {
			continue // the bus is written by shell; a torn line is not fatal
		}

		branch := rec.Branch
		if branch == "" {
			branch = branchFromWorker(rec.From)
		}
		if branch == "" {
			continue
		}

		r := out[branch]
		r.Branch = branch
		if r.Crew == nil {
			r.Crew = &CrewRef{}
		}
		if rec.CrewID != "" {
			r.Crew.Name = rec.CrewID
		}
		if rec.Tier != "" {
			r.Crew.Tier = rec.Tier
		}
		// Engine only appears on the dispatch record, but every status record
		// for the branch shares this map entry, so it sticks once set. Without
		// it a crew run carries no agent and the listing predicate would drop
		// it — this source exists specifically to surface blocked questions.
		if rec.Engine != "" {
			r.Agent = rec.Engine
		}
		if rec.Kind == "status" && rec.Body.State != "" {
			r.State = FromCrewState(rec.Body.State)
			r.UpdatedAt = rec.TS / 1000
			r.Question = nil
			if r.State == StateBlocked && rec.Body.Detail != "" {
				r.Question = &Question{Text: rec.Body.Detail, Via: "crew"}
			}
		}
		out[branch] = r
	}
	return out
}

// branchFromWorker turns "worker:fix/412#s1788-42" into "fix/412".
func branchFromWorker(from string) string {
	if !strings.HasPrefix(from, "worker:") {
		return ""
	}
	rest := strings.TrimPrefix(from, "worker:")
	if i := strings.LastIndex(rest, "#"); i >= 0 {
		rest = rest[:i]
	}
	return rest
}
