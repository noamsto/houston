package hub

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/noamsto/houston/claudemod"
	"github.com/noamsto/houston/hook"
)

// TrailChip is a single tool-call breadcrumb rendered on a card.
type TrailChip struct {
	Tool    string `json:"tool"`
	Hint    string `json:"hint"`
	Done    bool   `json:"done"`
	IsError bool   `json:"error,omitempty"`
}

// SessionView is the DTO the server emits over SSE for one agent card.
type SessionView struct {
	SessionID      string      `json:"session_id"`
	CWD            string      `json:"cwd,omitempty"`
	GitBranch      string      `json:"git_branch,omitempty"`
	TmuxSession    string      `json:"tmux_session,omitempty"`
	TmuxWindow     string      `json:"tmux_window,omitempty"`
	TmuxPane       string      `json:"tmux_pane,omitempty"`
	TmuxServer     string      `json:"tmux_server,omitempty"`
	State          hook.State  `json:"state"`
	Tool           string      `json:"tool,omitempty"`
	ToolInputHint  string      `json:"tool_input_hint,omitempty"`
	LastMessage    string      `json:"last_message,omitempty"`
	Turn           int         `json:"turn"`
	Since          int64       `json:"since,omitempty"`
	UpdatedAt      int64       `json:"updated_at"`
	SeenAt         int64       `json:"seen_at,omitempty"` // newest sign of life (hook, transcript, mod heartbeat); not in viewSignature, so a heartbeat does not broadcast
	Trail          []TrailChip `json:"trail,omitempty"`
	Asks           string      `json:"asks,omitempty"` // the question the turn ended on; reset by later tool/user activity
	InputTokens    int         `json:"input_tokens"`
	OutputTokens   int         `json:"output_tokens"`
	ContextUsed    int         `json:"context_used,omitempty"`  // latest assistant message: uncached + cached input
	ContextLimit   int         `json:"context_limit,omitempty"` // 0 when the model's window is unknown
	SpendUSD       *float64    `json:"spend_usd,omitempty"`     // pi only: sum of recorded per-message cost
	TranscriptPath string      `json:"transcript_path,omitempty"`
	Agent          string      `json:"agent"`

	Background []BackgroundTask `json:"background,omitempty"`
}

// DefaultPruneTTL is how long an ended hook state file is kept after its
// last update before pruneEnded removes it. Deliberately >= DefaultDiscoveryWindow
// (both 24h) so a freshly-pruned session's transcript can't still be inside
// the discovery window and get re-seeded — keep them equal, or keep this the
// larger of the two if you ever change one.
const DefaultPruneTTL = 24 * time.Hour

// pruneInterval is how often Run re-runs pruneEnded, independent of
// DefaultPruneTTL — a long-lived server should periodically reclaim
// regardless of the configured TTL.
const pruneInterval = time.Hour

// Hub aggregates hook state files + transcript tails and exposes updates.
type Hub struct {
	stateDir          string
	claudeProjectsDir string
	discoveryWindow   time.Duration
	pruneTTL          time.Duration
	log               *slog.Logger

	preRemoveStat func(string) (os.FileInfo, error) // test seam for pruneEnded's race guard

	mod *claudemod.Store // nil when the Claude Code mod is not listened for

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
	// PruneTTL is how long an ended hook state file is kept after its last
	// update before it is removed. Defaults to DefaultPruneTTL (24h) when
	// <=0. A state file whose last-written State is not StateEnded is removed
	// only through PruneDead, for a session the hooks layer ended on pane
	// evidence.
	PruneTTL time.Duration
	// Mod is the Claude Code mod's record store, overlaid on hook state while
	// the mod is live for a session. Nil leaves the hook path alone.
	Mod *claudemod.Store
}

// Session is the hub's per-session bookkeeping. One goroutine owns it via the
// hub's mutex; external readers take RLock.
type Session struct {
	view      SessionView
	hookState hook.SessionState // last merged state file; zero for a discovery-seeded session

	transcriptPath   string
	transcriptOffset int64
	// newest transcript event timestamp (unix seconds); file mtime is not used
	// because Claude Code appends timestamp-less bookkeeping records to an idle
	// session's transcript.
	transcriptAt int64

	trail  []TrailChip
	asks   string
	spend  *float64 // running sum of recorded per-message cost
	maxCtx int      // highest context seen; a window below it cannot be the real one

	lastTurn         int    // last turn whose trail was cleared (UserPromptSubmit bumps Turn)
	lastBroadcastSig string // last broadcast view signature; skip duplicates

	bg bgTracker

	openTools map[string]string      // tool_use id -> tool name, no tool_result yet
	tools     map[string]toolOutcome // latest call per tool name since the last reset

	chat *chatState // nil until first use, and for agents without a chat.Reader

	mod         claudemod.Session // the mod's fold; meaningful only while hasMod
	hasMod      bool
	modOnly     bool // created from mod records, not a state file or discovery
	lastModTurn int  // last mod turn count whose trail was cleared, for a session with no hook state
}

// toolOutcome is how a tool's latest call ended, and when the transcript said so.
type toolOutcome struct {
	resolved bool
	rejected bool // the user refused the call; an ordinary tool failure is not this
	at       time.Time
}

// isRejection reports whether an error tool_result is Claude Code's answer to a
// permission dialog the user declined. A "No" with feedback is excluded: the
// agent keeps working on it with no hook event.
func isRejection(text string) bool {
	return strings.HasPrefix(text, "The user doesn't want to") && !strings.Contains(text, "the user said:")
}

// resetTools forgets the tool bookkeeping the dialog heal reads.
func (s *Session) resetTools() {
	s.openTools, s.tools = nil, nil
}

// endTurn drops the tool_uses a new prompt leaves behind (an interrupt can
// strand one with no result) but keeps resolved outcomes, which a hook file
// that missed its later events may still be pointing at.
func (s *Session) endTurn() {
	s.openTools = nil
	for name, out := range s.tools {
		if !out.resolved {
			delete(s.tools, name)
		}
	}
}

func (s *Session) setOutcome(name string, out toolOutcome) {
	if s.tools == nil {
		s.tools = map[string]toolOutcome{}
	}
	s.tools[name] = out
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
	ttl := opts.PruneTTL
	if ttl <= 0 {
		ttl = DefaultPruneTTL
	}
	return &Hub{
		stateDir:          stateDir,
		claudeProjectsDir: projects,
		discoveryWindow:   window,
		pruneTTL:          ttl,
		log:               log,
		preRemoveStat:     os.Stat,
		mod:               opts.Mod,
		sessions:          map[string]*Session{},
		subs:              map[chan SessionView]struct{}{},
	}
}

// Run blocks until ctx is cancelled, watching the state dir and fanning out
// updates to subscribers. Pre-existing state files are loaded on startup.
func (h *Hub) Run(ctx context.Context) error {
	claudeDir := filepath.Join(h.stateDir, "claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil { //nolint:gosec // state dir under the user's home; 0755 matches the sibling state files
		return err
	}
	h.pruneEnded(claudeDir)

	w, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer func() { _ = w.Close() }()
	h.watcher = w

	if err := w.Add(claudeDir); err != nil {
		return err
	}

	if err := h.scan(claudeDir); err != nil {
		h.log.Warn("initial scan failed", "err", err)
	}
	h.runDiscovery(ctx)

	// Poll transcripts every 2s (fsnotify only watches state files) and
	// re-discover every 30s to pick up sessions started without hooks.
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	discover := time.NewTicker(30 * time.Second)
	defer discover.Stop()
	pruneTick := time.NewTicker(pruneInterval)
	defer pruneTick.Stop()

	var modCh <-chan struct{} // nil without a store: never fires
	if h.mod != nil {
		modCh = h.mod.Notify()
	}

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
		case <-modCh:
			h.applyModDirty()
		case <-tick.C:
			h.refreshAllTranscripts()
			if h.mod != nil {
				// Eviction and monitor expiry need no record traffic.
				h.mod.Sweep()
				h.tickMod(time.Now())
			}
		case <-discover.C:
			h.runDiscovery(ctx)
		case <-pruneTick.C:
			h.pruneEnded(claudeDir)
		}
	}
}

// runDiscovery scans the Claude projects directory and seeds any sessions we
// don't already know about. Silent no-op when discovery is disabled.
func (h *Hub) runDiscovery(ctx context.Context) {
	if h.claudeProjectsDir == "" {
		return
	}
	found, err := discoverClaudeSessions(ctx, h.claudeProjectsDir, h.discoveryWindow)
	if err != nil {
		h.log.Debug("discovery failed", "err", err)
		return
	}
	for _, s := range found {
		if ctx.Err() != nil {
			return
		}
		h.seed(s)
	}
}

// seed registers a discovered session only if we don't already know it —
// hook-fired state always wins over inferred.
func (h *Hub) seed(s hook.SessionState) {
	h.mu.Lock()
	if _, exists := h.sessions[s.SessionID]; exists {
		h.mu.Unlock()
		return
	}
	sess := &Session{transcriptPath: s.TranscriptPath}
	mergeStateIntoView(&sess.view, s)
	sess.view.SeenAt = sess.view.UpdatedAt
	h.sessions[s.SessionID] = sess
	view := sess.view
	h.mu.Unlock()

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

// pruneEnded removes hook state files older than h.pruneTTL whose
// last-written State is StateEnded. This is the only signal pruneEnded
// trusts: hook/hook.go sets StateEnded solely on a genuine SessionEnd hook
// event, so a live session's file, or a "ghost" session whose pane died
// without ever firing SessionEnd, is never a candidate here regardless of
// age; a non-ended file is removed only through PruneDead, for a session the
// hooks layer ended on pane evidence. A zero UpdatedAt (unwritten or
// corrupted) is never a prune candidate, since it can't be distinguished from
// a file that's merely stale. The race
// guard compares the file's mtime read right after hook.Read against its
// mtime read right before os.Remove; any change in between (e.g. a resumed
// session's SessionStart re-touching an old session id) means the file
// isn't ours to remove this pass.
func (h *Hub) pruneEnded(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-h.pruneTTL)
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		path := filepath.Join(dir, e.Name())
		s, err := hook.Read(path)
		if err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				h.log.Warn("prune: read state file", "path", path, "err", err)
			}
			continue
		}

		// Captured right after the read, so the guard below brackets the
		// full window during which a rewrite could race us.
		before, err := os.Stat(path)
		if err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				h.log.Warn("prune: stat state file", "path", path, "err", err)
			}
			continue
		}

		if s.SessionID == "" {
			continue
		}
		if s.State != hook.StateEnded {
			continue
		}
		if s.UpdatedAt == 0 {
			continue
		}
		if !time.Unix(s.UpdatedAt, 0).Before(cutoff) {
			continue
		}

		// A resumed session could rewrite the file between our read above
		// and the remove below. hook.Write always writes a temp file and
		// renames it over the target, replacing the inode. Check both
		// SameFile (detects rename-based rewrites regardless of mtime
		// granularity) and mtime equality (belt for an in-place writer).
		// If either changed, the file isn't ours to remove this pass.
		after, err := h.preRemoveStat(path)
		if err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				h.log.Warn("prune: stat state file", "path", path, "err", err)
			}
			continue
		}
		if after == nil || !os.SameFile(before, after) || !after.ModTime().Equal(before.ModTime()) {
			continue
		}

		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			h.log.Warn("prune: remove state file", "path", path, "err", err)
		}
	}
}

// PruneDead removes a session the caller has found dead on pane evidence: its
// pane is gone or foreign and it did not revive, or it owns no pane and has
// been silent. The hub decides freshness itself and removes it only after
// pruneTTL without a sign of life (SeenAt), re-checking the file on disk with
// pruneEnded's race guard. A discovery-seeded session (no state file) is
// dropped from memory; discovery dates a synthesized session from its newest
// event timestamp (mtime is only a pre-filter that over-includes), which SeenAt
// includes, and pruneTTL >= DefaultDiscoveryWindow, so it is not re-seeded
// while that event is older than the discovery window.
func (h *Hub) PruneDead(sessionID string) bool {
	h.mu.RLock()
	sess, ok := h.sessions[sessionID]
	var seenAt int64
	var hasState, hasMod bool
	if ok {
		seenAt = sess.view.SeenAt
		hasState = sess.hookState.SessionID != ""
		hasMod = sess.hasMod
	}
	h.mu.RUnlock()
	if !ok {
		return false
	}

	cutoff := time.Now().Add(-h.pruneTTL).Unix()
	if seenAt == 0 || seenAt >= cutoff {
		return false
	}

	if !hasState {
		if hasMod {
			return false
		}
		h.mu.Lock()
		defer h.mu.Unlock()
		cur, ok := h.sessions[sessionID]
		if !ok || cur.hookState.SessionID != "" || cur.hasMod || cur.view.SeenAt == 0 || cur.view.SeenAt >= cutoff {
			return false
		}
		if cur.chat != nil {
			cur.chat.closeSubs()
		}
		delete(h.sessions, sessionID)
		return true
	}

	path := hook.Path(h.stateDir, sessionID)
	s, err := hook.Read(path)
	if err != nil {
		h.log.Debug("prune dead: read state file", "session", sessionID, "err", err)
		return false
	}
	before, err := os.Stat(path)
	if err != nil {
		h.log.Debug("prune dead: stat state file", "session", sessionID, "err", err)
		return false
	}
	if s.SessionID != sessionID || s.UpdatedAt == 0 || s.UpdatedAt >= cutoff {
		return false
	}
	after, err := h.preRemoveStat(path)
	if err != nil {
		h.log.Debug("prune dead: stat state file", "session", sessionID, "err", err)
		return false
	}
	if after == nil || !os.SameFile(before, after) || !after.ModTime().Equal(before.ModTime()) {
		return false
	}
	if err := os.Remove(path); err != nil {
		h.log.Debug("prune dead: remove state file", "session", sessionID, "err", err)
		return false
	}
	return true
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
		if sess, ok := h.sessions[sid]; ok && sess.chat != nil {
			sess.chat.closeSubs()
		}
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
	sess.hookState = s

	// Clear the trail only when a new turn starts (Turn increments on
	// UserPromptSubmit). Since is bumped by every event, including PostToolUse,
	// so keying on it wiped the trail on every tool completion (#100).
	if s.Turn > sess.lastTurn {
		sess.lastTurn = s.Turn
		sess.trail = []TrailChip{}
		// An interrupt can leave a tool_use with no result, and a new prompt
		// ends the previous turn's tools. Keyed on the hook Turn, not on
		// transcript user text: meta and injected user records appear mid-turn.
		sess.resetTools()
	}
	sess.setTranscriptPath(sess.effectiveTranscript())
	sess.refreshView(time.Now())
	view := sess.view
	h.mu.Unlock()

	if sess.transcriptPath != "" {
		h.refreshTranscript(s.SessionID)
	}

	h.broadcastIfChanged(sess, view)
}

// setTranscriptPath points the tail at p and reports whether it moved.
func (s *Session) setTranscriptPath(p string) bool {
	if s.transcriptPath == p {
		return false
	}
	// The tail's offset and running totals belong to the old file.
	s.transcriptOffset = 0
	s.transcriptAt = 0
	s.spend, s.maxCtx = nil, 0
	s.view.ContextUsed, s.view.ContextLimit, s.view.SpendUSD = 0, 0, nil
	s.resetTools()
	s.transcriptPath = p
	if s.chat != nil {
		s.chat.setPath(p)
	}
	return true
}

// effectiveTranscript is the transcript the session tails: the hook state's,
// else the one the mod learnt.
func (s *Session) effectiveTranscript() string {
	if s.hookState.TranscriptPath != "" || !s.hasMod {
		return s.hookState.TranscriptPath
	}
	return s.mod.Transcript
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
	// Ahead of the trail read's early return: the trail reader can be caught
	// up while the chat reader is not.
	h.refreshChat(sessionID, false)

	h.mu.Lock()
	sess, ok := h.sessions[sessionID]
	if !ok || sess.transcriptPath == "" {
		h.mu.Unlock()
		return
	}
	path := sess.transcriptPath
	offset := sess.transcriptOffset
	h.mu.Unlock()

	var (
		events    []TranscriptEvent
		newOffset int64
		err       error
		initial   *prefixState
	)
	if offset == 0 {
		var prefix prefixState
		prefix, events, newOffset, err = readInitial(path)
		initial = &prefix
	} else {
		events, newOffset, err = ReadTranscriptFrom(path, offset)
	}
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			h.log.Debug("read transcript", "path", path, "err", err)
		}
		return
	}
	if len(events) == 0 && newOffset == offset {
		h.expireBackground(sess)
		return
	}

	h.mu.Lock()
	if initial != nil {
		// Read outside the lock: another read may have landed meanwhile.
		if sess.transcriptOffset != 0 || sess.transcriptPath != path {
			h.mu.Unlock()
			return
		}
		sess.bg = initial.bg
		sess.spend = initial.spend
		sess.view.SpendUSD = initial.spend
		sess.maxCtx = initial.maxCtx
	}
	sess.transcriptOffset = newOffset
	for _, ev := range events {
		// A prompt ends the previous turn's tools. Live, the hook Turn says so;
		// a first read has no such signal and folds every turn at once.
		if initial != nil && ev.Prompt {
			sess.endTurn()
		}
		applyTranscriptEvent(sess, ev)
	}
	sess.syncTranscriptView(time.Now())
	view := sess.view
	h.mu.Unlock()

	h.broadcastIfChanged(sess, view)
}

// syncTranscriptView copies the transcript-derived state into the view.
func (s *Session) syncTranscriptView(now time.Time) {
	s.view.Trail = append([]TrailChip(nil), s.trail...)
	s.refreshView(now)
}

// refreshView recomputes the view's state, background and asks. Every view
// recomputation goes through it, so the mod and transcript sources switch in
// one place and are never both counted.
func (s *Session) refreshView(now time.Time) {
	s.applyHook(now)
	s.view.SeenAt = max(s.view.UpdatedAt, s.transcriptAt)
	if s.hasMod {
		s.view.SeenAt = max(s.view.SeenAt, s.mod.Updated/1000)
	}
	s.view.Background = s.background(now)
	s.view.Asks = s.asksNow(now)
}

// applyHook merges the hook state into the view and heals a dialog the
// transcript proves is over. A dialog the user answers "No" or dismisses fires
// no hook event, but the tool it guarded gets an error tool_result. The result
// is recomputed from the raw hook state each time, so a later unresolved
// tool_use restores the blocked state.
//
// A permission state heals once its tool is resolved and nothing is open. A
// tool-running state (the dialog's Notification has not fired, or the hook
// events after the call were lost) heals only on a result from a later second
// than the last hook event (Since has second granularity): any result for
// AskUserQuestion, or a rejection for another tool. A plain failed call leaves
// tool-running, since no PostToolUseFailure hook is handled. Without the
// timestamp check a call that just started would read as the finished earlier
// call of the same tool.
//
// While the mod is live it replaces the state and the heal is off: the mod's
// turn.complete already covers dismissals and interrupts.
func (s *Session) applyHook(now time.Time) {
	switch {
	case s.hookState.SessionID != "":
		mergeStateIntoView(&s.view, s.hookState)
	case s.hasMod:
		s.mergeModCoords()
	default:
		return
	}
	if s.modLive(now) {
		s.overlayMod()
		return
	}
	if s.hookState.SessionID == "" {
		return
	}
	st := s.hookState
	if st.Tool == "" {
		return
	}
	out, used := s.tools[st.Tool]
	if !used || len(s.openTools) > 0 {
		return
	}
	switch st.State {
	case hook.StatePermission:
	case hook.StateToolRunning:
		if !out.resolved || out.at.IsZero() || out.at.Before(time.Unix(st.Since+1, 0)) {
			return
		}
		if st.Tool != "AskUserQuestion" && !out.rejected {
			return
		}
	default:
		return
	}
	s.view.State = hook.StateWaiting
	s.view.Tool = ""
	s.view.ToolInputHint = ""
	s.view.LastMessage = ""
}

// expireBackground drops monitors whose timeout has passed. Nothing is written
// to the transcript when one does, so no new event will ever say so.
func (h *Hub) expireBackground(sess *Session) {
	h.mu.Lock()
	now := time.Now()
	if backgroundSignature(sess.background(now)) == backgroundSignature(sess.view.Background) {
		h.mu.Unlock()
		return
	}
	sess.refreshView(now)
	view := sess.view
	h.mu.Unlock()
	h.broadcastIfChanged(sess, view)
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

// broadcastIfChanged skips fan-out when nothing material changed since the
// last broadcast for this session — avoids O(subscribers) work on every
// fsnotify write or transcript poll that yields no delta.
func (h *Hub) broadcastIfChanged(sess *Session, v SessionView) {
	sig := viewSignature(v)
	h.mu.Lock()
	if sess.lastBroadcastSig == sig {
		h.mu.Unlock()
		return
	}
	sess.lastBroadcastSig = sig
	h.mu.Unlock()
	h.broadcast(v)
}

func flagDigit(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

func viewSignature(v SessionView) string {
	var b strings.Builder
	b.Grow(128 + 24*len(v.Trail))
	b.WriteString(string(v.State))
	b.WriteByte('|')
	b.WriteString(v.Tool)
	b.WriteByte('|')
	b.WriteString(v.ToolInputHint)
	b.WriteByte('|')
	b.WriteString(v.LastMessage)
	b.WriteByte('|')
	b.WriteString(strconv.Itoa(v.Turn))
	b.WriteByte('|')
	for _, c := range v.Trail {
		b.WriteString(c.Tool + "\x00" + c.Hint + "\x00")
		b.WriteString(flagDigit(c.Done))
		b.WriteString(flagDigit(c.IsError))
		b.WriteByte(1)
	}
	b.WriteByte('|')
	b.WriteString(strconv.Itoa(v.InputTokens))
	b.WriteByte('|')
	b.WriteString(strconv.Itoa(v.OutputTokens))
	b.WriteByte('|')
	b.WriteString(strconv.Itoa(v.ContextUsed))
	b.WriteByte('|')
	b.WriteString(strconv.Itoa(v.ContextLimit))
	b.WriteByte('|')
	if v.SpendUSD != nil {
		b.WriteString(strconv.FormatFloat(*v.SpendUSD, 'g', -1, 64))
	}
	b.WriteByte('|')
	b.WriteString(v.Agent)
	b.WriteByte('|')
	b.WriteString(v.Asks)
	b.WriteByte('|')
	b.WriteString(backgroundSignature(v.Background))
	return b.String()
}

// mergeStateIntoView copies hook-owned fields into the view without clobbering
// transcript-owned ones (trail, token counts).
func mergeStateIntoView(v *SessionView, s hook.SessionState) {
	v.SessionID = s.SessionID
	v.CWD = s.CWD
	v.GitBranch = s.GitBranch
	v.TmuxSession = s.TmuxSession
	v.TmuxWindow = s.TmuxWindow
	v.TmuxPane = s.TmuxPane
	v.TmuxServer = s.TmuxServer
	v.State = s.State
	v.Tool = s.Tool
	v.ToolInputHint = s.ToolInputHint
	v.LastMessage = ""
	if s.State == hook.StateWaiting || s.State == hook.StatePermission {
		v.LastMessage = s.LastMessage
	}
	v.Turn = s.Turn
	v.Since = s.Since
	v.UpdatedAt = s.UpdatedAt
	v.TranscriptPath = s.TranscriptPath
	// Legacy files, native Claude hooks, and transcript discovery all carry
	// no engine — default them to claude rather than leaving the view blank.
	v.Agent = s.Agent
	if v.Agent == "" {
		v.Agent = hook.AgentClaude
	}
}

// maxTrail is how many trail chips a session keeps.
const maxTrail = 8

// eventTypePiToolCall is pi's tool-call content block, which parseLine passes
// through under its own type name.
const eventTypePiToolCall = "toolCall"

// applyTranscriptEvent updates trail/telemetry on sess from one event.
// Called under h.mu.
func applyTranscriptEvent(s *Session, ev TranscriptEvent) {
	if !ev.Timestamp.IsZero() {
		s.transcriptAt = max(s.transcriptAt, ev.Timestamp.Unix())
	}
	s.bg.apply(ev)

	switch ev.Type {
	case EventTypeToolUse:
		s.asks = ""
		if ev.ToolUseID != "" {
			if s.openTools == nil {
				s.openTools = map[string]string{}
			}
			s.openTools[ev.ToolUseID] = ev.ToolName
			s.setOutcome(ev.ToolName, toolOutcome{})
		}
		if n := len(s.trail); n > 0 && !s.trail[n-1].Done {
			s.trail[n-1].Done = true
		}
		s.trail = append(s.trail, TrailChip{Tool: ev.ToolName, Hint: ev.Text})
		if len(s.trail) > maxTrail {
			s.trail = s.trail[len(s.trail)-maxTrail:]
		}
	case EventTypeToolResult:
		s.asks = ""
		if name, ok := s.openTools[ev.ToolUseID]; ok {
			delete(s.openTools, ev.ToolUseID)
			s.setOutcome(name, toolOutcome{resolved: true, rejected: ev.IsError && isRejection(ev.Text), at: ev.Timestamp})
		}
		for i := len(s.trail) - 1; i >= 0; i-- {
			if !s.trail[i].Done {
				s.trail[i].Done = true
				if ev.IsError {
					s.trail[i].IsError = true
				}
				break
			}
		}
	case EventTypeText:
		if ev.Role == "assistant" {
			s.asks = questionTail(ev.Text)
		} else {
			s.asks = ""
		}
	case eventTypePiToolCall:
		s.asks = ""
	}

	// Roll up token usage (last observed wins; Claude reports running totals).
	// A last-wins field added to this roll-up must join readInitial's
	// tailSuffices, and a sum or maximum its pre-pass (foldPrefix), or a first
	// read from the tail can miss it. openTools and tools are tail-sensitive
	// too: a first read from the tail never sees a tool_use older than it. Missing
	// an unresolved older tool errs toward healing a dialog; missing an older
	// same-name tool_use errs toward blocked.
	if ev.InputTokens > 0 {
		s.view.InputTokens = ev.InputTokens
	}
	if ev.OutputTokens > 0 {
		s.view.OutputTokens = ev.OutputTokens
	}
	// Context is the latest assistant message's, not a maximum: it shrinks
	// after a compaction.
	if ev.ContextTokens > 0 {
		s.maxCtx = max(s.maxCtx, ev.ContextTokens)
		s.view.ContextUsed = ev.ContextTokens
		s.view.ContextLimit = contextLimit(ev.Model, s.maxCtx)
	}
	if ev.CostUSD != nil {
		total := *ev.CostUSD
		if s.spend != nil {
			total += *s.spend
		}
		s.spend = &total
		s.view.SpendUSD = &total
	}
}

func sessionIDFromPath(path string) string {
	name := filepath.Base(path)
	return strings.TrimSuffix(name, filepath.Ext(name))
}
