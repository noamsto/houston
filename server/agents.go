package server

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

// handleAgentsSnapshot returns the current set of agent SessionViews as JSON.
//
//	GET /api/agents         → []SessionView
func (s *Server) handleAgentsSnapshot(w http.ResponseWriter, _ *http.Request) {
	if s.hub == nil {
		http.Error(w, "hub not started", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(s.hub.Snapshot())
}

// handleAgentsStream emits SessionView updates over Server-Sent Events. The
// client gets the current snapshot as a single "snapshot" event, then one
// "update" event per session state change thereafter.
//
//	GET /api/agents/stream  → text/event-stream
func (s *Server) handleAgentsStream(w http.ResponseWriter, r *http.Request) {
	if s.hub == nil {
		http.Error(w, "hub not started", http.StatusServiceUnavailable)
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
	h.Set("X-Accel-Buffering", "no") // nginx: don't buffer SSE

	// 1) Initial snapshot.
	snap := s.hub.Snapshot()
	snapBytes, _ := json.Marshal(snap)
	fmt.Fprintf(w, "event: snapshot\ndata: %s\n\n", snapBytes)
	flusher.Flush()

	// 2) Live updates.
	sub := s.hub.Subscribe()
	defer s.hub.Unsubscribe(sub)

	// Keepalive so proxies don't drop the connection.
	ping := time.NewTicker(25 * time.Second)
	defer ping.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case v, ok := <-sub:
			if !ok {
				return
			}
			b, err := json.Marshal(v)
			if err != nil {
				slog.Warn("agents stream marshal", "err", err)
				continue
			}
			if _, err := fmt.Fprintf(w, "event: update\ndata: %s\n\n", b); err != nil {
				return
			}
			flusher.Flush()
		case <-ping.C:
			if _, err := fmt.Fprintf(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}
