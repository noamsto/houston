package runs

import (
	"bufio"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"
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
	statusTS                   int64  // ts (ms) of the row that last set state; 0 before any
	blockedSince               int64  // ts (ms) the current blocked episode began; 0 when not blocked
	watchdog                   watchdogItem
}

// watchdogItem is the attention item the newest classified watchdog status
// records; the zero value is none.
type watchdogItem struct{ kind, message string }

// identityFromCrewLog folds one bus log into the identity per branch.
func identityFromCrewLog(rd io.Reader) map[string]crewIdentity {
	ids := map[string]crewIdentity{}

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
		if branch == "" && rec.Kind == "msg" {
			branch = branchFromWorker(rec.To)
		}
		if branch == "" {
			continue
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
			id.statusTS, id.blockedSince, id.watchdog = 0, 0, watchdogItem{}
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
			}
			_ = json.Unmarshal(rec.Body, &body) // a body without a source reads as the worker's own
			if s := sessionEpoch(rec.From); s != 0 {
				id.session = s
			}
			id.workerSilent = body.Source == "watchdog"
			id.lastActivity = max(id.lastActivity, rec.TS)
			if body.Source == "watchdog" {
				id.foldWatchdog(body.State, body.Detail, rec.TS)
				break
			}
			if body.State == "blocked" && id.state != "blocked" {
				id.blockedSince = rec.TS
			} else if body.State != "blocked" {
				id.blockedSince = 0
			}
			id.state, id.detail, id.prURL, id.statusTS = body.State, body.Detail, body.PRURL, rec.TS
			id.watchdog = watchdogItem{}
		case "msg":
			if id.state == "blocked" && strings.HasPrefix(rec.From, "dispatcher:") {
				id.state, id.statusTS = "answered", rec.TS
			}
		case "reap", "release":
			id.state, id.statusTS = "reaped", rec.TS
		}
		ids[branch] = id
	}
	return ids
}

// foldWatchdog records a watchdog status: it leaves the state alone except for
// a `failed` (dead:) row, and records the item the detail's prefix names.
// A prefix watchdogPrefixes does not list claims nothing.
func (id *crewIdentity) foldWatchdog(state, detail string, ts int64) {
	switch {
	case state == "failed":
		id.state, id.statusTS = "failed", ts
		id.watchdog = watchdogItem{kind: "dead", message: detail}
	case strings.HasPrefix(detail, "prompt:"):
		id.watchdog = watchdogItem{kind: "prompt", message: detail}
	default:
		if _, listed := watchdogClassOf(detail); listed {
			id.watchdog = watchdogItem{kind: "watchdog", message: detail}
		}
	}
}

type identityLog struct {
	size int64
	mod  time.Time
	ids  map[string]crewIdentity
}

func readIdentityLog(path string, cache map[string]identityLog) (identityLog, bool) {
	fi, err := os.Stat(path)
	if err != nil {
		slog.Debug("crew identity log", "path", path, "error", err)
		return identityLog{}, false
	}
	if il, ok := cache[path]; ok && il.size == fi.Size() && il.mod.Equal(fi.ModTime()) {
		return il, true
	}
	f, err := os.Open(path) //nolint:gosec // path comes from houston's own state/transcript dirs, not from a request
	if err != nil {
		slog.Debug("crew identity log", "path", path, "error", err)
		return identityLog{}, false
	}
	defer func() { _ = f.Close() }()
	return identityLog{size: fi.Size(), mod: fi.ModTime(), ids: identityFromCrewLog(f)}, true
}
