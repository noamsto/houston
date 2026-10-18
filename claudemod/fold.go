package claudemod

import (
	"encoding/json"
	"maps"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/noamsto/houston/hook"
)

// BGTask is a background shell or monitor the fold holds as outstanding.
type BGTask struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"` // "shell" | "monitor"
	Hint    string `json:"hint,omitempty"`
	Since   int64  `json:"since"`             // unix seconds
	Expires int64  `json:"expires,omitempty"` // unix seconds; 0 = never
}

const (
	bgShell   = "shell"
	bgMonitor = "monitor"
)

type flight struct{ agent, id, tool, hint string }

// permission is a dialog the mod saw open. id is empty when no tool.check
// candidate matched it.
type permission struct {
	agent, tool, hint, id, message string
}

// candidate is the call a tool.check "ask" decision named: PermissionRequest
// carries no tool_use_id, so the dialog borrows this one.
type candidate struct{ agent, tool, id string }

type bgEntry struct {
	task    BGTask
	toolUse string
}

type bgStart struct {
	hint string
	ts   int64
}

// fold is the state a record sequence produces. Its slices and maps are
// owned, so clone before mutating a copy.
type fold struct {
	phase    hook.State
	since    int64    // unix s of the record that last changed the view state
	inflight []flight // start order; the last entry is the newest
	perm     *permission
	cand     *candidate
	turns    int
	last     string
	stopSeen bool
	bg       map[string]bgEntry
	bgStarts map[string]bgStart // keyed by the starting tool_use id

	cwd, transcript, pane, tmuxServer string

	known, bgKnown bool
}

func (f fold) clone() fold {
	c := f
	c.inflight = slices.Clone(f.inflight)
	if f.perm != nil {
		p := *f.perm
		c.perm = &p
	}
	if f.cand != nil {
		cd := *f.cand
		c.cand = &cd
	}
	c.bg = maps.Clone(f.bg)
	c.bgStarts = maps.Clone(f.bgStarts)
	return c
}

// viewState is the state the session shows: a pending dialog over the phase.
// Every transition into a phase that ends the turn clears the dialog.
func (f *fold) viewState() hook.State {
	if f.perm != nil {
		return hook.StatePermission
	}
	return f.phase
}

func (f *fold) apply(rec Record) {
	before := f.viewState()
	f.coords(rec)
	if rec.Agent != "" {
		f.applyAgent(rec)
	} else {
		f.applyMain(rec)
	}
	if f.viewState() != before {
		f.since = rec.TS / 1000
	}
}

func (f *fold) coords(rec Record) {
	set := func(dst *string, v string) {
		if v != "" {
			*dst = v
		}
	}
	set(&f.cwd, rec.CWD)
	set(&f.transcript, rec.Transcript)
	set(&f.pane, rec.Pane)
	set(&f.tmuxServer, rec.TmuxServer)
}

// applyAgent folds a subagent's record: it never moves the main loop, only
// its own calls and a dialog it raised.
func (f *fold) applyAgent(rec Record) {
	switch rec.Ev {
	case evToolStart:
		f.inflight = append(f.inflight, flight{rec.Agent, rec.ID, rec.Tool, toolHint(rec.Tool, rec.Input)})
	case evToolEnd:
		f.endFlight(rec)
		f.clearPermFor(rec)
	case evToolCheck:
		f.check(rec)
	case evPermission:
		f.permission(rec)
	case evNotification:
		f.notification(rec)
	}
}

func (f *fold) applyMain(rec Record) {
	switch rec.Ev {
	case evSessionStart:
		f.phase = hook.StateStarting
		f.inflight = nil
		f.clearTurn()
		f.last, f.stopSeen = "", false
		f.bgStarts = nil
		f.known = true
	case evTurnStart:
		f.phase = hook.StateThinking
		f.dropMainFlights()
		f.clearTurn()
		f.last, f.stopSeen = "", false
		f.bgStarts = nil
		f.turns++
		f.known = true
	case evToolStart:
		f.inflight = append(f.inflight, flight{"", rec.ID, rec.Tool, toolHint(rec.Tool, rec.Input)})
		f.phase = hook.StateToolRunning
		if (rec.Tool == "Bash" && rec.BG) || rec.Tool == "Monitor" {
			if f.bgStarts == nil {
				f.bgStarts = map[string]bgStart{}
			}
			f.bgStarts[rec.ID] = bgStart{bgHint(rec.Input), rec.TS / 1000}
		}
		f.known = true
	case evToolEnd:
		f.endMainTool(rec)
	case evToolCheck:
		f.check(rec)
	case evPermission:
		f.permission(rec)
		f.known = true
	case evNotification:
		f.notification(rec)
	case evTurnComplete:
		f.phase = hook.StateWaiting
		f.dropMainFlights()
		f.clearTurn()
		f.known = true
	case evCompact:
		f.phase = hook.StateCompacting
		f.clearTurn()
		f.known = true
	case evSessionEnd:
		f.phase = hook.StateEnded
		f.clearTurn()
		f.known = true
	case evStop:
		f.last, f.stopSeen = rec.Last, true
		if rec.Tasks != nil {
			f.snapshotBackground(rec)
			f.bgKnown = true
		}
	case evTaskEnd:
		for _, t := range rec.Tasks {
			delete(f.bg, t.ID)
			if t.ToolUse == "" {
				continue
			}
			for id, e := range f.bg {
				if e.toolUse == t.ToolUse {
					delete(f.bg, id)
				}
			}
		}
	}
}

func (f *fold) clearTurn() {
	f.perm, f.cand = nil, nil
}

func (f *fold) dropMainFlights() {
	f.inflight = slices.DeleteFunc(f.inflight, func(fl flight) bool { return fl.agent == "" })
}

// endFlight removes rec's call from its agent's flights and reports whether
// it was in flight.
func (f *fold) endFlight(rec Record) bool {
	i := slices.IndexFunc(f.inflight, func(fl flight) bool { return fl.agent == rec.Agent && fl.id == rec.ID })
	if i < 0 {
		return false
	}
	f.inflight = slices.Delete(f.inflight, i, i+1)
	if f.cand != nil && f.cand.id == rec.ID {
		f.cand = nil
	}
	return true
}

// endMainTool leaves the phase alone for an id that is not in flight: a turn
// end already cleared it, and a late tool.end must not reopen the turn.
func (f *fold) endMainTool(rec Record) {
	f.clearPermFor(rec)
	f.endBackground(rec)
	if !f.endFlight(rec) {
		return
	}
	f.known = true
	if slices.ContainsFunc(f.inflight, func(fl flight) bool { return fl.agent == "" }) {
		f.phase = hook.StateToolRunning
		return
	}
	f.phase = hook.StateThinking
}

func (f *fold) clearPermFor(rec Record) {
	p := f.perm
	if p == nil {
		return
	}
	if (p.id != "" && p.id == rec.ID) || (p.id == "" && p.agent == rec.Agent && p.tool == rec.Tool) {
		f.perm = nil
	}
}

func (f *fold) check(rec Record) {
	if rec.Decision == "ask" {
		f.cand = &candidate{rec.Agent, rec.Tool, rec.ID}
	}
}

func (f *fold) permission(rec Record) {
	p := permission{agent: rec.Agent, tool: rec.Tool, hint: toolHint(rec.Tool, rec.Input)}
	if f.cand != nil && f.cand.agent == rec.Agent && f.cand.tool == rec.Tool {
		p.id = f.cand.id
		if p.hint == "" {
			p.hint = f.flightHint(rec.Agent, p.id)
		}
	}
	if f.perm != nil {
		p.message = f.perm.message
	}
	f.perm = &p
}

func (f *fold) notification(rec Record) {
	if rec.Type != "permission_prompt" && rec.Type != "elicitation_dialog" {
		return
	}
	if f.perm != nil {
		if f.perm.message == "" {
			f.perm.message = rec.Message
		}
		return
	}
	p := permission{agent: rec.Agent, message: rec.Message}
	if f.cand != nil && f.cand.agent == rec.Agent {
		p.tool, p.id = f.cand.tool, f.cand.id
		p.hint = f.flightHint(rec.Agent, p.id)
	}
	f.perm = &p
}

func (f *fold) flightHint(agent, id string) string {
	for _, fl := range f.inflight {
		if fl.agent == agent && fl.id == id {
			return fl.hint
		}
	}
	return ""
}

// endBackground starts or stops a background task from a call's result. A
// failed call does neither.
func (f *fold) endBackground(rec Record) {
	start, started := f.bgStarts[rec.ID]
	delete(f.bgStarts, rec.ID)
	if rec.Err {
		return
	}
	if rec.StopTask != "" {
		delete(f.bg, rec.StopTask)
	}
	if rec.Task == "" {
		return
	}
	t := BGTask{ID: rec.Task, Kind: bgShell, Hint: start.hint, Since: start.ts}
	if !started {
		t.Since = rec.TS / 1000
	}
	if rec.Tool == "Monitor" {
		t.Kind = bgMonitor
		if rec.TimeoutMS > 0 {
			t.Expires = rec.TS/1000 + rec.TimeoutMS/1000
		}
	}
	if f.bg == nil {
		f.bg = map[string]bgEntry{}
	}
	f.bg[t.ID] = bgEntry{task: t, toolUse: rec.ID}
}

// snapshotBackground applies a stop's task list as membership: the snapshot
// reports a monitor as a shell, so a known entry is kept whole.
func (f *fold) snapshotBackground(rec Record) {
	next := map[string]bgEntry{}
	for _, t := range rec.Tasks {
		if t.Status != "running" && t.Status != "pending" {
			continue
		}
		if e, ok := f.bg[t.ID]; ok {
			next[t.ID] = e
			continue
		}
		if t.Type != bgShell && t.Type != bgMonitor {
			continue
		}
		next[t.ID] = bgEntry{
			task:    BGTask{ID: t.ID, Kind: t.Type, Hint: cutHint(t.Description), Since: rec.TS / 1000},
			toolUse: t.ToolUse,
		}
	}
	f.bg = next
}

func toolHint(tool string, in *ToolInput) string {
	if in == nil {
		return ""
	}
	b, err := json.Marshal(in)
	if err != nil {
		return ""
	}
	return hook.ToolHint(tool, b)
}

// bgHint prefers the call's own description over its command, as the
// transcript's background tracker does.
func bgHint(in *ToolInput) string {
	if in == nil {
		return ""
	}
	if h := cutHint(in.Description); h != "" {
		return h
	}
	return cutHint(in.Command)
}

func cutHint(s string) string {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) <= 120 {
		return s
	}
	return string([]rune(s)[:119]) + "…"
}
