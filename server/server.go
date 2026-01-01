// server/server.go
package server

import (
	"html/template"
	"net/http"
	"sync"

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
	tmpl, err := template.ParseGlob("templates/*.html")
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

	// Static files
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("static"))))

	// Routes
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

func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	// Will implement SSE vs HTML based on Accept header
	sessions, _ := s.tmux.ListSessions()
	statuses := s.watcher.GetAll()

	data := struct {
		Sessions []tmux.Session
		Statuses map[string]status.SessionStatus
	}{
		Sessions: sessions,
		Statuses: statuses,
	}

	s.templates.ExecuteTemplate(w, "sessions.html", data)
}

func (s *Server) handlePane(w http.ResponseWriter, r *http.Request) {
	// Will implement in next task
	w.Write([]byte("pane handler"))
}
