// Package crewfeed folds a crew bus's events.jsonl into a one-line-per-event
// activity feed. It is standalone — it imports only the standard library
// (enforced by boundary_test.go) — so the fold stays a pure function of the
// bus lines and never reaches back into the run registry.
package crewfeed

// PR links an entry to a GitHub pull request. It is set only for a URL that
// matches prURLRe.
type PR struct {
	Number int    `json:"number"`
	URL    string `json:"url"`
}

// Entry is one feed row. ID is "<epoch>.<byte offset of its line>" and doubles
// as the cursor; Fold.Line leaves it empty and the store fills it, because the
// epoch is a property of the file rather than of one line.
type Entry struct {
	ID       string `json:"id"`
	TS       int64  `json:"ts"`
	Kind     string `json:"kind"`
	Text     string `json:"text"`
	Branch   string `json:"branch,omitempty"`
	Codename string `json:"codename,omitempty"`
	State    string `json:"state,omitempty"`
	PR       *PR    `json:"pr,omitempty"`
}

// Page is a window of one crew's entries, oldest to newest.
type Page struct {
	Epoch   string  `json:"epoch"`
	Entries []Entry `json:"entries"`
	More    bool    `json:"more"`
}

// Entry kinds.
const (
	KindDispatch  = "dispatch"
	KindResume    = "resume"
	KindStatus    = "status"
	KindQuestion  = "question"
	KindFollowUps = "follow-ups"
	KindReply     = "reply"
	KindPR        = "pr"
	KindReap      = "reap"
)
