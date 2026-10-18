package claudemod

import (
	"cmp"
	"log/slog"
	"maps"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/noamsto/houston/hook"
)

// windowCap bounds the per-session window of records kept unfolded so a late
// record can still land where its seq puts it.
const windowCap = 64

// A session is evicted this long after its last record: an ended one soon, any
// other once it has been silent for a day. The sweep runs at most once per
// sweepEvery.
const (
	endedTTL   = 10 * time.Minute
	idleTTL    = 24 * time.Hour
	sweepEvery = time.Minute
)

// Session is the folded view of one Claude Code session.
type Session struct {
	SID        string     `json:"sid"`
	State      hook.State `json:"state"`
	Tool       string     `json:"tool,omitempty"`
	InFlight   []string   `json:"in_flight,omitempty"`
	Background []Task     `json:"background,omitempty"`
	Boot       string     `json:"boot"`
	LastSeq    int64      `json:"last_seq"` // highest seq seen this boot
	Applied    int        `json:"applied"`
	Stale      int        `json:"stale"`
	Reordered  int        `json:"reordered"`
	Updated    int64      `json:"updated"` // ms, newest applied record's ts
}

// Store folds records into per-session state. It is safe for concurrent use.
type Store struct {
	mu        sync.Mutex
	sessions  map[string]*session
	now       func() time.Time
	lastSweep time.Time
}

func NewStore() *Store {
	return &Store{sessions: map[string]*session{}, now: time.Now}
}

type flight struct{ id, tool string }

// fold is the state a record sequence produces. Its slices and map are owned,
// so clone before mutating a copy.
type fold struct {
	state    hook.State
	inflight []flight // start order; the last entry is the newest
	bg       map[string]Task
}

func (f fold) clone() fold {
	return fold{state: f.state, inflight: slices.Clone(f.inflight), bg: maps.Clone(f.bg)}
}

func (f *fold) apply(rec Record) {
	if rec.Agent != "" {
		return // a subagent's events say nothing about the main loop
	}
	switch rec.Ev {
	case evSessionStart:
		f.state = hook.StateStarting
	case evTurnStart:
		f.state = hook.StateThinking
		f.inflight = nil
	case evToolStart:
		f.inflight = append(f.inflight, flight{rec.ID, rec.Tool})
		f.state = hook.StateToolRunning
	case evToolEnd:
		f.endTool(rec)
	case evPermission:
		f.state = hook.StatePermission
	case evNotification:
		switch rec.Type {
		case "permission_prompt", "elicitation_dialog":
			f.state = hook.StatePermission
		case "idle_prompt":
			f.state = hook.StateWaiting
		}
	case evTurnComplete:
		f.state = hook.StateWaiting
		f.inflight = nil
	case evCompact:
		f.state = hook.StateCompacting
	case evSessionEnd:
		f.state = hook.StateEnded
	case evTaskEnd:
		for _, t := range rec.Tasks {
			delete(f.bg, t.ID)
		}
	case evStop:
		// A nil Tasks means the engine sent no background_tasks, so the list
		// stands; an empty list means nothing is in flight.
		if rec.Tasks != nil {
			f.bg = map[string]Task{}
			for _, t := range rec.Tasks {
				if t.Status == "running" || t.Status == "pending" {
					f.bg[t.ID] = t
				}
			}
		}
	}
}

// endTool leaves the state alone for an id that is not in flight: a turn end
// already cleared it, and a late tool.end must not reopen the turn.
func (f *fold) endTool(rec Record) {
	if rec.Task != "" {
		if f.bg == nil {
			f.bg = map[string]Task{}
		}
		f.bg[rec.Task] = Task{ID: rec.Task, Type: rec.Tool, Status: "running"}
	}
	i := slices.IndexFunc(f.inflight, func(fl flight) bool { return fl.id == rec.ID })
	if i < 0 {
		return
	}
	f.inflight = slices.Delete(f.inflight, i, i+1)
	if len(f.inflight) == 0 {
		f.state = hook.StateThinking
		return
	}
	f.state = hook.StateToolRunning
}

type session struct {
	boot    string
	bootTS  int64               // ts of the first record seen of the current boot
	retired map[string]struct{} // boots folded into base; their late records are stale
	base    fold
	window  []Record // current boot, ascending seq, at most windowCap
	folded  int64    // highest seq folded into base
	maxSeq  int64
	cur     fold // base with the window applied

	applied, stale, reordered int
	updated                   int64
	lastApply                 time.Time
}

// Apply inserts rec into its session's seq-ordered window and refolds. It
// reports false (and counts Stale) for a duplicate seq, one older than the
// window, or a record of a boot the session already retired. A record
// arriving below the highest seq seen is applied in place and counted in
// Reordered. A record of an unseen boot whose ts predates the current boot's
// first record is a delayed one from an older boot: it retires that boot
// instead of replacing the live one.
func (s *Store) Apply(rec Record) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()
	s.sweep(now)
	ss := s.sessions[rec.SID]
	if ss == nil {
		ss = &session{boot: rec.Boot, bootTS: rec.TS, retired: map[string]struct{}{}}
		s.sessions[rec.SID] = ss
	}
	ss.lastApply = now
	if _, ok := ss.retired[rec.Boot]; ok {
		ss.stale++
		return false
	}
	if rec.Boot != ss.boot {
		if rec.TS < ss.bootTS {
			ss.retired[rec.Boot] = struct{}{}
			ss.stale++
			return false
		}
		ss.newBoot(rec.Boot, rec.TS)
	}
	i, dup := slices.BinarySearchFunc(ss.window, rec.Seq, func(r Record, seq int64) int {
		return cmp.Compare(r.Seq, seq)
	})
	if dup || rec.Seq <= ss.folded {
		ss.stale++
		return false
	}
	if rec.Seq < ss.maxSeq {
		ss.reordered++
	}
	ss.maxSeq = max(ss.maxSeq, rec.Seq)
	rec.Message, rec.Ask = "", nil // fold never reads them
	ss.window = slices.Insert(ss.window, i, rec)
	if len(ss.window) > windowCap {
		ss.base.apply(ss.window[0])
		ss.folded = ss.window[0].Seq
		ss.window = ss.window[1:]
	}
	ss.applied++
	ss.updated = max(ss.updated, rec.TS)
	ss.cur = ss.base.clone()
	for _, r := range ss.window {
		ss.cur.apply(r)
	}
	if rec.Ev == evSessionEnd && ss.cur.state == hook.StateEnded {
		// Nothing after an end matters: later records fall under the stale rule.
		ss.foldWindow()
		ss.cur = ss.base.clone()
	}
	slog.Debug("claude-mod record", "sid", rec.SID, "boot", rec.Boot, "seq", rec.Seq,
		"ev", rec.Ev, "tool", rec.Tool, "id", rec.ID, "state", ss.cur.state)
	return true
}

func (ss *session) foldWindow() {
	for _, r := range ss.window {
		ss.base.apply(r)
		ss.folded = r.Seq
	}
	ss.window = nil
}

func (ss *session) newBoot(boot string, ts int64) {
	ss.foldWindow()
	ss.retired[ss.boot] = struct{}{}
	ss.boot = boot
	ss.bootTS = ts
	ss.folded = 0
	ss.maxSeq = 0
}

func (s *Store) sweep(now time.Time) {
	if now.Sub(s.lastSweep) < sweepEvery {
		return
	}
	s.lastSweep = now
	for sid, ss := range s.sessions {
		idle := now.Sub(ss.lastApply)
		if idle > idleTTL || (idle > endedTTL && ss.cur.state == hook.StateEnded) {
			delete(s.sessions, sid)
		}
	}
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
	v := Session{
		SID: sid, State: ss.cur.state, Boot: ss.boot, LastSeq: ss.maxSeq,
		Applied: ss.applied, Stale: ss.stale, Reordered: ss.reordered, Updated: ss.updated,
	}
	for _, fl := range ss.cur.inflight {
		v.InFlight = append(v.InFlight, fl.id)
		v.Tool = fl.tool // the last one is the newest
	}
	slices.Sort(v.InFlight)
	for _, t := range ss.cur.bg {
		v.Background = append(v.Background, t)
	}
	sort.Slice(v.Background, func(i, j int) bool { return v.Background[i].ID < v.Background[j].ID })
	return v
}
