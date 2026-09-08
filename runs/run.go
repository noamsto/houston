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
