package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/noamsto/houston/crewview"
	"github.com/noamsto/houston/runs"
)

const (
	maxCrewActionBody = 64 << 10

	crewActionTimeout = 15 * time.Second
	// crewMergeTimeout covers `crew merge`, which waits on gh and the remote.
	crewMergeTimeout = 120 * time.Second
)

// crewViewer is the part of the crew view manager the action route reads.
type crewViewer interface {
	Latest(bus string) (crewview.Overview, crewview.Status, bool)
	Root(bus string) (string, bool)
}

// handleRunCrewAction runs one attention item's action from crew's view.
//
//	POST /api/runs/{id}/crew/action {"item":"<id>","text"?,"confirm"?} → 200 {"output"}
//
// The client names a run and an item, never a command: the argv is the one
// crew's current view carries for that item, checked against the allowlisted
// shapes, and argv[0] is replaced by houston's own resolved crew.
func (s *Server) handleRunCrewAction(w http.ResponseWriter, r *http.Request) {
	out := &crewActionLog{id: r.PathValue("id")}
	defer out.emit()

	if s.crewViews == nil {
		out.fail(w, http.StatusServiceUnavailable, "crew view not started")
		return
	}
	if s.runs == nil {
		out.fail(w, http.StatusServiceUnavailable, "run registry not started")
		return
	}
	run, ok := s.findRun(out.id)
	if !ok {
		out.fail(w, http.StatusNotFound, "no such run")
		return
	}
	if run.CrewBus == "" || run.Crew == nil || run.Crew.Name == "" {
		out.fail(w, http.StatusNotFound, "no crew")
		return
	}
	view, status, ok := s.crewViews.Latest(run.CrewBus)
	if !ok {
		notice := status.Notice
		if notice == "" {
			notice = "crew view not ready"
		}
		out.fail(w, http.StatusServiceUnavailable, notice)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxCrewActionBody)
	var body struct {
		Item    string `json:"item"`
		Text    string `json:"text"`
		Confirm bool   `json:"confirm"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			out.fail(w, http.StatusRequestEntityTooLarge, "request too large")
			return
		}
		out.fail(w, http.StatusBadRequest, "malformed request body")
		return
	}

	item, ok := ownedItem(view, run, body.Item)
	if !ok {
		out.fail(w, http.StatusNotFound, "no such item")
		return
	}
	out.kind = item.Kind
	if crewview.Check(item) != nil {
		out.fail(w, http.StatusForbidden, "not executable")
		return
	}
	out.verb = item.Action.Verb
	merge := item.Action.Verb == "merge"
	if merge && !body.Confirm {
		out.status = http.StatusPreconditionRequired
		writeDispatchJSON(w, out.status, map[string]string{"confirm": "Squash-merge PR #" + strconv.Itoa(*item.Action.PR) + "?"})
		return
	}
	args, err := crewview.Args(item, body.Text)
	if err != nil {
		out.fail(w, http.StatusBadRequest, "text required")
		return
	}
	root, ok := s.crewViews.Root(run.CrewBus)
	if !ok {
		out.fail(w, http.StatusServiceUnavailable, "crew root unknown")
		return
	}

	timeout := crewActionTimeout
	if merge {
		timeout = crewMergeTimeout
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()
	res := s.crewRunner(ctx, s.crewPath, crewExec{Dir: root, CrewID: item.Crew, Args: args})

	switch {
	case errors.Is(res.Err, context.DeadlineExceeded):
		out.fail(w, http.StatusGatewayTimeout, "crew "+item.Action.Verb+" timed out")
	case res.Err != nil:
		out.fail(w, http.StatusBadGateway, "could not run crew "+item.Action.Verb+": "+res.Err.Error())
	case res.ExitCode != 0:
		// crew's own words, verbatim: 409 is crew's refusal, not ours.
		reason := res.Stderr
		if reason == "" {
			reason = "crew " + item.Action.Verb + " refused"
		}
		out.fail(w, http.StatusConflict, reason)
	default:
		out.status = http.StatusOK
		writeDispatchJSON(w, out.status, map[string]string{"output": res.Stdout})
	}
}

// ownedItem finds id in view among the items run may act on: a worker's own
// items, or a dispatcher's crew-scoped (branchless) items.
func ownedItem(view crewview.Overview, run runs.Run, id string) (crewview.Item, bool) {
	for _, it := range view.Attention {
		if it.ID != id || it.Crew != run.Crew.Name {
			continue
		}
		switch run.Role {
		case runs.RoleWorker:
			if it.Branch != "" && it.Branch == run.Branch {
				return it, true
			}
		case runs.RoleDispatcher:
			if it.Branch == "" {
				return it, true
			}
		}
	}
	return crewview.Item{}, false
}

// crewActionLog is one attempt's log line: never the text, argv, message or
// output, which carry task content.
type crewActionLog struct {
	id, kind, verb string
	status         int
}

func (l *crewActionLog) fail(w http.ResponseWriter, code int, detail string) {
	l.status = code
	http.Error(w, detail, code)
}

func (l *crewActionLog) emit() {
	slog.Info("run crew action", "id", l.id, "kind", l.kind, "verb", l.verb, "status", l.status)
}
