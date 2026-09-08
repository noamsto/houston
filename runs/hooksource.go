package runs

import (
	"context"
	"strconv"

	"github.com/noamsto/houston/hub"
)

// HookSource publishes Claude Code hook state. It rides the existing hub rather
// than re-watching the state dir and re-tailing transcripts.
type HookSource struct{ hub *hub.Hub }

func NewHookSource(h *hub.Hub) *HookSource { return &HookSource{hub: h} }

func (s *HookSource) Name() string { return "hooks" }

func (s *HookSource) Run(ctx context.Context, out chan<- Delta) error {
	sub := s.hub.Subscribe()
	defer s.hub.Unsubscribe(sub)

	for _, v := range s.hub.Snapshot() {
		key, r := runFromSessionView(v)
		select {
		case out <- Delta{Source: s.Name(), Key: key, Run: r}:
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case v, ok := <-sub:
			if !ok {
				return nil
			}
			key, r := runFromSessionView(v)
			select {
			case out <- Delta{Source: s.Name(), Key: key, Run: r}:
			case <-ctx.Done():
				return ctx.Err()
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
