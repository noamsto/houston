package runs

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/noamsto/houston/tmux"
)

// CrewSource reads dispatcher's git-backed bus. It contributes what no other
// source can express: a worker's question, addressed to its dispatcher — it
// surfaces to you only when the dispatcher is gone or has been silent for
// crewDispatcherSilence — and the watchdog's verdict that a worker is stuck.
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

	// sessions is the hooks layer's live session-per-pane view, used to keep a
	// terminal record from joining a different session inside the same engine
	// process (/clear, /resume). nil disables the check — tests, and a build
	// with no hub.
	sessions hookSessions

	// logs caches each bus log's parse by path, reused while size and mtime
	// hold. Same single-goroutine rule as crewDirs. The cached Runs are shared
	// across ticks, so nothing may write through their pointer fields.
	logs map[string]crewLog
}

type crewLog struct {
	size int64
	mod  time.Time
	runs map[string]crewBranch
}

func NewCrewSource(c lister, sessions hookSessions, every time.Duration) *CrewSource {
	if every <= 0 {
		every = 3 * time.Second
	}
	return &CrewSource{client: c, sessions: sessions, every: every, crewDirs: map[string]string{}, logs: map[string]crewLog{}, procStart: foregroundStart}
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

// crewRunFinished reports whether r's bus status has been terminal for
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

// tickCrewDeltas computes this cycle's deltas from the runs scan() currently
// sees, already keyed by their final registry keys, evicting anything whose
// bus status has been terminal for longer than crewEvictionGrace. seen is the
// previous tick's emitted key set; it returns the deltas to send and the new
// seen set.
//
// Every update is emitted before every Gone. A branch's key flips as its pane
// opens and closes, and that order is what stops a subscriber briefly holding
// neither key.
func tickCrewDeltas(current map[string]Run, seen map[string]bool, now time.Time) ([]Delta, map[string]bool) {
	var deltas []Delta
	newSeen := map[string]bool{}
	for key, r := range current {
		if crewRunFinished(r, now) {
			continue
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

// scan returns this tick's runs under their final registry keys: the pane id
// where the branch joins exactly one agent pane, otherwise "crew/<bus>/<branch>".
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

	roots := map[string]bool{}
	for _, w := range wins {
		if w.GitRoot != "" {
			roots[w.GitRoot] = true
		}
	}

	rootList := make([]string, 0, len(roots))
	for repo := range roots {
		rootList = append(rootList, repo)
	}

	// The pane → hook session map the terminal join consults, built from the
	// same listing and the hooks layer's own trust rules (paneSetFrom,
	// paneHookSessions).
	var paneSessions map[string]string
	if s.sessions != nil {
		paneSessions = paneHookSessions(s.sessions.Snapshot(), paneSetFrom(panes, time.Now()))
	}

	now := time.Now()
	out := map[string]Run{}
	for bus, branches := range s.scanRoots(rootList) {
		project := ProjectFromCommonDir(filepath.Dir(bus))
		for branch, r := range branches {
			routed := routeCrewQuestion(r, r.State == StateBlocked && dispatcherLive(bus, r.Crew.Name, wins, panes), now)
			routed.Project = project
			routed.Role = RoleWorker
			routed.Worktree = worktreeFor(bus, branch, wins, s.crewDir)
			paneID, candidates := resolvePane(bus, branch, r.State, r.UpdatedAt, r.session, r.CrewSession, paneSessions, wins, panes, s.crewDir, s.procStart)
			key := paneID
			if candidates != 1 {
				key = "crew/" + bus + "/" + branch
				slog.Debug("crew source: no pane join", "bus", bus, "branch", branch, "candidates", candidates)
			}
			out[key] = routed
		}
	}
	return out, true
}

// scanRoots reads every root's crew bus and folds it into the latest state per
// branch, grouped by bus directory. Several worktrees of one repo resolve to
// the same common git dir and therefore to one bus, which is the correct
// dedupe; two repos each holding a `main` record stay apart. Split out from
// scan() so the root list can be injected in tests without going through tmux.
func (s *CrewSource) scanRoots(roots []string) map[string]map[string]crewBranch {
	merged := map[string]map[string]crewBranch{}
	live := map[string]crewLog{}
	for _, repo := range roots {
		dir := s.crewDir(repo)
		if dir == "" {
			continue
		}
		logs, err := filepath.Glob(filepath.Join(dir, "*.jsonl"))
		if err != nil {
			continue
		}
		for _, path := range logs {
			cl, ok := s.readCrewLog(path)
			if !ok {
				continue
			}
			live[path] = cl
			for branch, r := range cl.runs {
				if merged[dir] == nil {
					merged[dir] = map[string]crewBranch{}
				}
				merged[dir][branch] = r
			}
		}
	}
	s.logs = live
	return merged
}

func (s *CrewSource) readCrewLog(path string) (crewLog, bool) {
	fi, err := os.Stat(path)
	if err != nil {
		slog.Debug("crew log", "path", path, "error", err)
		return crewLog{}, false
	}
	if cl, ok := s.logs[path]; ok && cl.size == fi.Size() && cl.mod.Equal(fi.ModTime()) {
		return cl, true
	}
	f, err := os.Open(path) //nolint:gosec // path comes from houston's own state/transcript dirs, not from a request
	if err != nil {
		slog.Debug("crew log", "path", path, "error", err)
		return crewLog{}, false
	}
	defer func() { _ = f.Close() }()
	return crewLog{size: fi.Size(), mod: fi.ModTime(), runs: deltasFromCrewLog(f)}, true
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

// crewStatusBody is crewRecord.Body decoded for a "status" record.
type crewStatusBody struct {
	State  string `json:"state"`
	Detail string `json:"detail"`
	PRURL  string `json:"pr_url"`
	Source string `json:"source"`
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

// crewBranch is deltasFromCrewLog's per-branch fold result. session is the
// s<epoch> of the worker id on the latest status record (0 when unknown) —
// kept off Run because it is crew-join evidence, not API.
type crewBranch struct {
	Run
	session int64
	// blockedSince is the bus ts (ms) of the first worker-blocked status of the
	// current episode: kept across re-stamps, restarted by any other status or
	// a dispatcher reply, 0 when the branch is not asking its dispatcher.
	blockedSince int64
}

// deltasFromCrewLog folds one bus log into the latest state per branch. The bus
// is append-only, so later records win.
func deltasFromCrewLog(rd io.Reader) map[string]crewBranch {
	out := map[string]crewBranch{}
	// lastStatusTS and dispatcherReplyTS are compared by timestamp, not by
	// line order, to decide whether a blocked question has been answered —
	// see the retirement pass below.
	lastStatusTS := map[string]int64{}
	dispatcherReplyTS := map[string]int64{}

	sc := bufio.NewScanner(rd)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for sc.Scan() {
		var rec crewRecord
		if err := json.Unmarshal(sc.Bytes(), &rec); err != nil {
			continue // the bus is written by shell; a torn line is not fatal
		}

		// A dispatcher reply is addressed with from: "dispatcher:…", to:
		// "worker:<branch>#…" — its branch lives in To, not From, since From
		// never resolves through branchFromWorker.
		branch := rec.Branch
		switch {
		case branch != "":
		case rec.Kind == "msg" && strings.HasPrefix(rec.From, "dispatcher:"):
			branch = branchFromWorker(rec.To)
		default:
			branch = branchFromWorker(rec.From)
		}
		if branch == "" {
			continue
		}

		r := out[branch]
		r.Branch = branch
		if r.Crew == nil {
			r.Crew = &CrewRef{}
		}
		if rec.CrewID != "" {
			r.Crew.Name = rec.CrewID
		}
		if rec.Tier != "" {
			r.Crew.Tier = rec.Tier
		}
		if rec.Title != "" {
			r.Crew.Title = rec.Title
		}
		if rec.Model != "" {
			r.Crew.Model = rec.Model
		}
		// Engine only appears on the dispatch record, but every status record
		// for the branch shares this map entry, so it sticks once set. Without
		// it a crew run carries no agent and the listing predicate would drop
		// it — this source exists specifically to surface blocked questions.
		if rec.Engine != "" {
			r.Agent = rec.Engine
		}
		// The newest dispatch/resume row is authoritative: a --fresh resume mints
		// a new session id.
		if rec.Kind == "dispatch" || rec.Kind == "resume" {
			r.CrewSession = rec.EngineSession
			r.Crew.Sessions++
		}
		if rec.Kind == "msg" && strings.HasPrefix(rec.From, "dispatcher:") && rec.TS > dispatcherReplyTS[branch] {
			dispatcherReplyTS[branch] = rec.TS
		}
		if rec.Kind == "status" {
			var body crewStatusBody
			if err := json.Unmarshal(rec.Body, &body); err == nil && body.State != "" {
				r.State = FromCrewState(body.State)
				r.UpdatedAt = rec.TS / 1000
				r.session = sessionEpoch(rec.From)
				lastStatusTS[branch] = rec.TS
				// Latest status wins, including an empty detail: a stale phase
				// must not outlive the status that replaced it. A watchdog
				// note is never a legitimate "current phase" for the card.
				if body.Source == "watchdog" {
					r.Crew.Detail = ""
				} else {
					r.Crew.Detail = body.Detail
				}
				if body.PRURL != "" {
					r.PR = &PRRef{URL: body.PRURL, Number: prNumberFromURL(body.PRURL)}
				}
				r.Question = nil
				r.Attention, r.AttentionNote = AttentionNone, ""
				if r.State != StateBlocked || body.Source == "watchdog" {
					r.blockedSince = 0
				}
				switch {
				case r.State == StateFailed && body.Source == "watchdog":
					r.AttentionNote = crewWatchdogDeadNote
				case r.State != StateBlocked:
				case body.Source != "watchdog":
					r.Question = &Question{Text: crewBlockedNoDetail, Via: "crew"}
					if body.Detail != "" {
						r.Question.Text = body.Detail
					}
					// A re-stamp keeps the episode unless the dispatcher answered
					// since it began.
					if r.blockedSince == 0 || dispatcherReplyTS[branch] > r.blockedSince {
						r.blockedSince = rec.TS
					}
				default:
					// Liveness bookkeeping the dispatcher owns: it never raises a
					// worker question, so the run reads running.
					r.State = StateRunning
					if c, ok := watchdogClassOf(body.Detail); ok {
						if c.needsYou {
							// Actionable at the pane: a human clears the prompt there, so
							// the question routes to the terminal rather than the bus.
							r.State = StateBlocked
							r.Question = &Question{Text: c.note, Via: "pane"}
						} else {
							r.Attention, r.AttentionNote = AttentionStuck, c.note
						}
					}
				}
			}
		}
		out[branch] = r
	}

	// A dispatcher reply newer than the blocking status has answered the
	// question: measured on this repo's own bus, a worker's next status
	// arrived 783s and 348s after the dispatcher's reply, so without this the
	// question — and the attention badge — persists for minutes after it was
	// answered. The layer then has no opinion left to publish: no Question,
	// no State.
	for branch, r := range out {
		if r.State == StateBlocked && dispatcherReplyTS[branch] > lastStatusTS[branch] {
			r.State = ""
			r.Question = nil
			r.blockedSince = 0
			r.Crew.Detail = ""
			out[branch] = r
		}
	}

	return out
}

// routeCrewQuestion decides whether a worker's question reaches the human. It
// is addressed to the dispatcher, so while the dispatcher is live and has been
// asked for less than crewDispatcherSilence the run reads running and the
// question stays only in Crew.Detail. A watchdog prompt (pane) question is the
// human's regardless. It returns a copy: b comes from the per-file parse cache,
// shared across ticks.
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
// answer: <bus>/crews/<crewID>/pane names a listed pane carrying an engine
// status, in a window stamped @crew_name dispatcher (which guards against a
// pane id reused after a tmux restart). A crew with no pane file has no
// dispatcher. crewID comes from a bus record, so it must stay a single path
// element.
func dispatcherLive(bus, crewID string, wins []tmux.WindowOptions, panes []tmux.PaneOptions) bool {
	if crewID == "" || crewID == "." || crewID == ".." || filepath.Base(crewID) != crewID {
		return false
	}
	raw, err := os.ReadFile(filepath.Join(bus, "crews", crewID, "pane")) //nolint:gosec // crewID is a single path element, checked above
	if err != nil {
		return false
	}
	id := strings.TrimSpace(string(raw))
	byTarget := windowsByTarget(wins)
	for _, p := range panes {
		if p.PaneID != id {
			continue
		}
		return (p.ClaudeStatus != "" || p.AgentScreen != "") && byTarget[p.Target].CrewName == dispatcherCrewName
	}
	return false
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
