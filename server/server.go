// server/server.go
package server

import (
	"fmt"
	"html/template"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/noams/tmux-dashboard/parser"
	"github.com/noams/tmux-dashboard/status"
	"github.com/noams/tmux-dashboard/tmux"
)

type Server struct {
	tmux      *tmux.Client
	watcher   *status.Watcher
	templates *template.Template
	mu        sync.RWMutex
}

type Config struct {
	StatusDir string
}

func New(cfg Config) (*Server, error) {
	tmpl, err := template.New("").Funcs(templateFuncs()).ParseGlob("templates/*.html")
	if err != nil {
		return nil, err
	}

	return &Server{
		tmux:      tmux.NewClient(),
		watcher:   status.NewWatcher(cfg.StatusDir),
		templates: tmpl,
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

	s.templates.ExecuteTemplate(w, "index.html", nil)
}

type sessionWithStatus struct {
	Session     tmux.Session
	Status      status.SessionStatus
	ParseResult parser.Result
}

type sessionsData struct {
	NeedsAttention []sessionWithStatus
	OtherSessions  []tmux.Session
}

func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	accept := r.Header.Get("Accept")

	// Check if SSE stream requested
	if strings.Contains(accept, "text/event-stream") || r.URL.Query().Get("stream") == "1" {
		s.streamSessions(w, r)
		return
	}

	data := s.buildSessionsData()
	s.templates.ExecuteTemplate(w, "sessions", data)
}

func (s *Server) buildSessionsData() sessionsData {
	sessions, _ := s.tmux.ListSessions()
	statuses := s.watcher.GetAll()

	var data sessionsData

	for _, sess := range sessions {
		st, hasStatus := statuses[sess.Name]

		// Capture pane output for parsing
		pane := tmux.Pane{Session: sess.Name, Window: 0, Index: 0}
		output, _ := s.tmux.CapturePane(pane, 100)
		parseResult := parser.Parse(output)

		needsAttention := hasStatus && st.Status == status.StatusNeedsAttention
		needsAttention = needsAttention || parseResult.Type == parser.TypeError
		needsAttention = needsAttention || parseResult.Type == parser.TypeChoice
		needsAttention = needsAttention || parseResult.Type == parser.TypeQuestion

		if needsAttention {
			data.NeedsAttention = append(data.NeedsAttention, sessionWithStatus{
				Session:     sess,
				Status:      st,
				ParseResult: parseResult,
			})
		} else {
			data.OtherSessions = append(data.OtherSessions, sess)
		}
	}

	return data
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

	// Send initial data
	s.sendSessionsEvent(w, flusher)

	// Poll and send updates
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			s.sendSessionsEvent(w, flusher)
		}
	}
}

func (s *Server) sendSessionsEvent(w http.ResponseWriter, flusher http.Flusher) {
	var buf strings.Builder
	data := s.buildSessionsData()
	s.templates.ExecuteTemplate(&buf, "sessions", data)

	// SSE format: data lines followed by blank line
	for _, line := range strings.Split(buf.String(), "\n") {
		fmt.Fprintf(w, "data: %s\n", line)
	}
	fmt.Fprintf(w, "\n")
	flusher.Flush()
}

func (s *Server) handlePane(w http.ResponseWriter, r *http.Request) {
	w.Write([]byte("pane handler - will implement next"))
}
