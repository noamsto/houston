package runs

import (
	"context"
	"encoding/base64"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
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

// sub is one subscriber's channel plus whether an update was dropped since the
// last check. dropped is atomic because the hot fan-out path in Apply only
// holds RLock — it must not need the write lock just to flag a drop.
type sub struct {
	ch      chan Run
	dropped atomic.Bool
}

// Registry composes per-source layers into one Run per key.
type Registry struct {
	order map[string]int

	mu         sync.RWMutex
	layers     map[string]map[string]Run // key -> source -> layer
	listedKeys map[string]bool           // key -> currently listed (an agent run)
	lastSig    map[string]string         // key -> signature of the last broadcast update
	subs       map[chan Run]*sub
}

func NewRegistry(order []string) *Registry {
	idx := make(map[string]int, len(order))
	for i, name := range order {
		idx[name] = i
	}
	return &Registry{
		order:      idx,
		layers:     map[string]map[string]Run{},
		listedKeys: map[string]bool{},
		lastSig:    map[string]string{},
		subs:       map[chan Run]*sub{},
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

	// A key is listed only while some layer describes it AND the composed run
	// has an agent. Removal fires on the listed -> not-listed edge, which
	// covers both "last layer gone" and "agent lost", and means a plain shell
	// emits nothing ever. The edge is load-bearing: without it, a hooks layer
	// going Gone while the tmux layer survives leaves the key live with no
	// agent, so the update is suppressed and nothing tells the subscriber.
	listed := live && composed.listed()
	was := r.listedKeys[d.Key]
	switch {
	case listed:
		r.listedKeys[d.Key] = true
	case was:
		delete(r.listedKeys, d.Key)
	}

	var payload Run
	broadcast := false

	switch {
	case listed:
		sig := runSignature(composed)
		if r.lastSig[d.Key] != sig {
			r.lastSig[d.Key] = sig
			payload = composed
			broadcast = true
		}
	case was:
		delete(r.lastSig, d.Key)
		payload = Run{ID: idFor(d.Key), Removed: true}
		broadcast = true
	}
	r.mu.Unlock()

	if !broadcast {
		return
	}

	// Fan out under RLock, as before. Unsubscribe takes the write lock to
	// close a channel, so it cannot close one while we hold this — which is
	// what stops a send racing a close and panicking.
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, sb := range r.subs {
		select {
		case sb.ch <- payload:
		default: // a slow subscriber drops updates; flag it, never block the source
			sb.dropped.Store(true)
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
	out.ID = idFor(key)
	return out, true
}

// idFor derives a URL-path-safe Run.ID from a source's correlation key. The
// key itself keeps flowing internally unchanged — it is load-bearing for layer
// bookkeeping — only the externally visible ID differs. Without this, "%307"
// decodes as a path segment to "/07", and "branch/fix/412" or "claude/<sid>"
// contain slashes that break /api/runs/{id} segment routing outright.
func idFor(key string) string {
	switch {
	case strings.HasPrefix(key, "%"):
		return "pane-" + strings.TrimPrefix(key, "%")
	case strings.HasPrefix(key, "claude/"):
		return "sess-" + strings.TrimPrefix(key, "claude/")
	case strings.HasPrefix(key, "branch/"):
		b := strings.TrimPrefix(key, "branch/")
		return "branch-" + base64.RawURLEncoding.EncodeToString([]byte(b))
	default:
		return "key-" + base64.RawURLEncoding.EncodeToString([]byte(key))
	}
}

// runSignature is a cheap comparable summary of the fields that matter to a
// subscriber, mirroring hub.broadcastIfChanged: skip fan-out when nothing
// material changed since the last broadcast for this key, so a poller that
// re-emits an unchanged layer every tick does not cost every subscriber an
// SSE event every tick too.
func runSignature(r Run) string {
	var b strings.Builder
	b.WriteString(r.Agent)
	b.WriteByte('|')
	b.WriteString(string(r.State))
	b.WriteByte('|')
	b.WriteString(r.Repo)
	b.WriteByte('|')
	b.WriteString(r.Branch)
	b.WriteByte('|')
	b.WriteString(r.Worktree)
	b.WriteByte('|')
	if r.Issue != nil {
		b.WriteString(r.Issue.ID)
	}
	b.WriteByte('|')
	if r.PR != nil {
		b.WriteString(r.PR.Number + "," + r.PR.State + "," + r.PR.CheckState + "," + r.PR.Mergeable)
	}
	b.WriteByte('|')
	if r.Crew != nil {
		b.WriteString(r.Crew.Name + "," + r.Crew.Tier)
	}
	b.WriteByte('|')
	if r.Question != nil {
		b.WriteString(r.Question.Text)
	}
	b.WriteByte('|')
	b.WriteString(r.Activity.Tool + "," + r.Activity.Hint + "," + r.Activity.Message + "," + r.Activity.Task + "," + r.Activity.Preview)
	return b.String()
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
		if run, ok := r.composeLocked(k); ok && run.listed() {
			out = append(out, run)
		}
	}
	return out
}

func (r *Registry) Subscribe() chan Run {
	ch := make(chan Run, 64)
	r.mu.Lock()
	r.subs[ch] = &sub{ch: ch}
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

// Dirty reports whether ch missed at least one update since the last call,
// and clears the flag. The SSE handler uses this on its keepalive tick to
// decide whether a bare ping is enough or a full resync is owed.
func (r *Registry) Dirty(ch chan Run) bool {
	r.mu.RLock()
	sb, ok := r.subs[ch]
	r.mu.RUnlock()
	if !ok {
		return false
	}
	return sb.dropped.Swap(false)
}
