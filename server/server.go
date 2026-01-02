// server/server.go
package server

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/noamsto/houston/parser"
	"github.com/noamsto/houston/status"
	"github.com/noamsto/houston/tmux"
	"github.com/noamsto/houston/views"
)

type Server struct {
	tmux    *tmux.Client
	watcher *status.Watcher
	mu      sync.RWMutex
}

type Config struct {
	StatusDir string
}

func New(cfg Config) (*Server, error) {
	return &Server{
		tmux:    tmux.NewClient(),
		watcher: status.NewWatcher(cfg.StatusDir),
	}, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("static"))))

	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/sessions", s.handleSessions)
	mux.HandleFunc("/pane/", s.handlePane)

	return mux
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	views.IndexPage().Render(r.Context(), w)
}

func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	accept := r.Header.Get("Accept")

	// Check if SSE stream requested
	if strings.Contains(accept, "text/event-stream") || r.URL.Query().Get("stream") == "1" {
		s.streamSessions(w, r)
		return
	}

	data := s.buildSessionsData()
	views.Sessions(data).Render(r.Context(), w)
}

func (s *Server) buildSessionsData() views.SessionsData {
	sessions, _ := s.tmux.ListSessions()
	statuses := s.watcher.GetAll()
	_ = statuses // TODO: integrate hook status per-window

	var data views.SessionsData

	for _, sess := range sessions {
		// Get all windows for this session
		windows, err := s.tmux.ListWindows(sess.Name)
		if err != nil || len(windows) == 0 {
			continue
		}

		sessionData := views.SessionWithWindows{
			Session: sess,
		}

		for _, win := range windows {
			// Get actual panes for this window
			panes, _ := s.tmux.ListPanes(sess.Name, win.Index)

			// Find pane to check (prefer active, fallback to first)
			paneIdx := 0
			if len(panes) > 0 {
				paneIdx = panes[0].Index
				for _, p := range panes {
					if p.Active {
						paneIdx = p.Index
						break
					}
				}
			}

			pane := tmux.Pane{Session: sess.Name, Window: win.Index, Index: paneIdx}
			output, _ := s.tmux.CapturePane(pane, 100)
			parseResult := parser.Parse(output)

			windowNeedsAttention := parseResult.Type == parser.TypeError ||
				parseResult.Type == parser.TypeChoice ||
				parseResult.Type == parser.TypeQuestion

			// Extract preview lines - more for attention states
			previewLines := 3
			if windowNeedsAttention {
				previewLines = 10 // Show more context for choices/questions/errors
			}
			preview := getPreviewLines(output, previewLines)

			windowStatus := views.WindowWithStatus{
				Window:         win,
				ParseResult:    parseResult,
				Preview:        preview,
				NeedsAttention: windowNeedsAttention,
			}

			sessionData.Windows = append(sessionData.Windows, windowStatus)

			if windowNeedsAttention {
				sessionData.AttentionCount++
			}
			if parseResult.Type == parser.TypeWorking {
				sessionData.HasWorking = true
			}
		}

		// Categorize session based on its windows' status
		// Skip attached sessions - user is already there
		if sess.Attached {
			data.Idle = append(data.Idle, sessionData)
		} else if sessionData.AttentionCount > 0 {
			data.NeedsAttention = append(data.NeedsAttention, sessionData)
		} else if sessionData.HasWorking {
			data.Active = append(data.Active, sessionData)
		} else {
			data.Idle = append(data.Idle, sessionData)
		}
	}

	return data
}

// getPreviewLines extracts the last n non-empty lines from output
func getPreviewLines(output string, n int) []string {
	lines := strings.Split(output, "\n")
	var result []string

	// Work backwards to find non-empty lines
	for i := len(lines) - 1; i >= 0 && len(result) < n; i-- {
		line := strings.TrimSpace(lines[i])
		if line != "" {
			result = append([]string{line}, result...)
		}
	}

	// Truncate long lines
	for i, line := range result {
		if len(line) > 60 {
			result[i] = line[:57] + "..."
		}
	}

	return result
}

func (s *Server) streamSessions(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "SSE not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	// Send initial comment to establish connection
	fmt.Fprintf(w, ": connected\n\n")
	flusher.Flush()

	// Send initial data
	if err := s.sendSessionsEvent(r.Context(), w, flusher); err != nil {
		slog.Debug("SSE sessions write failed", "error", err)
		return
	}

	// Poll and send updates
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			slog.Debug("SSE sessions client disconnected")
			return
		case <-ticker.C:
			if err := s.sendSessionsEvent(r.Context(), w, flusher); err != nil {
				slog.Debug("SSE sessions write failed", "error", err)
				return
			}
		}
	}
}

func (s *Server) sendSessionsEvent(ctx context.Context, w http.ResponseWriter, flusher http.Flusher) error {
	var buf strings.Builder
	data := s.buildSessionsData()
	views.Sessions(data).Render(ctx, &buf)

	// Build SSE message
	var msg strings.Builder
	for _, line := range strings.Split(buf.String(), "\n") {
		msg.WriteString("data: ")
		msg.WriteString(line)
		msg.WriteString("\n")
	}
	msg.WriteString("\n")

	// Write and check for errors
	_, err := w.Write([]byte(msg.String()))
	if err != nil {
		return err
	}
	flusher.Flush()
	return nil
}

func parsePaneTarget(path string) (tmux.Pane, error) {
	// Path format: /pane/session:window.pane or /pane/session:window.pane/send
	path = strings.TrimPrefix(path, "/pane/")
	path = strings.TrimSuffix(path, "/send")

	// URL-decode the path (handles %2F -> / in session names)
	decoded, err := url.PathUnescape(path)
	if err == nil {
		path = decoded
	}

	// Parse session:window.pane
	var session string
	var window, pane int

	colonIdx := strings.Index(path, ":")
	if colonIdx == -1 {
		return tmux.Pane{Session: path, Window: 0, Index: 0}, nil
	}

	session = path[:colonIdx]
	rest := path[colonIdx+1:]

	dotIdx := strings.Index(rest, ".")
	if dotIdx == -1 {
		fmt.Sscanf(rest, "%d", &window)
	} else {
		fmt.Sscanf(rest[:dotIdx], "%d", &window)
		fmt.Sscanf(rest[dotIdx+1:], "%d", &pane)
	}

	return tmux.Pane{Session: session, Window: window, Index: pane}, nil
}

func (s *Server) handlePane(w http.ResponseWriter, r *http.Request) {
	pane, err := parsePaneTarget(r.URL.Path)
	if err != nil {
		http.Error(w, "invalid pane target", http.StatusBadRequest)
		return
	}

	// Handle send action
	if strings.HasSuffix(r.URL.Path, "/send") {
		s.handlePaneSend(w, r, pane)
		return
	}

	accept := r.Header.Get("Accept")
	if strings.Contains(accept, "text/event-stream") || r.URL.Query().Get("stream") == "1" {
		s.streamPane(w, r, pane)
		return
	}

	// Get windows for navigation
	windows, _ := s.tmux.ListWindows(pane.Session)

	// If no specific window/pane requested, find priority pane (Claude activity)
	if pane.Window == 0 && pane.Index == 0 {
		priorityPaneID := status.FindPriorityPane(pane.Session)
		if priorityPaneID > 0 {
			if winIdx, paneIdx, err := s.tmux.GetPaneLocation(pane.Session, priorityPaneID); err == nil {
				pane.Window = winIdx
				pane.Index = paneIdx
			}
		}
	}

	// Still no window? Use active window
	if pane.Window == 0 && len(windows) > 0 {
		for _, w := range windows {
			if w.Active {
				pane.Window = w.Index
				break
			}
		}
		// Fallback to first window
		if pane.Window == 0 {
			pane.Window = windows[0].Index
		}
	}

	// Get panes for this window
	panes, _ := s.tmux.ListPanes(pane.Session, pane.Window)

	// If pane.Index is 0 but there are panes, find the active one
	if pane.Index == 0 && len(panes) > 0 {
		for _, p := range panes {
			if p.Active {
				pane.Index = p.Index
				break
			}
		}
		// Fallback to first pane
		if pane.Index == 0 {
			pane.Index = panes[0].Index
		}
	}

	// Now capture output with correct pane target
	capture, err := s.tmux.CapturePaneWithMode(pane, 500)
	if err != nil {
		http.Error(w, "failed to capture pane: "+err.Error(), http.StatusInternalServerError)
		return
	}
	parseResult := parser.Parse(capture.Output)

	// Override mode from tmux capture (parser sees filtered output without mode lines)
	switch capture.Mode {
	case "insert":
		parseResult.Mode = parser.ModeInsert
	case "normal":
		parseResult.Mode = parser.ModeNormal
	}

	data := views.PaneData{
		Pane:        pane,
		Output:      capture.Output,
		ParseResult: parseResult,
		Windows:     windows,
		Panes:       panes,
	}

	views.PanePage(data).Render(r.Context(), w)
}

func (s *Server) handlePaneSend(w http.ResponseWriter, r *http.Request, pane tmux.Pane) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	r.ParseForm()
	input := r.FormValue("input")
	special := r.FormValue("special") == "true"
	noEnter := r.FormValue("noenter") == "true"

	slog.Info("send keys", "pane", pane.Target(), "input", input, "special", special, "noenter", noEnter)

	var err error
	if special {
		err = s.tmux.SendSpecialKey(pane, input)
	} else {
		err = s.tmux.SendKeys(pane, input, !noEnter)
	}

	if err != nil {
		slog.Error("send keys failed", "error", err)
		http.Error(w, "failed to send keys: "+err.Error(), http.StatusInternalServerError)
		return
	}

	slog.Debug("send keys success")
	w.WriteHeader(http.StatusOK)
}

func (s *Server) streamPane(w http.ResponseWriter, r *http.Request, pane tmux.Pane) {
	slog.Debug("SSE pane stream started", "pane", pane.Target())

	flusher, ok := w.(http.Flusher)
	if !ok {
		slog.Error("SSE flusher not supported")
		http.Error(w, "SSE not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	// Send initial comment to establish connection
	if _, err := fmt.Fprintf(w, ": connected\n\n"); err != nil {
		slog.Error("SSE initial write failed", "error", err)
		return
	}
	flusher.Flush()

	var lastOutput string
	updateCount := 0

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			slog.Debug("SSE pane disconnected", "pane", pane.Target(), "updates", updateCount)
			return
		case <-ticker.C:
			capture, err := s.tmux.CapturePaneWithMode(pane, 500)
			if err != nil {
				slog.Warn("SSE capture failed", "pane", pane.Target(), "error", err)
				continue
			}

			if capture.Output != lastOutput {
				lastOutput = capture.Output
				lines := strings.Split(capture.Output, "\n")
				updateCount++
				slog.Debug("SSE pane update", "pane", pane.Target(), "bytes", len(capture.Output), "mode", capture.Mode)

				// Build the SSE message with mode as first line
				var buf strings.Builder
				// Send mode as special first line (will be parsed by client)
				buf.WriteString("data: __MODE__:")
				buf.WriteString(capture.Mode)
				buf.WriteString("\n")
				for _, line := range lines {
					// Remove carriage returns that can break SSE
					line = strings.ReplaceAll(line, "\r", "")
					buf.WriteString("data: ")
					buf.WriteString(line)
					buf.WriteString("\n")
				}
				buf.WriteString("\n")

				// Write and check for errors
				_, err := w.Write([]byte(buf.String()))
				if err != nil {
					slog.Error("SSE pane write failed", "pane", pane.Target(), "error", err)
					return
				}
				flusher.Flush()
			}
		}
	}
}
