package runs

import (
	"bufio"
	"encoding/json"
	"io"
	"strings"
)

// crewIdentity is who a branch's worker is, read from the bus: what the pane
// join and the card need. Its state comes from crew's view; the status fields
// below mirror what that view derives and are read only to list a worker whose
// crew the view omits (logWorkers).
type crewIdentity struct {
	crew, codename, color, engineSession string
	sessions                             int
	session, lastActivity                int64 // session: s<epoch> of the newest worker status sender; lastActivity: newest dispatch/resume/status ts (ms)
	workerSilent                         bool  // newest dispatch/resume/status row is not the worker's own status

	title, tier, engine, model string // newest dispatch row
	state, detail, prURL       string // crew state of the newest dispatch/status/reply/reap row
	statusTS                   int64  // ts (ms) of the row that last set state; 0 before any worker status
	blockedSince               int64  // ts (ms) the current blocked episode began; 0 when not blocked
	hasStatus                  bool   // any status row, the worker's or the watchdog's, since the last dispatch/resume
	watchdog                   watchdogItem
	watchdogTS                 int64 // ts (ms) of the row that set watchdog
}

// watchdogItem is the attention item the newest classified watchdog status
// records; the zero value is none.
type watchdogItem struct{ kind, message string }

// identityFromCrewLog folds one bus log into the identity per branch.
func identityFromCrewLog(rd io.Reader) map[string]crewIdentity {
	var f identityFold

	sc := bufio.NewScanner(rd)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		f.Row(sc.Bytes())
	}
	return f.result()
}

// identityFold folds bus rows, in log order, into the identity per branch. The
// zero value is ready to use.
type identityFold struct {
	ids map[string]crewIdentity
}

func (f *identityFold) result() map[string]crewIdentity {
	if f.ids == nil {
		return map[string]crewIdentity{}
	}
	return f.ids
}

// Row folds one bus row's JSON; a row that does not parse, or names no
// branch, is skipped.
func (f *identityFold) Row(line []byte) {
	if f.ids == nil {
		f.ids = map[string]crewIdentity{}
	}
	ids := f.ids
	var rec crewRecord
	if err := json.Unmarshal(line, &rec); err != nil {
		return // the bus is written by shell; a torn line is not fatal
	}
	branch := rec.Branch
	if branch == "" {
		branch = branchFromWorker(rec.From)
	}
	if branch == "" && rec.Kind == "msg" {
		branch = branchFromWorker(rec.To)
	}
	if branch == "" {
		return
	}

	id := ids[branch]
	if rec.CrewID != "" {
		id.crew = rec.CrewID
	}
	switch rec.Kind {
	case "dispatch", "resume":
		id.engineSession = rec.EngineSession
		id.sessions++
		id.workerSilent = true
		id.lastActivity = max(id.lastActivity, rec.TS)
		id.state, id.detail, id.prURL = "working", "", ""
		id.statusTS, id.blockedSince, id.watchdog, id.watchdogTS, id.hasStatus = 0, 0, watchdogItem{}, 0, false
		if rec.Kind == "dispatch" {
			id.title, id.tier, id.engine, id.model = rec.Title, rec.Tier, rec.Engine, rec.Model
		}
		// Bus text is untrusted, hence both patterns.
		if rec.Kind == "dispatch" && codenameRe.MatchString(rec.Name) && tmuxColorRe.MatchString(rec.TmuxColor) {
			id.codename = rec.Name
			id.color = tmuxColorToHex(rec.TmuxColor)
		}
	case "status":
		var body struct {
			Source string `json:"source"`
			State  string `json:"state"`
			Detail string `json:"detail"`
			PRURL  string `json:"pr_url"`

			Restamp bool `json:"restamp"`
		}
		_ = json.Unmarshal(rec.Body, &body) // a body without a source reads as the worker's own
		if s := sessionEpoch(rec.From); s != 0 {
			id.session = s
		}
		id.workerSilent = body.Source == "watchdog"
		id.hasStatus = true
		id.lastActivity = max(id.lastActivity, rec.TS)
		if body.Source == "watchdog" {
			id.foldWatchdog(body.State, body.Detail, rec.TS)
			break
		}
		// crew opens a blocked episode on a new question, not on a repeat or a restamp.
		switch {
		case body.State != "blocked":
			id.blockedSince = 0
		case id.state != "blocked" || (!body.Restamp && body.Detail != id.detail):
			id.blockedSince = rec.TS
		}
		id.state, id.detail, id.statusTS = body.State, body.Detail, rec.TS
		if body.PRURL != "" { // the terminal done row drops the URL; crew keeps the last one
			id.prURL = body.PRURL
		}
		id.watchdog = watchdogItem{}
	case "msg":
		if id.state == "blocked" && strings.HasPrefix(rec.From, "dispatcher:") {
			id.state, id.statusTS = "answered", rec.TS
		}
	case "reap":
		switch id.state {
		case "done", "failed", "exited":
			id.state, id.statusTS = "reaped", rec.TS
		}
	}
	ids[branch] = id
}

// foldWatchdog records a watchdog status: it leaves the state alone except for
// a `failed` (dead:) row. Only a `blocked` row records the item the detail's
// prefix names, a prefix watchdogPrefixes does not list claims nothing; any
// other state (a `working` clearance) clears the item.
func (id *crewIdentity) foldWatchdog(state, detail string, ts int64) {
	switch {
	case state == "failed":
		id.state, id.statusTS = "failed", ts
		id.watchdog, id.watchdogTS = watchdogItem{kind: "dead", message: detail}, ts
	case state != "blocked":
		id.watchdog, id.watchdogTS = watchdogItem{}, 0
	case strings.HasPrefix(detail, "prompt:"):
		id.watchdog, id.watchdogTS = watchdogItem{kind: "prompt", message: detail}, ts
	default:
		if _, listed := watchdogClassOf(detail); listed {
			id.watchdog, id.watchdogTS = watchdogItem{kind: "watchdog", message: detail}, ts
		}
	}
}
