package runs

import (
	"context"
	"encoding/json"
	"log/slog"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/noamsto/houston/crewfeed"
	"github.com/noamsto/houston/crewview"
)

// CrewSource publishes the crew layer: each worker's state from crew's own
// view of its bus (crewViews), joined to its pane with identity read from the
// bus. It contributes what no other source can express: a worker's question,
// addressed to its dispatcher — it surfaces to you only when the dispatcher is
// gone or has been silent for crewDispatcherSilence — and the watchdog's
// verdict that a worker is stuck.
//
// It derives its own repo roots from the same ListWindowOptions call tmuxsource
// uses, rather than taking them from the caller — that keeps construction free
// of I/O and self-contained.
type CrewSource struct {
	client lister
	every  time.Duration

	// crewDirs caches root -> bus directory, keyed by lazytmux's @git_root.
	// scan() runs only on this source's own goroutine, so a plain map needs no
	// mutex — do not add one, and do not read this from another goroutine.
	crewDirs map[string]string

	// procStart is the terminal join's identity probe (resolvePane's
	// procStart parameter) — a field so tests stay hermetic.
	procStart procStartFunc

	// procs is the dispatcher join's process probe (joinDispatchers' probe
	// parameter), a field for the same reason as procStart.
	procs procProbe

	// sessions is the hooks layer's live session-per-pane view, used to keep a
	// terminal record from joining a different session inside the same engine
	// process (/clear, /resume). nil disables the check — tests, and a build
	// with no hub.
	sessions hookSessions

	// ids caches each bus log's identity fold by path, reused while size and
	// mtime hold. Same single-goroutine rule as crewDirs.
	ids map[string]identityLog

	// views supplies crew's view per bus; nil means no worker runs, only
	// dispatcher joins (tests that do not care).
	views crewViews

	// rosters is each crew's last listed view rows per bus (spliceHeld): a
	// crew whose roster the view failed to collect is read from them. Same
	// single-goroutine rule as crewDirs.
	rosters map[string]map[string]rosterSnap

	// feed tails every resolved bus's events.jsonl for the activity feed; nil
	// disables it (tests, tmux mode).
	feed *crewfeed.Store

	// now is the clock scan uses; nil means time.Now. A field so tests can pin it.
	now func() time.Time
}

// crewViews is crew's view per bus (crewview.Manager).
type crewViews interface {
	Ensure(bus, root string)
	Retain(buses map[string]bool)
	Latest(bus string) (crewview.Overview, crewview.Status, bool)
}

func NewCrewSource(c lister, sessions hookSessions, feed *crewfeed.Store, views crewViews, every time.Duration) *CrewSource {
	if every <= 0 {
		every = 3 * time.Second
	}
	return &CrewSource{client: c, sessions: sessions, feed: feed, views: views, every: every, crewDirs: map[string]string{}, ids: map[string]identityLog{}, procStart: foregroundStart, procs: hostProcs{}}
}

func (s *CrewSource) Name() string { return "crew" }

func (s *CrewSource) Run(ctx context.Context, out chan<- Delta) error {
	t := time.NewTicker(s.every)
	defer t.Stop()

	seen := map[string]bool{}
	for {
		current, ok := s.scan()
		// A transient tmux error is not evidence that every crew-sourced
		// branch vanished: skip the tick entirely rather than diffing
		// against an empty map and emitting Gone for everything seen.
		if ok {
			deltas, newSeen := tickCrewDeltas(current, seen, time.Now())
			seen = newSeen
			for _, d := range deltas {
				select {
				case out <- d:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
}

// crewEvictionGrace is how long a terminal-state run stays visible after its
// last status update before CrewSource evicts it. Zero delay would yank the
// "done" card out from under a human looking at it the instant a worker
// posts done; 10 minutes is long enough to avoid that while still clearing
// finished runs out during a normal session.
const crewEvictionGrace = 10 * time.Minute

// crewStaleAfter bounds how long a windowless branch may go without a dispatch,
// resume or status row before scan stops listing it, when it has no live
// dispatcher or its worker has gone silent (see crewRunAbandoned): the bus is
// append-only, so a worker that died without a final status would otherwise
// stay listed forever.
const crewStaleAfter = 6 * time.Hour

// crewRunFinished reports whether r's state has been terminal for
// longer than crewEvictionGrace. UpdatedAt == 0 covers both a malformed
// status record and a dispatch-only branch with no status yet — neither is
// eligible, since treating a zero timestamp as "infinitely old" would evict
// instantly and bypass the grace period entirely.
func crewRunFinished(r Run, now time.Time) bool {
	if r.State != StateDone && r.State != StateFailed {
		return false
	}
	if r.UpdatedAt == 0 {
		return false
	}
	return now.Sub(time.Unix(r.UpdatedAt, 0)) > crewEvictionGrace
}

// identityOnly keeps who a finished worker is (crew, codename, project, branch,
// PR) and drops what crew's view last said it was doing, so the evicted layer
// stops contributing state, attention and a question to the composed run.
func identityOnly(r Run) Run {
	crew := *r.Crew
	crew.Detail = ""
	crew.Attention = nil
	crew.ViewNotice = ""
	r.Crew = &crew
	r.Stale = false
	if !r.CrewFinished {
		r.State = ""
	}
	r.Attention = ""
	r.AttentionNote = ""
	r.Question = nil
	r.UpdatedAt = 0
	r.Since = 0
	return r
}

// tickCrewDeltas computes this cycle's deltas from the runs scan() currently
// sees, already keyed by their final registry keys, evicting anything whose
// state has been terminal for longer than crewEvictionGrace. A crew/ or
// crewremote/ run is dropped, having no other layer to group. A pane-keyed
// run is not dropped but reduced to its identity (identityOnly): its pane still
// has tmux/hooks cards, which need crew.name and project to stay grouped under
// their dispatcher. seen is the previous tick's emitted key set; it returns the
// deltas to send and the new seen set.
//
// Every update is emitted before every Gone. A branch's key flips as its pane
// opens and closes, and that order is what stops a subscriber briefly holding
// neither key.
func tickCrewDeltas(current map[string]Run, seen map[string]bool, now time.Time) ([]Delta, map[string]bool) {
	var deltas []Delta
	newSeen := map[string]bool{}
	for key, r := range current {
		if crewRunFinished(r, now) {
			if strings.HasPrefix(key, "crew/") || strings.HasPrefix(key, crewRemotePrefix) {
				continue
			}
			r = identityOnly(r)
		}
		deltas = append(deltas, Delta{Source: "crew", Key: key, Run: r})
		newSeen[key] = true
	}
	for key := range seen {
		if !newSeen[key] {
			deltas = append(deltas, Delta{Source: "crew", Key: key, Gone: true})
		}
	}
	return deltas, newSeen
}

// crewRunAbandoned reports whether a branch has been silent past crewStaleAfter
// with nothing to revive it: no window, not waiting on a human, and either no
// live dispatcher or a worker that has not spoken since its newest dispatch or
// resume row (only a watchdog or nothing did). b.State must be the post-routing
// state. lastActivity == 0 (no row with a timestamp) is never abandoned.
func crewRunAbandoned(b crewBranch, windowless, dispatcherAlive bool, now time.Time) bool {
	if !windowless || b.State == StateBlocked || b.lastActivity == 0 {
		return false
	}
	if dispatcherAlive && !b.workerSilent {
		return false
	}
	return now.Sub(time.UnixMilli(b.lastActivity)) > crewStaleAfter
}

// scan returns this tick's runs under their final registry keys: for a local
// worker the pane id where its branch joins exactly one agent pane, otherwise
// "crew/<bus>/<branch>"; for a worker crew's view reports on another machine,
// CrewRemoteKey. Only workers in crew's view are listed, except that a crew
// whose roster the view reports failing is read from its last listed rows,
// marked stale (spliceHeld), and a crew absent from a bus's view whose
// dispatcher is joined on another bus is read from the bus log (logWorkers). It
// also returns, under its pane id, a dispatcher layer for every crew that joins
// its dispatcher pane (joinDispatchers) — Role, crew id, bus, its
// unfinished-worker count, the crew's own attention items and the view's notice
// and errors, so the key lists only while another layer supplies an agent.
//
// ok=false means a tmux query failed — a transient error, not evidence every
// crew-sourced branch vanished. The caller must skip the tick entirely rather
// than diff against an empty map.
func (s *CrewSource) scan() (map[string]Run, bool) {
	wins, err := s.client.ListWindowOptions()
	if err != nil {
		slog.Debug("crew source: list window options", "error", err)
		return nil, false
	}
	panes, err := s.client.ListPaneOptions()
	if err != nil {
		slog.Debug("crew source: list pane options", "error", err)
		return nil, false
	}

	now := time.Now()
	if s.now != nil {
		now = s.now()
	}

	roots := map[string]bool{}
	for _, w := range wins {
		if w.GitRoot != "" {
			roots[w.GitRoot] = true
		}
	}

	rootList := slices.Sorted(maps.Keys(roots))

	// The pane → hook session map the terminal join consults, built from the
	// same listing and the hooks layer's own trust rules (paneSetFrom,
	// paneHookSessions).
	var paneSessions map[string]string
	if s.sessions != nil {
		paneSessions = paneHookSessions(s.sessions.Snapshot(), paneSetFrom(panes, now))
	}

	// Every bus, not only those whose log names a branch: a dispatcher that
	// has not dispatched yet still joins its pane. busRoot is the first root,
	// sorted, that resolved to the bus and still exists: crew dash runs there.
	joins := map[string]map[string]string{}
	busRoot := map[string]string{}
	for _, repo := range rootList {
		bus := s.crewDir(repo)
		if bus == "" {
			continue
		}
		if _, done := joins[bus]; !done {
			joins[bus] = joinDispatchers(bus, wins, panes, s.procs)
			if s.feed != nil {
				s.feed.Advance(bus)
			}
		}
		if _, has := busRoot[bus]; !has {
			if fi, err := os.Stat(repo); err == nil && fi.IsDir() {
				busRoot[bus] = repo
			}
		}
	}
	buses := slices.Sorted(maps.Keys(joins))
	if s.views != nil {
		for _, bus := range buses {
			if root := busRoot[bus]; root != "" {
				s.views.Ensure(bus, root)
			}
		}
		live := make(map[string]bool, len(buses))
		for _, bus := range buses {
			live[bus] = true
		}
		s.views.Retain(live)
	}

	// The bus a crew's dispatcher is joined on, first bus (sorted) winning. A
	// worker may report on another bus than its dispatcher's: a dispatcher
	// launched in one repo can dispatch into another.
	joinedOn := map[string]string{}
	for _, bus := range buses {
		for crewID, paneID := range joins[bus] {
			if _, taken := joinedOn[crewID]; !taken && paneID != "" {
				joinedOn[crewID] = bus
			}
		}
	}

	homes := crewHomes(buses, joins)

	out := map[string]Run{}
	liveIDs := map[string]identityLog{}
	crewItems := map[string]map[string][]CrewAttention{} // bus -> crew id -> items
	notices := map[string]string{}
	viewErrs := map[string][]string{}
	rosters := map[string]map[string]rosterSnap{}
	liveWorkers := map[string]int{} // crew id -> unfinished workers
	for _, bus := range buses {
		if s.views == nil {
			continue
		}
		ids := s.busIdentity(bus, liveIDs)
		ov, st, ok := s.views.Latest(bus)
		if st.State == crewview.StateUnsupported || st.State == crewview.StateUnavailable {
			notices[bus] = st.Notice
		}
		if !ok {
			// No view lists no workers, but keeps the snapshots for a roster
			// failure after it.
			rosters[bus] = s.rosters[bus]
			continue
		}
		viewErrs[bus] = ov.Errors

		held := heldCrewsOf(ov.Errors)
		var spliced map[rowKey]bool
		ov, rosters[bus], _, spliced = spliceHeld(ov, held, s.rosters[bus])

		// crew's view lists a crew only on the bus its dispatcher is live on, so
		// a worker dispatched into this repo from another bus's dispatcher is
		// absent from it. Its rows are read from this bus's log instead.
		listed := map[string]bool{}
		for _, w := range ov.Workers {
			listed[w.Crew] = true
		}
		generatedAt := ov.GeneratedAt
		if generatedAt == 0 {
			generatedAt = now.Unix()
		}
		// crew's view also drops a finished worker whose window is still open,
		// leaving only the tmux layer's identity-less card. Such a worker is read
		// from the bus log too, pinned to the one pane that is provably its own.
		inView := map[rowKey]bool{}
		for _, w := range ov.Workers {
			inView[rowKey{w.Crew, w.Branch}] = true
		}
		type finishedJoin struct {
			pane  string
			stuck bool // only the relaxed join accepts the pane: its own stamp is stale
		}
		finishedPane := map[rowKey]finishedJoin{}
		for branch, id := range ids {
			k := rowKey{id.crew, branch}
			if id.crew == "" || !id.hasStatus || inView[k] || id.state != "done" && id.state != "failed" {
				continue
			}
			join := func(stuckOK bool) (string, bool) {
				p, n := resolvePaneAs(bus, branch, FromCrewState(id.state), id.statusTS/1000, id.session, id.engineSession, paneSessions, wins, panes, s.crewDir, s.procStart, stuckOK)
				return p, n == 1
			}
			if p, ok := join(false); ok {
				finishedPane[k] = finishedJoin{pane: p}
			} else if p, ok := join(true); ok {
				finishedPane[k] = finishedJoin{pane: p, stuck: true}
			}
		}
		synthWorkers, synthItems := logWorkers(ids, generatedAt, func(crew, branch string) bool {
			if _, ok := finishedPane[rowKey{crew, branch}]; ok {
				return true
			}
			other := joinedOn[crew]
			return other != "" && other != bus && !listed[crew]
		})
		synthItems = slices.DeleteFunc(synthItems, func(it crewview.Item) bool {
			_, ok := finishedPane[rowKey{it.Crew, it.Branch}]
			return ok
		})
		synthKeys := map[rowKey]bool{}
		for _, w := range synthWorkers {
			synthKeys[rowKey{w.Crew, w.Branch}] = true
		}
		if len(synthWorkers) > 0 {
			ov.GeneratedAt = generatedAt
			ov.Workers = slices.Concat(ov.Workers, synthWorkers)
			ov.Attention = slices.Concat(ov.Attention, synthItems)
		}
		vr := crewRunsFromView(ov, ids)
		crewItems[bus] = vr.crewItems

		project := ProjectFromCommonDir(filepath.Dir(bus))
		for branch, b := range vr.local {
			routed := routeCrewQuestion(b, dispatcherLive(joinedOn, b.Crew.Name), now)
			if home := homes[b.Crew.Name]; home != "" {
				routed.Crew.Home = home
			}
			routed.Project = project
			routed.Role = RoleWorker
			routed.CrewBus = bus
			routed.Worktree = worktreeFor(bus, branch, wins, s.crewDir)
			if spliced[rowKey{b.Crew.Name, b.Branch}] {
				routed.Stale, routed.Crew.ViewNotice = true, crewRosterHeldNote
			}
			paneID, candidates := resolvePane(bus, branch, b.State, b.UpdatedAt, b.session, b.CrewSession, paneSessions, wins, panes, s.crewDir, s.procStart)
			if f, ok := finishedPane[rowKey{b.Crew.Name, b.Branch}]; ok {
				paneID, candidates = f.pane, 1
				routed.CrewFinished = f.stuck
			}
			activity := crewBranch{Run: routed, lastActivity: max(b.lastActivity, b.UpdatedAt*1000), workerSilent: b.workerSilent}
			if crewRunAbandoned(activity, candidates != 1, joinedOn[b.Crew.Name] != "" && !synthKeys[rowKey{b.Crew.Name, b.Branch}], now) {
				continue
			}
			key := paneID
			if candidates != 1 {
				key = "crew/" + bus + "/" + branch
				slog.Debug("crew source: no pane join", "bus", bus, "branch", branch, "candidates", candidates)
			}
			out[key] = routed
			if unfinishedWorker(routed) {
				liveWorkers[b.Crew.Name]++
			}
		}
		// CrewBus is the bus whose view reported the row, so the action route
		// can find its items; it has no worktree, which keeps /reply off it. A
		// remote worker's question goes to its dispatcher on this bus; a peer
		// row carries no question to route. Synthesized rows are always local, so
		// none reach this loop.
		for _, b := range vr.remote {
			r := routeCrewQuestion(b, dispatcherLive(joinedOn, b.Crew.Name), now)
			r.Project = project
			r.CrewBus = bus
			if spliced[rowKey{b.Crew.Name, b.Branch}] {
				r.Stale, r.Crew.ViewNotice = true, crewRosterHeldNote
			}
			out[CrewRemoteKey(r.Crew.Name, r.Branch)] = r
			if unfinishedWorker(r) {
				liveWorkers[r.Crew.Name]++
			}
		}
	}
	s.rosters = rosters
	s.ids = liveIDs

	// Sorted so that two buses naming one pane resolve the same way every tick.
	for _, bus := range buses {
		for crewID, paneID := range joins[bus] {
			if held, taken := out[paneID]; taken {
				slog.Debug("crew source: dispatcher pane already keyed", "bus", bus, "crew", crewID, "pane", paneID, "branch", held.Branch)
				continue
			}
			out[paneID] = Run{Role: RoleDispatcher, Crew: &CrewRef{Name: crewID, LiveWorkers: liveWorkers[crewID], Attention: crewItems[bus][crewID], ViewNotice: viewNotice(notices[bus], viewErrs[bus], crewID)}, CrewBus: bus}
		}
	}
	return out, true
}

// unfinishedWorker reports whether a crew worker row is still on its way to a
// result: any crew-layer state but done, failed, review or none.
func unfinishedWorker(r Run) bool {
	return !slices.Contains([]State{"", StateDone, StateFailed, StateReview}, r.State)
}

// busIdentity folds every log in bus into the identity per branch, recording
// each log it read in live (the next tick's cache).
func (s *CrewSource) busIdentity(bus string, live map[string]identityLog) map[string]crewIdentity {
	ids := map[string]crewIdentity{}
	logs, err := filepath.Glob(filepath.Join(bus, "*.jsonl"))
	if err != nil {
		return ids
	}
	for _, path := range logs {
		il, ok := readIdentityLog(path, s.ids)
		if !ok {
			continue
		}
		live[path] = il
		maps.Copy(ids, il.ids)
	}
	return ids
}

// crewHomes maps each crew id to the project name of the one bus holding
// crews/<id>. A crew dir present in several buses takes the bus its dispatcher
// joined on (first join wins), and has no known home when none did.
func crewHomes(buses []string, joins map[string]map[string]string) map[string]string {
	holders := map[string][]string{} // crew id -> buses holding crews/<id>
	for _, bus := range buses {
		entries, err := os.ReadDir(filepath.Join(bus, "crews"))
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() {
				holders[e.Name()] = append(holders[e.Name()], bus)
			}
		}
	}
	homes := make(map[string]string, len(holders))
	for crewID, held := range holders {
		home := ""
		if len(held) == 1 {
			home = held[0]
		}
		for _, bus := range held {
			if home == "" && joins[bus][crewID] != "" {
				home = bus
			}
		}
		if home != "" {
			homes[crewID] = ProjectFromCommonDir(filepath.Dir(home))
		}
	}
	return homes
}

// crewDirTimeout bounds the git call below so a hung git cannot park this
// source's goroutine forever.
const crewDirTimeout = 5 * time.Second

// crewDir resolves where dispatcher writes its bus for a checkout.
// lazytmux's @git_root is the worktree's top level, but dispatcher writes under
// the COMMON git dir — and in a worktree <root>/.git is a file, not a
// directory, so joining ".git/crew" there finds nothing. Worktree-per-branch is
// the normal case here, not an edge case.
func (s *CrewSource) crewDir(root string) string {
	if dir, ok := s.crewDirs[root]; ok {
		return dir
	}
	ctx, cancel := context.WithTimeout(context.Background(), crewDirTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", "-C", root, "rev-parse", "--git-common-dir").Output()
	if err != nil {
		s.crewDirs[root] = ""
		return ""
	}
	dir := strings.TrimSpace(string(out))
	if !filepath.IsAbs(dir) {
		// Whether this comes back relative or absolute depends on whether root
		// is a worktree or the main checkout, not on git's version: verified on
		// git 2.55, a worktree answers absolute and the main checkout answers
		// relative (".git"). Resolve the relative case against root.
		dir = filepath.Join(root, dir)
	}
	dir = filepath.Join(dir, "crew")
	s.crewDirs[root] = dir
	return dir
}

var (
	codenameRe  = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)
	tmuxColorRe = regexp.MustCompile(`^colour[0-9]+$`)
)

type crewRecord struct {
	TS     int64  `json:"ts"`
	CrewID string `json:"crew_id"`
	From   string `json:"from"`
	To     string `json:"to"`
	Kind   string `json:"kind"`
	Branch string `json:"branch"`
	Title  string `json:"title"`
	Tier   string `json:"tier"`
	Engine string `json:"engine"`
	Model  string `json:"model"`
	// Name and TmuxColor are the codename and tmux colour a dispatch row records
	// for the worker's window.
	Name      string `json:"name"`
	TmuxColor string `json:"tmux"`
	// EngineSession is the engine's own session id on a dispatch or resume row;
	// null for codex/cursor and absent on rows from an older dispatcher.
	EngineSession string `json:"engine_session"`
	// Session is a tmux session name, present on the dispatch record. It is
	// useful for the later branch->pane join but is not used yet.
	Session string `json:"session"`
	// Body is a JSON object on a status record but a JSON string on a msg
	// record, so decoding straight into a struct fails on every msg line and
	// drops that record whole. Decode it only where the shape is known.
	Body json.RawMessage `json:"body"`
}

// crewBlockedNoDetail is the question synthesised for a `crew status <from>
// blocked` carrying no detail, since that detail is optional. The composed-run
// invariant (a non-nil Question implies StateBlocked) can only protect a
// blocked status that has a Question, so without this the higher-precedence
// hooks layer overwrites State and the block goes unseen. Worded as houston's
// own description: there is no worker text here to quote.
const crewBlockedNoDetail = "Blocked, no detail given."

// The notes below are houston's own wording for what a watchdog status means:
// the watchdog's script detail (e.g. "prompt: interactive prompt in pane %326
// — ...") is dispatcher-facing and prefix-coded, so it never reaches
// user-facing copy.
const (
	// crewWatchdogPromptNote is the question for a worker parked on a prompt a
	// human clears at the pane.
	crewWatchdogPromptNote = "Parked on a prompt in its pane — open the terminal to answer it."

	crewWatchdogQuotaNote     = "Paused on a usage limit — it resumes when the limit resets; see its terminal."
	crewWatchdogTurnStallNote = "Its turn stopped producing tokens — check its terminal."
	crewWatchdogQuietNote     = "No visible progress for a while — the watchdog flagged it; check its terminal."
	crewWatchdogStalledNote   = "Not progressing since launch — check its terminal."
	crewWatchdogRunawayNote   = "Its output went off the rails — verify the pane, then kill and re-dispatch."
	crewWatchdogUnreadNote    = "It has not read a reviewer verdict yet — its dispatcher can nudge it."
	crewWatchdogBudgetNote    = "Paused on its token budget — see its terminal."

	// crewWatchdogDeadNote is the stuck reason for a watchdog `failed` (dead:).
	crewWatchdogDeadNote = "Its engine stopped — the watchdog marked it failed."
)

// watchdogClass is what a watchdog blocked prefix means for the card.
type watchdogClass struct {
	needsYou bool   // a human clears it at the pane: a pane Question
	note     string // the Question text, or the stuck reason
}

// watchdogPrefixes maps the reserved detail prefix of a watchdog blocked
// status to its meaning. prompt: needs a human at the pane; the rest mean the
// worker stopped making progress and may need a look (stuck). A prefix not
// listed — load: (host load, engine-independent), or anything new — claims
// nothing: the run reads running. dead: is posted as `failed`, so it never
// reaches the blocked branch. Requiring the colon keeps a detail that merely
// begins with the word from misclassifying.
var watchdogPrefixes = map[string]watchdogClass{
	"prompt":     {needsYou: true, note: crewWatchdogPromptNote},
	"quota":      {note: crewWatchdogQuotaNote},
	"turn-stall": {note: crewWatchdogTurnStallNote},
	"quiet":      {note: crewWatchdogQuietNote},
	"stalled":    {note: crewWatchdogStalledNote},
	"runaway":    {note: crewWatchdogRunawayNote},
	"unread":     {note: crewWatchdogUnreadNote},
	"budget":     {note: crewWatchdogBudgetNote},
}

func watchdogClassOf(detail string) (watchdogClass, bool) {
	prefix, _, ok := strings.Cut(detail, ":")
	if !ok {
		return watchdogClass{}, false
	}
	c, listed := watchdogPrefixes[prefix]
	return c, listed
}

// crewDispatcherSilence is how long a dispatcher gets to answer a worker's
// question before it surfaces as needs-you: a live dispatcher normally answers
// within minutes, while one that is itself waiting on you leaves the question
// for you.
const crewDispatcherSilence = 15 * time.Minute

// crewBranch is one local worker from crew's view (crewRunsFromView) with the
// bus identity evidence the pane join needs. session is the s<epoch> of the
// worker id on the latest status record (0 when unknown) — kept off Run
// because it is crew-join evidence, not API.
type crewBranch struct {
	Run
	session int64
	// blockedSince is when (unix ms) the current episode of asking its
	// dispatcher began (blocked_since), 0 when the worker is not asking.
	blockedSince int64
	// lastActivity is the newest bus ts (ms) of a dispatch, resume or status row.
	lastActivity int64
	// workerSilent is set when the newest dispatch, resume or status row is not a
	// status the worker itself posted (a dispatch or resume row, or a watchdog
	// status). It follows file order while lastActivity is the max ts; the two
	// agree because the bus is append-only.
	workerSilent bool
}

// routeCrewQuestion decides whether a worker's question reaches the human. It
// is addressed to the dispatcher, so while the dispatcher is live and has been
// asked for less than crewDispatcherSilence the run reads running and the
// question stays only in Crew.Detail. A watchdog prompt (pane) question is the
// human's regardless. It returns a copy and never writes through b.
func routeCrewQuestion(b crewBranch, live bool, now time.Time) Run {
	if b.State == StateBlocked && b.Question != nil && b.Question.Via == "crew" &&
		live && now.Sub(time.UnixMilli(b.blockedSince)) < crewDispatcherSilence {
		r := b.Run
		r.State = StateRunning
		r.Question = nil
		return r
	}
	return b.Run
}

// dispatcherLive reports whether the crew's dispatcher is still there to
// answer, over this tick's joins on every bus (scan's joinedOn): the same
// verdict that nests a worker under its dispatcher, so routing and nesting
// never disagree about who is alive, wherever the worker reports.
func dispatcherLive(joined map[string]string, crewID string) bool {
	return joined[crewID] != ""
}

// sessionEpoch extracts <epoch> from a worker id "worker:<branch>#s<epoch>-<pid>".
// dispatch mints this id before it creates the window and launches the
// engine; <pid> is dispatch's own short-lived pid and carries nothing.
func sessionEpoch(from string) int64 {
	if !strings.HasPrefix(from, "worker:") {
		return 0
	}
	i := strings.LastIndex(from, "#")
	if i < 0 {
		return 0
	}
	seg := from[i+1:]
	if !strings.HasPrefix(seg, "s") {
		return 0
	}
	digits := seg[1:]
	if j := strings.IndexByte(digits, '-'); j >= 0 {
		digits = digits[:j]
	}
	n, err := strconv.ParseInt(digits, 10, 64)
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

// branchFromWorker turns "worker:fix/412#s1788-42" into "fix/412".
func branchFromWorker(from string) string {
	if !strings.HasPrefix(from, "worker:") {
		return ""
	}
	rest := strings.TrimPrefix(from, "worker:")
	if i := strings.LastIndex(rest, "#"); i >= 0 {
		rest = rest[:i]
	}
	return rest
}

// prNumberFromURL extracts N from ".../pull/N", or "" when the URL has another
// shape — an empty number is better than a wrong one.
func prNumberFromURL(u string) string {
	i := strings.LastIndex(u, "/pull/")
	if i < 0 {
		return ""
	}
	n := strings.TrimPrefix(u[i:], "/pull/")
	if n == "" || strings.Trim(n, "0123456789") != "" {
		return ""
	}
	return n
}
