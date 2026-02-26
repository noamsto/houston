// server/server.go
package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/noamsto/houston/agents"
	"github.com/noamsto/houston/agents/amp"
	"github.com/noamsto/houston/agents/claude"
	"github.com/noamsto/houston/agents/generic"
	"github.com/noamsto/houston/internal/ansi"
	"github.com/noamsto/houston/opencode"
	"github.com/noamsto/houston/parser"
	"github.com/noamsto/houston/status"
	"github.com/noamsto/houston/tmux"
	"github.com/noamsto/houston/views"
)

// getAgentState gets state from the detected agent.
// For Amp: prefer terminal parsing (real-time status) over file-based state.
// For Claude: prefer file-based state, with terminal fallback for choices.
func getAgentState(agent agents.Agent, panePath, terminalOutput string) parser.Result {
	if agent == nil {
		return parser.Result{Type: parser.TypeIdle}
	}

	// For Amp, always use terminal parsing as it shows real-time status
	// (thread files only update when messages complete, not during streaming)
	if agent.Type() == agents.AgentAmp {
		return agent.ParseOutput(terminalOutput).Result
	}

	// For Claude, try file-based state first for richer info
	if panePath != "" {
		state, err := agent.GetStateFromFiles(panePath)
		if err == nil {
			// Check if waiting for permission and use terminal for choices
			if agent.Type() == agents.AgentClaudeCode {
				if state.Result.Type == parser.TypeQuestion {
					terminalResult := parser.Parse(terminalOutput)
					if terminalResult.Type == parser.TypeChoice && len(terminalResult.Choices) > 0 {
						slog.Debug("Using terminal choices for permission", "choices", len(terminalResult.Choices))
						return terminalResult
					}
				}
			}
			return state.Result
		}
		slog.Debug("Agent file state unavailable, using terminal parser", "agent", agent.Type(), "error", err)
	}

	// Fallback: parse terminal output
	return agent.ParseOutput(terminalOutput).Result
}

// recentActivityTTL is how long a session stays in "Active" after becoming idle
const recentActivityTTL = 2 * time.Minute

type Server struct {
	tmux     *tmux.Client
	watcher  *status.Watcher
	registry *agents.Registry
	font     FontController
	uiFS     fs.FS // embedded React SPA (nil = use legacy templ handlers)
	mu       sync.RWMutex

	// Track when sessions last had activity (for keeping recently-active in Active section)
	lastActivity   map[string]time.Time // session name -> last working timestamp
	lastActivityMu sync.RWMutex

	// OpenCode integration
	ocDiscovery *opencode.Discovery
	ocManager   *opencode.Manager
}

// FontController controls terminal font size.
type FontController interface {
	Increase() error
	Decrease() error
	Reset() error
	Name() string
}

type Config struct {
	StatusDir      string
	FontController FontController

	// OpenCode configuration
	OpenCodeEnabled bool   // Enable OpenCode integration
	OpenCodeURL     string // Static URL (if set, skip discovery)
	OpenCodePorts   []int  // Ports to scan (default: 4096-4100)

	// UIFS is the embedded React SPA filesystem. When set, serves the SPA at /.
	// When nil, falls back to the legacy templ handlers.
	UIFS fs.FS
}

func New(cfg Config) (*Server, error) {
	registry := agents.NewRegistry(
		claude.New(),
		amp.New(),
		generic.New(), // Must be last (fallback)
	)

	s := &Server{
		tmux:         tmux.NewClient(),
		watcher:      status.NewWatcher(cfg.StatusDir),
		registry:     registry,
		font:         cfg.FontController,
		uiFS:         cfg.UIFS,
		lastActivity: make(map[string]time.Time),
	}

	// Initialize OpenCode integration if enabled
	if cfg.OpenCodeEnabled {
		var opts []opencode.DiscoveryOption
		if cfg.OpenCodeURL != "" {
			opts = append(opts, opencode.WithStaticURL(cfg.OpenCodeURL))
		}
		if len(cfg.OpenCodePorts) > 0 {
			opts = append(opts, opencode.WithPorts(cfg.OpenCodePorts))
		}

		s.ocDiscovery = opencode.NewDiscovery(opts...)
		s.ocManager = opencode.NewManager(s.ocDiscovery)

		// Do initial scan synchronously
		ctx := context.Background()
		if cfg.OpenCodeURL != "" {
			slog.Info("OpenCode scanning", "url", cfg.OpenCodeURL)
		} else {
			slog.Info("OpenCode scanning", "ports", "4096-4100")
		}
		servers := s.ocDiscovery.Scan(ctx)
		if len(servers) > 0 {
			slog.Info("OpenCode servers found", "count", len(servers))
		} else {
			slog.Info("OpenCode no servers found (will keep scanning)")
		}

		// Start background discovery
		s.ocDiscovery.StartBackgroundScan(ctx, 30*time.Second)
		s.ocManager.StartBackgroundRefresh(ctx, 10*time.Second)
	}

	return s, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("static"))))

	// Serve React SPA when embedded FS is available; otherwise fall back to legacy templ handlers.
	if s.uiFS != nil {
		mux.Handle("/", SPAHandler(s.uiFS))
	} else {
		mux.HandleFunc("/", s.handleIndex)
		mux.HandleFunc("/sessions", s.handleSessions)
		mux.HandleFunc("/pane/", s.handlePane)
		mux.HandleFunc("/font/", s.handleFont)
		mux.HandleFunc("/opencode/sessions", s.handleOpenCodeSessions)
		mux.HandleFunc("/opencode/session/", s.handleOpenCodeSession)
	}

	// JSON API routes (always available)
	apiMux := http.NewServeMux()
	apiMux.HandleFunc("/api/sessions", s.handleAPISessions)
	apiMux.HandleFunc("/api/pane/", s.handleAPIPane)
	apiMux.HandleFunc("/api/opencode/sessions", s.handleAPIOpenCodeSessions)
	apiMux.HandleFunc("/api/opencode/session/", s.handleAPIOpenCodeSession)
	mux.Handle("/api/", corsMiddleware(apiMux))

	return mux
}

// SPAHandler serves an embedded filesystem with fallback to index.html for client-side routing.
func SPAHandler(uiFS fs.FS) http.Handler {
	fileServer := http.FileServer(http.FS(uiFS))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" {
			path = "index.html"
		}

		// Serve the file if it exists; otherwise fallback to index.html (client-side routing).
		if _, err := fs.Stat(uiFS, path); err == nil {
			fileServer.ServeHTTP(w, r)
			return
		}

		r.URL.Path = "/"
		fileServer.ServeHTTP(w, r)
	})
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

// paneScore represents the priority score for a pane
type paneScore struct {
	info  *tmux.PaneInfo
	index int
	score int // Higher score = higher priority
}

// findBestPane selects the best pane to display for a window
// Priority: Agent attention > Agent working > Agent idle > active > first
func (s *Server) findBestPane(session string, windowIdx int, panes []tmux.PaneInfo) paneScore {
	if len(panes) == 0 {
		return paneScore{nil, 0, 0}
	}

	bestPane := paneScore{&panes[0], panes[0].Index, 0}

	for i := range panes {
		p := &panes[i]
		score := 0

		pane := tmux.Pane{Session: session, Window: windowIdx, Index: p.Index}
		paneID := pane.Target()
		output, err := s.tmux.CapturePane(pane, 100)
		if err != nil {
			continue
		}

		agent := s.registry.Detect(paneID, p.Command, output)
		if agent.Type() != agents.AgentGeneric {
			parseResult := getAgentState(agent, p.Path, output)

			// Agent pane needing attention = highest priority
			if parseResult.Type == parser.TypeError ||
				parseResult.Type == parser.TypeChoice ||
				parseResult.Type == parser.TypeQuestion {
				score = 100 // Highest priority
			} else if parseResult.Type == parser.TypeWorking {
				score = 50 // Working agent
			} else {
				score = 30 // Idle/done agent
			}
		} else {
			// Non-agent pane: prefer active
			if p.Active {
				score = 10
			} else {
				score = 1
			}
		}

		if score > bestPane.score {
			bestPane = paneScore{p, p.Index, score}
		}
	}

	return bestPane
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

		// Get worktrees once per session (using first window's pane path)
		var worktrees map[string]string
		var worktreesLoaded bool

		for _, win := range windows {
			// Get actual panes for this window
			panes, _ := s.tmux.ListPanes(sess.Name, win.Index)

			// Find best pane to display based on priority:
			// 1. Agent pane needing attention (error/choice/question)
			// 2. Agent pane that's working
			// 3. Agent pane that's idle/done
			// 4. Active pane (non-agent)
			// 5. First pane
			var activePaneInfo *tmux.PaneInfo
			paneIdx := 0
			if len(panes) > 0 {
				bestPane := s.findBestPane(sess.Name, win.Index, panes)
				activePaneInfo = bestPane.info
				paneIdx = bestPane.index
			}

			// Load worktrees on first window (lazy load)
			if !worktreesLoaded && activePaneInfo != nil && activePaneInfo.Path != "" {
				worktrees, _ = tmux.GetWorktrees(activePaneInfo.Path)
				worktreesLoaded = true
			}

			// Get branch for this window's pane
			var branch string
			if activePaneInfo != nil {
				branch = tmux.GetBranchForPath(activePaneInfo.Path, worktrees)
			}
			process := win.Name

			pane := tmux.Pane{Session: sess.Name, Window: win.Index, Index: paneIdx}
			paneID := pane.Target()
			output, _ := s.tmux.CapturePane(pane, 100)

			// Get pane path for agent state lookup
			var panePath string
			var paneCommand string
			if activePaneInfo != nil {
				panePath = activePaneInfo.Path
				paneCommand = activePaneInfo.Command
			}

			// Detect agent and get state
			agent := s.registry.Detect(paneID, paneCommand, output)
			parseResult := getAgentState(agent, panePath, output)

			// Only mark as needing attention if it's an agent window
			isAgentWindow := agent.Type() != agents.AgentGeneric
			windowNeedsAttention := isAgentWindow && (parseResult.Type == parser.TypeError ||
				parseResult.Type == parser.TypeChoice ||
				parseResult.Type == parser.TypeQuestion)

			// Extract preview lines - more for attention states
			previewLines := 15
			if windowNeedsAttention {
				previewLines = 25
			}
			preview := s.getPreviewLines(agent, output, previewLines)

			windowStatus := views.WindowWithStatus{
				Window:         win,
				Pane:           pane,
				ParseResult:    parseResult,
				Preview:        preview,
				NeedsAttention: windowNeedsAttention,
				Branch:         branch,
				Process:        process,
				AgentType:      agent.Type(),
			}

			sessionData.Windows = append(sessionData.Windows, windowStatus)

			if windowNeedsAttention {
				sessionData.AttentionCount++
			}
			// Check if window is actively working using smarter heuristics
			cmd := ""
			if activePaneInfo != nil {
				cmd = activePaneInfo.Command
			}
			if isWindowActive(cmd, win.LastActivity, isAgentWindow, parseResult) {
				sessionData.HasWorking = true
			}
		}

		// Sort windows by activity: attention first, then working, then idle
		sort.SliceStable(sessionData.Windows, func(i, j int) bool {
			wi, wj := sessionData.Windows[i], sessionData.Windows[j]
			// Priority: attention > working > idle
			scoreI := windowActivityScore(wi)
			scoreJ := windowActivityScore(wj)
			return scoreI > scoreJ
		})

		// Update last activity tracking
		if sessionData.HasWorking {
			s.lastActivityMu.Lock()
			s.lastActivity[sess.Name] = time.Now()
			s.lastActivityMu.Unlock()
		}

		// Check if session has recent activity (within TTL)
		s.lastActivityMu.RLock()
		lastActive, hasLastActive := s.lastActivity[sess.Name]
		s.lastActivityMu.RUnlock()
		recentlyActive := hasLastActive && time.Since(lastActive) < recentActivityTTL

		// Categorize session based on its windows' actual status
		if sessionData.AttentionCount > 0 {
			data.NeedsAttention = append(data.NeedsAttention, sessionData)
		} else if sessionData.HasWorking || recentlyActive {
			// Keep in Active if currently working OR recently active
			data.Active = append(data.Active, sessionData)
		} else {
			data.Idle = append(data.Idle, sessionData)
		}
	}

	return data
}

// buildAgentStripItems returns strip items for all agent windows across all sessions,
// for the desktop pane page navigation strip.
func (s *Server) buildAgentStripItems(activeSession string, activeWindow, activePane int) []views.AgentStripItem {
	sessions, _ := s.tmux.ListSessions()
	var items []views.AgentStripItem

	for _, sess := range sessions {
		windows, err := s.tmux.ListWindows(sess.Name)
		if err != nil || len(windows) == 0 {
			continue
		}

		// Load worktrees once per session
		var worktrees map[string]string
		var worktreesLoaded bool

		for _, win := range windows {
			panes, _ := s.tmux.ListPanes(sess.Name, win.Index)
			if len(panes) == 0 {
				continue
			}

			bestPane := s.findBestPane(sess.Name, win.Index, panes)
			activePaneInfo := bestPane.info
			paneIdx := bestPane.index

			if !worktreesLoaded && activePaneInfo != nil && activePaneInfo.Path != "" {
				worktrees, _ = tmux.GetWorktrees(activePaneInfo.Path)
				worktreesLoaded = true
			}

			var panePath, paneCommand string
			if activePaneInfo != nil {
				panePath = activePaneInfo.Path
				paneCommand = activePaneInfo.Command
			}

			pane := tmux.Pane{Session: sess.Name, Window: win.Index, Index: paneIdx}
			paneID := pane.Target()
			output, _ := s.tmux.CapturePane(pane, 50)

			agent := s.registry.Detect(paneID, paneCommand, output)
			// Skip non-agent windows
			if agent.Type() == agents.AgentGeneric {
				continue
			}

			parseResult := getAgentState(agent, panePath, output)

			var branch string
			if activePaneInfo != nil {
				branch = tmux.GetBranchForPath(activePaneInfo.Path, worktrees)
			}

			indicator := "idle"
			if parseResult.Type == parser.TypeError || parseResult.Type == parser.TypeChoice || parseResult.Type == parser.TypeQuestion {
				indicator = "attention"
			} else if parseResult.Type == parser.TypeWorking {
				indicator = "working"
			} else if parseResult.Type == parser.TypeDone {
				indicator = "done"
			}

			displayName := branch
			if displayName == "" {
				displayName = paneCommand
			}
			if displayName == "" {
				displayName = win.Name
			}

			items = append(items, views.AgentStripItem{
				Session:   sess.Name,
				Window:    win.Index,
				Pane:      paneIdx,
				Name:      displayName,
				Indicator: indicator,
				AgentType: agent.Type(),
				Active:    sess.Name == activeSession && win.Index == activeWindow && paneIdx == activePane,
			})
		}
	}
	return items
}

// getPreviewLines extracts the last n non-empty lines from output, using agent-specific filtering
// Note: Preview lines in window cards are now only used as fallback - action bar uses SSE for live data
func (s *Server) getPreviewLines(agent agents.Agent, output string, n int) []string {
	// Filter output using agent-specific status bar handling
	filtered := agent.FilterStatusBar(output)
	lines := strings.Split(filtered, "\n")
	var result []string

	// Work backwards to find non-empty lines
	for i := len(lines) - 1; i >= 0 && len(result) < n; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		// Skip prompt line (just ">")
		if line == ">" {
			continue
		}
		// Skip separator lines (all dashes or box drawing)
		if isAllSeparator(line) {
			continue
		}
		// Strip ANSI codes for window card preview (ESC gets lost in HTML anyway)
		line = ansi.StripOrphaned(line)
		result = append([]string{line}, result...)
	}

	return result
}

// windowActivityScore returns a score for sorting windows by activity
// Higher score = more important (should appear first)
func windowActivityScore(win views.WindowWithStatus) int {
	if win.NeedsAttention {
		return 4 // Highest priority - needs user attention
	}
	if win.ParseResult.Type == parser.TypeWorking {
		return 3 // Claude actively working
	}
	// Check process type for non-Claude windows
	procType := classifyProcess(win.Process)
	switch procType {
	case ProcessServer:
		return 2 // Servers running
	case ProcessUnknown:
		// Unknown process with recent activity
		if time.Since(win.Window.LastActivity) < 30*time.Second {
			return 2 // Recent activity
		}
		return 1
	default:
		return 1 // Shell or interactive - idle
	}
}

// ProcessType categorizes what kind of process is running
type ProcessType int

const (
	ProcessShell       ProcessType = iota // bash, zsh, fish - idle prompt
	ProcessInteractive                    // vim, less, htop - waiting for user input
	ProcessServer                         // servers, daemons - running in background
	ProcessUnknown                        // other processes
)

// classifyProcess determines what type of process is running
func classifyProcess(cmd string) ProcessType {
	cmd = strings.ToLower(cmd)

	// Shells - always idle
	shells := []string{"bash", "zsh", "fish", "sh", "dash", "ksh", "tcsh", "csh"}
	for _, s := range shells {
		if cmd == s {
			return ProcessShell
		}
	}

	// Interactive tools - waiting for user input, effectively idle
	interactive := []string{
		"vim", "nvim", "vi", "nano", "emacs", "pico", "micro", // editors
		"less", "more", "most", "man", "info", // pagers
		"htop", "top", "btop", "atop", "glances", // monitors
		"lazygit", "lazydocker", "tig", "gitui", // git TUIs
		"ranger", "mc", "nnn", "lf", "yazi", // file managers
		"tmux", "screen", // multiplexers (nested)
		"fzf", "sk", // fuzzy finders
	}
	for _, i := range interactive {
		if cmd == i {
			return ProcessInteractive
		}
	}

	// Known server/daemon processes
	servers := []string{
		"node", "deno", "bun", // JS runtimes
		"nginx", "apache", "caddy", "httpd", // web servers
		"postgres", "mysql", "redis", "mongo", "sqlite", // databases
		"docker", "podman", "containerd", // containers
	}
	for _, s := range servers {
		if cmd == s {
			return ProcessServer
		}
	}

	return ProcessUnknown
}

// isWindowActive determines if a window is actively working based on:
// - Process type (shells/interactive are idle)
// - Recent activity (output in last N seconds)
// - For agent windows, use the parser
func isWindowActive(cmd string, lastActivity time.Time, isAgentWindow bool, parseResult parser.Result) bool {
	// Agent windows use their own detection
	if isAgentWindow {
		return parseResult.Type == parser.TypeWorking
	}

	procType := classifyProcess(cmd)

	switch procType {
	case ProcessShell:
		// Shells are always idle
		return false
	case ProcessInteractive:
		// Interactive tools are waiting for user - idle
		return false
	case ProcessServer:
		// Servers are always "active" (running useful background work)
		return true
	default:
		// Unknown process - check for recent activity
		// If there was output in the last 30 seconds, consider it active
		return time.Since(lastActivity) < 30*time.Second
	}
}

// ClaudeMode represents a detected Claude Code mode indicator
type ClaudeMode struct {
	Icon  string `json:"icon"`  // "⏵⏵", "⏸", etc.
	Label string `json:"label"` // "accept edits", "plan mode", etc.
	State string `json:"state"` // "on" or "off"
}

// detectClaudeMode finds the current Claude Code mode from status line
// Looks for patterns like: "⏵⏵ accept edits on (shift+tab...)" or "⏸ plan mode on"
// Input is the extracted status line (after separator), which may span multiple lines if wrapped
func detectClaudeMode(statusLine string) ClaudeMode {
	// Strip ANSI codes - Claude Code wraps text with color codes
	stripped := ansi.Strip(statusLine)

	// Check for "accept edits on" first (most common)
	if strings.Contains(stripped, "accept edits on") {
		return ClaudeMode{Icon: "⏵⏵", Label: "accept edits", State: "on"}
	}
	if strings.Contains(stripped, "accept edits off") {
		return ClaudeMode{Icon: "⏵⏵", Label: "accept edits", State: "off"}
	}

	// Check for "plan mode on"
	if strings.Contains(stripped, "plan mode on") {
		return ClaudeMode{Icon: "⏸", Label: "plan mode", State: "on"}
	}
	if strings.Contains(stripped, "plan mode off") {
		return ClaudeMode{Icon: "⏸", Label: "plan mode", State: "off"}
	}

	return ClaudeMode{} // Empty = not detected
}

// isAllSeparator checks if a line is just separator characters
func isAllSeparator(line string) bool {
	for _, r := range line {
		// Allow box drawing chars, dashes, equals
		if r != '─' && r != '-' && r != '=' && r != '━' && r != '│' && r != '┃' {
			return false
		}
	}
	return len(line) > 3 // Must be at least a few chars to be a separator
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
	// Path format: /pane/session:window.pane or /pane/session:window.pane/action
	path = strings.TrimPrefix(path, "/pane/")
	path = strings.TrimSuffix(path, "/send-with-images")
	path = strings.TrimSuffix(path, "/send-with-image")
	path = strings.TrimSuffix(path, "/send")
	path = strings.TrimSuffix(path, "/kill")
	path = strings.TrimSuffix(path, "/respawn")
	path = strings.TrimSuffix(path, "/kill-window")
	path = strings.TrimSuffix(path, "/zoom")
	path = strings.TrimSuffix(path, "/resize")
	path = strings.TrimSuffix(path, "/ws")

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

	// Handle send with multiple images action
	if strings.HasSuffix(r.URL.Path, "/send-with-images") {
		s.handlePaneSendWithImages(w, r, pane)
		return
	}

	// Handle send with image action (legacy, single image)
	if strings.HasSuffix(r.URL.Path, "/send-with-image") {
		s.handlePaneSendWithImage(w, r, pane)
		return
	}

	// Handle send action
	if strings.HasSuffix(r.URL.Path, "/send") {
		s.handlePaneSend(w, r, pane)
		return
	}

	// Handle kill pane action
	if strings.HasSuffix(r.URL.Path, "/kill") {
		s.handlePaneKill(w, r, pane)
		return
	}

	// Handle respawn pane action
	if strings.HasSuffix(r.URL.Path, "/respawn") {
		s.handlePaneRespawn(w, r, pane)
		return
	}

	// Handle kill window action
	if strings.HasSuffix(r.URL.Path, "/kill-window") {
		s.handleWindowKill(w, r, pane)
		return
	}

	// Handle resize pane action
	if strings.HasSuffix(r.URL.Path, "/resize") {
		s.handlePaneResize(w, r, pane)
		return
	}

	// Handle zoom pane action
	if strings.HasSuffix(r.URL.Path, "/zoom") {
		s.handlePaneZoom(w, r, pane)
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

	// Get pane info for agent detection
	var panePath, paneCommand string
	for _, p := range panes {
		if p.Index == pane.Index {
			panePath = p.Path
			paneCommand = p.Command
			break
		}
	}

	// Detect agent and get state
	paneID := pane.Target()
	agent := s.registry.Detect(paneID, paneCommand, capture.Output)
	parseResult := getAgentState(agent, panePath, capture.Output)

	// Filter output for display
	filteredOutput := agent.FilterStatusBar(capture.Output)

	// Extract prompt suggestion from terminal output for Claude Code panes
	var suggestion string
	if agent.Type() == agents.AgentClaudeCode {
		suggestion = claude.ExtractSuggestion(capture.Output)
	}

	// Build agent strip items for desktop navigation
	stripItems := s.buildAgentStripItems(pane.Session, pane.Window, pane.Index)

	data := views.PaneData{
		Pane:        pane,
		Output:      filteredOutput,
		ParseResult: parseResult,
		Windows:     windows,
		Panes:       panes,
		Suggestion:  suggestion,
		StripItems:  stripItems,
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

func (s *Server) handlePaneSendWithImage(w http.ResponseWriter, r *http.Request, pane tmux.Pane) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 50*1024*1024) // 50MB limit

	var req struct {
		Text  string `json:"text"`
		Image struct {
			Name string `json:"name"`
			Type string `json:"type"`
			Data string `json:"data"` // base64 encoded
		} `json:"image"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		slog.Error("failed to decode image request", "error", err)
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	// Decode base64 image
	imageData, err := base64.StdEncoding.DecodeString(req.Image.Data)
	if err != nil {
		slog.Error("failed to decode base64 image", "error", err)
		http.Error(w, "invalid image data", http.StatusBadRequest)
		return
	}

	// Write image to temp file with sanitized filename
	safeName := filepath.Base(req.Image.Name)
	tmpPath := fmt.Sprintf("/tmp/houston-%d-%s", time.Now().UnixNano(), safeName)
	tmpFile, err := os.Create(tmpPath)
	if err != nil {
		slog.Error("failed to create temp file", "error", err)
		http.Error(w, "failed to save image", http.StatusInternalServerError)
		return
	}

	if _, err := tmpFile.Write(imageData); err != nil {
		slog.Error("failed to write image", "error", err)
		tmpFile.Close()
		os.Remove(tmpFile.Name()) // Clean up on error
		http.Error(w, "failed to save image", http.StatusInternalServerError)
		return
	}
	tmpFile.Close()

	// Note: We don't clean up temp file after sending
	// It remains in /tmp for user to reference and will be cleaned by OS

	// Send image path and text to Claude Code
	// Format: type the image path + newline + text + Enter
	message := tmpFile.Name()
	if req.Text != "" {
		message = fmt.Sprintf("%s\n%s", tmpFile.Name(), req.Text)
	}

	slog.Info("send image with text", "pane", pane.Target(), "image", tmpFile.Name(), "text", req.Text)

	if err := s.tmux.SendKeys(pane, message, true); err != nil {
		slog.Error("failed to send image", "error", err)
		http.Error(w, "failed to send: "+err.Error(), http.StatusInternalServerError)
		return
	}

	slog.Debug("send image success")
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handlePaneSendWithImages(w http.ResponseWriter, r *http.Request, pane tmux.Pane) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 50*1024*1024) // 50MB limit

	var req struct {
		Text   string `json:"text"`
		Images []struct {
			Name string `json:"name"`
			Type string `json:"type"`
			Data string `json:"data"` // base64 encoded
		} `json:"images"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		slog.Error("failed to decode images request", "error", err)
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if len(req.Images) == 0 {
		http.Error(w, "no images provided", http.StatusBadRequest)
		return
	}

	// Process all images and create temp files
	var tmpFiles []string
	var cleanupOnError []string

	for i, img := range req.Images {
		// Decode base64 image
		imageData, err := base64.StdEncoding.DecodeString(img.Data)
		if err != nil {
			slog.Error("failed to decode base64 image", "error", err, "index", i)
			// Clean up any files created so far on error
			for _, f := range cleanupOnError {
				os.Remove(f)
			}
			http.Error(w, fmt.Sprintf("invalid image data at index %d", i), http.StatusBadRequest)
			return
		}

		// Write image to temp file with sanitized filename
		safeName := filepath.Base(img.Name)
		tmpPath := fmt.Sprintf("/tmp/houston-%d-%s", time.Now().UnixNano(), safeName)
		tmpFile, err := os.Create(tmpPath)
		if err != nil {
			slog.Error("failed to create temp file", "error", err, "index", i)
			// Clean up any files created so far on error
			for _, f := range cleanupOnError {
				os.Remove(f)
			}
			http.Error(w, "failed to save image", http.StatusInternalServerError)
			return
		}

		if _, err := tmpFile.Write(imageData); err != nil {
			slog.Error("failed to write image", "error", err, "index", i)
			tmpFile.Close()
			os.Remove(tmpFile.Name())
			// Clean up any files created so far on error
			for _, f := range cleanupOnError {
				os.Remove(f)
			}
			http.Error(w, "failed to save image", http.StatusInternalServerError)
			return
		}
		tmpFile.Close()

		tmpFiles = append(tmpFiles, tmpFile.Name())
		cleanupOnError = append(cleanupOnError, tmpFile.Name())
	}

	// Note: We don't clean up temp files after sending
	// They remain in /tmp for user to reference and will be cleaned by OS

	// Send all image paths and text to Claude Code as a single prompt line
	// Format: image1 image2 image3 text + Enter
	message := strings.Join(tmpFiles, " ")
	if req.Text != "" {
		message = fmt.Sprintf("%s %s", message, req.Text)
	}

	slog.Info("send images with text", "pane", pane.Target(), "count", len(tmpFiles), "text", req.Text)

	if err := s.tmux.SendKeys(pane, message, true); err != nil {
		slog.Error("failed to send images", "error", err)
		http.Error(w, "failed to send: "+err.Error(), http.StatusInternalServerError)
		return
	}

	slog.Debug("send images success", "count", len(tmpFiles))
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handlePaneKill(w http.ResponseWriter, r *http.Request, pane tmux.Pane) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	slog.Info("kill pane", "pane", pane.Target())

	if err := s.tmux.KillPane(pane); err != nil {
		slog.Error("kill pane failed", "error", err)
		http.Error(w, "failed to kill pane: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Redirect back to session or home
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) handlePaneRespawn(w http.ResponseWriter, r *http.Request, pane tmux.Pane) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	slog.Info("respawn pane", "pane", pane.Target())

	if err := s.tmux.RespawnPane(pane); err != nil {
		slog.Error("respawn pane failed", "error", err)
		http.Error(w, "failed to respawn pane: "+err.Error(), http.StatusInternalServerError)
		return
	}

	slog.Debug("respawn pane success")
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleWindowKill(w http.ResponseWriter, r *http.Request, pane tmux.Pane) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	slog.Info("kill window", "session", pane.Session, "window", pane.Window)

	if err := s.tmux.KillWindow(pane.Session, pane.Window); err != nil {
		slog.Error("kill window failed", "error", err)
		http.Error(w, "failed to kill window: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Redirect back to home
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) handlePaneResize(w http.ResponseWriter, r *http.Request, pane tmux.Pane) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	r.ParseForm()
	direction := r.FormValue("direction") // U, D, L, R
	adjustment := 5                       // default
	if adj := r.FormValue("adjustment"); adj != "" {
		if n, err := strconv.Atoi(adj); err == nil && n > 0 {
			adjustment = n
		}
	}

	// Validate direction
	switch direction {
	case "U", "D", "L", "R":
		// valid
	default:
		http.Error(w, "invalid direction: must be U, D, L, or R", http.StatusBadRequest)
		return
	}

	slog.Info("resize pane", "pane", pane.Target(), "direction", direction, "adjustment", adjustment)

	if err := s.tmux.ResizePane(pane, direction, adjustment); err != nil {
		slog.Error("resize pane failed", "error", err)
		http.Error(w, "failed to resize pane: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

func (s *Server) handlePaneZoom(w http.ResponseWriter, r *http.Request, pane tmux.Pane) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	slog.Info("zoom pane", "pane", pane.Target())

	if err := s.tmux.ZoomPane(pane); err != nil {
		slog.Error("zoom pane failed", "error", err)
		http.Error(w, "failed to zoom pane: "+err.Error(), http.StatusInternalServerError)
		return
	}

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

	// Get pane info for agent detection (once at start)
	var panePath, paneCommand string
	panes, _ := s.tmux.ListPanes(pane.Session, pane.Window)
	for _, p := range panes {
		if p.Index == pane.Index {
			panePath = p.Path
			paneCommand = p.Command
			break
		}
	}
	paneID := pane.Target()

	// Send initial comment to establish connection
	if _, err := fmt.Fprintf(w, ": connected\n\n"); err != nil {
		slog.Error("SSE initial write failed", "error", err)
		return
	}
	flusher.Flush()

	var lastOutput string
	var lastStatusLine string
	var lastAgentModeJSON string
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

			// Detect agent and get mode
			agent := s.registry.Detect(paneID, paneCommand, capture.Output)
			mode := agent.DetectMode(capture.Output)
			statusLine := agent.ExtractStatusLine(capture.Output)
			filteredOutput := agent.FilterStatusBar(capture.Output)

			// Detect agent mode indicator from status line
			agentMode := detectClaudeMode(statusLine) // TODO: make agent-specific
			agentModeJSON, _ := json.Marshal(agentMode)
			agentModeJSONStr := string(agentModeJSON)

			// Send update if output, status line, or mode changed
			statusChanged := statusLine != lastStatusLine
			modeChanged := agentModeJSONStr != lastAgentModeJSON
			if filteredOutput != lastOutput || statusChanged || modeChanged {
				lastOutput = filteredOutput
				lastStatusLine = statusLine
				lastAgentModeJSON = agentModeJSONStr
				lines := strings.Split(filteredOutput, "\n")
				updateCount++

				// Parse output for choices
				strippedOutput := ansi.Strip(capture.Output)
				parseResult := getAgentState(agent, panePath, strippedOutput)

				// Build the SSE message with metadata as first lines
				var buf strings.Builder
				slog.Debug("SSE mode", "pane", pane.Target(), "mode", mode.String(), "agent", agent.Type())
				buf.WriteString("data: __MODE__:")
				buf.WriteString(mode.String())
				buf.WriteString("\n")
				buf.WriteString("data: __AGENT__:")
				buf.WriteString(string(agent.Type()))
				buf.WriteString("\n")
				buf.WriteString("data: __CHOICES__:")
				buf.WriteString(strings.Join(parseResult.Choices, "|"))
				buf.WriteString("\n")
				buf.WriteString("data: __CLAUDEMODE__:")
				buf.Write(agentModeJSON)
				buf.WriteString("\n")
				if statusLine != "" {
					slog.Debug("SSE status line", "pane", pane.Target(), "status", statusLine, "len", len(statusLine))
				}
				// Replace newlines with placeholder for SSE transmission
				sseStatusLine := strings.ReplaceAll(statusLine, "\n", "␊")
				buf.WriteString("data: __STATUSLINE__:")
				buf.WriteString(sseStatusLine)
				buf.WriteString("\n")
				// Send structured Amp status if available
				if agent.Type() == agents.AgentAmp {
					ampStatus := amp.ParseStatus(statusLine)
					buf.WriteString("data: __AMPSTATUS__:")
					buf.WriteString(ampStatus.FormatStatusJSON())
					buf.WriteString("\n")
				}
				// Extract prompt suggestion from terminal output for Claude Code
				var suggestion string
				if agent.Type() == agents.AgentClaudeCode {
					suggestion = claude.ExtractSuggestion(capture.Output)
				}
				buf.WriteString("data: __SUGGESTION__:")
				buf.WriteString(suggestion)
				buf.WriteString("\n")
				for _, line := range lines {
					line = strings.ReplaceAll(line, "\r", "")
					buf.WriteString("data: ")
					buf.WriteString(line)
					buf.WriteString("\n")
				}
				buf.WriteString("\n")

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

func (s *Server) handleFont(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if s.font == nil {
		http.Error(w, "no terminal font controller configured", http.StatusNotImplemented)
		return
	}

	action := strings.TrimPrefix(r.URL.Path, "/font/")
	var err error

	switch action {
	case "increase":
		err = s.font.Increase()
	case "decrease":
		err = s.font.Decrease()
	case "reset":
		err = s.font.Reset()
	case "info":
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"terminal":"` + s.font.Name() + `"}`))
		return
	default:
		http.Error(w, "unknown action: "+action, http.StatusBadRequest)
		return
	}

	if err != nil {
		slog.Error("font control failed", "action", action, "error", err)
		http.Error(w, "font control failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	slog.Info("font size changed", "action", action, "terminal", s.font.Name())
	w.WriteHeader(http.StatusOK)
}

// OpenCode handlers

func (s *Server) handleOpenCodeSessions(w http.ResponseWriter, r *http.Request) {
	if s.ocManager == nil {
		http.Error(w, "OpenCode integration not enabled", http.StatusNotImplemented)
		return
	}

	accept := r.Header.Get("Accept")

	// Check if SSE stream requested
	if strings.Contains(accept, "text/event-stream") || r.URL.Query().Get("stream") == "1" {
		s.streamOpenCodeSessions(w, r)
		return
	}

	data := s.buildOpenCodeData(r.Context())

	// Return JSON if requested
	if strings.Contains(accept, "application/json") {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(data)
		return
	}

	// Return HTML fragment
	views.OpenCodeSessions(data).Render(r.Context(), w)
}

func (s *Server) buildOpenCodeData(ctx context.Context) views.OpenCodeData {
	states := s.ocManager.GetAllSessions(ctx)
	servers := s.ocDiscovery.GetServers()

	var data views.OpenCodeData
	data.Servers = servers

	for _, state := range states {
		ocSession := views.OpenCodeSession{
			State: state,
		}

		// Determine status category
		switch state.Status {
		case "error":
			ocSession.NeedsAttention = true
			data.NeedsAttention = append(data.NeedsAttention, ocSession)
		case "busy":
			ocSession.IsWorking = true
			data.Active = append(data.Active, ocSession)
		default:
			// Check if there are active todos that might need attention
			if state.ActiveTodos > 0 {
				data.Active = append(data.Active, ocSession)
			} else {
				data.Idle = append(data.Idle, ocSession)
			}
		}
	}

	return data
}

func (s *Server) streamOpenCodeSessions(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "SSE not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	// Send initial comment
	fmt.Fprintf(w, ": connected\n\n")
	flusher.Flush()

	// Send initial data
	if err := s.sendOpenCodeSessionsEvent(r.Context(), w, flusher); err != nil {
		slog.Debug("SSE OpenCode sessions write failed", "error", err)
		return
	}

	// Poll and send updates
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			slog.Debug("SSE OpenCode sessions client disconnected")
			return
		case <-ticker.C:
			if err := s.sendOpenCodeSessionsEvent(r.Context(), w, flusher); err != nil {
				slog.Debug("SSE OpenCode sessions write failed", "error", err)
				return
			}
		}
	}
}

func (s *Server) sendOpenCodeSessionsEvent(ctx context.Context, w http.ResponseWriter, flusher http.Flusher) error {
	var buf strings.Builder
	data := s.buildOpenCodeData(ctx)
	views.OpenCodeSessions(data).Render(ctx, &buf)

	// Build SSE message
	var msg strings.Builder
	for _, line := range strings.Split(buf.String(), "\n") {
		msg.WriteString("data: ")
		msg.WriteString(line)
		msg.WriteString("\n")
	}
	msg.WriteString("\n")

	_, err := w.Write([]byte(msg.String()))
	if err != nil {
		return err
	}
	flusher.Flush()
	return nil
}

func (s *Server) handleOpenCodeSession(w http.ResponseWriter, r *http.Request) {
	if s.ocManager == nil {
		http.Error(w, "OpenCode integration not enabled", http.StatusNotImplemented)
		return
	}

	// Parse path: /opencode/session/{serverURL}/{sessionID}/action
	path := strings.TrimPrefix(r.URL.Path, "/opencode/session/")
	parts := strings.SplitN(path, "/", 3)

	if len(parts) < 2 {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}

	serverURL, err := url.PathUnescape(parts[0])
	if err != nil {
		http.Error(w, "invalid server URL", http.StatusBadRequest)
		return
	}
	sessionID := parts[1]

	// Handle actions
	if len(parts) == 3 {
		action := parts[2]
		switch action {
		case "send":
			s.handleOpenCodeSend(w, r, serverURL, sessionID)
		case "abort":
			s.handleOpenCodeAbort(w, r, serverURL, sessionID)
		default:
			http.Error(w, "unknown action", http.StatusBadRequest)
		}
		return
	}

	// Get session details
	state, err := s.ocManager.GetSessionDetails(r.Context(), serverURL, sessionID)
	if err != nil {
		slog.Error("failed to get OpenCode session", "error", err)
		http.Error(w, "failed to get session: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Return JSON
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(state)
}

func (s *Server) handleOpenCodeSend(w http.ResponseWriter, r *http.Request, serverURL, sessionID string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	r.ParseForm()
	text := r.FormValue("input")

	if text == "" {
		http.Error(w, "input required", http.StatusBadRequest)
		return
	}

	slog.Info("send to OpenCode", "server", serverURL, "session", sessionID, "text", text)

	if err := s.ocManager.SendPrompt(r.Context(), serverURL, sessionID, text); err != nil {
		slog.Error("failed to send to OpenCode", "error", err)
		http.Error(w, "failed to send: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleOpenCodeAbort(w http.ResponseWriter, r *http.Request, serverURL, sessionID string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	slog.Info("abort OpenCode session", "server", serverURL, "session", sessionID)

	if err := s.ocManager.AbortSession(r.Context(), serverURL, sessionID); err != nil {
		slog.Error("failed to abort OpenCode session", "error", err)
		http.Error(w, "failed to abort: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}
