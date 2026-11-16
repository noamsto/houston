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
// join and the card need. Its state never comes from here; crew's view owns it.
type crewIdentity struct {
	crew, codename, color, engineSession string
	sessions                             int
	session, lastActivity                int64 // session: s<epoch> of the newest worker status sender; lastActivity: newest dispatch/resume/status ts (ms)
	workerSilent                         bool  // newest dispatch/resume/status row is not the worker's own status
}

// identityFromCrewLog folds one bus log into the identity per branch and the
// newest dispatcher reply ts (ms) per branch. Status state, detail and PR are
// deliberately never read.
func identityFromCrewLog(rd io.Reader) (map[string]crewIdentity, map[string]int64) {
	ids := map[string]crewIdentity{}
	replies := map[string]int64{}

	sc := bufio.NewScanner(rd)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for sc.Scan() {
		var rec crewRecord
		if err := json.Unmarshal(sc.Bytes(), &rec); err != nil {
			continue // the bus is written by shell; a torn line is not fatal
		}

		// A dispatcher reply carries its branch in To, since From never
		// resolves through branchFromWorker.
		branch := rec.Branch
		switch {
		case branch != "":
		case rec.Kind == "msg" && strings.HasPrefix(rec.From, "dispatcher:"):
			branch = branchFromWorker(rec.To)
		default:
			branch = branchFromWorker(rec.From)
		}
		if branch == "" {
			continue
		}

		if rec.Kind == "msg" && strings.HasPrefix(rec.From, "dispatcher:") {
			replies[branch] = max(replies[branch], rec.TS)
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
			// Bus text is untrusted, hence both patterns.
			if rec.Kind == "dispatch" && codenameRe.MatchString(rec.Name) && tmuxColorRe.MatchString(rec.TmuxColor) {
				id.codename = rec.Name
				id.color = tmuxColorToHex(rec.TmuxColor)
			}
		case "status":
			var body struct {
				Source string `json:"source"`
			}
			_ = json.Unmarshal(rec.Body, &body) // a body without a source reads as the worker's own
			if s := sessionEpoch(rec.From); s != 0 {
				id.session = s
			}
			id.workerSilent = body.Source == "watchdog"
			id.lastActivity = max(id.lastActivity, rec.TS)
		}
		ids[branch] = id
	}
	return ids, replies
}

// answeredByDispatcher is a display workaround: crew's view keeps an answered
// worker blocked until the worker re-stamps (noamsto/dispatcher#1583), so a
// dispatcher reply at or after the last status stamp (ms vs s) reads as
// answered. Delete it once crew clears answered blocks itself.
func answeredByDispatcher(replyMS, lastStatusSec int64) bool {
	return replyMS > 0 && replyMS >= lastStatusSec*1000
}

type identityLog struct {
	size    int64
	mod     time.Time
	ids     map[string]crewIdentity
	replies map[string]int64
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
	ids, replies := identityFromCrewLog(f)
	return identityLog{size: fi.Size(), mod: fi.ModTime(), ids: ids, replies: replies}, true
}
