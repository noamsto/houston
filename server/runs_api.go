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

	// Subscribe before writing the snapshot: anything that changes in that
	// window must land in the channel, not get missed. Updates that arrive
	// while the snapshot is being written just buffer — the snapshot is
	// written first, so a replayed update afterward is same-or-newer and
	// harmless.
	sub := s.runs.Subscribe()
	defer s.runs.Unsubscribe(sub)

	snap, _ := json.Marshal(s.runs.Snapshot())
	fmt.Fprintf(w, "event: snapshot\ndata: %s\n\n", snap)
	flusher.Flush()

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
			// A dropped update is otherwise permanent — the registry only
			// dedupes redundant re-emissions, it does not retry a drop. If
			// this subscriber missed one, a full resync is owed instead of a
			// bare keepalive.
			if s.runs.Dirty(sub) {
				snap, err := json.Marshal(s.runs.Snapshot())
				if err != nil {
					slog.Warn("runs stream resync marshal", "err", err)
					continue
				}
				if _, err := fmt.Fprintf(w, "event: snapshot\ndata: %s\n\n", snap); err != nil {
					return
				}
			} else if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}
