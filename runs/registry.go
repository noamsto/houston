package runs

import (
	"context"
	"encoding/base64"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/noamsto/houston/chat"
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
	lastSig    map[string]signature      // key -> signature of the last broadcast update
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
		lastSig:    map[string]signature{},
		subs:       map[chan Run]*sub{},
	}
}

// Apply records a source's layer and broadcasts the recomposed run.
func (r *Registry) Apply(d Delta) {
	r.mu.Lock()
	keys := []string{d.Key}
	// A layer that names a session can shadow that session's history card, so
	// its old and new Session/CrewSession both need re-settling.
	named := func(l Run) {
		for _, sid := range []string{l.CrewSession, l.Session} {
			if sid != "" {
				keys = append(keys, sessionKeyFor(sid))
			}
		}
	}
	if old, ok := r.layers[d.Key][d.Source]; ok {
		named(old)
	}
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
		named(d.Run)
	}

	var payloads []Run
	for _, key := range keys {
		if p, ok := r.settleLocked(key); ok {
			payloads = append(payloads, p)
		}
	}
	r.mu.Unlock()

	if len(payloads) == 0 {
		return
	}

	// Fan out under RLock, as before. Unsubscribe takes the write lock to
	// close a channel, so it cannot close one while we hold this — which is
	// what stops a send racing a close and panicking.
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, payload := range payloads {
		for _, sb := range r.subs {
			select {
			case sb.ch <- payload:
			default: // a slow subscriber drops updates; flag it, never block the source
				sb.dropped.Store(true)
			}
		}
	}
}

// settleLocked recomposes one key and returns the update to broadcast, if any.
// Caller holds mu.
func (r *Registry) settleLocked(key string) (Run, bool) {
	composed, live := r.composeLocked(key)

	// A key is listed only while some layer describes it AND the composed run
	// has an agent. Removal fires on the listed -> not-listed edge, which
	// covers both "last layer gone" and "agent lost", and means a plain shell
	// emits nothing ever. The edge is load-bearing: without it, a hooks layer
	// going Gone while the tmux layer survives leaves the key live with no
	// agent, so the update is suppressed and nothing tells the subscriber.
	listed := live && composed.listed() && !r.shadowedLocked(key)
	was := r.listedKeys[key]
	switch {
	case listed:
		r.listedKeys[key] = true
	case was:
		delete(r.listedKeys, key)
	}

	switch {
	case listed:
		sig := runSignature(composed)
		if prev, ok := r.lastSig[key]; !ok || prev != sig {
			r.lastSig[key] = sig
			return composed, true
		}
	case was:
		delete(r.lastSig, key)
		return Run{ID: idFor(key), Removed: true}, true
	}
	return Run{}, false
}

// sessionKeyFor is the key the hooks layer files a session under when it has
// no pane to trust (see sessionKey in hooksource.go).
func sessionKeyFor(sid string) string { return "claude/" + sid }

// shadowedLocked reports whether key is a session-keyed history card whose
// session another key's worker card already carries: that key's crew layer
// names it, the bus engine has a chat reader, and the card's hooks layer (if
// any) shows the same session rather than a newer one after /clear. Caller
// holds mu.
func (r *Registry) shadowedLocked(key string) bool {
	sid, ok := strings.CutPrefix(key, "claude/")
	if !ok || sid == "" {
		return false
	}
	for other, bySource := range r.layers {
		crew := bySource["crew"]
		if other == key || crew.CrewSession != sid || chat.For(crew.Agent) == nil {
			continue
		}
		if hs := bySource["hooks"].Session; hs == "" || hs == sid {
			return true
		}
	}
	return false
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
	if crew := bySource["crew"]; out.Session == "" && chat.For(crew.Agent) != nil {
		out.Session = crew.CrewSession
	}
	out.Caps = deriveCaps(bySource)
	// run.go documents "non-nil Question implies State == StateBlocked", and
	// composition is where that invariant must actually hold: hooksource.go
	// keys on the same pane id as the crew layer and sits above it in
	// DefaultOrder, so a worker blocked on `crew await` merges as State ==
	// StateRunning from hooks while the crew layer's Question survives. Forcing
	// it here restores the invariant without touching precedence: a layer that
	// reports blocked without a Question is unaffected.
	if out.Question != nil {
		out.State = StateBlocked
	}
	out.Attention, out.AttentionNote = attentionOf(out, bySource)
	return out, true
}

const tmuxFailedNote = "Its last turn failed — check its terminal."

// attentionOf derives the attention dimension from the composed run and its
// layers. The tmux layer's failed verdict (lazytmux's `error`, written on
// StopFailure and kept until the next prompt) is read from the layer because
// the hooks layer has no StopFailure mapping and would otherwise hide it; the
// crew layer's done/review likewise holds after hooks' idle wins State.
func attentionOf(out Run, bySource map[string]Run) (Attention, string) {
	if out.State == StateBlocked {
		return AttentionNeedsYou, ""
	}
	crew := bySource["crew"]
	tmuxFailed := bySource["tmux"].State == StateFailed
	switch {
	case crew.Attention == AttentionStuck, crew.State == StateFailed:
		return AttentionStuck, crew.AttentionNote
	case out.State == StateFailed || tmuxFailed:
		return AttentionStuck, tmuxFailedNote
	case out.State == StateReview, crew.State == StateDone, crew.State == StateReview:
		return AttentionDone, ""
	}
	return AttentionNone, ""
}

// deriveCaps derives Caps from which layers are present for a key, not from
// any Caps field a source publishes — only TmuxSource's poll is ground truth
// for "a live pane exists right now," since it diffs against a fresh
// ListPaneOptions call every tick and emits Gone the instant a pane
// disappears, whereas HookSource asserts from a cached ref that can outlive
// the pane it names. A composed run can still carry a non-nil Tmux ref
// (hooksource.go's cached ref) alongside Caps.Terminal == false — that's
// intended: Caps is the affordance signal, not Tmux != nil. Chat is likewise
// derived from the hooks layer's own Session and Agent, not the composed
// run's — a lower layer's Agent must never grant it. The one other grant is a
// crew-named session (the bus's engine_session) whose engine, from that same
// bus record, has a reader.
func deriveCaps(bySource map[string]Run) Caps {
	_, hasTmux := bySource["tmux"]
	crew, hasCrew := bySource["crew"]
	hooks, hasHooks := bySource["hooks"]
	return Caps{
		Terminal: hasTmux,
		Kill:     hasTmux,
		Reply:    hasTmux || hasCrew,
		Chat: hasHooks && hooks.Session != "" && chat.For(hooks.Agent) != nil ||
			hasCrew && crew.CrewSession != "" && chat.For(crew.Agent) != nil,
	}
}

// PaneRunID is the Run.ID Fleet gives a tmux pane, so callers outside this
// package never re-derive idFor's encoding.
func PaneRunID(paneID string) string { return idFor(paneID) }

// idFor derives a URL-path-safe Run.ID from a source's correlation key. The
// key itself keeps flowing internally unchanged — it is load-bearing for layer
// bookkeeping — only the externally visible ID differs. Without this, "%307"
// decodes as a path segment to "/07", and "crew/<busDir>/<branch>" or
// "claude/<sid>" contain slashes that break /api/runs/{id} segment routing
// outright.
func idFor(key string) string {
	switch {
	case strings.HasPrefix(key, "%"):
		return "pane-" + strings.TrimPrefix(key, "%")
	case strings.HasPrefix(key, "claude/"):
		return "sess-" + strings.TrimPrefix(key, "claude/")
	case strings.HasPrefix(key, "crew/"):
		b := strings.TrimPrefix(key, "crew/")
		return "crew-" + base64.RawURLEncoding.EncodeToString([]byte(b))
	default:
		return "key-" + base64.RawURLEncoding.EncodeToString([]byte(key))
	}
}

type signature struct {
	agent, state, repo, project, role, branch, worktree string

	attention, attentionNote string

	issueID string

	hasPR                                      bool
	prNumber, prState, prCheck, prMerge, prURL string

	hasCrew                                                bool
	crewName, crewCodename, crewColor, crewTier, crewTitle string
	crewModel, crewDetail                                  string

	hasQuestion bool
	qText, qVia string

	actTool, actHint, actMessage, actTask, actPreview string

	session string

	// background encodes the task list as a string; the struct must stay comparable.
	background string

	stale, capTerminal, capReply, capKill, capChat bool
}

// runSignature is a cheap comparable summary of the fields that matter to a
// subscriber, mirroring hub.broadcastIfChanged: skip fan-out when nothing
// material changed since the last broadcast for this key, so a poller that
// re-emits an unchanged layer every tick does not cost every subscriber an
// SSE event every tick too.
func runSignature(r Run) signature {
	s := signature{
		agent:         r.Agent,
		state:         string(r.State),
		attention:     string(r.Attention),
		attentionNote: r.AttentionNote,
		repo:          r.Repo,
		project:       r.Project,
		role:          r.Role,
		branch:        r.Branch,
		worktree:      r.Worktree,
		actTool:       r.Activity.Tool,
		actHint:       r.Activity.Hint,
		actMessage:    r.Activity.Message,
		actTask:       r.Activity.Task,
		actPreview:    r.Activity.Preview,
		session:       r.Session,
		stale:         r.Stale,
		capTerminal:   r.Caps.Terminal,
		capReply:      r.Caps.Reply,
		capKill:       r.Caps.Kill,
		capChat:       r.Caps.Chat,
	}
	for _, t := range r.Background {
		s.background += t.ID + "\x00"
	}
	if r.Issue != nil {
		s.issueID = r.Issue.ID
	}
	if r.PR != nil {
		s.hasPR = true
		s.prNumber, s.prState, s.prCheck, s.prMerge, s.prURL = r.PR.Number, r.PR.State, r.PR.CheckState, r.PR.Mergeable, r.PR.URL
	}
	if r.Crew != nil {
		s.hasCrew = true
		s.crewName, s.crewCodename, s.crewColor, s.crewTier = r.Crew.Name, r.Crew.Codename, r.Crew.Color, r.Crew.Tier
		s.crewTitle, s.crewModel, s.crewDetail = r.Crew.Title, r.Crew.Model, r.Crew.Detail
	}
	if r.Question != nil {
		s.hasQuestion = true
		s.qText, s.qVia = r.Question.Text, r.Question.Via
	}
	return s
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
	// First opinion wins, unlike the fields around it: a hook's cwd drifts
	// (a `cd` into another checkout) and must not overwrite the project a
	// lower layer took from the window's @git_root.
	if src.Project != "" && dst.Project == "" {
		dst.Project = src.Project
	}
	// First opinion wins, like Project.
	if src.Session != "" && dst.Session == "" {
		dst.Session = src.Session
	}
	// A dispatcher window also carries a crew record, so the crew source's
	// "worker" must not demote it.
	if src.Role != "" && (src.Role != RoleWorker || dst.Role != RoleDispatcher) {
		dst.Role = src.Role
	}
	if src.Branch != "" {
		dst.Branch = src.Branch
	}
	if src.Worktree != "" {
		dst.Worktree = src.Worktree
	}
	if src.Tmux != nil {
		// Field-wise when both layers name the same pane: a higher layer that
		// publishes a ref with an empty Server (a legacy hook state file written
		// before tmux_server existed) must not erase the server pid the
		// tmux-polling layer recorded. runPane's send-time mismatch check treats
		// an empty Server as "unknown" and never refuses, so erasing it silently
		// disables the guard. A ref for a different pane still wins wholesale:
		// its server pid belongs to a different server incarnation and must not
		// be grafted onto this pane.
		switch {
		case dst.Tmux == nil:
			dst.Tmux = src.Tmux
		case dst.Tmux.PaneID != src.Tmux.PaneID:
			dst.Tmux = src.Tmux
		default:
			// dst.Tmux may alias a lower layer's ref; copy before writing so that
			// layer keeps the values it published.
			merged := *dst.Tmux
			if src.Tmux.Session != "" {
				merged.Session = src.Tmux.Session
			}
			if src.Tmux.Window != 0 {
				merged.Window = src.Tmux.Window
			}
			if src.Tmux.Server != "" {
				merged.Server = src.Tmux.Server
			}
			dst.Tmux = &merged
		}
	}
	if src.Issue != nil {
		dst.Issue = src.Issue
	}
	if src.PR != nil {
		// Field-wise: tmux owns State/CheckState/Mergeable, the crew bus owns
		// URL. Crew sits above tmux in DefaultOrder, so it can only fill fields
		// tmux leaves empty or restate the same number.
		if dst.PR == nil {
			dst.PR = &PRRef{}
		}
		prevNumber := dst.PR.Number
		if src.PR.Number != "" {
			dst.PR.Number = src.PR.Number
		}
		if src.PR.State != "" {
			dst.PR.State = src.PR.State
		}
		if src.PR.CheckState != "" {
			dst.PR.CheckState = src.PR.CheckState
		}
		if src.PR.Mergeable != "" {
			dst.PR.Mergeable = src.PR.Mergeable
		}
		if src.PR.Draft {
			dst.PR.Draft = true
		}
		// A URL for a different PR than tmux reports would make a hybrid.
		if src.PR.URL != "" && (src.PR.Number == "" || prevNumber == "" || src.PR.Number == prevNumber) {
			dst.PR.URL = src.PR.URL
		}
	}
	if src.Crew != nil {
		// Field-wise, not wholesale: tmux owns Codename/Color and the crew bus
		// owns Name/Tier, so two layers can each set half of this struct on the
		// same key. Wholesale replacement would let whichever layer merges last
		// erase the other's half.
		if dst.Crew == nil {
			dst.Crew = &CrewRef{}
		}
		if src.Crew.Name != "" {
			dst.Crew.Name = src.Crew.Name
		}
		if src.Crew.Codename != "" {
			dst.Crew.Codename = src.Crew.Codename
		}
		if src.Crew.Color != "" {
			dst.Crew.Color = src.Crew.Color
		}
		if src.Crew.Tier != "" {
			dst.Crew.Tier = src.Crew.Tier
		}
		if src.Crew.Title != "" {
			dst.Crew.Title = src.Crew.Title
		}
		if src.Crew.Model != "" {
			dst.Crew.Model = src.Crew.Model
		}
		if src.Crew.Detail != "" {
			dst.Crew.Detail = src.Crew.Detail
		}
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
	// Only the hooks layer sets Background, so "no opinion" is safe to treat
	// as "keep the lower layer's list" like Trail above.
	if len(src.Background) > 0 {
		dst.Background = append([]BackgroundTask(nil), src.Background...)
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
		if run, ok := r.composeLocked(k); ok && run.listed() && !r.shadowedLocked(k) {
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
