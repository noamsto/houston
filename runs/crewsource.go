package runs

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
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
}

func NewCrewSource(c *tmux.Client, every time.Duration) *CrewSource {
	if every <= 0 {
		every = 3 * time.Second
	}
	return &CrewSource{client: c, every: every}
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

	merged := map[string]Run{}
	for repo := range roots {
		logs, err := filepath.Glob(filepath.Join(repo, ".git", "crew", "*.jsonl"))
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

type crewRecord struct {
	TS     int64  `json:"ts"`
	CrewID string `json:"crew_id"`
	From   string `json:"from"`
	Kind   string `json:"kind"`
	Branch string `json:"branch"`
	Title  string `json:"title"`
	Tier   string `json:"tier"`
	Body   struct {
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
