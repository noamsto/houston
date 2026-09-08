package runs

import (
	"context"
	"sort"
	"sync"
)

// Delta is one source's view of one run. A source publishes only the fields it
// owns; everything else stays zero and is filled by another layer.
type Delta struct {
	Source string
	Key    string // correlation key — the tmux pane id where there is one
	Run    Run
	Gone   bool // this source no longer describes Key
}

// Source produces Deltas until ctx is cancelled.
type Source interface {
	Name() string
	Run(ctx context.Context, out chan<- Delta) error
}

// DefaultOrder is the precedence, lowest first. Hooks win because they fire
// synchronously with the event they describe; tmux options lose because they
// are a periodic scrape.
var DefaultOrder = []string{"tmux", "crew", "hooks"}

// Registry composes per-source layers into one Run per key.
type Registry struct {
	order map[string]int

	mu     sync.RWMutex
	layers map[string]map[string]Run // key -> source -> layer
	subs   map[chan Run]struct{}
}

func NewRegistry(order []string) *Registry {
	idx := make(map[string]int, len(order))
	for i, name := range order {
		idx[name] = i
	}
	return &Registry{
		order:  idx,
		layers: map[string]map[string]Run{},
		subs:   map[chan Run]struct{}{},
	}
}

// Apply records a source's layer and broadcasts the recomposed run.
func (r *Registry) Apply(d Delta) {
	r.mu.Lock()
	if d.Gone {
		if bySource, ok := r.layers[d.Key]; ok {
			delete(bySource, d.Source)
			if len(bySource) == 0 {
				delete(r.layers, d.Key)
			}
		}
	} else {
		if r.layers[d.Key] == nil {
			r.layers[d.Key] = map[string]Run{}
		}
		r.layers[d.Key][d.Source] = d.Run
	}
	composed, live := r.composeLocked(d.Key)
	r.mu.Unlock()

	if !live {
		return
	}

	// Fan out under RLock, as hub.broadcast does. Unsubscribe takes the write
	// lock to close a channel, so it cannot close one while we hold this —
	// which is what stops a send racing a close and panicking.
	r.mu.RLock()
	defer r.mu.RUnlock()
	for ch := range r.subs {
		select {
		case ch <- composed:
		default: // a slow subscriber drops updates, never blocks the source
		}
	}
}

// composeLocked merges one key's layers in precedence order. Caller holds mu.
func (r *Registry) composeLocked(key string) (Run, bool) {
	bySource, ok := r.layers[key]
	if !ok || len(bySource) == 0 {
		return Run{}, false
	}

	names := make([]string, 0, len(bySource))
	for name := range bySource {
		names = append(names, name)
	}
	// A name absent from order is strictly lowest precedence (merged first),
	// rather than tying with index 0 and landing wherever the unstable sort
	// happens to put it.
	rank := func(name string) int {
		if i, ok := r.order[name]; ok {
			return i
		}
		return -1
	}
	sort.Slice(names, func(i, j int) bool { return rank(names[i]) < rank(names[j]) })

	var out Run
	for _, name := range names {
		mergeInto(&out, bySource[name])
	}
	out.ID = key
	return out, true
}

// mergeInto copies every set field of src over dst. A zero field means "this
// source has no opinion", so it never blanks a value a lower layer supplied.
func mergeInto(dst *Run, src Run) {
	if src.Host != "" {
		dst.Host = src.Host
	}
	if src.Agent != "" {
		dst.Agent = src.Agent
	}
	if src.State != "" {
		dst.State = src.State
	}
	if src.Repo != "" {
		dst.Repo = src.Repo
	}
	if src.Branch != "" {
		dst.Branch = src.Branch
	}
	if src.Worktree != "" {
		dst.Worktree = src.Worktree
	}
	if src.Tmux != nil {
		dst.Tmux = src.Tmux
	}
	if src.Issue != nil {
		dst.Issue = src.Issue
	}
	if src.PR != nil {
		dst.PR = src.PR
	}
	if src.Crew != nil {
		dst.Crew = src.Crew
	}
	if src.Question != nil {
		dst.Question = src.Question
	}
	if src.Activity.Tool != "" {
		dst.Activity.Tool = src.Activity.Tool
	}
	if src.Activity.Hint != "" {
		dst.Activity.Hint = src.Activity.Hint
	}
	if src.Activity.Message != "" {
		dst.Activity.Message = src.Activity.Message
	}
	if src.Activity.Task != "" {
		dst.Activity.Task = src.Activity.Task
	}
	if len(src.Activity.Trail) > 0 {
		dst.Activity.Trail = append([]TrailChip(nil), src.Activity.Trail...)
	}
	if src.Activity.Preview != "" {
		dst.Activity.Preview = src.Activity.Preview
	}
	if src.Activity.Turn != 0 {
		dst.Activity.Turn = src.Activity.Turn
	}
	if src.Tokens.Input != 0 {
		dst.Tokens.Input = src.Tokens.Input
	}
	if src.Tokens.Output != 0 {
		dst.Tokens.Output = src.Tokens.Output
	}
	if src.Since != 0 {
		dst.Since = src.Since
	}
	if src.UpdatedAt > dst.UpdatedAt {
		dst.UpdatedAt = src.UpdatedAt
	}
	if src.Stale {
		dst.Stale = true
	}
	if src.Caps.Terminal {
		dst.Caps.Terminal = true
	}
	if src.Caps.Reply {
		dst.Caps.Reply = true
	}
	if src.Caps.Kill {
		dst.Caps.Kill = true
	}
}

func (r *Registry) Snapshot() []Run {
	r.mu.RLock()
	defer r.mu.RUnlock()

	keys := make([]string, 0, len(r.layers))
	for k := range r.layers {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	out := make([]Run, 0, len(keys))
	for _, k := range keys {
		if run, ok := r.composeLocked(k); ok {
			out = append(out, run)
		}
	}
	return out
}

func (r *Registry) Subscribe() chan Run {
	ch := make(chan Run, 64)
	r.mu.Lock()
	r.subs[ch] = struct{}{}
	r.mu.Unlock()
	return ch
}

func (r *Registry) Unsubscribe(ch chan Run) {
	r.mu.Lock()
	if _, ok := r.subs[ch]; ok {
		delete(r.subs, ch)
		close(ch)
	}
	r.mu.Unlock()
}
