package runs

import (
	"maps"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/noamsto/houston/crewview"
)

// blockedMemory keeps each worker's blocked-episode start across views: crew
// resets age_sec on every re-stamp (a blocked worker re-stamps every 300 s), so
// age alone would never let routeCrewQuestion's 15-minute silence elapse.
// Values are unix ms.
type blockedMemory map[crewWorkerKey]int64

type crewWorkerKey struct{ crew, branch string }

// viewRuns is what one overview view says about the crew layer.
type viewRuns struct {
	local     map[string]crewBranch      // by branch
	remote    []crewBranch               // remote_worker and peer rows
	crewItems map[string][]CrewAttention // crew id -> items with no branch
}

// projectItem keeps the item's start (generatedAt - age_sec) rather than its
// age, so an unchanged item does not change the run's signature every view.
func projectItem(it crewview.Item, generatedAt int64) CrewAttention {
	a := CrewAttention{ID: it.ID, Kind: it.Kind, Severity: it.Severity, Message: itemMessage(it), Since: generatedAt - it.AgeSec}
	switch {
	case crewview.Executable(it):
		a.Action = &CrewAction{Verb: it.Action.Verb, Label: it.Action.Label, Command: it.Action.Command, NeedsInput: it.Action.NeedsInput, PR: it.Action.PR}
	case it.Action != nil && it.Origin != crewview.OriginPeer:
		a.Command = it.Action.Command
	}
	return a
}

// watchdogNameRe is the shape of a watchdog prefix houston will name, the same
// rule the activity feed applies (crewfeed's watchdogText).
var watchdogNameRe = regexp.MustCompile(`^[a-z][a-z-]{0,15}$`)

// itemMessage is the text a Crew tab item shows. A watchdog's message is
// dispatcher-facing and prefix-coded, so it is replaced by houston's wording.
func itemMessage(it crewview.Item) string {
	switch it.Kind {
	case "prompt":
		return crewWatchdogPromptNote
	case "dead":
		return crewWatchdogDeadNote
	case "stall", "watchdog":
		if c, ok := watchdogClassOf(it.Message); ok {
			return c.note
		}
		prefix, _, ok := strings.Cut(it.Message, ":")
		if prefix = strings.TrimSpace(prefix); ok && watchdogNameRe.MatchString(prefix) {
			return "watchdog: " + prefix
		}
		return "watchdog"
	}
	return it.Message
}

// heldCrews is which crews' rosters crew's view failed to collect, from its
// errors[]: their absence from the view says nothing about their workers.
type heldCrews struct {
	all   bool // "roster: …": every crew on the bus
	crews map[string]bool
}

func heldCrewsOf(errs []string) heldCrews {
	h := heldCrews{crews: map[string]bool{}}
	for _, e := range errs {
		if strings.HasPrefix(e, "roster: ") {
			h.all = true
			continue
		}
		rest, ok := strings.CutPrefix(e, "crew ")
		if !ok {
			continue
		}
		if id, why, ok := strings.Cut(rest, ": "); ok && strings.HasPrefix(why, "workers: ") {
			h.crews[id] = true
		}
	}
	return h
}

func (h heldCrews) has(crewID string) bool { return h.all || h.crews[crewID] }

// crewRosterHeldNote is the notice a held worker carries (spliceHeld).
const crewRosterHeldNote = "crew could not read this crew's roster; showing its last known state"

// rosterSnap is one crew's rows from the last view that listed them, generated
// at at (crew's clock, unix s).
type rosterSnap struct {
	at      int64
	workers []crewview.Worker
	items   []crewview.Item
}

// spliceHeld replaces the rows of each crew whose roster failed (held) with
// its snapshot in prev, ages shifted by the view time since, so UpdatedAt and
// an item's Since keep their listed values and the rows are routed afresh.
// The view's crew-scoped (branchless) items stand unless the whole roster
// failed. A snapshot older than crewStaleAfter is dropped and its crew reads
// as the view says. It returns the spliced view, the snapshots for the next
// tick and the crews it spliced; ov's slices, which the stream shares, and
// prev are never written through.
func spliceHeld(ov crewview.Overview, held heldCrews, prev map[string]rosterSnap) (crewview.Overview, map[string]rosterSnap, map[string]bool) {
	maxAge := int64(crewStaleAfter / time.Second)
	next := map[string]rosterSnap{}
	heldIDs := map[string]bool{}
	for c, snap := range prev {
		if held.has(c) && ov.GeneratedAt-snap.at <= maxAge {
			next[c] = snap
			heldIDs[c] = true
		}
	}

	workers := make([]crewview.Worker, 0, len(ov.Workers))
	for _, w := range ov.Workers {
		if heldIDs[w.Crew] {
			continue
		}
		workers = append(workers, w)
		if !held.has(w.Crew) {
			snap := next[w.Crew]
			snap.at, snap.workers = ov.GeneratedAt, append(snap.workers, w)
			next[w.Crew] = snap
		}
	}
	items := make([]crewview.Item, 0, len(ov.Attention))
	for _, it := range ov.Attention {
		if heldIDs[it.Crew] && (it.Branch != "" || held.all) {
			continue
		}
		items = append(items, it)
		if !held.has(it.Crew) {
			snap := next[it.Crew]
			snap.at, snap.items = ov.GeneratedAt, append(snap.items, it)
			next[it.Crew] = snap
		}
	}

	for _, c := range slices.Sorted(maps.Keys(heldIDs)) {
		snap := next[c]
		shift := ov.GeneratedAt - snap.at
		for _, w := range snap.workers {
			if w.AgeSec != nil {
				age := *w.AgeSec + shift
				w.AgeSec = &age
			}
			workers = append(workers, w)
		}
		for _, it := range snap.items {
			if it.Branch == "" && !held.all {
				continue
			}
			it.AgeSec += shift
			items = append(items, it)
		}
	}
	ov.Workers, ov.Attention = workers, items
	return ov, next, heldIDs
}

// maxViewNotice caps a dispatcher's notice so a long error list stays a line.
const maxViewNotice = 300

// viewNotice is the notice a crew's dispatcher run carries: the view's status
// notice, then every error of the view except one naming another crew.
func viewNotice(status string, errs []string, crewID string) string {
	var parts []string
	if status != "" {
		parts = append(parts, status)
	}
	for _, e := range errs {
		if rest, ok := strings.CutPrefix(e, "crew "); ok {
			if id, _, _ := strings.Cut(rest, ": "); id != crewID {
				continue
			}
		}
		parts = append(parts, e)
	}
	n := strings.Join(parts, "; ")
	if len(n) <= maxViewNotice {
		return n
	}
	cut := maxViewNotice
	for cut > 0 && !utf8.RuneStart(n[cut]) {
		cut--
	}
	return n[:cut]
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// crewRunsFromView turns crew's overview into crew-layer runs. State comes from
// the view; ids supplies only identity and pane-join evidence. mem is mutated.
func crewRunsFromView(v crewview.Overview, ids map[string]crewIdentity, replies map[string]int64, mem blockedMemory) viewRuns {
	out := viewRuns{local: map[string]crewBranch{}, crewItems: map[string][]CrewAttention{}}

	type itemKey struct{ crew, branch string }
	byWorker := map[itemKey][]crewview.Item{}
	for _, it := range v.Attention {
		if it.Branch == "" {
			out.crewItems[it.Crew] = append(out.crewItems[it.Crew], projectItem(it, v.GeneratedAt))
			continue
		}
		k := itemKey{it.Crew, it.Branch}
		byWorker[k] = append(byWorker[k], it)
	}

	for _, w := range v.Workers {
		if w.Crew == "" || w.Branch == "" {
			continue
		}
		items := byWorker[itemKey{w.Crew, w.Branch}]
		key := crewWorkerKey{w.Crew, w.Branch}
		local := w.Origin == crewview.OriginLocal || w.Origin == ""
		peer := w.Origin == crewview.OriginPeer

		r := Run{
			Branch: w.Branch,
			State:  FromCrewState(w.State),
			Agent:  deref(w.Engine),
			Crew: &CrewRef{
				Name:   w.Crew,
				Origin: w.Origin,
				Tier:   deref(w.Tier),
				Title:  deref(w.Title),
				Model:  deref(w.Model),
			},
		}
		if !local {
			r.Role = RoleWorker
			r.Crew.Host = w.Host
			if r.Agent == "" {
				r.Agent = "crew"
			}
		}
		if w.AgeSec != nil {
			r.UpdatedAt = v.GeneratedAt - *w.AgeSec
		}
		if n := deref(w.Name); codenameRe.MatchString(n) {
			r.Crew.Codename = n
		}
		if w.PRURL != nil {
			if n := prNumberFromURL(*w.PRURL); n != "" {
				r.PR = &PRRef{URL: *w.PRURL, Number: n}
			}
		}
		for _, it := range items {
			r.Crew.Attention = append(r.Crew.Attention, projectItem(it, v.GeneratedAt))
		}

		detail := deref(w.Detail)
		if peer {
			// The owning host's houston surfaces a peer's question to its own
			// dispatcher; here it is never needs-you.
			if r.State == StateBlocked {
				r.State = StateRunning
			}
		} else {
			applyItemKinds(&r, items, detail)
		}

		b := crewBranch{Run: r}
		if local {
			if id, ok := ids[w.Branch]; ok && (id.crew == "" || id.crew == w.Crew) {
				b.Crew.Color = id.color
				b.Crew.Sessions = id.sessions
				b.CrewSession = id.engineSession
				b.session, b.lastActivity, b.workerSilent = id.session, id.lastActivity, id.workerSilent
				if b.Crew.Codename == "" {
					b.Crew.Codename = id.codename
				}
			}
		}
		// A remote worker belongs to a crew on this bus: its dispatcher's reply
		// is written here, and its question is routed like a local one.
		if !peer {
			asks := b.State == StateBlocked && b.Question != nil && b.Question.Via == "crew"
			// An unknown status time (no age_sec) cannot be compared to a reply.
			if asks && b.UpdatedAt > 0 && answeredByDispatcher(replies[w.Branch], b.UpdatedAt) {
				b.State, b.Question, b.Crew.Detail = "", nil, ""
				asks = false
			}
			if asks {
				if _, ok := mem[key]; !ok {
					mem[key] = v.GeneratedAt * 1000
					if b.UpdatedAt > 0 {
						mem[key] = b.UpdatedAt * 1000
					}
				}
				b.blockedSince = mem[key]
			} else {
				delete(mem, key)
			}
		}
		if local {
			out.local[w.Branch] = b
			continue
		}
		out.remote = append(out.remote, b)
	}
	return out
}

// applyItemKinds sets the state, question and detail a worker's own attention
// items imply; the first kind present wins, in the order prompt, dead,
// stall/watchdog, blocked.
func applyItemKinds(r *Run, items []crewview.Item, detail string) {
	find := func(kinds ...string) (crewview.Item, bool) {
		for _, it := range items {
			if slices.Contains(kinds, it.Kind) {
				return it, true
			}
		}
		return crewview.Item{}, false
	}
	if _, ok := find("prompt"); ok {
		r.State = StateBlocked
		r.Question = &Question{Text: crewWatchdogPromptNote, Via: "pane", Kind: QuestionDialog}
		return
	}
	if _, ok := find("dead"); ok {
		r.AttentionNote = crewWatchdogDeadNote
		return
	}
	if it, ok := find("stall", "watchdog"); ok {
		r.State = StateRunning
		if c, ok := watchdogClassOf(it.Message); ok && !c.needsYou {
			r.Attention, r.AttentionNote = AttentionStuck, c.note
		}
		return
	}
	r.Crew.Detail = detail
	if _, ok := find("blocked"); ok {
		r.State = StateBlocked
		text := detail
		if text == "" {
			text = crewBlockedNoDetail
		}
		r.Question = &Question{Text: text, Via: "crew", Kind: QuestionCrew}
	}
}
