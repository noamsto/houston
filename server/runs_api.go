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

	snap, _ := json.Marshal(s.runs.Snapshot())
	fmt.Fprintf(w, "event: snapshot\ndata: %s\n\n", snap)
	flusher.Flush()

	sub := s.runs.Subscribe()
	defer s.runs.Unsubscribe(sub)

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
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}
