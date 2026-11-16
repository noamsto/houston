package runs

import (
	"bufio"
	"encoding/json"
	"io"
	"log/slog"
	"os"
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

// identityFromCrewLog folds one bus log into the identity per branch. Status
// state, detail and PR are deliberately never read.
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
	return ids
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
