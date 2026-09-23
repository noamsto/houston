// Package runs models one agent run — a Claude Code, Codex, Cursor, amp,
// OpenCode or pi session — independent of where it happens to be running.
package runs

// Run roles. A run with no role is a solo session.
const (
	RoleDispatcher = "dispatcher"
	RoleWorker     = "worker"
)

// Run is one agent run, anywhere. It is the only object the UI lists.
type Run struct {
	ID    string `json:"id"`
	Host  string `json:"host,omitempty"` // "" means local
	Agent string `json:"agent"`
	State State  `json:"state"`

	Repo     string `json:"repo,omitempty"`
	Project  string `json:"project,omitempty"` // main repo name; a worktree resolves to its main repo
	Role     string `json:"role,omitempty"`    // RoleDispatcher, RoleWorker, or "" for a solo run
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
	//
	// ConnectionSource sets it while a run's tmux session has no control
	// connection, and clears it by dropping that marker layer (Apply replaces
	// a source's layer wholesale). mergeInto ORs it across layers, so a layer
	// that publishes no Stale opinion never blanks another's.
	Stale bool `json:"stale,omitempty"`

	Caps Caps `json:"caps"`

	// Removed marks a broadcast-only payload telling subscribers a run left
	// the listing. Only ID is set alongside it; every other field is zero.
	// Never present in Snapshot or in a source's layer.
	Removed bool `json:"removed,omitempty"`
}

// listed reports whether a composed Run is an agent run and therefore belongs
// in /api/runs. A pane with no agent is a workspace pane, not a run.
func (r Run) listed() bool { return r.Agent != "" }

// TmuxRef locates a run's pane. Nil for agents with no pane.
type TmuxRef struct {
	Session string `json:"session"`
	Window  int    `json:"window"`
	PaneID  string `json:"pane_id"` // "%307"
	// Server is the tmux server pid that minted PaneID, at the time this ref
	// was recorded. Internal only — unlike the fields above, it is excluded
	// from the runs JSON/SSE API.
	Server string `json:"-"`
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
	Name     string `json:"name"`               // crew id — the grouping key. Crew bus only. "" when only tmux knows this run.
	Codename string `json:"codename,omitempty"` // lazytmux @crew_name: this worker's codename. tmux only.
	Color    string `json:"color,omitempty"`    // "#rrggbb" or "". tmux only.
	Tier     string `json:"tier,omitempty"`     // Crew bus only.
	Title    string `json:"title,omitempty"`    // Task title from the dispatch record. Crew bus only.
	Model    string `json:"model,omitempty"`    // Model the worker was dispatched on. Crew bus only.
	Detail   string `json:"detail,omitempty"`   // Latest status detail — the live phase. Crew bus only.
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
	// Via is how an answer is delivered: "pane" (answer at the run's
	// terminal) or "crew" (reply on the crew bus). "watchdog" was houston's
	// synthesized informational note; watchdog statuses no longer produce a
	// Question, so only the UI still recognizes it for compatibility.
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
