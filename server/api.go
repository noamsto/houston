package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

func (s *Server) handleAPIOpenCodeSessions(w http.ResponseWriter, r *http.Request) {
	if s.ocManager == nil {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(OpenCodeData{})
		return
	}

	if r.URL.Query().Get("stream") == "1" {
		s.streamAPIOpenCodeJSON(w, r)
		return
	}

	data := s.buildOpenCodeData(r.Context())
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(data)
}

func (s *Server) streamAPIOpenCodeJSON(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	_, _ = fmt.Fprintf(w, ": connected\n\n")
	flusher.Flush()

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	if err := s.sendAPIOpenCodeEvent(r.Context(), w, flusher); err != nil {
		return
	}

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			if err := s.sendAPIOpenCodeEvent(r.Context(), w, flusher); err != nil {
				slog.Debug("SSE opencode write error", "error", err)
				return
			}
		}
	}
}

func (s *Server) sendAPIOpenCodeEvent(ctx context.Context, w http.ResponseWriter, flusher http.Flusher) error {
	data := s.buildOpenCodeData(ctx)
	jsonBytes, err := json.Marshal(data)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "data: %s\n\n", jsonBytes)
	if err != nil {
		return err
	}
	flusher.Flush()
	return nil
}

func (s *Server) handleAPIOpenCodeSession(w http.ResponseWriter, r *http.Request) {
	// Rewrite path: strip /api prefix so handleOpenCodeSession (which expects /opencode/session/...) works
	r.URL.Path = strings.TrimPrefix(r.URL.Path, "/api")
	r.URL.RawPath = strings.TrimPrefix(r.URL.RawPath, "/api")
	s.handleOpenCodeSession(w, r)
}
