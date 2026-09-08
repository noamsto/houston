package runs

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/noamsto/houston/tmux"
)

// TmuxSource reads the enrichment lazytmux already computes and parks in tmux
// user-options: branch, worktree, linked issue, PR state and crew membership.
// Houston never recomputes any of it.
type TmuxSource struct {
	client *tmux.Client
	every  time.Duration
}

func NewTmuxSource(c *tmux.Client, every time.Duration) *TmuxSource {
	if every <= 0 {
		every = 2 * time.Second
	}
	return &TmuxSource{client: c, every: every}
}

func (s *TmuxSource) Name() string { return "tmux" }

func (s *TmuxSource) Run(ctx context.Context, out chan<- Delta) error {
	t := time.NewTicker(s.every)
	defer t.Stop()

	seen := map[string]bool{}
	for {
		wins, winErr := s.client.ListWindowOptions()
		if winErr != nil {
			slog.Debug("tmux window options", "error", winErr)
		}
		panes, paneErr := s.client.ListPaneOptions()
		if paneErr != nil {
			slog.Debug("tmux pane options", "error", paneErr)
		}

		// A transient tmux error is not evidence that every pane vanished:
		// skip the tick entirely rather than emitting Gone for everything seen.
		if winErr == nil && paneErr == nil {
			now := map[string]bool{}
			for _, d := range deltasFromTmux(wins, panes) {
				now[d.Key] = true
				select {
				case out <- d:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			for key := range seen {
				if !now[key] {
					select {
					case out <- Delta{Source: s.Name(), Key: key, Gone: true}:
					case <-ctx.Done():
						return ctx.Err()
					}
				}
			}
			seen = now
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
}

// deltasFromTmux joins each pane to its window's enrichment. A pane whose
// window was not listed is skipped rather than emitted bare — it means the two
// queries raced a window closing.
func deltasFromTmux(wins []tmux.WindowOptions, panes []tmux.PaneOptions) []Delta {
	byTarget := make(map[string]tmux.WindowOptions, len(wins))
	for _, w := range wins {
		byTarget[fmt.Sprintf("%s:%d", w.Session, w.Window)] = w
	}

	out := make([]Delta, 0, len(panes))
	for _, p := range panes {
		w, ok := byTarget[p.Target]
		if !ok {
			continue
		}

		r := Run{
			State:    FromClaudeStatus(p.ClaudeStatus),
			Branch:   w.Branch,
			Worktree: w.GitRoot,
			Tmux:     &TmuxRef{Session: w.Session, Window: w.Window, PaneID: p.PaneID},
			Activity: Activity{Task: firstNonEmpty(p.ClaudeTask, w.Task)},
			Caps:     Caps{Terminal: true, Reply: true, Kill: true},
		}
		if w.GitRoot != "" {
			r.Repo = filepath.Base(w.GitRoot)
		}
		if w.IssueID != "" {
			r.Issue = &IssueRef{ID: w.IssueID}
		}
		if w.PRNumber != "" && w.PRNumber != "none" {
			r.PR = &PRRef{
				Number: w.PRNumber, State: w.PRState,
				CheckState: w.PRCheckState, Mergeable: w.PRMergeable,
			}
		}
		if w.CrewName != "" {
			r.Crew = &CrewRef{Name: w.CrewName}
		}
		out = append(out, Delta{Source: "tmux", Key: p.PaneID, Run: r})
	}
	return out
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
