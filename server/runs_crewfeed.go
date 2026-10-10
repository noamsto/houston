package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/noamsto/houston/crewfeed"
	"github.com/noamsto/houston/runs"
)

const (
	crewFeedPingInterval  = 25 * time.Second
	crewFeedCheckInterval = 2 * time.Second
)

// crewFeedRun resolves a run to its crew's feed, writing the refusal itself
// when it has none.
func (s *Server) crewFeedRun(w http.ResponseWriter, r *http.Request) (runs.Run, bool) {
	id := r.PathValue("id")
	if s.runs == nil {
		crewFeedRefusal(w, id, http.StatusServiceUnavailable, "run registry not started")
		return runs.Run{}, false
	}
	run, ok := s.findRun(id)
	if !ok {
		crewFeedRefusal(w, id, http.StatusNotFound, "no such run")
		return runs.Run{}, false
	}
	if run.Role != runs.RoleDispatcher || run.Crew == nil || run.Crew.Name == "" || run.CrewBus == "" || s.crewFeed == nil {
		crewFeedRefusal(w, id, http.StatusNotFound, "no crew")
		return runs.Run{}, false
	}
	return run, true
}

func crewFeedRefusal(w http.ResponseWriter, id string, code int, detail string) {
	slog.Info("run crew feed refused", "id", id, "status", code, "outcome", detail)
	http.Error(w, detail, code)
}

// crewFeedReset answers a cursor that no longer names the bus file.
func crewFeedReset(w http.ResponseWriter, id string) {
	slog.Info("run crew feed refused", "id", id, "status", http.StatusConflict, "outcome", "reset")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusConflict)
	_, _ = fmt.Fprint(w, `{"reset":true}`)
}

// parseFeedCursor splits "<epoch>.<offset>" on its last dot.
func parseFeedCursor(c string) (string, int64, bool) {
	i := strings.LastIndexByte(c, '.')
	if i <= 0 {
		return "", 0, false
	}
	off, err := strconv.ParseInt(c[i+1:], 10, 64)
	if err != nil || off < 0 {
		return "", 0, false
	}
	return c[:i], off, true
}

// handleRunCrewFeed returns one page of a dispatcher's crew feed, oldest
// first.
//
//	GET /api/runs/{id}/crew/feed?before=<epoch>.<offset>&limit=<n> → crewfeed.Page
func (s *Server) handleRunCrewFeed(w http.ResponseWriter, r *http.Request) {
	run, ok := s.crewFeedRun(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	var limit int
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			crewFeedRefusal(w, run.ID, http.StatusBadRequest, "bad limit")
			return
		}
		limit = n
	}
	var beforeEpoch string
	var before int64
	if v := q.Get("before"); v != "" {
		if beforeEpoch, before, ok = parseFeedCursor(v); !ok {
			crewFeedRefusal(w, run.ID, http.StatusBadRequest, "malformed cursor")
			return
		}
	}

	epoch, known := s.crewFeed.Epoch(run.CrewBus, run.Crew.Name)
	if !known {
		crewFeedRefusal(w, run.ID, http.StatusNotFound, "no crew")
		return
	}
	if beforeEpoch != "" && beforeEpoch != epoch {
		crewFeedReset(w, run.ID)
		return
	}

	page := crewfeed.Page{Epoch: epoch, Entries: []crewfeed.Entry{}}
	// Page reads an offset of 0 or less as "newest overall", but nothing is
	// older than the first byte.
	if beforeEpoch == "" || before > 0 {
		var err error
		if page, err = s.crewFeed.Page(run.CrewBus, run.Crew.Name, before, limit); err != nil {
			switch {
			case errors.Is(err, crewfeed.ErrEpoch):
				crewFeedReset(w, run.ID)
			case errors.Is(err, crewfeed.ErrNoBus):
				crewFeedRefusal(w, run.ID, http.StatusNotFound, "no crew")
			default:
				slog.Warn("run crew feed failed", "id", run.ID, "err", err)
				http.Error(w, "crew feed unavailable", http.StatusInternalServerError)
			}
			return
		}
		if beforeEpoch != "" && page.Epoch != beforeEpoch {
			crewFeedReset(w, run.ID)
			return
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(page)
}

// crewFeedStart is where a cursorless stream begins: the current tail, so
// only entries appended later are sent.
func (s *Server) crewFeedStart(run runs.Run) (string, int64, bool) {
	page, err := s.crewFeed.Page(run.CrewBus, run.Crew.Name, 0, 1)
	if err != nil {
		return "", 0, false
	}
	if len(page.Entries) == 0 {
		return page.Epoch, -1, true
	}
	_, off, ok := parseFeedCursor(page.Entries[0].ID)
	return page.Epoch, off, ok
}

// handleRunCrewFeedStream emits a dispatcher's crew feed over SSE, resuming
// after the cursor. Each batch's id is the cursor to resume after it; "reset"
// means the cursor can no longer be served and ends the stream. The cursor is
// Last-Event-ID, else after=<epoch>.<offset>, else from=<epoch> (everything
// in that epoch from the first byte), else the current tail.
//
//	GET /api/runs/{id}/crew/feed/stream → text/event-stream
func (s *Server) handleRunCrewFeedStream(w http.ResponseWriter, r *http.Request) {
	run, ok := s.crewFeedRun(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	var epoch string
	var after int64
	cursor := r.Header.Get("Last-Event-ID")
	if cursor == "" {
		cursor = q.Get("after")
	}
	switch {
	case cursor != "":
		if epoch, after, ok = parseFeedCursor(cursor); !ok {
			crewFeedRefusal(w, run.ID, http.StatusBadRequest, "malformed cursor")
			return
		}
	case q.Get("from") != "":
		epoch, after = q.Get("from"), -1
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}
	if epoch == "" {
		if epoch, after, ok = s.crewFeedStart(run); !ok {
			crewFeedRefusal(w, run.ID, http.StatusNotFound, "no crew")
			return
		}
	}

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")

	reset := func() {
		fmt.Fprint(w, "event: reset\ndata: {}\n\n")
		flusher.Flush()
	}
	// pull sends everything after the cursor; false ends the stream.
	pull := func() bool {
		entries, ok := s.crewFeed.Since(run.CrewBus, run.Crew.Name, epoch, after)
		if !ok {
			reset()
			return false
		}
		if len(entries) == 0 {
			return true
		}
		last := entries[len(entries)-1].ID
		_, off, ok := parseFeedCursor(last)
		if !ok {
			reset()
			return false
		}
		b, err := json.Marshal(entries)
		if err != nil {
			slog.Warn("run crew feed stream marshal", "id", run.ID, "err", err)
			return false
		}
		if _, err := fmt.Fprintf(w, "id: %s\nevent: entries\ndata: %s\n\n", last, b); err != nil {
			return false
		}
		flusher.Flush()
		after = off
		return true
	}

	if !pull() {
		return
	}
	// Commit the headers even when there was nothing to send yet.
	flusher.Flush()

	ping := time.NewTicker(orDefault(s.crewFeedPing, crewFeedPingInterval))
	defer ping.Stop()
	check := time.NewTicker(orDefault(s.crewFeedCheck, crewFeedCheckInterval))
	defer check.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-check.C:
			// A different crew or bus on the run invalidates the cursor.
			cur, ok := s.findRun(run.ID)
			if !ok || cur.Crew == nil || cur.Crew.Name != run.Crew.Name || cur.CrewBus != run.CrewBus {
				reset()
				return
			}
			if !pull() {
				return
			}
		case <-ping.C:
			if _, err := fmt.Fprint(w, "event: ping\ndata: \n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}
