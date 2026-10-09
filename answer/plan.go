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
	MultiSelect bool
	Options     []string // option labels, in order
}

// ParseQuestions decodes the input of an AskUserQuestion tool call.
func ParseQuestions(raw json.RawMessage) ([]Question, error) {
	var in struct {
		Questions []struct {
			Question    string `json:"question"`
			MultiSelect bool   `json:"multiSelect"`
			Options     []struct {
				Label string `json:"label"`
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
		labels := make([]string, 0, len(q.Options))
		for _, o := range q.Options {
			labels = append(labels, o.Label)
		}
		qs = append(qs, Question{Text: q.Question, MultiSelect: q.MultiSelect, Options: labels})
	}
	return qs, nil
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
// shows the expected dialog state.
type Check func(capture string) bool

// Step is one round of an answer: the pane must pass Expect before Keys are
// sent.
type Step struct {
	Expect Check
	Keys   []Key
}

var (
	keyDown  = Key{Special: "Down"}
	keyEnter = Key{Special: "Enter"}
)

func digit(k int) Key { return Key{Special: strconv.Itoa(k)} }

// Plan returns the steps that answer qs with as, typing what a human would.
// The executor runs them in order: wait until steps[i].Expect passes on a
// fresh capture (for i == 0, the first capture must pass, else nothing is
// typed), send steps[i].Keys, then wait for steps[i+1].Expect. Nothing is
// checked after the last step's keys: they submit the answer (the option digit
// or Enter of a single-question plain dialog, "1" on a tabbed dialog's review).
// Any Expect that does not pass means the pane moved on: stop, type nothing
// more.
func Plan(qs []Question, as []Answer) ([]Step, error) {
	if len(qs) == 0 {
		return nil, errors.New("no questions")
	}
	if len(as) != len(qs) {
		return nil, fmt.Errorf("%d answers for %d questions", len(as), len(qs))
	}
	for i := range qs {
		if err := validate(qs[i], as[i]); err != nil {
			return nil, fmt.Errorf("question %d: %w", i, err)
		}
	}
	tabbed := len(qs) > 1 || slices.ContainsFunc(qs, func(q Question) bool { return q.MultiSelect })

	var steps []Step
	expect := func(c Check) { steps = append(steps, Step{Expect: c}) }
	send := func(keys ...Key) {
		last := &steps[len(steps)-1]
		last.Keys = append(last.Keys, keys...)
	}
	for i, q := range qs {
		spec := newSpec(qs, i, tabbed)
		a := as[i]
		other := Normalize(a.Text)
		n := len(q.Options)
		expect(spec.check(tabState{cursor: 1}))
		switch {
		case !q.MultiSelect && len(a.Options) == 1:
			send(digit(a.Options[0] + 1))
		case !q.MultiSelect:
			send(digit(n + 1))
			expect(spec.check(tabState{cursor: n + 1, touched: true}))
			send(Key{Text: a.Text})
			expect(spec.check(tabState{cursor: n + 1, other: other, touched: true}))
			send(keyEnter)
		default:
			// Digits toggle boxes without moving the cursor, and Down then
			// counts rows from row 1 to reach Other and Next/Submit.
			checked := sortedOptions(a)
			for _, o := range checked {
				send(digit(o + 1))
			}
			if len(checked) > 0 {
				expect(spec.check(tabState{cursor: 1, checked: checked, touched: true}))
			}
			if a.Text == "" {
				send(slices.Repeat([]Key{keyDown}, n+1)...)
			} else {
				send(slices.Repeat([]Key{keyDown}, n)...)
				expect(spec.check(tabState{cursor: n + 1, checked: checked, touched: true}))
				send(Key{Text: a.Text})
				expect(spec.check(tabState{cursor: n + 1, checked: checked, other: other, touched: true}))
				send(keyDown)
			}
			expect(spec.check(tabState{cursor: 0, checked: checked, other: other, touched: true}))
			send(keyEnter)
		}
	}
	if tabbed {
		expect(reviewCheck(qs, as))
		send(digit(1))
	}
	return steps, nil
}

func validate(q Question, a Answer) error {
	if Normalize(q.Text) == "" {
		return errors.New("empty question text")
	}
	if len(q.Options) == 0 || len(q.Options) > maxOptions {
		return fmt.Errorf("%d options, want 1..%d", len(q.Options), maxOptions)
	}
	for _, l := range q.Options {
		if Normalize(l) == "" {
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
	if q.MultiSelect {
		if len(a.Options) == 0 && a.Text == "" {
			return errors.New("empty answer")
		}
		return nil
	}
	if (len(a.Options) == 1) == (a.Text != "") || len(a.Options) > 1 {
		return errors.New("single-select needs exactly one option or text")
	}
	return nil
}
