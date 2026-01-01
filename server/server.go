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

		var needsAttention bool
		var parseResult parser.Result

		// Check hook status first (for Claude permission/idle prompts)
		hookNeedsAttention := hasStatus && st.IsFresh(30*time.Second) && st.Status.NeedsAttention()

		// Always check terminal for app-level prompts (brainstorming, choices, etc)
		pane := tmux.Pane{Session: sess.Name, Window: 0, Index: 0}
		output, _ := s.tmux.CapturePane(pane, 100)
		parseResult = parser.Parse(output)

		terminalNeedsAttention := parseResult.Type == parser.TypeError ||
			parseResult.Type == parser.TypeChoice ||
			parseResult.Type == parser.TypeQuestion

		// Either source can trigger attention
		needsAttention = hookNeedsAttention || terminalNeedsAttention

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

func parsePaneTarget(path string) (tmux.Pane, error) {
	// Path format: /pane/session:window.pane or /pane/session:window.pane/send
	path = strings.TrimPrefix(path, "/pane/")
	path = strings.TrimSuffix(path, "/send")

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

	output, err := s.tmux.CapturePane(pane, 500)
	if err != nil {
		http.Error(w, "failed to capture pane: "+err.Error(), http.StatusInternalServerError)
		return
	}

	parseResult := parser.Parse(output)

	data := struct {
		Pane        tmux.Pane
		Output      string
		ParseResult parser.Result
	}{
		Pane:        pane,
		Output:      output,
		ParseResult: parseResult,
	}

	s.templates.ExecuteTemplate(w, "pane.html", data)
}

func (s *Server) handlePaneSend(w http.ResponseWriter, r *http.Request, pane tmux.Pane) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	r.ParseForm()
	input := r.FormValue("input")
	special := r.FormValue("special") == "true"

	var err error
	if special {
		err = s.tmux.SendSpecialKey(pane, input)
	} else {
		err = s.tmux.SendKeys(pane, input, true)
	}

	if err != nil {
		http.Error(w, "failed to send keys: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

func (s *Server) streamPane(w http.ResponseWriter, r *http.Request, pane tmux.Pane) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "SSE not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	var lastOutput string

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			output, err := s.tmux.CapturePane(pane, 500)
			if err != nil {
				continue
			}

			if output != lastOutput {
				lastOutput = output
				for _, line := range strings.Split(output, "\n") {
					fmt.Fprintf(w, "data: %s\n", line)
				}
				fmt.Fprintf(w, "\n")
				flusher.Flush()
			}
		}
	}
}
