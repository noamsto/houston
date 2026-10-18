package hub

import (
	"time"

	"github.com/noamsto/houston/claudemod"
	"github.com/noamsto/houston/hook"
)

// modLagGrace is how far, in seconds, the hook state may run ahead of the
// newest mod record before the mod counts as stopped. Every hook event has a
// mod record emitted at the same moment, so a hook write further ahead means
// the mod is no longer posting (unloaded, broken, a failed reload).
const modLagGrace = 10

// modLive reports whether the mod's fold replaces the hook state. Known
// requires a state-defining record since houston started, so a restart
// mid-session keeps the hook path until the next one. There is no time-based
// expiry: an idle session posts nothing for hours and must stay on the mod.
func (s *Session) modLive() bool {
	if !s.hasMod || !s.mod.Known {
		return false
	}
	return s.hookState.SessionID == "" || s.hookState.UpdatedAt <= s.mod.Updated/1000+modLagGrace
}

// mergeModCoords fills the view of a session with no hook state from the
// mod. A coordinate the mod has not learnt keeps what discovery seeded.
func (s *Session) mergeModCoords() {
	m, v := &s.mod, &s.view
	v.SessionID = m.SID
	if m.CWD != "" {
		v.CWD = m.CWD
	}
	if m.Pane != "" {
		v.TmuxPane = m.Pane
	}
	if m.TmuxServer != "" {
		v.TmuxServer = m.TmuxServer
	}
	v.TranscriptPath = s.transcriptPath
	v.Agent = hook.AgentClaude
	v.UpdatedAt = m.Updated / 1000
	v.Turn = m.Turns
}

// overlayMod puts the mod's state over the merged view. A hook permission
// written at or after the newest main-loop record wins while the mod reads
// tool-running: where Claude Code is managed, the mod's classic hooks do not
// run and only the hook path learns of the dialog. A subagent's records must
// not hide it, hence MainUpdated.
func (s *Session) overlayMod() {
	m, v := &s.mod, &s.view
	v.State = m.State
	v.Tool = m.Tool
	v.ToolInputHint = m.Hint
	v.LastMessage = m.Message
	v.Since = m.Since
	v.UpdatedAt = max(v.UpdatedAt, m.Updated/1000)

	st := s.hookState
	if m.State == hook.StateToolRunning && st.State == hook.StatePermission && st.UpdatedAt >= m.MainUpdated/1000 {
		v.State, v.Tool, v.ToolInputHint, v.LastMessage = st.State, st.Tool, st.ToolInputHint, st.LastMessage
	}
}

// background is the mod's list once a stop has made it authoritative, else
// the transcript's. Never both: one source per computation.
func (s *Session) background(now time.Time) []BackgroundTask {
	if !s.modLive() || !s.mod.BGKnown {
		return s.bg.list(now)
	}
	var out []BackgroundTask
	for _, t := range s.mod.BackgroundAt(now) {
		out = append(out, BackgroundTask{ID: t.ID, Kind: t.Kind, Hint: t.Hint, Since: t.Since})
	}
	return out
}

// asksNow takes the question from the mod's Stop record once one arrived this
// turn, so it shows before the transcript tail catches up.
func (s *Session) asksNow() string {
	if s.modLive() && s.mod.StopSeen {
		return questionTail(s.mod.Last)
	}
	return s.asks
}

// applyModDirty folds the sessions the mod store changed or evicted into the
// hub and broadcasts them at once, so a permission reaches Fleet without
// waiting for a tick.
func (h *Hub) applyModDirty() {
	for _, sid := range h.mod.TakeDirty() {
		m, ok := h.mod.Session(sid)
		h.applyMod(sid, m, ok)
	}
}

func (h *Hub) applyMod(sid string, m claudemod.Session, ok bool) {
	now := time.Now()
	h.mu.Lock()
	sess, exists := h.sessions[sid]
	if !ok {
		if !exists {
			h.mu.Unlock()
			return
		}
		sess.mod, sess.hasMod = claudemod.Session{}, false
		if sess.modOnly && sess.hookState.SessionID == "" {
			// Nothing else names it, so it goes as a removed state file does.
			if sess.chat != nil {
				sess.chat.closeSubs()
			}
			delete(h.sessions, sid)
			h.mu.Unlock()
			return
		}
		sess.refreshView(now)
		view := sess.view
		h.mu.Unlock()
		h.broadcastIfChanged(sess, view)
		return
	}
	if !exists {
		// An unknown state would show a card with nothing to say.
		if !m.Known {
			h.mu.Unlock()
			return
		}
		sess = &Session{modOnly: true}
		h.sessions[sid] = sess
	}
	sess.mod, sess.hasMod = m, true
	moved := false
	if p := sess.effectiveTranscript(); p != "" {
		moved = sess.setTranscriptPath(p)
	}
	if sess.hookState.SessionID == "" && m.Turns > sess.lastModTurn {
		sess.lastModTurn = m.Turns
		sess.trail = []TrailChip{}
		sess.resetTools()
	}
	sess.refreshView(now)
	view := sess.view
	h.mu.Unlock()

	// Broadcast first: the transcript read broadcasts the newer view.
	h.broadcastIfChanged(sess, view)
	if moved {
		h.refreshTranscript(sid)
	}
}

// tickMod recomputes every session with mod data: a monitor's timeout passes
// with no record, and a mod-only session may have no transcript to poll.
func (h *Hub) tickMod(now time.Time) {
	type update struct {
		sess *Session
		view SessionView
	}
	var ups []update
	h.mu.Lock()
	for _, sess := range h.sessions {
		if !sess.hasMod {
			continue
		}
		sess.refreshView(now)
		ups = append(ups, update{sess, sess.view})
	}
	h.mu.Unlock()
	for _, u := range ups {
		h.broadcastIfChanged(u.sess, u.view)
	}
}
