package runs

import (
	"context"
	"strconv"
	"time"

	"github.com/noamsto/houston/hub"
)

// hookGoneCheckInterval is how often HookSource diffs against hub.Snapshot to
// catch a session hub deleted without broadcasting (see Run).
const hookGoneCheckInterval = 5 * time.Second

// HookSource publishes Claude Code hook state. It rides the existing hub rather
// than re-watching the state dir and re-tailing transcripts.
type HookSource struct{ hub *hub.Hub }

func NewHookSource(h *hub.Hub) *HookSource { return &HookSource{hub: h} }

func (s *HookSource) Name() string { return "hooks" }

func (s *HookSource) Run(ctx context.Context, out chan<- Delta) error {
	sub := s.hub.Subscribe()
	defer s.hub.Unsubscribe(sub)

	seen := map[string]bool{}
	emit := func(key string, r Run) error {
		seen[key] = true
		select {
		case out <- Delta{Source: s.Name(), Key: key, Run: r}:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	for _, v := range s.hub.Snapshot() {
		key, r := runFromSessionView(v)
		if err := emit(key, r); err != nil {
			return err
		}
	}

	// hub deletes a session on file removal and broadcasts nothing about it,
	// so a killed agent would otherwise stay listed forever at its last known
	// state, capabilities and all. Diff against hub.Snapshot on a ticker to
	// catch that.
	t := time.NewTicker(hookGoneCheckInterval)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case v, ok := <-sub:
			if !ok {
				return nil
			}
			key, r := runFromSessionView(v)
			if err := emit(key, r); err != nil {
				return err
			}
		case <-t.C:
			now := map[string]bool{}
			for _, v := range s.hub.Snapshot() {
				key, _ := runFromSessionView(v)
				now[key] = true
			}
			for key := range seen {
				if now[key] {
					continue
				}
				delete(seen, key)
				select {
				case out <- Delta{Source: s.Name(), Key: key, Gone: true}:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
		}
	}
}

// runFromSessionView converts one hub view into this source's layer. The key is
// the tmux pane id when there is one, because that is what tmux and crew
// deltas can also produce; a headless session falls back to its own id and
// simply never correlates with them.
func runFromSessionView(v hub.SessionView) (string, Run) {
	r := Run{
		Agent:     "claude",
		State:     FromHookState(v.State),
		Worktree:  v.CWD,
		UpdatedAt: v.UpdatedAt,
		Since:     v.Since,
		Tokens:    Tokens{Input: v.InputTokens, Output: v.OutputTokens},
		Activity: Activity{
			Tool:    v.Tool,
			Hint:    v.ToolInputHint,
			Message: v.LastMessage,
			Preview: v.Preview,
			Turn:    v.Turn,
		},
	}
	for _, c := range v.Trail {
		r.Activity.Trail = append(r.Activity.Trail, TrailChip{
			Tool: c.Tool, Hint: c.Hint, Done: c.Done, IsError: c.IsError,
		})
	}

	key := "claude/" + v.SessionID
	if v.TmuxPane != "" {
		key = v.TmuxPane
		win, _ := strconv.Atoi(v.TmuxWindow)
		r.Tmux = &TmuxRef{Session: v.TmuxSession, Window: win, PaneID: v.TmuxPane}
		r.Caps = Caps{Terminal: true, Reply: true, Kill: true}
	}

	if r.State == StateBlocked && v.LastMessage != "" {
		r.Question = &Question{Text: v.LastMessage, Via: "pane"}
	}
	return key, r
}
