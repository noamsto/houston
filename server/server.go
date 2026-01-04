// server/server.go
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
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

		// Get worktrees once per session (using first window's pane path)
		var worktrees map[string]string
		var worktreesLoaded bool

		for _, win := range windows {
			// Get actual panes for this window
			panes, _ := s.tmux.ListPanes(sess.Name, win.Index)

			// Find pane to check (prefer active, fallback to first)
			var activePaneInfo *tmux.PaneInfo
			paneIdx := 0
			if len(panes) > 0 {
				activePaneInfo = &panes[0]
				paneIdx = panes[0].Index
				for i, p := range panes {
					if p.Active {
						activePaneInfo = &panes[i]
						paneIdx = p.Index
						break
					}
				}
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
			// Use window name for process (includes nerd font icons from tmux plugin)
			process := win.Name

			pane := tmux.Pane{Session: sess.Name, Window: win.Index, Index: paneIdx}
			output, _ := s.tmux.CapturePane(pane, 100)

			// Use MessageParser for detection
			msgParser := parser.NewClaudeCodeParser()
			msgParser.ProcessBuffer(output)
			state := msgParser.GetState()
			parseResult := state.ToLegacyResult()

			// Only mark as needing attention if it's a Claude Code window
			isClaudeWindow := tmux.LooksLikeClaudeOutput(output)
			windowNeedsAttention := isClaudeWindow && (parseResult.Type == parser.TypeError ||
				parseResult.Type == parser.TypeChoice ||
				parseResult.Type == parser.TypeQuestion)

			// Extract preview lines - more for attention states
			previewLines := 15
			if windowNeedsAttention {
				previewLines = 25 // Show more context for choices/questions/errors
			}
			preview := getPreviewLines(output, previewLines)

			windowStatus := views.WindowWithStatus{
				Window:         win,
				Pane:           pane,
				ParseResult:    parseResult,
				Preview:        preview,
				NeedsAttention: windowNeedsAttention,
				Branch:         branch,
				Process:        process,
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
			if isWindowActive(cmd, win.LastActivity, isClaudeWindow, parseResult) {
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

		// Categorize session based on its windows' actual status
		if sessionData.AttentionCount > 0 {
			data.NeedsAttention = append(data.NeedsAttention, sessionData)
		} else if sessionData.HasWorking {
			data.Active = append(data.Active, sessionData)
		} else {
			data.Idle = append(data.Idle, sessionData)
		}
	}

	return data
}

// getPreviewLines extracts the last n non-empty lines from output, skipping Claude's status bar
func getPreviewLines(output string, n int) []string {
	lines := strings.Split(output, "\n")
	var result []string

	// Work backwards to find non-empty lines, skipping status bar elements
	for i := len(lines) - 1; i >= 0 && len(result) < n; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		// Skip Claude's status bar lines
		if isStatusBarLine(line) {
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
		result = append([]string{line}, result...)
	}

	// Truncate long lines
	for i, line := range result {
		if len(line) > 60 {
			result[i] = line[:57] + "..."
		}
	}

	return result
}

// isStatusBarLine checks if a line is part of Claude's status bar
func isStatusBarLine(line string) bool {
	// Claude status bar contains these indicators
	statusIndicators := []string{
		"-- INSERT --", "-- NORMAL --", // vim mode
		"🤖", "📊", "⏱️", "💬",           // Claude stats
		"❄", "📂",                         // env/path indicators
		"accept edits",                    // edit acceptance hint
	}
	for _, indicator := range statusIndicators {
		if strings.Contains(line, indicator) {
			return true
		}
	}
	return false
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
// - For Claude windows, use the parser
func isWindowActive(cmd string, lastActivity time.Time, isClaudeWindow bool, parseResult parser.Result) bool {
	// Claude windows use their own detection
	if isClaudeWindow {
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

// detectClaudeMode finds the current Claude Code mode from output
// Looks for patterns like: "⏵⏵ accept edits on (shift+tab...)" or "⏸ plan mode on"
func detectClaudeMode(output string) ClaudeMode {
	lines := strings.Split(output, "\n")
	start := len(lines) - 15 // Check more lines (status bar can vary)
	if start < 0 {
		start = 0
	}

	// Known mode icons (ordered by priority - check more specific first)
	modeIcons := []string{"⏵⏵", "⏸"}

	for i := len(lines) - 1; i >= start; i-- {
		line := lines[i]
		for _, icon := range modeIcons {
			if idx := strings.Index(line, icon); idx >= 0 {
				rest := strings.TrimSpace(line[idx+len(icon):])
				// Look for "label on" or "label off" pattern
				// The pattern may have more text after (e.g., "(shift+tab to cycle)")
				if strings.Contains(rest, " on") {
					// Extract label: everything before " on"
					onIdx := strings.Index(rest, " on")
					label := strings.TrimSpace(rest[:onIdx])
					return ClaudeMode{Icon: icon, Label: label, State: "on"}
				}
				if strings.Contains(rest, " off") {
					// Extract label: everything before " off"
					offIdx := strings.Index(rest, " off")
					label := strings.TrimSpace(rest[:offIdx])
					return ClaudeMode{Icon: icon, Label: label, State: "off"}
				}
			}
		}
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
	path = strings.TrimSuffix(path, "/send")
	path = strings.TrimSuffix(path, "/kill")
	path = strings.TrimSuffix(path, "/respawn")
	path = strings.TrimSuffix(path, "/kill-window")

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

	// Use MessageParser for detection
	msgParser := parser.NewClaudeCodeParser()
	msgParser.ProcessBuffer(capture.Output)
	state := msgParser.GetState()
	parseResult := state.ToLegacyResult()

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

				// Parse output for choices using MessageParser
				msgParser := parser.NewClaudeCodeParser()
				msgParser.ProcessBuffer(capture.Output)
				state := msgParser.GetState()
				parseResult := state.ToLegacyResult()
				slog.Debug("SSE pane update", "pane", pane.Target(), "bytes", len(capture.Output), "mode", capture.Mode, "choices", len(parseResult.Choices))

				// Detect Claude mode indicator from output
				claudeMode := detectClaudeMode(capture.Output)
				claudeModeJSON, _ := json.Marshal(claudeMode)

				// Build the SSE message with metadata as first lines
				var buf strings.Builder
				// Send mode as special first line (will be parsed by client)
				slog.Debug("SSE mode", "pane", pane.Target(), "mode", capture.Mode)
				buf.WriteString("data: __MODE__:")
				buf.WriteString(capture.Mode)
				buf.WriteString("\n")
				// Send choices as special second line
				buf.WriteString("data: __CHOICES__:")
				buf.WriteString(strings.Join(parseResult.Choices, "|"))
				buf.WriteString("\n")
				// Send Claude mode state as JSON
				buf.WriteString("data: __CLAUDEMODE__:")
				buf.Write(claudeModeJSON)
				buf.WriteString("\n")
				// Send status line with ANSI colors
				if capture.StatusLine != "" {
					slog.Debug("SSE status line", "pane", pane.Target(), "status", capture.StatusLine, "len", len(capture.StatusLine))
				}
				buf.WriteString("data: __STATUSLINE__:")
				buf.WriteString(capture.StatusLine)
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
