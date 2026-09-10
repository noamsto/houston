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
	"strings"
	"time"
)

// CrewSource reads dispatcher's git-backed bus. It contributes the one thing
// no other source can express: a worker blocked on a question addressed to you.
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
}

func NewCrewSource(c lister, every time.Duration) *CrewSource {
	if every <= 0 {
		every = 3 * time.Second
	}
	return &CrewSource{client: c, every: every, crewDirs: map[string]string{}}
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

	out := map[string]Run{}
	for bus, branches := range s.scanRoots(rootList) {
		for branch, r := range branches {
			r.Worktree = worktreeFor(bus, branch, wins, s.crewDir)
			paneID, candidates := resolvePane(bus, branch, wins, panes, s.crewDir)
			key := paneID
			if candidates != 1 {
				key = "crew/" + bus + "/" + branch
				slog.Debug("crew source: no pane join", "bus", bus, "branch", branch, "candidates", candidates)
			}
			out[key] = r
		}
	}
	return out, true
}

// scanRoots reads every root's crew bus and folds it into the latest state per
// branch, grouped by bus directory. Several worktrees of one repo resolve to
// the same common git dir and therefore to one bus, which is the correct
// dedupe; two repos each holding a `main` record stay apart. Split out from
// scan() so the root list can be injected in tests without going through tmux.
func (s *CrewSource) scanRoots(roots []string) map[string]map[string]Run {
	merged := map[string]map[string]Run{}
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
			f, err := os.Open(path)
			if err != nil {
				slog.Debug("crew log", "path", path, "error", err)
				continue
			}
			for branch, r := range deltasFromCrewLog(f) {
				if merged[dir] == nil {
					merged[dir] = map[string]Run{}
				}
				merged[dir][branch] = r
			}
			_ = f.Close()
		}
	}
	return merged
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
}

// crewBlockedNoDetail is the question synthesised for a `crew status <from>
// blocked` carrying no detail, since that detail is optional. The composed-run
// invariant (a non-nil Question implies StateBlocked) can only protect a
// blocked status that has a Question, so without this the higher-precedence
// hooks layer overwrites State and the block goes unseen. Worded as houston's
// own description: there is no worker text here to quote.
const crewBlockedNoDetail = "Blocked, no detail given."

// deltasFromCrewLog folds one bus log into the latest state per branch. The bus
// is append-only, so later records win.
func deltasFromCrewLog(rd io.Reader) map[string]Run {
	out := map[string]Run{}
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
		// Engine only appears on the dispatch record, but every status record
		// for the branch shares this map entry, so it sticks once set. Without
		// it a crew run carries no agent and the listing predicate would drop
		// it — this source exists specifically to surface blocked questions.
		if rec.Engine != "" {
			r.Agent = rec.Engine
		}
		if rec.Kind == "msg" && strings.HasPrefix(rec.From, "dispatcher:") && rec.TS > dispatcherReplyTS[branch] {
			dispatcherReplyTS[branch] = rec.TS
		}
		if rec.Kind == "status" {
			var body crewStatusBody
			if err := json.Unmarshal(rec.Body, &body); err == nil && body.State != "" {
				r.State = FromCrewState(body.State)
				r.UpdatedAt = rec.TS / 1000
				lastStatusTS[branch] = rec.TS
				r.Question = nil
				if r.State == StateBlocked {
					r.Question = &Question{Text: crewBlockedNoDetail, Via: "crew"}
					if body.Detail != "" {
						r.Question.Text = body.Detail
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
			out[branch] = r
		}
	}

	return out
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
