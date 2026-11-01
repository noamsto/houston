package answer

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode"
)

// maxOptions keeps the Other row's digit (options+1) a single key.
const maxOptions = 8

// Question is one question of an AskUserQuestion call.
type Question struct {
	Text        string
	Header      string
	MultiSelect bool
	Options     []Option
}

// Option is one choice of a Question. Its row shows the label with the
// description under it, unless the question renders in the preview layout.
type Option struct {
	Label       string
	Description string
	Preview     string
}

// PreviewLayout reports whether q renders in Claude's preview layout: label
// rows beside the focused option's preview, no descriptions and no Other row.
// Only a single-select question with a non-blank preview does; a multi-select
// one ignores previews.
func (q Question) PreviewLayout() bool {
	return !q.MultiSelect && slices.ContainsFunc(q.Options, func(o Option) bool { return strings.TrimSpace(o.Preview) != "" })
}

// ParseQuestions decodes the input of an AskUserQuestion tool call.
func ParseQuestions(raw json.RawMessage) ([]Question, error) {
	var in struct {
		Questions []struct {
			Question    string `json:"question"`
			Header      string `json:"header"`
			MultiSelect bool   `json:"multiSelect"`
			Options     []struct {
				Label       string `json:"label"`
				Description string `json:"description"`
				Preview     string `json:"preview"`
			} `json:"options"`
		} `json:"questions"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, fmt.Errorf("decode AskUserQuestion input: %w", err)
	}
	if len(in.Questions) == 0 {
		return nil, errors.New("AskUserQuestion input has no questions")
	}
	qs := make([]Question, 0, len(in.Questions))
	for _, q := range in.Questions {
		opts := make([]Option, 0, len(q.Options))
		for _, o := range q.Options {
			opts = append(opts, Option(o))
		}
		qs = append(qs, Question{Text: q.Question, Header: q.Header, MultiSelect: q.MultiSelect, Options: opts})
	}
	return qs, nil
}

// Tabbed reports whether Claude shows qs as a tabbed dialog (a tab bar, a
// tab per question and a review) rather than a plain one-question dialog.
func Tabbed(qs []Question) bool {
	return len(qs) > 1 || slices.ContainsFunc(qs, func(q Question) bool { return q.MultiSelect })
}

// Repeats reports whether two questions render alike: equal normalized text
// and option rows. The pane identifies a tab only by what it shows, so such
// tabs cannot be told apart.
func Repeats(qs []Question) bool {
	for i := range qs {
		for _, p := range qs[:i] {
			if Normalize(p.Text) == Normalize(qs[i].Text) && slices.Equal(optionRows(p), optionRows(qs[i])) {
				return true
			}
		}
	}
	return false
}

// optionRows is each option's row as its tab spells it, normalized: the
// label followed by its description, or the label alone in the preview layout.
func optionRows(q Question) []string {
	rows := make([]string, len(q.Options))
	for k, o := range q.Options {
		rows[k] = Normalize(o.Label)
		if !q.PreviewLayout() {
			rows[k] += Normalize(o.Description)
		}
	}
	return rows
}

// Answer answers one question: 0-based option indexes and/or Other text.
// A single-select answer is one option or text; a multi-select one is any
// options plus optional text.
type Answer struct {
	Options []int
	Text    string
}

// Key is one send to the pane. Exactly one field is set: Special is a tmux key
// name ("1".."9", "Down", "Enter"); Text is typed literally.
type Key struct {
	Special string
	Text    string
}

// Check reports whether a capture (tmux capture-pane -p -e, ANSI included)
// shows the expected dialog state. width is the pane's width in cells; the
// capture is the pane at that width.
type Check func(capture string, width int) bool

// Step is one round of an answer: the pane must pass Expect before Keys are
// sent.
type Step struct {
	Expect Check
	Keys   []Key
}

var (
	keyDown  = Key{Special: "Down"}
	keyEnter = Key{Special: "Enter"}
	keyUp    = Key{Special: "Up"}
	keyLeft  = Key{Special: "Left"}
	keyClear = Key{Special: "C-u"}
)

func digit(k int) Key { return Key{Special: strconv.Itoa(k)} }

// plan accumulates steps: each Expect, then the keys sent once it passes.
type plan struct {
	qs    []Question
	steps []Step
}

func (p *plan) expect(c Check) { p.steps = append(p.steps, Step{Expect: c}) }

func (p *plan) send(keys ...Key) {
	last := &p.steps[len(p.steps)-1]
	last.Keys = append(last.Keys, keys...)
}

// startPlan opens every plan: the pane must show exactly v, the view the plan
// is built from. Up then returns the cursor to the first row (Submit on the
// review) from anywhere (testdata/2.1.310/v3-q2-up4), checked with the view
// otherwise unchanged, and startPlan returns that view.
func startPlan(qs []Question, v View) (*plan, View) {
	p := &plan{qs: qs}
	p.expect(viewIs(qs, v))
	ups := v.Cursor - 1
	if v.Step < len(qs) && v.Cursor == 0 {
		ups = len(qs[v.Step].Options) + 1
	}
	v.Cursor = 1
	if ups > 0 {
		p.send(slices.Repeat([]Key{keyUp}, ups)...)
		p.expect(viewIs(qs, v))
	}
	return p, v
}

// tabIs checks the pane shows want after keys were sent on its tab: viewIs,
// except that a multi-select tab's own tab-bar box may tick or clear as its
// boxes and Other text change.
func (p *plan) tabIs(want View) Check {
	if !p.qs[want.Step].MultiSelect {
		return viewIs(p.qs, want)
	}
	return func(capture string, width int) bool {
		got, ok := ReadView(p.qs, capture, width)
		if !ok || got.Step != want.Step || len(got.boxes) != len(want.boxes) {
			return false
		}
		got.boxes = slices.Clone(got.boxes)
		got.boxes[want.Step] = want.boxes[want.Step]
		return sameView(got, want)
	}
}

// landsOnTab checks the pane shows tab j with the cursor on row 1 and the
// bar boxes of ticked ticked, whatever the tab holds.
func landsOnTab(qs []Question, j int, ticked ...int) Check {
	return func(capture string, width int) bool {
		got, ok := ReadView(qs, capture, width)
		if !ok || got.Step != j || got.Cursor != 1 {
			return false
		}
		for _, i := range ticked {
			if !got.boxes[i] {
				return false
			}
		}
		return true
	}
}

// landsOnReview checks the pane shows the review of qs, whatever it holds.
func landsOnReview(qs []Question) Check {
	return func(capture string, width int) bool {
		got, ok := ReadView(qs, capture, width)
		return ok && got.Step == len(qs)
	}
}

// PlanStep returns the steps that answer tab index of qs with a, starting
// from v, the view the pane showed. The executor runs them in order: wait
// until steps[i].Expect passes on a fresh capture (for i == 0, the first
// capture must pass, else nothing is typed), then send steps[i].Keys. Any
// Expect that does not pass means the pane moved on: stop, type nothing more.
// Only this tab's keys are sent. A tabbed dialog's plan ends with a keyless
// landing step (the next tab on row 1 with this tab's box ticked, or the
// review); a plain dialog's last keys submit, with nothing checked after.
func PlanStep(qs []Question, index int, a Answer, v View) ([]Step, error) {
	if Repeats(qs) {
		return nil, errors.New("questions repeat")
	}
	if index < 0 || index >= len(qs) {
		return nil, fmt.Errorf("tab %d out of range", index)
	}
	if v.Step != index {
		return nil, fmt.Errorf("pane shows tab %d, not %d", v.Step, index)
	}
	q := qs[index]
	if err := validate(q, a); err != nil {
		return nil, err
	}
	p, v := startPlan(qs, v)
	switch {
	case q.PreviewLayout():
		// A digit only moves the cursor here; Enter commits.
		k := a.Options[0] + 1
		p.send(digit(k))
		want := v
		want.Cursor = k
		p.expect(p.tabIs(want))
		p.send(keyEnter)
	case !q.MultiSelect && len(a.Options) == 1:
		p.send(digit(a.Options[0] + 1))
	case !q.MultiSelect:
		p.singleOther(v, a.Text)
	default:
		p.multi(v, a)
	}
	switch {
	case !Tabbed(qs):
	case index == len(qs)-1:
		p.expect(landsOnReview(qs))
	default:
		p.expect(landsOnTab(qs, index+1, index))
	}
	return p.steps, nil
}

// singleOther types text into a single-select tab's Other field from row 1
// of v. It reaches the field with Down, never the Other digit, which commits
// any text the field still holds, and clears that text first. A
// committed Other keeps its commit mark through clearing and typing.
func (p *plan) singleOther(v View, text string) {
	n := len(p.qs[v.Step].Options)
	p.send(slices.Repeat([]Key{keyDown}, n)...)
	want := v
	want.Cursor = n + 1
	p.expect(p.tabIs(want))
	p.clearOther(v, &want)
	p.send(Key{Text: text})
	want.other = Normalize(text)
	if want.committed {
		want.Text = text
	}
	p.expect(p.tabIs(want))
	p.send(keyEnter)
}

// clearOther empties the focused Other field when v shows it holding text.
// C-u clears one visual line, the focused field can wrap to one line more
// than v shows, and extra presses on an empty field change nothing.
func (p *plan) clearOther(v View, want *View) {
	if v.other == "" {
		return
	}
	p.send(slices.Repeat([]Key{keyClear}, 2*v.otherLines+1)...)
	want.other, want.Text = "", ""
	if p.qs[v.Step].MultiSelect {
		want.committed = false
	}
	p.expect(p.tabIs(*want))
}

// multi sets a multi-select tab to a from row 1 of v: one digit per box to
// flip (digits leave the cursor in place), then the Other field, then Enter
// on the Next/Submit row.
func (p *plan) multi(v View, a Answer) {
	n := len(p.qs[v.Step].Options)
	wanted := sortedOptions(a)
	var toggles []Key
	for o := range n {
		if slices.Contains(wanted, o) != slices.Contains(v.Options, o) {
			toggles = append(toggles, digit(o+1))
		}
	}
	want := v
	if len(toggles) > 0 {
		p.send(toggles...)
		want.Options = wanted
		p.expect(p.tabIs(want))
	}
	if v.other == "" && a.Text == "" {
		p.send(slices.Repeat([]Key{keyDown}, n+1)...)
	} else {
		p.send(slices.Repeat([]Key{keyDown}, n)...)
		want.Cursor = n + 1
		p.expect(p.tabIs(want))
		p.clearOther(v, &want)
		if a.Text != "" {
			p.send(Key{Text: a.Text})
			want.other, want.Text, want.committed = Normalize(a.Text), a.Text, true
			p.expect(p.tabIs(want))
		}
		p.send(keyDown)
	}
	want.Cursor = 0
	want.other, want.Text, want.committed = Normalize(a.Text), a.Text, a.Text != ""
	p.expect(p.tabIs(want))
	p.send(keyEnter)
}

// PlanBack returns the steps that move a tabbed dialog from tab index (or
// the review, index len(qs)) back one tab, starting from v: Left, then a
// keyless landing step on tab index-1, row 1.
func PlanBack(qs []Question, index int, v View) ([]Step, error) {
	if !Tabbed(qs) {
		return nil, errors.New("a plain dialog has no tabs")
	}
	if Repeats(qs) {
		return nil, errors.New("questions repeat")
	}
	if index < 1 || index > len(qs) {
		return nil, fmt.Errorf("tab %d has no tab before it", index)
	}
	if v.Step != index {
		return nil, fmt.Errorf("pane shows tab %d, not %d", v.Step, index)
	}
	p, _ := startPlan(qs, v)
	p.send(keyLeft)
	p.expect(landsOnTab(qs, index-1))
	return p.steps, nil
}

// PlanSubmit returns the steps that submit a tabbed dialog from its review,
// starting from v: the review must show review (normalized), then "1".
// Nothing is checked after.
func PlanSubmit(qs []Question, review []string, v View) ([]Step, error) {
	if !Tabbed(qs) {
		return nil, errors.New("a plain dialog has no review")
	}
	if Repeats(qs) {
		return nil, errors.New("questions repeat")
	}
	if len(review) != len(qs) {
		return nil, fmt.Errorf("%d review answers for %d questions", len(review), len(qs))
	}
	if v.Step != len(qs) {
		return nil, fmt.Errorf("pane shows tab %d, not the review", v.Step)
	}
	v.Review = slices.Clone(review)
	p, _ := startPlan(qs, v)
	p.send(digit(1))
	return p.steps, nil
}

func validate(q Question, a Answer) error {
	if Normalize(q.Text) == "" {
		return errors.New("empty question text")
	}
	if len(q.Options) == 0 || len(q.Options) > maxOptions {
		return fmt.Errorf("%d options, want 1..%d", len(q.Options), maxOptions)
	}
	for _, o := range q.Options {
		if Normalize(o.Label) == "" {
			return errors.New("empty option label")
		}
	}
	for i, o := range a.Options {
		if o < 0 || o >= len(q.Options) {
			return fmt.Errorf("option %d out of range", o)
		}
		if slices.Contains(a.Options[:i], o) {
			return fmt.Errorf("option %d chosen twice", o)
		}
	}
	if a.Text != "" && (Normalize(a.Text) == "" || strings.ContainsFunc(a.Text, unicode.IsControl)) {
		return errors.New("text must be non-blank with no control characters")
	}
	if IsOtherPlaceholder(a.Text) {
		return errors.New("text may not read as the Other placeholder")
	}
	if strings.Contains(a.Text, commitMark) {
		return errors.New("text may not contain the commit mark ✔")
	}
	if q.MultiSelect {
		if len(a.Options) == 0 && a.Text == "" {
			return errors.New("empty answer")
		}
		return nil
	}
	if (len(a.Options) == 1) == (a.Text != "") || len(a.Options) > 1 {
		return errors.New("single-select needs exactly one option or text")
	}
	if a.Text != "" && q.PreviewLayout() {
		return errors.New("a question with previews has no Other row")
	}
	return nil
}
