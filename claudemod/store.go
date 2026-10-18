package claudemod

import (
	"cmp"
	"log/slog"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/noamsto/houston/hook"
)

// windowCap bounds the per-session window of records kept unfolded so a late
// record can still land where its seq puts it.
const windowCap = 64

// A session is evicted this long after its last applied record. The sweep
// inside Apply runs at most once per sweepEvery.
const (
	endedTTL    = 24 * time.Hour
	idleTTL     = 24 * time.Hour
	sweepEvery  = time.Minute
	maxSessions = 1000
)

// bootDelaySlack is how long after a boot's first sight an unseen boot with an
// older ts is still taken for a delayed record of an older boot. Deliveries
// take milliseconds; later than this it is a reload whose clock stepped back.
const bootDelaySlack = 5 * time.Second

// retiredCap bounds the boots a session remembers as retired; a late record of
// an older one falls to the bootDelaySlack rule.
const retiredCap = 16

// Session is the folded view of one Claude Code session.
type Session struct {
	SID         string     `json:"sid"`
	State       hook.State `json:"state"`
	Tool        string     `json:"tool,omitempty"`
	Hint        string     `json:"hint,omitempty"`
	Message     string     `json:"message,omitempty"`
	Since       int64      `json:"since"`        // unix s of the last state change
	Updated     int64      `json:"updated"`      // ms, newest applied record's ts
	MainUpdated int64      `json:"main_updated"` // ms, newest applied main-loop record's ts
	Known       bool       `json:"known"`        // a state-defining main-loop record folded since houston started (a reload keeps it)
	Turns       int        `json:"turns"`
	Last        string     `json:"last,omitempty"`
	StopSeen    bool       `json:"stop_seen"` // a main-loop stop since the last turn.start
	Background  []BGTask   `json:"background,omitempty"`
	BGKnown     bool       `json:"bg_known"`
	CWD         string     `json:"cwd,omitempty"`
	Transcript  string     `json:"transcript,omitempty"`
	Pane        string     `json:"pane,omitempty"`
	TmuxServer  string     `json:"tmux_server,omitempty"`

	Boot      string   `json:"boot"`
	LastSeq   int64    `json:"last_seq"` // highest seq seen this boot
	InFlight  []string `json:"in_flight,omitempty"`
	Applied   int      `json:"applied"`
	Stale     int      `json:"stale"`
	Reordered int      `json:"reordered"`
}

// BackgroundAt returns the background tasks still outstanding at now: a
// monitor past its timeout is gone.
func (s Session) BackgroundAt(now time.Time) []BGTask {
	var out []BGTask
	for _, t := range s.Background {
		if t.Expires != 0 && t.Expires <= now.Unix() {
			continue
		}
		out = append(out, t)
	}
	return out
}

// Store folds records into per-session state. It is safe for concurrent use.
type Store struct {
	mu        sync.Mutex
	sessions  map[string]*session
	dirty     map[string]struct{}
	notify    chan struct{}
	now       func() time.Time
	lastSweep time.Time
}

func NewStore() *Store {
	return &Store{
		sessions: map[string]*session{},
		dirty:    map[string]struct{}{},
		notify:   make(chan struct{}, 1),
		now:      time.Now,
	}
}

type session struct {
	boot     string
	bootTS   int64     // ts of the first record seen of the current boot
	bootSeen time.Time // houston clock when that record arrived
	retired  []string  // boots folded into base, oldest first; their late records are stale
	base     fold
	window   []Record // current boot, ascending seq, at most windowCap
	folded   int64    // highest seq folded into base
	maxSeq   int64
	cur      fold // base with the window applied

	applied, stale, reordered int
	updated, mainUpdated      int64
	lastApply                 time.Time
}

// Apply inserts rec into its session's seq-ordered window and refolds. It
// reports false (and counts Stale) for a duplicate seq, one older than the
// window, or a record of a boot the session already retired. A record
// arriving below the highest seq seen is applied in place and counted in
// Reordered. A record of an unseen boot whose ts predates the current boot's
// first record, arriving within bootDelaySlack of that first record, is a
// delayed one from an older boot: it retires that boot instead of replacing
// the live one. An applied record marks its session dirty and signals Notify.
func (s *Store) Apply(rec Record) bool {
	applied, changed := s.apply(rec)
	if changed {
		s.signal()
	}
	return applied
}

func (s *Store) apply(rec Record) (applied, changed bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()
	changed = s.sweep(now, false)
	ss := s.sessions[rec.SID]
	if ss == nil {
		changed = s.makeRoom() || changed
		ss = &session{boot: rec.Boot, bootTS: rec.TS, bootSeen: now}
		s.sessions[rec.SID] = ss
	}
	if slices.Contains(ss.retired, rec.Boot) {
		ss.stale++
		return false, changed
	}
	if rec.Boot != ss.boot {
		if rec.TS < ss.bootTS && now.Sub(ss.bootSeen) < bootDelaySlack {
			ss.retire(rec.Boot)
			ss.stale++
			return false, changed
		}
		ss.newBoot(rec.Boot, rec.TS, now)
	}
	i, dup := slices.BinarySearchFunc(ss.window, rec.Seq, func(r Record, seq int64) int {
		return cmp.Compare(r.Seq, seq)
	})
	if dup || rec.Seq <= ss.folded {
		ss.stale++
		return false, changed
	}
	if rec.Seq < ss.maxSeq {
		ss.reordered++
	}
	ss.maxSeq = max(ss.maxSeq, rec.Seq)
	if rec.Ev != evNotification {
		rec.Message = ""
	}
	rec.Ask = nil // fold never reads it
	ss.window = slices.Insert(ss.window, i, rec)
	if len(ss.window) > windowCap {
		ss.base.apply(ss.window[0])
		ss.folded = ss.window[0].Seq
		ss.window = ss.window[1:]
	}
	ss.applied++
	ss.lastApply = now
	ss.updated = max(ss.updated, rec.TS)
	if rec.Agent == "" {
		ss.mainUpdated = max(ss.mainUpdated, rec.TS)
	}
	ss.cur = ss.base.clone()
	for _, r := range ss.window {
		ss.cur.apply(r)
	}
	if rec.Ev == evSessionEnd && ss.cur.phase == hook.StateEnded {
		// Folding the window makes a late record at or below the highest folded
		// seq stale; a higher seq still applies.
		ss.foldWindow()
		ss.cur = ss.base.clone()
	}
	s.dirty[rec.SID] = struct{}{}
	slog.Debug("claude-mod record", "sid", rec.SID, "boot", rec.Boot, "seq", rec.Seq,
		"ev", rec.Ev, "agent", rec.Agent, "tool", rec.Tool, "id", rec.ID,
		"state", ss.cur.viewState(), "known", ss.cur.known)
	return true, true
}

// signal wakes a Notify reader without blocking: one pending signal covers
// every sid marked dirty before the reader takes the set.
func (s *Store) signal() {
	select {
	case s.notify <- struct{}{}:
	default:
	}
}

// Notify fires after Apply or Sweep marks a session dirty; read the sids with
// TakeDirty.
func (s *Store) Notify() <-chan struct{} { return s.notify }

// TakeDirty returns the sids changed or evicted since the last call, sorted,
// and clears the set.
func (s *Store) TakeDirty() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]string, 0, len(s.dirty))
	for sid := range s.dirty {
		out = append(out, sid)
	}
	clear(s.dirty)
	slices.Sort(out)
	return out
}

func (ss *session) foldWindow() {
	for _, r := range ss.window {
		ss.base.apply(r)
		ss.folded = r.Seq
	}
	ss.window = nil
}

// newBoot keeps the folded state: a hot reload continues the session.
func (ss *session) newBoot(boot string, ts int64, now time.Time) {
	ss.foldWindow()
	ss.retire(ss.boot)
	ss.boot = boot
	ss.bootTS = ts
	ss.bootSeen = now
	ss.folded = 0
	ss.maxSeq = 0
}

func (ss *session) retire(boot string) {
	if slices.Contains(ss.retired, boot) {
		return
	}
	ss.retired = append(ss.retired, boot)
	if len(ss.retired) > retiredCap {
		ss.retired = slices.Delete(ss.retired, 0, len(ss.retired)-retiredCap)
	}
}

// Sweep evicts expired sessions now, marking them dirty.
func (s *Store) Sweep() {
	s.mu.Lock()
	changed := s.sweep(s.now(), true)
	s.mu.Unlock()
	if changed {
		s.signal()
	}
}

func (s *Store) sweep(now time.Time, force bool) bool {
	if !force && now.Sub(s.lastSweep) < sweepEvery {
		return false
	}
	s.lastSweep = now
	evicted := false
	for sid, ss := range s.sessions {
		idle := now.Sub(ss.lastApply)
		if idle > idleTTL || (idle > endedTTL && ss.cur.phase == hook.StateEnded) {
			s.evict(sid)
			evicted = true
		}
	}
	return evicted
}

// makeRoom evicts the least recently applied session when a new one would
// pass maxSessions.
func (s *Store) makeRoom() bool {
	if len(s.sessions) < maxSessions {
		return false
	}
	var oldest string
	var at time.Time
	for sid, ss := range s.sessions {
		if oldest == "" || ss.lastApply.Before(at) {
			oldest, at = sid, ss.lastApply
		}
	}
	s.evict(oldest)
	return true
}

func (s *Store) evict(sid string) {
	delete(s.sessions, sid)
	s.dirty[sid] = struct{}{}
}

// Session returns the view of one session.
func (s *Store) Session(sid string) (Session, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	ss := s.sessions[sid]
	if ss == nil {
		return Session{}, false
	}
	return ss.view(sid), true
}

// Snapshot returns a copy of every session, sorted by SID.
func (s *Store) Snapshot() []Session {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]Session, 0, len(s.sessions))
	for sid, ss := range s.sessions {
		out = append(out, ss.view(sid))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SID < out[j].SID })
	return out
}

func (ss *session) view(sid string) Session {
	f := &ss.cur
	v := Session{
		SID: sid, State: f.viewState(), Since: f.since,
		Updated: ss.updated, MainUpdated: ss.mainUpdated,
		Known: f.known, Turns: f.turns, Last: f.last, StopSeen: f.stopSeen, BGKnown: f.bgKnown,
		CWD: f.cwd, Transcript: f.transcript, Pane: f.pane, TmuxServer: f.tmuxServer,
		Boot: ss.boot, LastSeq: ss.maxSeq, Applied: ss.applied, Stale: ss.stale, Reordered: ss.reordered,
	}
	var newest *flight
	for i, fl := range f.inflight {
		if fl.agent != "" {
			continue
		}
		v.InFlight = append(v.InFlight, fl.id)
		newest = &f.inflight[i]
	}
	slices.Sort(v.InFlight)
	switch {
	case f.perm != nil:
		v.Tool, v.Hint, v.Message = f.perm.tool, f.perm.hint, f.perm.message
		if v.Message == "" {
			v.Message = "Claude needs your permission"
			if f.perm.tool != "" {
				v.Message += " to use " + f.perm.tool
			}
		}
	case f.phase == hook.StateToolRunning && newest != nil:
		v.Tool, v.Hint = newest.tool, newest.hint
	}
	for _, e := range f.bg {
		v.Background = append(v.Background, e.task)
	}
	slices.SortFunc(v.Background, func(a, b BGTask) int {
		return cmp.Or(cmp.Compare(a.Since, b.Since), cmp.Compare(a.ID, b.ID))
	})
	return v
}
