# Run model, source registry and /api/runs — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the agent run — not the tmux pane — the thing houston is a list of, and expose it at `/api/runs` **alongside** the existing routes so nothing breaks while the UI still uses them.

**Architecture:** One `Run` struct with a single state vocabulary. Sources publish sparse per-source *layers*; a registry composes them by a fixed precedence, so "which source wins for this field" is one ordered list rather than logic scattered across writers. Three sources ship here: Claude Code hooks (via the existing hub), tmux user-options written by lazytmux, and the dispatcher crew bus. Runs correlate on tmux pane id, which every source can produce or resolve.

**Tech Stack:** Go stdlib, plus the existing `fsnotify` and `gorilla/websocket`. No new dependencies. No frontend changes.

**Spec:** `docs/superpowers/specs/2026-09-07-houston-overhaul-design.md` — sections "The `Run` model", "Sources, correlation and merge", "API surface".

## Global Constraints

- **Additive only.** `/api/sessions`, `/api/agents`, `/api/pane/*` keep working
  exactly as they do. Deleting them is a separate plan, after the UI moves.
- **No frontend changes.** `ui/` is untouched.
- **No new dependencies.**
- **Every new route sits behind the existing auth gate**, i.e. registered on the
  same `apiMux` that `s.auth.middleware` wraps. A new route added outside it
  would reopen the hole closed in #4.
- **Tests:** `go test ./...` from the repo root. NOTE: `server/agents_test.go:90`
  has a pre-existing data race — run the server package WITHOUT `-race`, and use
  `-race` scoped with `-run` to your own tests. Tracked in issue #6.
- `go build ./...` needs `ui/dist`; use `just build`, or build `./runs/...` and
  `./server/...` directly.

---

## File Structure

| File | Responsibility | Change |
|---|---|---|
| `runs/run.go` | `Run` and its sub-structs; no logic | **Create** |
| `runs/state.go` | The single state vocabulary + the three mappings into it | **Create** |
| `runs/registry.go` | `Source`, `Delta`, layered `Registry`, precedence, correlation | **Create** |
| `runs/hooksource.go` | Claude Code hook state, via the existing hub | **Create** |
| `runs/tmuxsource.go` | lazytmux's tmux user-options | **Create** |
| `runs/crewsource.go` | dispatcher crew bus | **Create** |
| `tmux/options.go` | Two format-string queries for window/pane user-options | **Create** |
| `server/runs_api.go` | `GET /api/runs`, `GET /api/runs/stream` | **Create** |
| `server/server.go` | Build the registry, register the two routes | Modify |

A new `runs/` package rather than growing `server/`: the registry has no HTTP
concerns, and keeping it importable without `server` is what will let a future
peer-federation source live beside it.

---

### Task 1: The Run model and one state vocabulary

Three vocabularies exist today and disagree. This is the only place they are
reconciled.

**Files:**
- Create: `runs/run.go`, `runs/state.go`
- Test: `runs/state_test.go`

**Interfaces:**
- Produces: `Run`, `State` + its constants, `TmuxRef`, `IssueRef`, `PRRef`,
  `CrewRef`, `Activity`, `Question`, `Tokens`, `Caps`,
  `FromHookState(hook.State) State`, `FromCrewState(string) State`,
  `FromClaudeStatus(string) State`.

- [ ] **Step 1: Write the failing test**

Create `runs/state_test.go`:

```go
package runs

import (
	"testing"

	"github.com/noamsto/houston/hook"
)

func TestFromHookState(t *testing.T) {
	tests := map[hook.State]State{
		hook.StateThinking:    StateThinking,
		hook.StateStarting:    StateThinking,
		hook.StateToolRunning: StateRunning,
		hook.StateWaiting:     StateBlocked,
		hook.StatePermission:  StateBlocked,
		hook.StateCompacting:  StateCompacting,
		hook.StateEnded:       StateDone,
	}
	for in, want := range tests {
		if got := FromHookState(in); got != want {
			t.Errorf("FromHookState(%q) = %q, want %q", in, got, want)
		}
	}
	if got := FromHookState(hook.State("nonsense")); got != StateIdle {
		t.Errorf("unknown hook state = %q, want %q", got, StateIdle)
	}
}

func TestFromCrewState(t *testing.T) {
	tests := map[string]State{
		"working": StateRunning,
		"blocked": StateBlocked,
		"pr_open": StateReview,
		"done":    StateDone,
		"failed":  StateFailed,
		"exited":  StateFailed,
	}
	for in, want := range tests {
		if got := FromCrewState(in); got != want {
			t.Errorf("FromCrewState(%q) = %q, want %q", in, got, want)
		}
	}
	if got := FromCrewState(""); got != StateIdle {
		t.Errorf("empty crew state = %q, want %q", got, StateIdle)
	}
}

func TestFromClaudeStatus(t *testing.T) {
	// The value is lazytmux's "@claude_status", whose first field is the state.
	tests := map[string]State{
		"processing 1788848628 ":  StateRunning,
		"waiting 1788848628 1":    StateBlocked,
		"denied 1788848628 ":      StateBlocked,
		"compacting 1788848628 ":  StateCompacting,
		"error 1788848628 ":       StateFailed,
		"done 1788848628 ":        StateDone,
		"idle 1788846509 ":        StateIdle,
		"interrupted 1788846509 ": StateIdle,
		"":                        StateIdle,
	}
	for in, want := range tests {
		if got := FromClaudeStatus(in); got != want {
			t.Errorf("FromClaudeStatus(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBlockedIsTheOnlyNeedsYouState(t *testing.T) {
	// Exactly one state means "a human is required". The badge, the sort order
	// and (later) push notifications all key off this, so nothing else may
	// claim it.
	needsYou := 0
	for _, s := range AllStates() {
		if s.NeedsAttention() {
			needsYou++
		}
	}
	if needsYou != 1 {
		t.Fatalf("%d states report NeedsAttention, want exactly 1 (blocked)", needsYou)
	}
	if !StateBlocked.NeedsAttention() {
		t.Fatal("blocked does not report NeedsAttention")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./runs/ -v`
Expected: FAIL to compile — the package does not exist.

- [ ] **Step 3: Write `runs/run.go`**

```go
// Package runs models one agent run — a Claude Code, Codex, Cursor, amp,
// OpenCode or pi session — independent of where it happens to be running.
package runs

// Run is one agent run, anywhere. It is the only object the UI lists.
type Run struct {
	ID    string `json:"id"`
	Host  string `json:"host,omitempty"` // "" means local
	Agent string `json:"agent"`
	State State  `json:"state"`

	Repo     string `json:"repo,omitempty"`
	Branch   string `json:"branch,omitempty"`
	Worktree string `json:"worktree,omitempty"`

	Tmux  *TmuxRef  `json:"tmux,omitempty"`
	Issue *IssueRef `json:"issue,omitempty"`
	PR    *PRRef    `json:"pr,omitempty"`
	Crew  *CrewRef  `json:"crew,omitempty"`

	Activity Activity  `json:"activity"`
	Question *Question `json:"question,omitempty"`
	Tokens   Tokens    `json:"tokens"`

	Since     int64 `json:"since,omitempty"`
	UpdatedAt int64 `json:"updated_at"`

	// Stale means a source stopped reporting. The Run keeps its last known
	// values and says so; it is never a State, because a stale run still has
	// one.
	Stale bool `json:"stale,omitempty"`

	Caps Caps `json:"caps"`
}

// TmuxRef locates a run's pane. Nil for agents with no pane.
type TmuxRef struct {
	Session string `json:"session"`
	Window  int    `json:"window"`
	PaneID  string `json:"pane_id"` // "%307"
}

type IssueRef struct {
	ID       string `json:"id"` // "#320"
	Title    string `json:"title,omitempty"`
	URL      string `json:"url,omitempty"`
	Provider string `json:"provider,omitempty"`
}

type PRRef struct {
	Number     string `json:"number"`
	State      string `json:"state,omitempty"`
	CheckState string `json:"check_state,omitempty"`
	Mergeable  string `json:"mergeable,omitempty"`
	Draft      bool   `json:"draft,omitempty"`
	URL        string `json:"url,omitempty"`
}

type CrewRef struct {
	Name  string `json:"name"`
	Color string `json:"color,omitempty"`
	Tier  string `json:"tier,omitempty"`
}

// Activity is what the run is doing right now.
type Activity struct {
	Tool    string      `json:"tool,omitempty"`
	Hint    string      `json:"hint,omitempty"`
	Message string      `json:"message,omitempty"`
	Task    string      `json:"task,omitempty"`
	Trail   []TrailChip `json:"trail,omitempty"`
	Preview string      `json:"preview,omitempty"`
	Turn    int         `json:"turn,omitempty"`
}

type TrailChip struct {
	Tool    string `json:"tool"`
	Hint    string `json:"hint,omitempty"`
	Done    bool   `json:"done"`
	IsError bool   `json:"error,omitempty"`
}

// Question is the thing a human answers. Non-nil implies State == StateBlocked.
type Question struct {
	Text string `json:"text"`
	// Via is how an answer is delivered: "pane" or "crew".
	Via string `json:"via"`
}

type Tokens struct {
	Input  int `json:"input"`
	Output int `json:"output"`
}

// Caps is what this run supports right now. The UI renders affordances from
// capabilities rather than branching on Agent, so a new agent needs no UI change.
type Caps struct {
	Terminal bool `json:"terminal"`
	Reply    bool `json:"reply"`
	Kill     bool `json:"kill"`
}
```

- [ ] **Step 4: Write `runs/state.go`**

```go
package runs

import (
	"strings"

	"github.com/noamsto/houston/hook"
)

// State is houston's single vocabulary for what a run is doing. Three upstream
// vocabularies map into it — Claude Code hooks, the dispatcher crew bus, and
// lazytmux's @claude_status — and they disagree with each other, so this is the
// only place the reconciliation lives.
type State string

const (
	StateThinking   State = "thinking"
	StateRunning    State = "running"
	StateBlocked    State = "blocked"
	StateCompacting State = "compacting"
	StateReview     State = "review"
	StateDone       State = "done"
	StateFailed     State = "failed"
	StateIdle       State = "idle"
)

// AllStates lists every state, for exhaustiveness tests.
func AllStates() []State {
	return []State{
		StateThinking, StateRunning, StateBlocked, StateCompacting,
		StateReview, StateDone, StateFailed, StateIdle,
	}
}

// NeedsAttention reports whether a human is required. Exactly one state says
// yes: it drives the badge, the sort order and later the push notification, so
// nothing else may claim it.
func (s State) NeedsAttention() bool { return s == StateBlocked }

func FromHookState(s hook.State) State {
	switch s {
	case hook.StateThinking, hook.StateStarting:
		return StateThinking
	case hook.StateToolRunning:
		return StateRunning
	case hook.StateWaiting, hook.StatePermission:
		return StateBlocked
	case hook.StateCompacting:
		return StateCompacting
	case hook.StateEnded:
		return StateDone
	default:
		return StateIdle
	}
}

func FromCrewState(s string) State {
	switch s {
	case "working":
		return StateRunning
	case "blocked":
		return StateBlocked
	case "pr_open":
		return StateReview
	case "done":
		return StateDone
	case "failed", "exited":
		return StateFailed
	default:
		return StateIdle
	}
}

// FromClaudeStatus reads lazytmux's @claude_status pane option, whose format is
// "<state> <epoch> <unseen>". Only the first field is a state.
func FromClaudeStatus(v string) State {
	word, _, _ := strings.Cut(strings.TrimSpace(v), " ")
	switch word {
	case "processing":
		return StateRunning
	case "waiting", "denied":
		return StateBlocked
	case "compacting":
		return StateCompacting
	case "error":
		return StateFailed
	case "done":
		return StateDone
	default:
		// Covers "idle", "interrupted" and the empty string. Interrupting is
		// something the user did deliberately, so it must not raise a badge.
		return StateIdle
	}
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./runs/ -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add runs/
git commit -m "feat(runs): Run model and one state vocabulary"
```

---

### Task 2: Layered registry with fixed precedence

Sources never see each other. Each publishes a sparse `Run` as its own layer;
the registry composes layers in a fixed order. Precedence is therefore one
ordered list rather than merge logic spread across writers.

**Files:**
- Create: `runs/registry.go`
- Test: `runs/registry_test.go`

**Interfaces:**
- Consumes: `Run`, `State` from Task 1.
- Produces:
  - `type Delta struct { Source, Key string; Run Run; Gone bool }`
  - `type Source interface { Name() string; Run(ctx context.Context, out chan<- Delta) error }`
  - `func NewRegistry(order []string) *Registry`
  - `func (r *Registry) Apply(d Delta)`
  - `func (r *Registry) Snapshot() []Run`
  - `func (r *Registry) Subscribe() chan Run` / `Unsubscribe(chan Run)`
  - `var DefaultOrder = []string{"hooks", "crew", "tmux"}`

- [ ] **Step 1: Write the failing test**

Create `runs/registry_test.go`:

```go
package runs

import (
	"testing"
)

func TestPrecedenceHooksBeatTmux(t *testing.T) {
	r := NewRegistry(DefaultOrder)
	r.Apply(Delta{Source: "tmux", Key: "%1", Run: Run{State: StateIdle, Branch: "main"}})
	r.Apply(Delta{Source: "hooks", Key: "%1", Run: Run{State: StateRunning}})

	got := r.Snapshot()
	if len(got) != 1 {
		t.Fatalf("%d runs, want 1 — both deltas key on the same pane", len(got))
	}
	if got[0].State != StateRunning {
		t.Errorf("State = %q, want %q — hooks outrank tmux", got[0].State, StateRunning)
	}
	if got[0].Branch != "main" {
		t.Errorf("Branch = %q, want %q — tmux still supplies fields hooks leaves empty",
			got[0].Branch, "main")
	}
}

func TestLowerPrecedenceCannotBlankAHigherField(t *testing.T) {
	r := NewRegistry(DefaultOrder)
	r.Apply(Delta{Source: "hooks", Key: "%1", Run: Run{State: StateRunning}})
	r.Apply(Delta{Source: "tmux", Key: "%1", Run: Run{State: ""}})

	if got := r.Snapshot()[0].State; got != StateRunning {
		t.Fatalf("State = %q, want %q — an empty field must not overwrite a set one", got, StateRunning)
	}
}

func TestCrewBeatsTmuxButNotHooks(t *testing.T) {
	r := NewRegistry(DefaultOrder)
	r.Apply(Delta{Source: "tmux", Key: "%1", Run: Run{State: StateIdle}})
	r.Apply(Delta{Source: "crew", Key: "%1", Run: Run{State: StateBlocked}})
	if got := r.Snapshot()[0].State; got != StateBlocked {
		t.Fatalf("State = %q, want %q", got, StateBlocked)
	}

	r.Apply(Delta{Source: "hooks", Key: "%1", Run: Run{State: StateRunning}})
	if got := r.Snapshot()[0].State; got != StateRunning {
		t.Fatalf("State = %q, want %q", got, StateRunning)
	}
}

func TestGoneRemovesOnlyThatSourcesLayer(t *testing.T) {
	r := NewRegistry(DefaultOrder)
	r.Apply(Delta{Source: "tmux", Key: "%1", Run: Run{Branch: "main"}})
	r.Apply(Delta{Source: "hooks", Key: "%1", Run: Run{State: StateRunning}})

	r.Apply(Delta{Source: "hooks", Key: "%1", Gone: true})

	got := r.Snapshot()
	if len(got) != 1 {
		t.Fatalf("%d runs, want 1 — the tmux layer still describes this pane", len(got))
	}
	if got[0].Branch != "main" {
		t.Errorf("Branch = %q, want main", got[0].Branch)
	}

	r.Apply(Delta{Source: "tmux", Key: "%1", Gone: true})
	if n := len(r.Snapshot()); n != 0 {
		t.Fatalf("%d runs after the last layer went away, want 0", n)
	}
}

func TestSubscribeReceivesComposedRun(t *testing.T) {
	r := NewRegistry(DefaultOrder)
	sub := r.Subscribe()
	defer r.Unsubscribe(sub)

	r.Apply(Delta{Source: "tmux", Key: "%1", Run: Run{Branch: "main"}})
	r.Apply(Delta{Source: "hooks", Key: "%1", Run: Run{State: StateRunning}})

	var last Run
	for i := 0; i < 2; i++ {
		select {
		case last = <-sub:
		default:
			t.Fatalf("only %d broadcasts received", i)
		}
	}
	if last.State != StateRunning || last.Branch != "main" {
		t.Fatalf("subscriber got %+v, want the composed run, not one layer", last)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./runs/ -run 'TestPrecedence|TestLower|TestCrew|TestGone|TestSubscribe' -v`
Expected: FAIL to compile — `undefined: NewRegistry`.

- [ ] **Step 3: Write the implementation**

Create `runs/registry.go`:

```go
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
	subs := make([]chan Run, 0, len(r.subs))
	for ch := range r.subs {
		subs = append(subs, ch)
	}
	r.mu.Unlock()

	if !live {
		return
	}
	for _, ch := range subs {
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
	sort.Slice(names, func(i, j int) bool { return r.order[names[i]] < r.order[names[j]] })

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
		dst.Activity.Trail = src.Activity.Trail
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
```

Note `composeLocked` is called from `Snapshot` under `RLock` and from `Apply`
under `Lock`. It only reads, so both are safe.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./runs/ -race -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add runs/registry.go runs/registry_test.go
git commit -m "feat(runs): layered registry with fixed source precedence"
```

---

### Task 3: `hooksource` — Claude Code hooks via the existing hub

The hub already watches `<state-dir>/claude/` and tails transcripts. Reuse it
rather than duplicating that machinery: `hooksource` subscribes and converts.

**Files:**
- Create: `runs/hooksource.go`
- Test: `runs/hooksource_test.go`

**Interfaces:**
- Consumes: `Delta`, `Source`, `FromHookState` from Tasks 1-2.
- Produces: `func NewHookSource(h *hub.Hub) *HookSource`, and
  `func runFromSessionView(v hub.SessionView) (key string, r Run)` — exported
  within the package for the test.

- [ ] **Step 1: Write the failing test**

Create `runs/hooksource_test.go`:

```go
package runs

import (
	"testing"

	"github.com/noamsto/houston/hook"
	"github.com/noamsto/houston/hub"
)

func TestRunFromSessionViewKeysOnPane(t *testing.T) {
	key, r := runFromSessionView(hub.SessionView{
		SessionID:   "abc-123",
		TmuxSession: "houston",
		TmuxWindow:  "1",
		TmuxPane:    "%307",
		State:       hook.StateToolRunning,
		Tool:        "Bash",
	})

	if key != "%307" {
		t.Fatalf("key = %q, want the tmux pane id — that is what every source correlates on", key)
	}
	if r.State != StateRunning {
		t.Errorf("State = %q, want %q", r.State, StateRunning)
	}
	if r.Tmux == nil || r.Tmux.PaneID != "%307" || r.Tmux.Session != "houston" || r.Tmux.Window != 1 {
		t.Errorf("TmuxRef = %+v, want session houston window 1 pane %%307", r.Tmux)
	}
	if r.Agent != "claude" {
		t.Errorf("Agent = %q, want claude", r.Agent)
	}
	if !r.Caps.Terminal || !r.Caps.Reply {
		t.Errorf("Caps = %+v, want terminal and reply for a pane-backed run", r.Caps)
	}
}

func TestRunFromSessionViewFallsBackToSessionID(t *testing.T) {
	key, r := runFromSessionView(hub.SessionView{SessionID: "abc-123"})

	if key != "claude/abc-123" {
		t.Fatalf("key = %q, want claude/abc-123 when there is no pane", key)
	}
	if r.Tmux != nil {
		t.Errorf("TmuxRef = %+v, want nil without a pane", r.Tmux)
	}
	if r.Caps.Terminal {
		t.Error("Caps.Terminal is true without a pane — there is nothing to attach to")
	}
}

func TestRunFromSessionViewCarriesQuestionWhenBlocked(t *testing.T) {
	_, r := runFromSessionView(hub.SessionView{
		SessionID:   "abc",
		TmuxPane:    "%1",
		State:       hook.StatePermission,
		LastMessage: "Allow Bash(rm -rf)?",
	})
	if r.State != StateBlocked {
		t.Fatalf("State = %q, want blocked", r.State)
	}
	if r.Question == nil || r.Question.Text != "Allow Bash(rm -rf)?" {
		t.Fatalf("Question = %+v, want the permission prompt", r.Question)
	}
	if r.Question.Via != "pane" {
		t.Errorf("Question.Via = %q, want pane", r.Question.Via)
	}
}
```

- [ ] **Step 2: Check whether `hub.SessionView` exposes the pane**

Run: `rg -n 'TmuxPane' hub/hub.go`

`hub.SessionView` currently carries `TmuxSession` and `TmuxWindow` but **not**
`TmuxPane`, while `hook.SessionState` does. Add the field to `hub.SessionView`
and populate it wherever `TmuxSession`/`TmuxWindow` are set — it is the
correlation key every other source needs:

```go
	TmuxPane    string `json:"tmux_pane,omitempty"`
```

- [ ] **Step 3: Run the test to verify it fails**

Run: `go test ./runs/ -run TestRunFromSessionView -v`
Expected: FAIL to compile — `undefined: runFromSessionView`.

- [ ] **Step 4: Write the implementation**

Create `runs/hooksource.go`:

```go
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
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./runs/ -race -v` and `go test ./hub/...`
Expected: PASS both.

- [ ] **Step 6: Commit**

```bash
git add runs/hooksource.go runs/hooksource_test.go hub/hub.go
git commit -m "feat(runs): hook source over the existing hub"
```

---

### Task 4: `tmuxsource` — harvest lazytmux's user-options

lazytmux already computes PR state, issue identity, crew membership, branch and
worktree for every window, keeps them fresh, and parks them in tmux. Houston
reads them; it does not recompute any of it.

**Verified working on a live server** — two calls harvest everything:

```
$ tmux list-windows -a -F '#{session_name}|#{window_index}|#{@branch}|#{@issue_id}|#{@pr_number}|#{@crew_name}|#{@window_task}'
lazytmux|2|feat/320-relay-sixel-and-osc-1337-graphics-for-no|#320|565|mauve|

$ tmux list-panes -a -F '#{pane_id}|#{session_name}:#{window_index}|#{@claude_status}|#{@claude_task}'
%307|houston:1|processing 1788848628 |and add a proper ci flows task for later
```

**Files:**
- Create: `tmux/options.go`, `runs/tmuxsource.go`
- Test: `tmux/options_test.go`, `runs/tmuxsource_test.go`

**Interfaces:**
- Produces:
  - `tmux.WindowOptions` / `tmux.PaneOptions` structs
  - `func (c *Client) ListWindowOptions() ([]WindowOptions, error)`
  - `func (c *Client) ListPaneOptions() ([]PaneOptions, error)`
  - `func ParseWindowOptions(out string) []WindowOptions` (pure, for tests)
  - `func ParsePaneOptions(out string) []PaneOptions` (pure, for tests)
  - `func NewTmuxSource(c *tmux.Client, every time.Duration) *TmuxSource`

- [ ] **Step 1: Write the failing parser test**

Create `tmux/options_test.go`:

```go
package tmux

import "testing"

func TestParseWindowOptions(t *testing.T) {
	// Real output shape, including the common all-empty-options case.
	out := "lazytmux|2|feat/320-relay|#320|565|mauve|open|passing|MERGEABLE||\n" +
		"houston|1|main||||||||\n"

	got := ParseWindowOptions(out)
	if len(got) != 2 {
		t.Fatalf("%d windows, want 2", len(got))
	}

	w := got[0]
	if w.Session != "lazytmux" || w.Window != 2 {
		t.Errorf("target = %s:%d, want lazytmux:2", w.Session, w.Window)
	}
	if w.Branch != "feat/320-relay" || w.IssueID != "#320" || w.PRNumber != "565" {
		t.Errorf("got branch=%q issue=%q pr=%q", w.Branch, w.IssueID, w.PRNumber)
	}
	if w.CrewName != "mauve" || w.PRState != "open" || w.PRCheckState != "passing" {
		t.Errorf("got crew=%q pr_state=%q checks=%q", w.CrewName, w.PRState, w.PRCheckState)
	}

	bare := got[1]
	if bare.Branch != "main" || bare.IssueID != "" || bare.PRNumber != "" {
		t.Errorf("a window with no enrichment should carry only its branch, got %+v", bare)
	}
}

func TestParseWindowOptionsSkipsMalformedLines(t *testing.T) {
	got := ParseWindowOptions("too|few|fields\n\nhouston|1|main||||||||\n")
	if len(got) != 1 {
		t.Fatalf("%d windows, want 1 — short and empty lines are skipped, not fatal", len(got))
	}
}

func TestParsePaneOptions(t *testing.T) {
	out := "%307|houston:1|processing 1788848628 |and add a ci task\n" +
		"%283|dispatcher:1||\n"

	got := ParsePaneOptions(out)
	if len(got) != 2 {
		t.Fatalf("%d panes, want 2", len(got))
	}
	if got[0].PaneID != "%307" || got[0].ClaudeStatus != "processing 1788848628 " {
		t.Errorf("got %+v", got[0])
	}
	if got[0].ClaudeTask != "and add a ci task" {
		t.Errorf("task = %q", got[0].ClaudeTask)
	}
	if got[1].ClaudeStatus != "" {
		t.Errorf("a pane with no claude status should be empty, got %q", got[1].ClaudeStatus)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./tmux/ -run TestParse.*Options -v`
Expected: FAIL to compile — `undefined: ParseWindowOptions`.

- [ ] **Step 3: Write `tmux/options.go`**

```go
package tmux

import (
	"strconv"
	"strings"
)

// windowOptionsFormat harvests every lazytmux window user-option in one call.
// tmux resolves #{@name} inline, so no per-window show-options loop is needed.
const windowOptionsFormat = "#{session_name}|#{window_index}|#{@branch}|#{@issue_id}|" +
	"#{@pr_number}|#{@crew_name}|#{@pr_state}|#{@pr_check_state}|#{@pr_mergeable}|" +
	"#{@window_task}|#{@git_root}"

const paneOptionsFormat = "#{pane_id}|#{session_name}:#{window_index}|#{@claude_status}|#{@claude_task}"

// WindowOptions is lazytmux's per-window enrichment. Every field may be empty:
// a window with no linked issue or PR simply has none.
type WindowOptions struct {
	Session      string
	Window       int
	Branch       string
	IssueID      string
	PRNumber     string
	CrewName     string
	PRState      string
	PRCheckState string
	PRMergeable  string
	Task         string
	GitRoot      string
}

type PaneOptions struct {
	PaneID       string
	Target       string // "session:window"
	ClaudeStatus string
	ClaudeTask   string
}

func (c *Client) ListWindowOptions() ([]WindowOptions, error) {
	out, err := c.output("list-windows", "-a", "-F", windowOptionsFormat)
	if err != nil {
		return nil, err
	}
	return ParseWindowOptions(string(out)), nil
}

func (c *Client) ListPaneOptions() ([]PaneOptions, error) {
	out, err := c.output("list-panes", "-a", "-F", paneOptionsFormat)
	if err != nil {
		return nil, err
	}
	return ParsePaneOptions(string(out)), nil
}

func ParseWindowOptions(out string) []WindowOptions {
	var res []WindowOptions
	for _, line := range strings.Split(out, "\n") {
		f := strings.Split(line, "|")
		if len(f) < 11 {
			continue
		}
		idx, err := strconv.Atoi(f[1])
		if err != nil {
			continue
		}
		res = append(res, WindowOptions{
			Session: f[0], Window: idx, Branch: f[2], IssueID: f[3],
			PRNumber: f[4], CrewName: f[5], PRState: f[6], PRCheckState: f[7],
			PRMergeable: f[8], Task: f[9], GitRoot: f[10],
		})
	}
	return res
}

func ParsePaneOptions(out string) []PaneOptions {
	var res []PaneOptions
	for _, line := range strings.Split(out, "\n") {
		f := strings.Split(line, "|")
		if len(f) < 4 {
			continue
		}
		res = append(res, PaneOptions{
			PaneID: f[0], Target: f[1], ClaudeStatus: f[2], ClaudeTask: f[3],
		})
	}
	return res
}
```

- [ ] **Step 4: Write the source test**

Create `runs/tmuxsource_test.go`:

```go
package runs

import (
	"testing"

	"github.com/noamsto/houston/tmux"
)

func TestDeltasFromTmuxJoinsPanesToWindows(t *testing.T) {
	wins := []tmux.WindowOptions{{
		Session: "lazytmux", Window: 2, Branch: "feat/320",
		IssueID: "#320", PRNumber: "565", PRState: "open", PRCheckState: "passing",
		CrewName: "mauve", GitRoot: "/home/n/git/lazytmux",
	}}
	panes := []tmux.PaneOptions{{
		PaneID: "%459", Target: "lazytmux:2", ClaudeStatus: "processing 1788 ",
	}}

	got := deltasFromTmux(wins, panes)
	if len(got) != 1 {
		t.Fatalf("%d deltas, want 1", len(got))
	}
	d := got[0]
	if d.Key != "%459" {
		t.Fatalf("Key = %q, want the pane id", d.Key)
	}
	if d.Run.State != StateRunning {
		t.Errorf("State = %q, want running (from @claude_status)", d.Run.State)
	}
	if d.Run.Branch != "feat/320" {
		t.Errorf("Branch = %q — the pane must inherit its window's enrichment", d.Run.Branch)
	}
	if d.Run.Issue == nil || d.Run.Issue.ID != "#320" {
		t.Errorf("Issue = %+v, want #320", d.Run.Issue)
	}
	if d.Run.PR == nil || d.Run.PR.Number != "565" || d.Run.PR.CheckState != "passing" {
		t.Errorf("PR = %+v", d.Run.PR)
	}
	if d.Run.Crew == nil || d.Run.Crew.Name != "mauve" {
		t.Errorf("Crew = %+v", d.Run.Crew)
	}
	if d.Run.Repo != "lazytmux" {
		t.Errorf("Repo = %q, want the git root's base name", d.Run.Repo)
	}
}

func TestDeltasFromTmuxOmitsAbsentEnrichment(t *testing.T) {
	got := deltasFromTmux(
		[]tmux.WindowOptions{{Session: "houston", Window: 1, Branch: "main"}},
		[]tmux.PaneOptions{{PaneID: "%1", Target: "houston:1"}},
	)
	d := got[0].Run
	if d.Issue != nil || d.PR != nil || d.Crew != nil {
		t.Fatalf("absent options must stay nil, got issue=%+v pr=%+v crew=%+v", d.Issue, d.PR, d.Crew)
	}
	if d.Branch != "main" {
		t.Errorf("Branch = %q, want main", d.Branch)
	}
}

func TestDeltasFromTmuxSkipsPanesWithNoWindow(t *testing.T) {
	got := deltasFromTmux(nil, []tmux.PaneOptions{{PaneID: "%9", Target: "ghost:1"}})
	if len(got) != 0 {
		t.Fatalf("%d deltas for a pane whose window was not listed, want 0", len(got))
	}
}
```

- [ ] **Step 5: Run it to verify it fails**

Run: `go test ./runs/ -run TestDeltasFromTmux -v`
Expected: FAIL to compile — `undefined: deltasFromTmux`.

- [ ] **Step 6: Write `runs/tmuxsource.go`**

```go
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
		wins, err := s.client.ListWindowOptions()
		if err != nil {
			slog.Debug("tmux window options", "error", err)
		}
		panes, err := s.client.ListPaneOptions()
		if err != nil {
			slog.Debug("tmux pane options", "error", err)
		}

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
```

- [ ] **Step 7: Run all tests**

Run: `go test ./tmux/... ./runs/... -race`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add tmux/options.go tmux/options_test.go runs/tmuxsource.go runs/tmuxsource_test.go
git commit -m "feat(runs): harvest lazytmux enrichment from tmux user-options"
```

---

### Task 5: `crewsource` — the dispatcher bus

The bus is an append-only JSONL log under `.git/crew/`. Records look like:

```json
{"ts":1788,"crew_id":"c1","from":"worker:fix/412#s1","to":"dispatcher:c1","kind":"status","body":{"state":"blocked","detail":"Keep the alias?"}}
{"ts":1788,"crew_id":"c1","kind":"dispatch","branch":"fix/412","title":"fix the ws drop"}
```

**Files:**
- Create: `runs/crewsource.go`
- Test: `runs/crewsource_test.go`

**Interfaces:**
- Produces: `func NewCrewSource(repos []string, every time.Duration) *CrewSource`,
  and `func deltasFromCrewLog(r io.Reader) map[string]Run` keyed by branch.

- [ ] **Step 1: Write the failing test**

Create `runs/crewsource_test.go`:

```go
package runs

import (
	"strings"
	"testing"
)

const crewFixture = `{"ts":1000,"crew_id":"c1","kind":"dispatch","branch":"fix/412","title":"fix the ws drop","tier":"standard"}
{"ts":1100,"crew_id":"c1","from":"worker:fix/412#s1","kind":"status","body":{"state":"working"}}
{"ts":1200,"crew_id":"c1","from":"worker:fix/412#s1","kind":"status","body":{"state":"blocked","detail":"Keep the legacy route?"}}
{"ts":1300,"crew_id":"c1","from":"worker:feat/413#s2","kind":"status","body":{"state":"pr_open","pr_url":"https://github.com/x/y/pull/9"}}
not json at all
`

func TestDeltasFromCrewLogTakesLatestStatePerBranch(t *testing.T) {
	got := deltasFromCrewLog(strings.NewReader(crewFixture))

	r, ok := got["fix/412"]
	if !ok {
		t.Fatalf("no entry for fix/412, got keys %v", keysOf(got))
	}
	if r.State != StateBlocked {
		t.Errorf("State = %q, want blocked — the latest status wins", r.State)
	}
	if r.Question == nil || r.Question.Text != "Keep the legacy route?" {
		t.Errorf("Question = %+v, want the blocking detail", r.Question)
	}
	if r.Question != nil && r.Question.Via != "crew" {
		t.Errorf("Question.Via = %q, want crew — it is answered with `crew reply`", r.Question.Via)
	}
}

func TestDeltasFromCrewLogCarriesDispatchMetadata(t *testing.T) {
	got := deltasFromCrewLog(strings.NewReader(crewFixture))
	r := got["fix/412"]
	if r.Crew == nil || r.Crew.Name != "c1" || r.Crew.Tier != "standard" {
		t.Fatalf("Crew = %+v, want name c1 tier standard", r.Crew)
	}
	if r.Branch != "fix/412" {
		t.Errorf("Branch = %q", r.Branch)
	}
}

func TestDeltasFromCrewLogSurvivesGarbageLines(t *testing.T) {
	got := deltasFromCrewLog(strings.NewReader(crewFixture))
	if _, ok := got["feat/413"]; !ok {
		t.Fatal("a malformed line stopped later records being read")
	}
	if got["feat/413"].State != StateReview {
		t.Errorf("feat/413 State = %q, want review", got["feat/413"].State)
	}
}

func keysOf(m map[string]Run) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./runs/ -run TestDeltasFromCrewLog -v`
Expected: FAIL to compile — `undefined: deltasFromCrewLog`.

- [ ] **Step 3: Write `runs/crewsource.go`**

```go
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
)

// CrewSource reads dispatcher's git-backed bus. It contributes the one thing
// no other source can express: a worker blocked on a question addressed to you.
type CrewSource struct {
	repos []string // repo roots to look under for .git/crew
	every time.Duration
}

func NewCrewSource(repos []string, every time.Duration) *CrewSource {
	if every <= 0 {
		every = 3 * time.Second
	}
	return &CrewSource{repos: repos, every: every}
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
	merged := map[string]Run{}
	for _, repo := range s.repos {
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
```

Note the key is `branch/<name>`, not a pane id — a crew record knows its branch,
not its pane. Joining crew runs to their panes needs the branch that
`tmuxsource` publishes, and is deliberately **not** attempted here: it is a
registry-level concern and belongs in the plan that adds the Crews tab. Until
then a crew-backed run appears as its own entry, which is honest.

- [ ] **Step 4: Run the tests**

Run: `go test ./runs/... -race`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add runs/crewsource.go runs/crewsource_test.go
git commit -m "feat(runs): read dispatcher crew bus for blocked questions"
```

---

### Task 6: `/api/runs` and `/api/runs/stream`

Additive. The existing routes are untouched.

**Files:**
- Create: `server/runs_api.go`
- Modify: `server/server.go`
- Test: `server/runs_api_test.go`

**Interfaces:**
- Consumes: `runs.Registry` from Task 2, the sources from Tasks 3-5.
- Produces: `Server.runs *runs.Registry`, two handlers.

- [ ] **Step 1: Write the failing test**

Create `server/runs_api_test.go`:

```go
package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/noamsto/houston/runs"
)

func TestRunsSnapshotReturnsComposedRuns(t *testing.T) {
	reg := runs.NewRegistry(runs.DefaultOrder)
	reg.Apply(runs.Delta{Source: "tmux", Key: "%1", Run: runs.Run{Branch: "main"}})
	reg.Apply(runs.Delta{Source: "hooks", Key: "%1", Run: runs.Run{State: runs.StateRunning}})

	s := &Server{runs: reg}
	rec := httptest.NewRecorder()
	s.handleRunsSnapshot(rec, httptest.NewRequest("GET", "/api/runs", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200", rec.Code)
	}
	var got []runs.Run
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v — body %s", err, rec.Body.String())
	}
	if len(got) != 1 || got[0].State != runs.StateRunning || got[0].Branch != "main" {
		t.Fatalf("got %+v, want one composed run", got)
	}
}

func TestRunsSnapshotWithoutRegistryIs503(t *testing.T) {
	s := &Server{} // registry never started
	rec := httptest.NewRecorder()
	s.handleRunsSnapshot(rec, httptest.NewRequest("GET", "/api/runs", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d, want 503", rec.Code)
	}
}

func TestRunsRoutesAreBehindTheAuthGate(t *testing.T) {
	// A new route registered outside apiMux would reopen the hole closed in #4.
	dir := t.TempDir()
	s, err := New(Config{StatusDir: dir, AuthEnabled: true})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	req := httptest.NewRequest("GET", "http://127.0.0.1/api/runs", nil)
	req.Host = "127.0.0.1"
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status %d, want 401 — /api/runs must require a token", rec.Code)
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./server/ -run TestRuns -v`
Expected: FAIL to compile — `unknown field runs in struct literal`.

- [ ] **Step 3: Write `server/runs_api.go`**

```go
package server

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

// handleRunsSnapshot returns every known run.
//
//	GET /api/runs → []runs.Run
func (s *Server) handleRunsSnapshot(w http.ResponseWriter, _ *http.Request) {
	if s.runs == nil {
		http.Error(w, "run registry not started", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(s.runs.Snapshot())
}

// handleRunsStream emits run updates over SSE: one "snapshot" event, then one
// "update" per composed change.
//
//	GET /api/runs/stream → text/event-stream
func (s *Server) handleRunsStream(w http.ResponseWriter, r *http.Request) {
	if s.runs == nil {
		http.Error(w, "run registry not started", http.StatusServiceUnavailable)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")

	snap, _ := json.Marshal(s.runs.Snapshot())
	fmt.Fprintf(w, "event: snapshot\ndata: %s\n\n", snap)
	flusher.Flush()

	sub := s.runs.Subscribe()
	defer s.runs.Unsubscribe(sub)

	ping := time.NewTicker(25 * time.Second)
	defer ping.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case run, ok := <-sub:
			if !ok {
				return
			}
			b, err := json.Marshal(run)
			if err != nil {
				slog.Warn("runs stream marshal", "err", err)
				continue
			}
			if _, err := fmt.Fprintf(w, "event: update\ndata: %s\n\n", b); err != nil {
				return
			}
			flusher.Flush()
		case <-ping.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}
```

- [ ] **Step 4: Wire it into `server/server.go`**

Add to the `Server` struct:

```go
	runs *runs.Registry
```

In `New`, after the hub is started, build the registry and start the sources.
Reuse the same context the hub uses:

```go
	reg := runs.NewRegistry(runs.DefaultOrder)
	s.runs = reg

	deltas := make(chan runs.Delta, 256)
	go func() {
		for d := range deltas {
			reg.Apply(d)
		}
	}()

	for _, src := range []runs.Source{
		runs.NewHookSource(s.hub),
		runs.NewTmuxSource(tmuxClient, 2*time.Second),
		runs.NewCrewSource(crewRepoRoots(), 3*time.Second),
	} {
		go func(src runs.Source) {
			if err := src.Run(ctx, deltas); err != nil && ctx.Err() == nil {
				slog.Warn("run source stopped", "source", src.Name(), "error", err)
			}
		}(src)
	}
```

Add a small helper that returns the repo roots to scan for `.git/crew`, derived
from the git roots tmux already reports so no configuration is needed:

```go
// crewRepoRoots returns the distinct git roots tmux knows about, which is where
// dispatcher's bus lives. Deriving them costs one tmux call and needs no config.
func crewRepoRoots() []string {
	c := tmux.NewClient()
	wins, err := c.ListWindowOptions()
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, w := range wins {
		if w.GitRoot != "" && !seen[w.GitRoot] {
			seen[w.GitRoot] = true
			out = append(out, w.GitRoot)
		}
	}
	return out
}
```

Register the routes **on `apiMux`**, so they inherit the auth gate:

```go
	apiMux.HandleFunc("/api/runs", s.handleRunsSnapshot)
	apiMux.HandleFunc("/api/runs/stream", s.handleRunsStream)
```

- [ ] **Step 5: Run the tests**

Run: `go test ./server/... ./runs/... ./tmux/...`
Expected: PASS. Then `go test ./runs/... ./tmux/... -race`.

- [ ] **Step 6: Verify against the live server by hand**

```bash
just build && ./houston -addr 127.0.0.1:9095 &
curl -s -H "Authorization: Bearer $(cat ~/.local/state/houston/token)" \
     http://127.0.0.1:9095/api/runs | head -c 2000
```

Expect real runs with `branch`, and where lazytmux has enriched the window,
`issue` and `pr` populated. Confirm `/api/sessions` and `/api/agents` still
answer — this plan must not disturb them. Kill the server afterwards; do NOT run
any tmux command that affects a server or session you did not create.

- [ ] **Step 7: Commit**

```bash
git add server/ runs/
git commit -m "feat(server): serve /api/runs alongside the existing routes"
```

---

## Self-Review

**Spec coverage:**

| Spec requirement | Task |
|---|---|
| `Run` model with `Caps` and `Stale` | 1 |
| Single state vocabulary, three mappings | 1 |
| `Source` interface + registry | 2 |
| Per-field precedence hooks → crew → tmux | 2 |
| Correlation on tmux pane id | 2, 3, 4 |
| `hooksource` | 3 |
| `tmuxsource` harvesting lazytmux options | 4 |
| `crewsource` for blocked questions | 5 |
| `GET /api/runs`, `GET /api/runs/stream` | 6 |
| Deleting the old routes | **out of scope** — follow-up plan, after the UI moves |
| Screen-scraping demoted to amp/generic | **out of scope** — happens when `/api/sessions` goes |
| `Run.Host` for federation | field exists; no peer source yet |

**Placeholder scan:** none.

**Type consistency:** `Run`, `State`, `Delta`, `Source`, `Registry`,
`NewRegistry`, `Apply`, `Snapshot`, `Subscribe`, `Unsubscribe`, `DefaultOrder`,
`FromHookState`, `FromCrewState`, `FromClaudeStatus`, `runFromSessionView`,
`deltasFromTmux`, `deltasFromCrewLog`, `ListWindowOptions`, `ListPaneOptions`,
`ParseWindowOptions`, `ParsePaneOptions` are spelled identically everywhere.

**One thing a reviewer should push on:** crew runs key on `branch/<name>` while
hook and tmux runs key on a pane id, so a dispatcher worker currently appears
twice — once as its pane, once as its branch. That is deliberate for this plan
(joining them needs branch-to-pane resolution that belongs with the Crews tab)
but it is visible in `/api/runs` output, and it must not be mistaken for a bug.
