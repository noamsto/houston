// server/server.go
package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/noamsto/houston/claudelog"
	"github.com/noamsto/houston/internal/ansi"
	"github.com/noamsto/houston/internal/statusbar"
	"github.com/noamsto/houston/parser"
	"github.com/noamsto/houston/status"
	"github.com/noamsto/houston/tmux"
	"github.com/noamsto/houston/views"
)


// parseClaudeState tries to get state from Claude's JSONL logs first,
// falling back to terminal parsing if unavailable.
// panePath is the working directory of the pane (used to find Claude session).
// terminalOutput is the captured terminal output (used for fallback parsing).
func parseClaudeState(panePath, terminalOutput string) parser.Result {
	var result parser.Result

	fmt.Printf("DEBUG: parseClaudeState called for path=%s outputLen=%d\n", panePath, len(terminalOutput))
	slog.Info("parseClaudeState called", "panePath", panePath, "outputLen", len(terminalOutput))

	// Try claudelog first if we have a valid pane path
	if panePath != "" {
		state, err := claudelog.GetStateForPane(panePath)
		if err == nil {
			slog.Info("Claudelog result", "IsWaitingPermission", state.IsWaitingPermission, "PendingTool", state.PendingToolName)
			result = state.ToParserResult()

			// If JSONL indicates waiting for permission, check terminal for choices
			if state.IsWaitingPermission {
				slog.Info("Permission prompt detected in JSONL", "panePath", panePath, "pendingTool", state.PendingToolName)
				terminalResult := parser.Parse(terminalOutput)
				slog.Info("Terminal parse result", "type", terminalResult.Type, "choices", len(terminalResult.Choices))
				// If terminal has choice prompt, use that (more specific)
				if terminalResult.Type == parser.TypeChoice && len(terminalResult.Choices) > 0 {
					slog.Info("Using terminal choices for permission prompt", "choices", terminalResult.Choices)
					result = terminalResult
				} else {
					snippet := terminalOutput
					if len(terminalOutput) > 200 {
						snippet = terminalOutput[len(terminalOutput)-200:]
					}
					slog.Warn("No choices found in terminal despite pending tool_use", "terminalSnippet", snippet)
				}
			}
		} else {
			slog.Debug("Claudelog error, using terminal parser", "error", err)
			// Fall through to terminal parsing on error
			result = parser.Parse(terminalOutput)
		}
	} else {
		// Fallback: parse terminal output
		result = parser.Parse(terminalOutput)
	}

	return result
}

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

// paneScore represents the priority score for a pane
type paneScore struct {
	info  *tmux.PaneInfo
	index int
	score int // Higher score = higher priority
}

// findBestPane selects the best pane to display for a window
// Priority: Claude attention > Claude working > Claude idle > active > first
func findBestPane(client *tmux.Client, session string, windowIdx int, panes []tmux.PaneInfo) paneScore {
	if len(panes) == 0 {
		return paneScore{nil, 0, 0}
	}

	bestPane := paneScore{&panes[0], panes[0].Index, 0}

	for i := range panes {
		p := &panes[i]
		score := 0

		// Capture output to check if it's Claude
		pane := tmux.Pane{Session: session, Window: windowIdx, Index: p.Index}
		output, err := client.CapturePane(pane, 100)
		if err != nil {
			continue
		}

		isClaudeWindow := tmux.LooksLikeClaudeOutput(output)
		if isClaudeWindow {
			parseResult := parseClaudeState(p.Path, output)

			// Claude pane needing attention = highest priority
			if parseResult.Type == parser.TypeError ||
				parseResult.Type == parser.TypeChoice ||
				parseResult.Type == parser.TypeQuestion {
				score = 100 // Highest priority
			} else if parseResult.Type == parser.TypeWorking {
				score = 50 // Working Claude
			} else {
				score = 30 // Idle/done Claude
			}
		} else {
			// Non-Claude pane: prefer active
			if p.Active {
				score = 10
			} else {
				score = 1
			}
		}

		// Update best pane if this one has higher score
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
			// 1. Claude pane needing attention (error/choice/question)
			// 2. Claude pane that's working
			// 3. Claude pane that's idle/done
			// 4. Active pane (non-Claude)
			// 5. First pane
			var activePaneInfo *tmux.PaneInfo
			paneIdx := 0
			if len(panes) > 0 {
				bestPane := findBestPane(s.tmux, sess.Name, win.Index, panes)
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
			// Use window name for process (includes nerd font icons from tmux plugin)
			process := win.Name

			pane := tmux.Pane{Session: sess.Name, Window: win.Index, Index: paneIdx}
			output, _ := s.tmux.CapturePane(pane, 100)

			// Get pane path for claudelog lookup
			var panePath string
			if activePaneInfo != nil {
				panePath = activePaneInfo.Path
			}

			// Try claudelog first (for accurate state), fallback to terminal parsing
			parseResult := parseClaudeState(panePath, output)

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
// Note: Preview lines in window cards are now only used as fallback - action bar uses SSE for live data
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
		if statusbar.IsStatusLine(line) {
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

// detectClaudeMode finds the current Claude Code mode from status line
// Looks for patterns like: "⏵⏵ accept edits on (shift+tab...)" or "⏸ plan mode on"
// Input is the extracted status line (after separator), which may span multiple lines if wrapped
func detectClaudeMode(statusLine string) ClaudeMode {
	// Check for "accept edits on" first (most common)
	if strings.Contains(statusLine, "accept edits on") {
		return ClaudeMode{Icon: "⏵⏵", Label: "accept edits", State: "on"}
	}
	if strings.Contains(statusLine, "accept edits off") {
		return ClaudeMode{Icon: "⏵⏵", Label: "accept edits", State: "off"}
	}

	// Check for "plan mode on"
	if strings.Contains(statusLine, "plan mode on") {
		return ClaudeMode{Icon: "⏸", Label: "plan mode", State: "on"}
	}
	if strings.Contains(statusLine, "plan mode off") {
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

	// Get pane path for claudelog lookup
	var panePath string
	for _, p := range panes {
		if p.Index == pane.Index {
			panePath = p.Path
			break
		}
	}

	// Try claudelog first (for accurate state), fallback to terminal parsing
	parseResult := parseClaudeState(panePath, capture.Output)

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

// getFileExtension extracts the file extension from a filename
func getFileExtension(filename string) string {
	parts := strings.Split(filename, ".")
	if len(parts) > 1 {
		return parts[len(parts)-1]
	}
	return "jpg" // default to jpg if no extension found
}

func (s *Server) handlePaneSendWithImage(w http.ResponseWriter, r *http.Request, pane tmux.Pane) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

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

	// Write image to temp file with original filename preserved
	// Use timestamp prefix to ensure uniqueness
	tmpPath := fmt.Sprintf("/tmp/houston-%d-%s", time.Now().UnixNano(), req.Image.Name)
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

		// Write image to temp file with original filename preserved
		// Use timestamp prefix to ensure uniqueness
		tmpPath := fmt.Sprintf("/tmp/houston-%d-%s", time.Now().UnixNano(), img.Name)
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

	// Send all image paths and text to Claude Code
	// Format: image1\nimage2\nimage3\ntext + Enter
	message := strings.Join(tmpFiles, "\n")
	if req.Text != "" {
		message = fmt.Sprintf("%s\n%s", message, req.Text)
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

	// Get pane path for claudelog lookup (once at start)
	var panePath string
	panes, _ := s.tmux.ListPanes(pane.Session, pane.Window)
	for _, p := range panes {
		if p.Index == pane.Index {
			panePath = p.Path
			break
		}
	}

	// Send initial comment to establish connection
	if _, err := fmt.Fprintf(w, ": connected\n\n"); err != nil {
		slog.Error("SSE initial write failed", "error", err)
		return
	}
	flusher.Flush()

	var lastOutput string
	var lastStatusLine string
	var lastClaudeModeJSON string
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

			// Detect Claude mode indicator from status line
			claudeMode := detectClaudeMode(capture.StatusLine)
			claudeModeJSON, _ := json.Marshal(claudeMode)
			claudeModeJSONStr := string(claudeModeJSON)

			// Send update if output, status line, or mode changed
			statusChanged := capture.StatusLine != lastStatusLine
			modeChanged := claudeModeJSONStr != lastClaudeModeJSON
			if capture.Output != lastOutput || statusChanged || modeChanged {
				lastOutput = capture.Output
				lastStatusLine = capture.StatusLine
				lastClaudeModeJSON = claudeModeJSONStr
				lines := strings.Split(capture.Output, "\n")
				updateCount++


				// Parse output for choices using claudelog (JSONL) with terminal parser fallback
				strippedOutput := ansi.Strip(capture.Output)
				parseResult := parseClaudeState(panePath, strippedOutput)
				slog.Debug("SSE pane update", "pane", pane.Target(), "bytes", len(capture.Output), "mode", capture.Mode, "choices", len(parseResult.Choices), "statusChanged", statusChanged, "modeChanged", modeChanged)
				if len(parseResult.Choices) > 0 {
					slog.Info("SSE choices detected", "pane", pane.Target(), "count", len(parseResult.Choices), "choices", parseResult.Choices)
				}

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
