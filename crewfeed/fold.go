package crewfeed

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// maxBusText caps the bus-authored part of an entry's text, in runes.
const maxBusText = 200

const followUpsPrefix = "follow-ups (untracked):"

// maxBranch is git's ref name limit in bytes.
const maxBranch = 255

var (
	prURLRe        = regexp.MustCompile(`^https://github\.com/[A-Za-z0-9-]+/[A-Za-z0-9._-]+/pull/[0-9]+$`)
	watchdogNameRe = regexp.MustCompile(`^[a-z][a-z-]{0,15}$`)
)

// record is one events.jsonl line. Body is a JSON object on a status record
// and a JSON string on a msg record, so it is decoded only where its shape is
// known; PR is a URL string on a reap record.
type record struct {
	TS      int64           `json:"ts"`
	CrewID  string          `json:"crew_id"`
	From    string          `json:"from"`
	To      string          `json:"to"`
	Kind    string          `json:"kind"`
	Branch  string          `json:"branch"`
	Title   string          `json:"title"`
	Tier    string          `json:"tier"`
	Engine  string          `json:"engine"`
	Model   string          `json:"model"`
	Name    string          `json:"name"`
	State   string          `json:"state"`
	PRState string          `json:"pr_state"`
	PR      json.RawMessage `json:"pr"`
	Body    json.RawMessage `json:"body"`
}

type statusBody struct {
	State  string `json:"state"`
	Detail string `json:"detail"`
	PRURL  string `json:"pr_url"`
	Source string `json:"source"`
}

type prWatchBody struct {
	PR      int      `json:"pr"`
	URL     string   `json:"url"`
	Changed []string `json:"changed"`
	State   struct {
		State  string `json:"state"`
		Checks string `json:"checks"`
	} `json:"state"`
	Was struct {
		Checks string `json:"checks"`
	} `json:"was"`
}

// statusClass is what Fold remembers about one source class (worker or
// watchdog) of one branch's status rows.
type statusClass struct {
	state string
	seen  bool
}

type branchState struct {
	worker, watchdog statusClass
	// blockedDetail is the last non-watchdog blocked detail; replied is set by
	// a dispatcher reply and cleared by the next non-watchdog blocked, so a
	// re-asked question shows even when its text did not change.
	blockedDetail string
	blockedSeen   bool
	replied       bool
}

// Fold turns bus lines into entries. It is purely line-driven: the entries it
// yields depend only on the sequence of lines, never on how a file was read in
// chunks. The zero value is ready to use; it is not safe for concurrent use.
type Fold struct {
	branchCrew map[string]string
	branches   map[string]*branchState
}

// Line folds one complete bus line read at byte offset off. ok is false when
// the line yields no entry (unparseable, excluded, unattributable or a status
// re-stamp), but the line still updates the fold's state where the rules say
// so. e.ID is left empty for the store to fill from the epoch and off.
func (f *Fold) Line(line []byte, off int64) (crew string, e Entry, ok bool) {
	var r record
	if json.Unmarshal(line, &r) != nil {
		return "", Entry{}, false
	}
	e.TS = r.TS

	switch r.Kind {
	case "dispatch":
		e, ok = f.dispatch(r, e)
	case "resume":
		e, ok = f.resume(r, e)
	case "status":
		e, ok = f.status(r, e)
	case "msg":
		e, ok = f.msg(r, e)
	case "reap":
		e, ok = reap(r, e)
	case "reclaim":
		e, ok = reclaim(r, e, "Reclaimed windows")
	case "release":
		e, ok = reclaim(r, e, "Released session")
	}
	if !ok {
		return "", Entry{}, false
	}
	crew = r.CrewID
	if crew == "" {
		crew = f.branchCrew[e.Branch]
	}
	if crew == "" {
		return "", Entry{}, false
	}
	return crew, e, true
}

func (f *Fold) learn(r record) {
	if r.CrewID == "" || r.Branch == "" {
		return
	}
	if f.branchCrew == nil {
		f.branchCrew = map[string]string{}
	}
	f.branchCrew[r.Branch] = r.CrewID
}

func (f *Fold) branch(name string) *branchState {
	if f.branches == nil {
		f.branches = map[string]*branchState{}
	}
	b := f.branches[name]
	if b == nil {
		b = &branchState{}
		f.branches[name] = b
	}
	return b
}

func (f *Fold) dispatch(r record, e Entry) (Entry, bool) {
	if !validBranch(r.Branch) {
		return e, false
	}
	f.learn(r)
	text := "Dispatched"
	if title := clean(r.Title, maxBusText); title != "" {
		text += ` "` + title + `"`
	}
	e.Kind = KindDispatch
	e.Text = text + joinParts(r.Tier, r.Engine, r.Model)
	e.Branch = r.Branch
	e.Codename = clean(r.Name, maxBusText)
	return e, true
}

func (f *Fold) resume(r record, e Entry) (Entry, bool) {
	if !validBranch(r.Branch) {
		return e, false
	}
	f.learn(r)
	e.Kind = KindResume
	e.Text = "Resumed" + joinParts(r.Engine, r.Model)
	e.Branch = r.Branch
	return e, true
}

// joinParts renders each non-empty part as " · part".
func joinParts(parts ...string) string {
	var b strings.Builder
	for _, p := range parts {
		if p = clean(p, maxBusText); p != "" {
			b.WriteString(" · " + p)
		}
	}
	return b.String()
}

func (f *Fold) status(r record, e Entry) (Entry, bool) {
	branch := branchFromWorker(r.From)
	if branch == "" || !validBranch(branch) {
		return e, false
	}
	var body statusBody
	if json.Unmarshal(r.Body, &body) != nil {
		return e, false
	}
	state := clean(body.State, maxBusText)
	if state == "" {
		return e, false
	}
	b := f.branch(branch)
	watchdog := body.Source == "watchdog"

	var emit bool
	if watchdog {
		emit = !b.watchdog.seen || b.watchdog.state != state
		b.watchdog = statusClass{state: state, seen: true}
		e.Text = watchdogText(body.Detail)
	} else {
		detail := clean(body.Detail, 0)
		emit = !b.worker.seen || b.worker.state != state
		if state == "blocked" {
			emit = emit || !b.blockedSeen || b.blockedDetail != detail || b.replied
			b.blockedDetail, b.blockedSeen, b.replied = detail, true, false
		}
		b.worker = statusClass{state: state, seen: true}
		// The entry's State already shows the state, so the text is the detail.
		e.Text = state
		if d := clean(body.Detail, maxBusText); d != "" {
			e.Text = d
		}
	}
	if !emit {
		return e, false
	}
	e.Kind = KindStatus
	e.Branch = branch
	e.State = state
	e.PR = parsePR(body.PRURL)
	return e, true
}

// watchdogText words a watchdog status as houston's own: the script's detail is
// dispatcher-facing, so only a well-formed prefix before ':' survives.
func watchdogText(detail string) string {
	prefix, _, _ := strings.Cut(detail, ":")
	if watchdogNameRe.MatchString(prefix) {
		return "watchdog: " + prefix
	}
	return "watchdog"
}

func (f *Fold) msg(r record, e Entry) (Entry, bool) {
	var text string
	if json.Unmarshal(r.Body, &text) != nil {
		return e, false
	}
	from, to := r.From, r.To
	switch {
	case strings.HasPrefix(from, "worker:") && strings.HasPrefix(to, "dispatcher:"):
		e.Branch = branchFromWorker(from)
		if !validBranch(e.Branch) {
			return e, false
		}
		e.Kind = KindQuestion
		if rest, found := strings.CutPrefix(strings.TrimLeftFunc(text, unicode.IsSpace), followUpsPrefix); found {
			e.Kind = KindFollowUps
			text = rest
		}
		body := clean(text, maxBusText)
		if body == "" {
			return e, false
		}
		e.Text = body
		if e.Kind == KindFollowUps {
			e.Text = "Follow-ups: " + body
		}
		return e, true
	case strings.HasPrefix(from, "dispatcher:") && strings.HasPrefix(to, "worker:"):
		e.Branch = branchFromWorker(to)
		if !validBranch(e.Branch) {
			return e, false
		}
		f.branch(e.Branch).replied = true
		e.Kind = KindReply
		e.Text = clean(text, maxBusText)
		return e, e.Text != ""
	case strings.HasPrefix(from, "pr-watch:") && strings.HasPrefix(to, "dispatcher:"):
		return prWatch(text, e)
	}
	return e, false
}

// prWatch words a pr-watch notification; a body that does not parse, or a
// change that is neither a checks nor a merged/closed state change, is no entry.
func prWatch(text string, e Entry) (Entry, bool) {
	var b prWatchBody
	if json.Unmarshal([]byte(text), &b) != nil {
		return e, false
	}
	e.PR = parsePR(b.URL)
	n := b.PR
	if n <= 0 && e.PR != nil {
		n = e.PR.Number
	}
	if n <= 0 {
		return e, false
	}
	label := "PR #" + strconv.Itoa(n)

	var changedState, changedChecks bool
	for _, c := range b.Changed {
		changedState = changedState || c == "state"
		changedChecks = changedChecks || c == "checks"
	}
	switch {
	case changedState && b.State.State == "MERGED":
		e.Text = label + " merged"
	case changedState && b.State.State == "CLOSED":
		e.Text = label + " closed"
	case changedChecks:
		now := clean(b.State.Checks, maxBusText)
		if now == "" {
			return e, false
		}
		e.Text = label + " checks"
		if was := clean(b.Was.Checks, maxBusText); was != "" {
			e.Text += " " + was + " →"
		}
		e.Text += " " + now
	default:
		return e, false
	}
	e.Kind = KindPR
	return e, true
}

func reap(r record, e Entry) (Entry, bool) {
	if !validBranch(r.Branch) {
		return e, false
	}
	var url string
	if json.Unmarshal(r.PR, &url) != nil {
		url = ""
	}
	e.PR = parsePR(url)
	var detail []string
	if e.PR != nil {
		detail = append(detail, "PR #"+strconv.Itoa(e.PR.Number))
	}
	if s := clean(r.PRState, maxBusText); s != "" {
		detail = append(detail, s)
	}
	e.Kind = KindReap
	e.Text = "Reaped" + parenthesized(detail)
	e.Branch = r.Branch
	return e, true
}

func reclaim(r record, e Entry, label string) (Entry, bool) {
	if !validBranch(r.Branch) {
		return e, false
	}
	var detail []string
	if s := clean(r.State, maxBusText); s != "" {
		detail = append(detail, s)
	}
	e.Kind = KindReap
	e.Text = label + parenthesized(detail)
	e.Branch = r.Branch
	return e, true
}

func parenthesized(parts []string) string {
	if len(parts) == 0 {
		return ""
	}
	return " (" + strings.Join(parts, " ") + ")"
}

// parsePR returns the link for a GitHub pull URL, nil for anything else.
func parsePR(url string) *PR {
	if !prURLRe.MatchString(url) {
		return nil
	}
	n, err := strconv.Atoi(url[strings.LastIndexByte(url, '/')+1:])
	if err != nil {
		return nil
	}
	return &PR{Number: n, URL: url}
}

// branchFromWorker turns "worker:fix/412#s1788-42" into "fix/412".
func branchFromWorker(from string) string {
	rest, ok := strings.CutPrefix(from, "worker:")
	if !ok {
		return ""
	}
	if i := strings.LastIndex(rest, "#"); i >= 0 {
		rest = rest[:i]
	}
	return rest
}

// validBranch reports whether b could be a git ref: at most maxBranch bytes
// with no control rune. A branch is kept unmodified, so client-side matching
// on it stays exact; one that fails this drops its record instead.
func validBranch(b string) bool {
	return len(b) <= maxBranch && !strings.ContainsFunc(b, unicode.IsControl)
}

// clean drops control runes (C0 and C1), collapses every whitespace run to one
// space and trims. A positive limit keeps at most that many runes, marking a
// cut with "…"; limit 0 means no cap.
func clean(s string, limit int) string {
	var b strings.Builder
	runes := 0
	pendingSpace := false
	for _, r := range s {
		if isSpace(r) {
			pendingSpace = runes > 0
			continue
		}
		if unicode.IsControl(r) {
			continue
		}
		need := 1
		if pendingSpace {
			need = 2
		}
		if limit > 0 && runes+need > limit {
			b.WriteString("…")
			return b.String()
		}
		if pendingSpace {
			b.WriteByte(' ')
			pendingSpace = false
		}
		b.WriteRune(r)
		runes += need
	}
	return b.String()
}

// isSpace is unicode.IsSpace minus the C1 control NEL, which is dropped like
// the other C1 runes.
func isSpace(r rune) bool {
	return unicode.IsSpace(r) && r != 0x85
}
