package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/noamsto/houston/tmux"
)

func (s *Server) handleAPISessions(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("stream") == "1" {
		s.streamAPISessionsJSON(w, r)
		return
	}

	data := s.buildSessionsData()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(data)
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

	_, _ = fmt.Fprintf(w, ": connected\n\n")
	flusher.Flush()

	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	var lastJSON []byte

	send := func() error {
		data := s.buildSessionsData()
		jsonBytes, err := json.Marshal(data)
		if err != nil {
			return err
		}
		if bytes.Equal(jsonBytes, lastJSON) {
			return nil
		}
		lastJSON = jsonBytes
		if _, err := fmt.Fprintf(w, "data: %s\n\n", jsonBytes); err != nil {
			return err
		}
		flusher.Flush()
		return nil
	}

	if err := send(); err != nil {
		slog.Debug("SSE sessions initial write error", "error", err)
		return
	}

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			if err := send(); err != nil {
				slog.Debug("SSE sessions write error", "error", err)
				return
			}
		}
	}
}

func (s *Server) handleAPIPane(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api")

	var serve func(http.ResponseWriter, *http.Request, tmux.Pane)
	switch {
	case strings.HasSuffix(path, "/ws"):
		serve = s.handlePaneWS
	case strings.HasSuffix(path, "/send") && r.Method == http.MethodPost:
		serve = s.handlePaneSend
	case strings.HasSuffix(path, "/send-with-images") && r.Method == http.MethodPost:
		serve = s.handlePaneSendWithImages
	default:
		http.NotFound(w, r)
		return
	}

	pane, ok := s.legacyPane(w, r)
	if !ok {
		return
	}
	serve(w, r, pane)
}

var tmuxPaneID = regexp.MustCompile(`^%[0-9]+$`)

// legacyPane resolves the pane identity a classic-view request presents. The
// URL coordinate is not trusted: tmux renumbers and reuses coordinates (and
// pane ids, across a server restart), so the request must name the pane id
// and the server it was listed from, and the action targets what that id
// resolves to now.
func (s *Server) legacyPane(w http.ResponseWriter, r *http.Request) (tmux.Pane, bool) {
	q := r.URL.Query()
	paneID, clientServer := q.Get("pane_id"), q.Get("server")
	if !tmuxPaneID.MatchString(paneID) || clientServer == "" {
		legacyPaneRefusal(w, paneID, http.StatusBadRequest, "pane identity required")
		return tmux.Pane{}, false
	}

	pane, err := s.runPanes.ResolvePane(paneID)
	if err != nil {
		if errors.Is(err, tmux.ErrPaneNotFound) {
			slog.Debug("resolve legacy pane failed", "pane_id", paneID, "error", err)
			legacyPaneRefusal(w, paneID, http.StatusConflict, "terminal pane is gone")
			return tmux.Pane{}, false
		}
		slog.Warn("resolve legacy pane failed", "pane_id", paneID, "error", err)
		legacyPaneRefusal(w, paneID, http.StatusServiceUnavailable, "tmux unavailable")
		return tmux.Pane{}, false
	}
	if tmux.ServerMismatch(clientServer, pane.Server) {
		slog.Info("resolve legacy pane refused: server mismatch", "pane_id", paneID, "client_server", clientServer, "pane_server", pane.Server)
		legacyPaneRefusal(w, paneID, http.StatusConflict, "terminal pane belongs to a different tmux server")
		return tmux.Pane{}, false
	}
	return pane, true
}

func legacyPaneRefusal(w http.ResponseWriter, paneID string, code int, detail string) {
	if !tmuxPaneID.MatchString(paneID) {
		paneID = ""
	}
	slog.Info("legacy pane refused", "pane_id", paneID, "status", code, "outcome", detail)
	http.Error(w, detail, code)
}

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
