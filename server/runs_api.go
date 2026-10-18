package server

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/noamsto/houston/peer"
	"github.com/noamsto/houston/runs"
)

// runsPingInterval is the runs stream's keep-alive period.
const runsPingInterval = 25 * time.Second

// scopeOf reads ?scope=: absent means every run, "local" only this host's own.
// Anything else is answered 400 here and ok is false.
func scopeOf(w http.ResponseWriter, r *http.Request) (localOnly, ok bool) {
	switch scopes := r.URL.Query()["scope"]; {
	case len(scopes) == 0:
		return false, true
	case len(scopes) == 1 && scopes[0] == peer.ScopeLocal:
		return true, true
	}
	http.Error(w, "unknown scope", http.StatusBadRequest)
	return false, false
}

// localRuns keeps the runs this host composed itself, dropping peer mirrors.
func localRuns(all []runs.Run) []runs.Run {
	out := make([]runs.Run, 0, len(all))
	for _, r := range all {
		if r.Host == "" {
			out = append(out, r)
		}
	}
	return out
}

// handleRunsSnapshot returns every known run, or with ?scope=local only this
// host's own.
//
//	GET /api/runs[?scope=local] → []runs.Run
func (s *Server) handleRunsSnapshot(w http.ResponseWriter, r *http.Request) {
	if s.runs == nil {
		http.Error(w, "run registry not started", http.StatusServiceUnavailable)
		return
	}
	localOnly, ok := scopeOf(w, r)
	if !ok {
		return
	}
	snap := s.runs.Snapshot()
	if localOnly {
		snap = localRuns(snap)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(snap)
}

// handleRunsStream emits run updates over SSE: one "snapshot" event, then one
// "update" per composed change.
//
//	GET /api/runs/stream[?scope=local] → text/event-stream
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
	localOnly, ok := scopeOf(w, r)
	if !ok {
		return
	}
	snapshot := s.runs.Snapshot
	if localOnly {
		snapshot = func() []runs.Run { return localRuns(s.runs.Snapshot()) }
	}

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	h.Set(peer.InstanceHeader, s.instance)

	// Subscribe before writing the snapshot, so a change during the write
	// lands in the channel instead of being missed. Ordering makes a replay
	// harmless: the snapshot goes out first, so a buffered update is
	// same-or-newer.
	sub := s.runs.Subscribe()
	defer s.runs.Unsubscribe(sub)

	snap, _ := json.Marshal(snapshot())
	fmt.Fprintf(w, "event: snapshot\ndata: %s\n\n", snap)
	flusher.Flush()

	ping := time.NewTicker(orDefault(s.runsPing, runsPingInterval))
	defer ping.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case run, ok := <-sub:
			if !ok {
				return
			}
			if localOnly && run.Host != "" {
				continue
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
				snap, err := json.Marshal(snapshot())
				if err != nil {
					slog.Warn("runs stream resync marshal", "err", err)
					continue
				}
				if _, err := fmt.Fprintf(w, "event: snapshot\ndata: %s\n\n", snap); err != nil {
					return
				}
			} else if _, err := fmt.Fprint(w, "event: ping\ndata: \n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}
