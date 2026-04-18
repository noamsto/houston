package hub

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/noamsto/houston/hook"
)

// TrailChip is a single tool-call breadcrumb rendered above a card preview.
type TrailChip struct {
	Tool    string `json:"tool"`
	Hint    string `json:"hint"`
	Done    bool   `json:"done"`
	IsError bool   `json:"error,omitempty"`
}

// SessionView is the DTO the server emits over SSE for one agent card.
type SessionView struct {
	SessionID      string          `json:"session_id"`
	CWD            string          `json:"cwd,omitempty"`
	TmuxSession    string          `json:"tmux_session,omitempty"`
	TmuxWindow     string          `json:"tmux_window,omitempty"`
	State          hook.State      `json:"state"`
	Tool           string          `json:"tool,omitempty"`
	ToolInputHint  string          `json:"tool_input_hint,omitempty"`
	LastMessage    string          `json:"last_message,omitempty"`
	Turn           int             `json:"turn"`
	Since          int64           `json:"since,omitempty"`
	UpdatedAt      int64           `json:"updated_at"`
	Trail          []TrailChip     `json:"trail,omitempty"`
	Preview        string          `json:"preview,omitempty"`
	InputTokens    int             `json:"input_tokens"`
	OutputTokens   int             `json:"output_tokens"`
	TranscriptPath string          `json:"transcript_path,omitempty"`
}

// Hub aggregates hook state files + transcript tails and exposes updates.
type Hub struct {
	stateDir          string
	claudeProjectsDir string
	discoveryWindow   time.Duration
	log               *slog.Logger

	mu       sync.RWMutex
	sessions map[string]*Session
	subs     map[chan SessionView]struct{}

	watcher *fsnotify.Watcher
}

// Options tunes hub behavior. Zero values use sensible defaults.
type Options struct {
	// ClaudeProjectsDir is where Claude Code stores per-project transcripts.
	// Defaults to ~/.claude/projects. Set to "-" to disable discovery.
	ClaudeProjectsDir string
	// DiscoveryWindow caps how far back we consider a transcript "live".
	// Defaults to DefaultDiscoveryWindow (24h).
	DiscoveryWindow time.Duration
}

// Session is the hub's per-session bookkeeping. One goroutine owns it via the
// hub's mutex; external readers take RLock.
type Session struct {
	view SessionView

	// transcript tail bookkeeping
	transcriptPath   string
	transcriptOffset int64

	// bounded trail + preview
	trail   []TrailChip
	preview []string

	lastTurnStart int64 // unix-sec when turn began (UserPromptSubmit)
}

// New creates a hub rooted at stateDir with default options. Call Run to start it.
func New(stateDir string, log *slog.Logger) *Hub {
	return NewWithOptions(stateDir, Options{}, log)
}

// NewWithOptions creates a hub with explicit options.
func NewWithOptions(stateDir string, opts Options, log *slog.Logger) *Hub {
	if log == nil {
		log = slog.Default()
	}
	projects := opts.ClaudeProjectsDir
	switch projects {
	case "":
		projects = DefaultClaudeProjectsDir()
	case "-":
		projects = "" // explicit opt-out
	}
	window := opts.DiscoveryWindow
	if window <= 0 {
		window = DefaultDiscoveryWindow
	}
	return &Hub{
		stateDir:          stateDir,
		claudeProjectsDir: projects,
		discoveryWindow:   window,
		log:               log,
		sessions:          map[string]*Session{},
		subs:              map[chan SessionView]struct{}{},
	}
}

// Run blocks until ctx is cancelled, watching the state dir and fanning out
// updates to subscribers. Pre-existing state files are loaded on startup.
func (h *Hub) Run(ctx context.Context) error {
	claudeDir := filepath.Join(h.stateDir, "claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		return err
	}

	w, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer func() { _ = w.Close() }()
	h.watcher = w

	if err := w.Add(claudeDir); err != nil {
		return err
	}

	// Initial scan of hook state files.
	if err := h.scan(claudeDir); err != nil {
		h.log.Warn("initial scan failed", "err", err)
	}
	// Initial discovery of Claude transcripts for sessions that were running
	// before houston started and haven't yet produced a hook event.
	h.runDiscovery()

	// Fast tick drives transcript refresh for known sessions; slow tick
	// re-runs discovery so new sessions started after startup are picked up
	// even if the user hasn't installed hooks.
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	discover := time.NewTicker(30 * time.Second)
	defer discover.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case evt, ok := <-w.Events:
			if !ok {
				return nil
			}
			h.handleFSEvent(evt)
		case err, ok := <-w.Errors:
			if !ok {
				return nil
			}
			h.log.Warn("watcher error", "err", err)
		case <-tick.C:
			h.refreshAllTranscripts()
		case <-discover.C:
			h.runDiscovery()
		}
	}
}

// runDiscovery scans the Claude projects directory and seeds any sessions we
// don't already know about. Silent no-op when discovery is disabled.
func (h *Hub) runDiscovery() {
	if h.claudeProjectsDir == "" {
		return
	}
	found, err := DiscoverClaudeSessions(h.claudeProjectsDir, h.discoveryWindow)
	if err != nil {
		h.log.Debug("discovery failed", "err", err)
		return
	}
	for _, s := range found {
		h.seed(s)
	}
}

// seed registers a session from a synthesized state, but only if we don't
// already have it (hook-fired state always wins over inferred state).
func (h *Hub) seed(s hook.SessionState) {
	h.mu.Lock()
	if _, exists := h.sessions[s.SessionID]; exists {
		h.mu.Unlock()
		return
	}
	sess := &Session{transcriptPath: s.TranscriptPath}
	mergeStateIntoView(&sess.view, s)
	h.sessions[s.SessionID] = sess
	view := sess.view
	h.mu.Unlock()

	// Pull fresh trail + preview from the transcript.
	if sess.transcriptPath != "" {
		h.refreshTranscript(s.SessionID)
		return
	}
	h.broadcast(view)
}

// Snapshot returns a slice view of every known session, suitable for the
// initial SSE "hello" payload.
func (h *Hub) Snapshot() []SessionView {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]SessionView, 0, len(h.sessions))
	for _, s := range h.sessions {
		out = append(out, s.view)
	}
	return out
}

// Subscribe returns a channel that receives every SessionView update. The
// caller must read quickly (dropped sends are logged but not blocking) and
// call Unsubscribe when done.
func (h *Hub) Subscribe() chan SessionView {
	ch := make(chan SessionView, 64)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch
}

// Unsubscribe removes a previously-subscribed channel and closes it.
func (h *Hub) Unsubscribe(ch chan SessionView) {
	h.mu.Lock()
	if _, ok := h.subs[ch]; ok {
		delete(h.subs, ch)
		close(ch)
	}
	h.mu.Unlock()
}

// ---------- internals ----------

func (h *Hub) scan(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		h.loadStateFile(filepath.Join(dir, e.Name()))
	}
	return nil
}

func (h *Hub) handleFSEvent(evt fsnotify.Event) {
	if !strings.HasSuffix(evt.Name, ".json") {
		return
	}
	switch {
	case evt.Op&(fsnotify.Create|fsnotify.Write) != 0:
		h.loadStateFile(evt.Name)
	case evt.Op&fsnotify.Remove != 0:
		sid := sessionIDFromPath(evt.Name)
		h.mu.Lock()
		delete(h.sessions, sid)
		h.mu.Unlock()
	}
}

func (h *Hub) loadStateFile(path string) {
	s, err := hook.Read(path)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			h.log.Warn("read state file", "path", path, "err", err)
		}
		return
	}
	if s.SessionID == "" {
		return
	}

	h.mu.Lock()
	sess, ok := h.sessions[s.SessionID]
	if !ok {
		sess = &Session{}
		h.sessions[s.SessionID] = sess
	}
	mergeStateIntoView(&sess.view, s)

	// If the hook signals a new turn, reset the trail so it reflects only
	// *this* turn. Old tool_use events remain in the transcript but won't
	// appear in trail chips.
	if s.State == hook.StateThinking && s.Since > sess.lastTurnStart {
		sess.lastTurnStart = s.Since
		sess.trail = sess.trail[:0]
	}
	sess.transcriptPath = s.TranscriptPath
	view := sess.view
	h.mu.Unlock()

	// Ingest any new transcript bytes for this session.
	if sess.transcriptPath != "" {
		h.refreshTranscript(s.SessionID)
	}

	h.broadcast(view)
}

func (h *Hub) refreshAllTranscripts() {
	h.mu.RLock()
	ids := make([]string, 0, len(h.sessions))
	for id := range h.sessions {
		ids = append(ids, id)
	}
	h.mu.RUnlock()
	for _, id := range ids {
		h.refreshTranscript(id)
	}
}

func (h *Hub) refreshTranscript(sessionID string) {
	h.mu.Lock()
	sess, ok := h.sessions[sessionID]
	if !ok || sess.transcriptPath == "" {
		h.mu.Unlock()
		return
	}
	path := sess.transcriptPath
	offset := sess.transcriptOffset
	h.mu.Unlock()

	events, newOffset, err := ReadTranscriptFrom(path, offset)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			h.log.Debug("read transcript", "path", path, "err", err)
		}
		return
	}
	if len(events) == 0 && newOffset == offset {
		return
	}

	h.mu.Lock()
	sess.transcriptOffset = newOffset
	for _, ev := range events {
		applyTranscriptEvent(sess, ev)
	}
	sess.view.Trail = append([]TrailChip(nil), sess.trail...)
	sess.view.Preview = strings.Join(sess.preview, "\n")
	view := sess.view
	h.mu.Unlock()

	h.broadcast(view)
}

func (h *Hub) broadcast(v SessionView) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for ch := range h.subs {
		select {
		case ch <- v:
		default:
			h.log.Debug("subscriber slow, dropping update", "session", v.SessionID)
		}
	}
}

// mergeStateIntoView copies hook-owned fields into the view without clobbering
// transcript-owned ones (trail, preview, token counts).
func mergeStateIntoView(v *SessionView, s hook.SessionState) {
	v.SessionID = s.SessionID
	v.CWD = s.CWD
	v.TmuxSession = s.TmuxSession
	v.TmuxWindow = s.TmuxWindow
	v.State = s.State
	v.Tool = s.Tool
	v.ToolInputHint = s.ToolInputHint
	v.LastMessage = s.LastMessage
	v.Turn = s.Turn
	v.Since = s.Since
	v.UpdatedAt = s.UpdatedAt
	v.TranscriptPath = s.TranscriptPath
}

// applyTranscriptEvent updates trail/preview/telemetry on sess from one event.
// Called under h.mu.
func applyTranscriptEvent(s *Session, ev TranscriptEvent) {
	const maxTrail = 8
	const maxPreview = 40

	switch ev.Type {
	case "tool_use":
		// Mark any existing "current" chip done, append a new one.
		if n := len(s.trail); n > 0 && !s.trail[n-1].Done {
			s.trail[n-1].Done = true
		}
		s.trail = append(s.trail, TrailChip{
			Tool: ev.ToolName,
			Hint: ev.Text,
			Done: false,
		})
		if len(s.trail) > maxTrail {
			s.trail = s.trail[len(s.trail)-maxTrail:]
		}
	case "tool_result":
		// Find the matching tool_use (walking back) and mark done.
		for i := len(s.trail) - 1; i >= 0; i-- {
			if !s.trail[i].Done {
				s.trail[i].Done = true
				if ev.IsError {
					s.trail[i].IsError = true
				}
				break
			}
		}
		if ev.Text != "" {
			s.preview = append(s.preview, "→ "+ev.Text)
		}
	case "text":
		if ev.Role == "assistant" && ev.Text != "" {
			s.preview = append(s.preview, ev.Text)
		}
	case "thinking":
		if ev.Text != "" {
			s.preview = append(s.preview, "◆ "+ev.Text)
		}
	}
	if len(s.preview) > maxPreview {
		s.preview = s.preview[len(s.preview)-maxPreview:]
	}

	// Roll up token usage (last observed wins; Claude reports running totals).
	if ev.InputTokens > 0 {
		s.view.InputTokens = ev.InputTokens
	}
	if ev.OutputTokens > 0 {
		s.view.OutputTokens = ev.OutputTokens
	}
}

func sessionIDFromPath(path string) string {
	name := filepath.Base(path)
	return strings.TrimSuffix(name, filepath.Ext(name))
}
