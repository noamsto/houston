package server

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

func (s *Server) handleAPISessions(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("stream") == "1" {
		s.streamAPISessionsJSON(w, r)
		return
	}

	data := s.buildSessionsData()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(data)
}

func (s *Server) streamAPISessionsJSON(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	fmt.Fprintf(w, ": connected\n\n")
	flusher.Flush()

	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	// Send initial data immediately
	if err := s.sendAPISessionsEvent(w, flusher); err != nil {
		slog.Debug("SSE sessions initial write error", "error", err)
		return
	}

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			if err := s.sendAPISessionsEvent(w, flusher); err != nil {
				slog.Debug("SSE sessions write error", "error", err)
				return
			}
		}
	}
}

func (s *Server) sendAPISessionsEvent(w http.ResponseWriter, flusher http.Flusher) error {
	data := s.buildSessionsData()
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

// corsMiddleware adds CORS headers for development (Vite dev server on different port).
func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
