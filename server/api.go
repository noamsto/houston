package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/noamsto/houston/agents"
	"github.com/noamsto/houston/agents/claude"
	"github.com/noamsto/houston/tmux"
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
	// Rewrite path: strip /api prefix so parsePaneTarget (which expects /pane/...) works
	path := strings.TrimPrefix(r.URL.Path, "/api")
	pane, err := parsePaneTarget(path)
	if err != nil {
		http.Error(w, "invalid pane target", http.StatusBadRequest)
		return
	}

	// Route based on suffix
	switch {
	case strings.HasSuffix(path, "/ws"):
		s.handlePaneWS(w, r, pane)
	case strings.HasSuffix(path, "/send") && r.Method == http.MethodPost:
		s.handlePaneSend(w, r, pane)
	case strings.HasSuffix(path, "/send-with-images") && r.Method == http.MethodPost:
		s.handlePaneSendWithImages(w, r, pane)
	case strings.HasSuffix(path, "/kill") && r.Method == http.MethodPost:
		s.handlePaneKill(w, r, pane)
	case strings.HasSuffix(path, "/respawn") && r.Method == http.MethodPost:
		s.handlePaneRespawn(w, r, pane)
	case strings.HasSuffix(path, "/kill-window") && r.Method == http.MethodPost:
		s.handleWindowKill(w, r, pane)
	case strings.HasSuffix(path, "/zoom") && r.Method == http.MethodPost:
		s.handlePaneZoom(w, r, pane)
	default:
		s.handlePaneJSON(w, r, pane)
	}
}

func (s *Server) handlePaneJSON(w http.ResponseWriter, r *http.Request, pane tmux.Pane) {
	windows, _ := s.tmux.ListWindows(pane.Session)
	paneInfos, _ := s.tmux.ListPanes(pane.Session, pane.Window)

	capture, err := s.tmux.CapturePaneWithMode(pane, 500)
	if err != nil {
		http.Error(w, "failed to capture pane", http.StatusInternalServerError)
		return
	}

	var panePath, paneCommand string
	for _, p := range paneInfos {
		if p.Index == pane.Index {
			panePath = p.Path
			paneCommand = p.Command
			break
		}
	}

	paneID := pane.Target()
	agent := s.registry.Detect(paneID, paneCommand, capture.Output)
	parseResult := getAgentState(agent, panePath, capture.Output)

	suggestion := ""
	if agent.Type() == agents.AgentClaudeCode {
		suggestion = claude.ExtractSuggestion(capture.Output)
	}

	width, height, _ := s.tmux.GetPaneSize(pane)

	data := PaneData{
		Pane:        pane,
		Output:      agent.FilterStatusBar(capture.Output),
		ParseResult: parseResult,
		Windows:     windows,
		Panes:       paneInfos,
		PaneWidth:   width,
		PaneHeight:  height,
		Suggestion:  suggestion,
		StripItems:  s.buildAgentStripItems(pane.Session, pane.Window, pane.Index),
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(data)
}

func (s *Server) handleAPIOpenCodeSessions(w http.ResponseWriter, r *http.Request) {
	if s.ocManager == nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(OpenCodeData{})
		return
	}

	if r.URL.Query().Get("stream") == "1" {
		s.streamAPIOpenCodeJSON(w, r)
		return
	}

	data := s.buildOpenCodeData(r.Context())
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(data)
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

	fmt.Fprintf(w, ": connected\n\n")
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
	s.handleOpenCodeSession(w, r)
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
