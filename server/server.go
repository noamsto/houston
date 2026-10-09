// server/server.go
package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/noamsto/houston/agents"
	"github.com/noamsto/houston/agents/amp"
	"github.com/noamsto/houston/agents/claude"
	"github.com/noamsto/houston/agents/generic"
	"github.com/noamsto/houston/hub"
	"github.com/noamsto/houston/mode"
	"github.com/noamsto/houston/opencode"
	"github.com/noamsto/houston/parser"
	"github.com/noamsto/houston/runs"
	"github.com/noamsto/houston/tmux"
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

type Server struct {
	mode       mode.Mode
	tmux       *tmux.Client
	controlMgr *tmux.ControlManager
	registry   *agents.Registry
	font       FontController
	uiFS       fs.FS // embedded React SPA

	// OpenCode integration
	ocDiscovery *opencode.Discovery
	ocManager   *opencode.Manager
	// ocScanDone/ocRefreshDone close once the OpenCode background scan/
	// refresh goroutines have exited. Nil when OpenCode isn't enabled.
	ocScanDone    <-chan struct{}
	ocRefreshDone <-chan struct{}

	// Agent-card hub (new) — aggregates hook state + transcript tails.
	hub *hub.Hub

	// runs composes the hook, tmux and crew sources into one Run per key.
	runs     *runs.Registry
	runPanes runPaneOps
	// answerPoll and answerWait override how often and how long an answer
	// waits for the pane to reach its next step, when non-zero. answerLocks
	// holds one *sync.Mutex per pane target, so two answers never interleave
	// keys in one pane.
	answerPoll  time.Duration
	answerWait  time.Duration
	answerLocks sync.Map

	// chat serves a run's chat transcript; the hub in production. chatPing
	// and chatCheck override the stream's keep-alive and session-check
	// intervals when non-zero.
	chat      chatSource
	chatPing  time.Duration
	chatCheck time.Duration

	// replyRunner delivers a crew answer. It exists so a test can observe that
	// no command ran, which no assertion about the response alone can prove.
	replyRunner replyRunner

	// wsRepos classifies a @git_root as a main checkout or a linked
	// worktree for the Workspace tab's bucketing.
	wsRepos mainCheckoutChecker
	// wsTmux lists the raw window/pane options the Workspace tab joins
	// against a run snapshot. Narrow enough to fake in tests.
	wsTmux workspaceLister

	// dispatchRunner launches a worker via the dispatch CLI. A field, like
	// replyRunner, so a test can observe the resolved argv/env/dir without
	// running a real command.
	dispatchRunner dispatchRunner
	// dispatchRepos computes the known-repo set fresh per request. A field so
	// tests can fake it without a real tmux server or git checkout.
	dispatchRepos func() ([]dispatchRepo, error)
	// repoReg is the persisted half of the known-repo set; dispatchEngines
	// lists the engines the host's dispatch can launch.
	repoReg         *repoRegistry
	dispatchEngines func(ctx context.Context, serverPath string) ([]string, error)
	// dispatchModels reads dispatch's tier map; see dispatchModelSet.
	dispatchModels func(ctx context.Context, serverPath string) (dispatchModelSet, error)
	// dispatchSlot caps in-flight dispatches at one: dispatch mutates the
	// repo (worktrees, branches, the crew bus, GitHub issues), and running
	// two at once against one repo is not something it is designed for.
	dispatchSlot    chan struct{}
	dispatchTimeout time.Duration
	// dispatchNewCrewID mints a crew id in crew's <unix>-<pid> format for a
	// "new" crew request. A field so a test can force a same-second collision.
	dispatchNewCrewID func() string

	// tmuxRun and launchCommonDir are the dispatcher launch's outside
	// world, fields so tests run it without tmux or git.
	tmuxRun         tmuxRunner
	launchCommonDir func(root string) (string, error)
	// houstonExe is what tmux runs as the wrapper; launchDir holds the
	// launch files it reads.
	houstonExe string
	launchDir  string
	// launchSlot caps in-flight dispatcher launches at one, separately from
	// dispatchSlot so a 120 s worker dispatch doesn't block a launch.
	launchSlot  chan struct{}
	launchCheck time.Duration
	launchPoll  time.Duration

	auth  *authGate
	hosts *hostGate

	// Background goroutines started by New run on ctx; Close cancels it and
	// waits for them.
	cancel    context.CancelFunc
	sourcesWG sync.WaitGroup // hub + run sources: the deltas producers
	pumpDone  chan struct{}
	deltas    chan runs.Delta
	closeOnce sync.Once
	closeErr  error
}

// closeTimeout bounds how long Close waits for background goroutines that
// are mid-exec (tmux, git) and only notice cancellation when the call returns.
const closeTimeout = 10 * time.Second

// workspaceLister is the tmux surface handleWorkspace needs — narrow enough
// to fake in tests without shelling out to a real tmux server.
type workspaceLister interface {
	ListWindowOptions() ([]tmux.WindowOptions, error)
	ListPaneOptions() ([]tmux.PaneOptions, error)
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

	// UIFS is the embedded React SPA filesystem.
	UIFS fs.FS

	// AuthEnabled gates /api/ behind the state-dir token. Disabled only by
	// an explicit -no-auth.
	AuthEnabled bool
	// AllowedOrigins are extra origins permitted beyond same-origin, e.g. the
	// Vite dev server.
	AllowedOrigins []string
	// AllowedHosts are extra Host values this server answers to, beyond what
	// it can derive about itself (loopback, hostname, Tailscale addresses).
	// For reverse proxies or custom DNS.
	AllowedHosts []string

	// RepoRoots confine which directories the repo registry accepts.
	RepoRoots []string

	// Mode is Dispatcher or Tmux, resolved by the caller; New rejects
	// anything else.
	Mode mode.Mode
}

func New(cfg Config) (*Server, error) {
	if cfg.Mode != mode.Dispatcher && cfg.Mode != mode.Tmux {
		return nil, fmt.Errorf("server: mode must be resolved, got %q", cfg.Mode)
	}

	registry := agents.NewRegistry(
		claude.New(),
		amp.New(),
		generic.New(), // Must be last (fallback)
	)

	ctx, cancel := context.WithCancel(context.Background())
	tmuxClient := tmux.NewClient()
	repoReg := newRepoRegistry(filepath.Join(cfg.StatusDir, "repos.json"), resolveRepoRoots(cfg.RepoRoots), gitCommonDir)
	launchDir := filepath.Join(cfg.StatusDir, dispatcherLaunchDirName)
	sweepLaunchDir(launchDir)
	houstonExe, err := os.Executable()
	if err != nil {
		slog.Warn("dispatcher launch unavailable: cannot resolve houston's executable", "error", err)
	}
	s := &Server{
		mode:           cfg.Mode,
		cancel:         cancel,
		pumpDone:       make(chan struct{}),
		tmux:           tmuxClient,
		controlMgr:     tmux.NewControlManager(),
		registry:       registry,
		font:           cfg.FontController,
		uiFS:           cfg.UIFS,
		hub:            hub.New(cfg.StatusDir, slog.Default()),
		replyRunner:    execCrewReply,
		wsRepos:        newRepoClassifier(),
		wsTmux:         tmuxClient,
		runPanes:       tmuxClient,
		dispatchRunner: execDispatch,
		repoReg:        repoReg,
		dispatchRepos: func() ([]dispatchRepo, error) {
			return listDispatchRepos(tmuxClient, gitCommonDir, repoReg.ValidPaths())
		},
		dispatchEngines: execDispatchEngines,
		dispatchModels:  execDispatchModels,
		dispatchSlot:    make(chan struct{}, 1),
		dispatchTimeout: dispatchTimeout,
		dispatchNewCrewID: func() string {
			return strconv.FormatInt(time.Now().Unix(), 10) + "-" + strconv.Itoa(os.Getpid())
		},
		tmuxRun:         execTmux,
		launchCommonDir: gitCommonDir,
		houstonExe:      houstonExe,
		launchDir:       launchDir,
		launchSlot:      make(chan struct{}, 1),
		launchCheck:     3 * time.Second,
		launchPoll:      200 * time.Millisecond,
	}
	s.chat = s.hub

	// Run the hub in the background. It watches <status-dir>/claude/ and the
	// transcripts referenced from hook state files.
	s.sourcesWG.Add(1)
	go func() {
		defer s.sourcesWG.Done()
		if err := s.hub.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			slog.Warn("agent hub stopped", "err", err)
		}
	}()

	reg := runs.NewRegistry(runs.Order(cfg.Mode))
	s.runs = reg

	deltas := make(chan runs.Delta, 256)
	s.deltas = deltas
	go func() {
		defer close(s.pumpDone)
		for d := range deltas {
			reg.Apply(d)
		}
	}()

	for _, src := range runSources(cfg.Mode, s.hub, tmuxClient, s.controlMgr) {
		s.sourcesWG.Add(1)
		go func(src runs.Source) {
			defer s.sourcesWG.Done()
			if err := src.Run(ctx, deltas); err != nil && !errors.Is(err, context.Canceled) {
				slog.Warn("run source stopped", "source", src.Name(), "error", err)
			}
		}(src)
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
		s.ocScanDone = s.ocDiscovery.StartBackgroundScan(ctx, 30*time.Second)
		s.ocRefreshDone = s.ocManager.StartBackgroundRefresh(ctx, 10*time.Second)
	}

	gate := &authGate{enabled: cfg.AuthEnabled, allowedOrigins: cfg.AllowedOrigins}
	if cfg.AuthEnabled {
		tok, err := LoadOrCreateToken(cfg.StatusDir)
		if err != nil {
			_ = s.Close()
			return nil, fmt.Errorf("api token: %w", err)
		}
		gate.token = tok
	}
	s.auth = gate
	s.hosts = deriveHosts(cfg.AllowedHosts, cfg.AllowedOrigins)

	return s, nil
}

// runSources lists the run sources for m in the registry's layer order; tmux
// mode has no crew bus.
func runSources(m mode.Mode, h *hub.Hub, c *tmux.Client, cm *tmux.ControlManager) []runs.Source {
	srcs := []runs.Source{
		runs.NewHookSource(h, c),
		runs.NewTmuxSource(c, 2*time.Second, m),
	}
	if m != mode.Tmux {
		srcs = append(srcs, runs.NewCrewSource(c, h, 3*time.Second))
	}
	return append(srcs, runs.NewConnectionSource(cm, c, 2*time.Second))
}

// Close stops every background goroutine New started and waits for them, so
// nothing is still writing under StatusDir once it returns. Safe to call more
// than once.
func (s *Server) Close() error {
	s.closeOnce.Do(func() {
		s.cancel()

		stopped := make(chan struct{})
		go func() {
			s.sourcesWG.Wait()
			// Every producer has returned, so nothing can send on deltas.
			close(s.deltas)
			<-s.pumpDone

			if s.ocScanDone != nil {
				<-s.ocScanDone
			}
			if s.ocRefreshDone != nil {
				<-s.ocRefreshDone
			}

			// Goroutines have stopped; now close what they depended on.
			s.controlMgr.Close()
			if s.ocManager != nil {
				s.ocManager.Close()
			}

			close(stopped)
		}()

		select {
		case <-stopped:
		case <-time.After(closeTimeout):
			s.closeErr = errors.New("server: background goroutines did not stop in time")
		}
	})
	return s.closeErr
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	if s.uiFS != nil {
		mux.Handle("/", SPAHandler(s.uiFS, s.auth))
	}

	// JSON API routes (always available)
	apiMux := http.NewServeMux()
	apiMux.HandleFunc("/api/opencode/sessions", s.handleAPIOpenCodeSessions)
	apiMux.HandleFunc("/api/opencode/session/", s.handleAPIOpenCodeSession)
	apiMux.HandleFunc("/api/runs", s.handleRunsSnapshot)
	apiMux.HandleFunc("/api/runs/stream", s.handleRunsStream)
	apiMux.HandleFunc("GET /api/runs/{id}/terminal", s.handleRunTerminal)
	apiMux.HandleFunc("POST /api/runs/{id}/input", s.handleRunInput)
	apiMux.HandleFunc("POST /api/runs/{id}/answer", s.handleRunAnswer)
	apiMux.HandleFunc("GET /api/runs/{id}/prompt", s.handleRunPrompt)
	apiMux.HandleFunc("GET /api/runs/{id}/chat", s.handleRunChat)
	apiMux.HandleFunc("GET /api/runs/{id}/chat/stream", s.handleRunChatStream)
	apiMux.HandleFunc("GET /api/runs/{id}/chat/tool/{callId}", s.handleRunChatTool)
	apiMux.HandleFunc("GET /api/workspace", s.handleWorkspace)
	apiMux.HandleFunc("GET /api/mode", s.handleMode)
	// Tmux mode leaves the command-executing routes (crew reply, dispatch,
	// launch, repo registry) unregistered, so they 404.
	if s.mode != mode.Tmux {
		apiMux.HandleFunc("POST /api/runs/{id}/reply", s.handleRunReply)
		apiMux.HandleFunc("POST /api/dispatch", s.handleDispatch)
		apiMux.HandleFunc("GET /api/dispatch/options", s.handleDispatchOptions)
		apiMux.HandleFunc("POST /api/dispatch/dispatcher", s.handleDispatcherLaunch)
		apiMux.HandleFunc("GET /api/repos", s.handleReposList)
		apiMux.HandleFunc("POST /api/repos", s.handleReposAdd)
		apiMux.HandleFunc("DELETE /api/repos", s.handleReposRemove)
		apiMux.HandleFunc("GET /api/repos/candidates", s.handleRepoCandidates)
	}
	mux.Handle("/api/", s.auth.middleware(apiMux))

	// The host gate wraps everything, including "/", so a rebound domain is
	// never handed the token via SPAHandler's setCookie.
	return s.hosts.middleware(mux)
}

// SPAHandler serves the embedded SPA, falling back to index.html for
// client-side routing. Every response issues the auth cookie, which is how the
// browser comes to hold a token it can send on same-origin fetch, EventSource
// and WebSocket calls. It must therefore stay inside the host gate: served to
// an unrecognised Host, it would hand the token to a rebound attacker.
func SPAHandler(uiFS fs.FS, auth *authGate) http.Handler {
	var fileServer http.Handler
	if uiFS != nil {
		fileServer = http.FileServer(http.FS(uiFS))
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth.setCookie(w)
		if fileServer == nil {
			w.WriteHeader(http.StatusOK)
			return
		}

		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" {
			path = "index.html"
		}
		if _, err := fs.Stat(uiFS, path); err == nil {
			fileServer.ServeHTTP(w, r)
			return
		}
		r.URL.Path = "/"
		fileServer.ServeHTTP(w, r)
	})
}

type imageUpload struct {
	Name string `json:"name"`
	Type string `json:"type"`
	Data string `json:"data"` // base64 encoded
}

// safeImageName reduces an upload's name to at most 64 bytes of
// [A-Za-z0-9._-] that never starts with "-" or ".". The temp path is typed
// into an agent's prompt, so it must carry no control characters or spaces.
func safeImageName(name string) string {
	var b strings.Builder
	for _, r := range filepath.Base(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	s := b.String()

	stem, ext := s, filepath.Ext(s)
	if len(ext) >= 2 && len(ext) <= 9 && len(ext) < len(s) {
		stem = s[:len(s)-len(ext)]
	} else {
		ext = ""
	}
	if limit := 64 - len(ext); len(stem) > limit {
		stem = stem[:limit]
	}

	rest := strings.TrimLeft(stem, "-.")
	return strings.Repeat("_", len(stem)-len(rest)) + rest + ext
}

// saveImages writes each upload to /tmp and schedules its removal after an
// hour. On failure it removes whatever it already wrote and returns the HTTP
// status and message to report.
func saveImages(images []imageUpload) (paths []string, status int, err error) {
	var tmpFiles []string
	var cleanupOnError []string

	for i, img := range images {
		// Decode base64 image
		imageData, err := base64.StdEncoding.DecodeString(img.Data)
		if err != nil {
			slog.Error("failed to decode base64 image", "error", err, "index", i)
			// Clean up any files created so far on error
			for _, f := range cleanupOnError {
				_ = os.Remove(f)
			}
			return nil, http.StatusBadRequest, fmt.Errorf("invalid image data at index %d", i)
		}

		// Write image to temp file with sanitized filename
		tmpFile, err := os.CreateTemp("/tmp", "houston-*-"+safeImageName(img.Name))
		if err != nil {
			slog.Error("failed to create temp file", "error", err, "index", i)
			// Clean up any files created so far on error
			for _, f := range cleanupOnError {
				_ = os.Remove(f)
			}
			return nil, http.StatusInternalServerError, errors.New("failed to save image")
		}

		if _, err := tmpFile.Write(imageData); err != nil {
			slog.Error("failed to write image", "error", err, "index", i)
			_ = tmpFile.Close()
			_ = os.Remove(tmpFile.Name())
			// Clean up any files created so far on error
			for _, f := range cleanupOnError {
				_ = os.Remove(f)
			}
			return nil, http.StatusInternalServerError, errors.New("failed to save image")
		}
		_ = tmpFile.Close()

		tmpFiles = append(tmpFiles, tmpFile.Name())
		cleanupOnError = append(cleanupOnError, tmpFile.Name())
	}

	// Clean up temp files after 1 hour (gives user time to reference them)
	for _, f := range tmpFiles {
		path := f
		time.AfterFunc(1*time.Hour, func() {
			_ = os.Remove(path)
		})
	}
	return tmpFiles, 0, nil
}

// OpenCode handlers

func (s *Server) buildOpenCodeData(ctx context.Context) OpenCodeData {
	states := s.ocManager.GetAllSessions(ctx)
	servers := s.ocDiscovery.GetServers()

	// Initialize slices to empty (not nil) so JSON serializes as [] not null.
	data := OpenCodeData{
		NeedsAttention: []OpenCodeSession{},
		Active:         []OpenCodeSession{},
		Idle:           []OpenCodeSession{},
		Servers:        servers,
	}

	for _, state := range states {
		ocSession := OpenCodeSession{
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

func (s *Server) handleOpenCodeSession(w http.ResponseWriter, r *http.Request) {
	if s.ocManager == nil {
		http.Error(w, "OpenCode integration not enabled", http.StatusNotImplemented)
		return
	}

	// Parse path: /opencode/session/{serverURL}/{sessionID}/action
	path := strings.TrimPrefix(r.URL.EscapedPath(), "/opencode/session/")
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
	sessionID, err := url.PathUnescape(parts[1])
	if err != nil {
		http.Error(w, "invalid session ID", http.StatusBadRequest)
		return
	}
	if !opencode.ValidSessionID(sessionID) {
		http.Error(w, "invalid session ID", http.StatusBadRequest)
		return
	}

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
		if errors.Is(err, opencode.ErrUnknownServer) {
			http.Error(w, "unknown OpenCode server", http.StatusNotFound)
			return
		}
		slog.Error("failed to get OpenCode session", "error", err)
		http.Error(w, "failed to get session: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Return JSON
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(state)
}

func (s *Server) handleOpenCodeSend(w http.ResponseWriter, r *http.Request, serverURL, sessionID string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	_ = r.ParseForm()
	text := r.FormValue("input")

	if text == "" {
		http.Error(w, "input required", http.StatusBadRequest)
		return
	}

	slog.Info("send to OpenCode", "server", serverURL, "session", sessionID, "text", text)

	if err := s.ocManager.SendPrompt(r.Context(), serverURL, sessionID, text); err != nil {
		if errors.Is(err, opencode.ErrUnknownServer) {
			http.Error(w, "unknown OpenCode server", http.StatusNotFound)
			return
		}
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
		if errors.Is(err, opencode.ErrUnknownServer) {
			http.Error(w, "unknown OpenCode server", http.StatusNotFound)
			return
		}
		slog.Error("failed to abort OpenCode session", "error", err)
		http.Error(w, "failed to abort: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}
