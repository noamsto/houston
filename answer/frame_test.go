package answer

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

var (
	colorPlain = Question{Text: "Which color do you prefer?", Options: opts("Red", "Warm", "Blue", "Cool", "Green", "Natural")}
	colorTab   = Question{Text: "Which color do you prefer?", Options: opts("Red", "Warm", "Blue", "Cool")}
	toppings   = Question{Text: "Which toppings do you want?", MultiSelect: true, Options: opts("Cheese", "Melty", "Olives", "Salty", "Basil", "Fresh")}
	notes      = Question{
		Text:    "Which of these deliberately long-winded and verbose option labels should we pick for the release notes?",
		Options: opts("A very long first option label that keeps going past the edge", "First", "Short", "Second"),
	}
	race = Question{Text: "Should `go test` run with **race** on?", Options: opts("Yes `-race`", "Slower", "No", "Faster")}
	logo = Question{Text: "Which color should the logo be?", Options: opts("Red", "Warm", "Blue", "Cool", "Green", "Natural")}
)

// opts pairs up labels and descriptions: label, description, label, ….
func opts(pairs ...string) []Option {
	var out []Option
	for i := 0; i < len(pairs); i += 2 {
		out = append(out, Option{Label: pairs[i], Description: pairs[i+1]})
	}
	return out
}

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name+".txt"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func fixtureNames(t *testing.T) []string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join("testdata", "*.txt"))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, p := range paths {
		names = append(names, strings.TrimSuffix(filepath.Base(p), ".txt"))
	}
	sort.Strings(names)
	return names
}

// colorize wraps every non-blank line and the cursor in SGR sequences, and a
// label in an OSC 8 hyperlink, the way capture-pane -e returns them.
func colorize(s string) string {
	s = strings.ReplaceAll(s, "❯", "\x1b[38;5;12m❯\x1b[39m")
	s = strings.ReplaceAll(s, "Blue", "\x1b]8;;https://example.test\x1b\\Blue\x1b]8;;\x1b\\")
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		if strings.TrimSpace(l) != "" {
			lines[i] = "\x1b[1m" + l + "\x1b[0m"
		}
	}
	return strings.Join(lines, "\n")
}

func TestNormalize(t *testing.T) {
	tests := []struct{ in, want string }{
		{"Which color do you prefer?", "Whichcolordoyouprefer?"},
		{"│ Which of these\n│ labels?", "Whichofthese" + "labels?"},
		{" │ ● Which\n │   option", "●Which" + "option"},
		{"● User answered", "●Useranswered"},
		{"a │ b", "a│b"},
		{"\t\n  \n", ""},
	}
	for _, tt := range tests {
		if got := Normalize(tt.in); got != tt.want {
			t.Errorf("Normalize(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestQuestionChecks(t *testing.T) {
	tabbed := []Question{colorTab, toppings}
	mfirst := []Question{toppings, colorTab}
	tests := []struct {
		name    string
		check   Check
		fixture string
		want    bool
	}{
		{"plain initial", newSpec([]Question{colorPlain}, 0, false).check(tabState{cursor: 1}), "plain-q", true},
		{"plain initial, ANSI", newSpec([]Question{colorPlain}, 0, false).check(tabState{cursor: 1}), "plain-q@ansi", true},
		{"scrollback quotes the question above another dialog", newSpec([]Question{colorPlain}, 0, false).check(tabState{cursor: 1}), "scrollback-negative", false},
		{"the other dialog itself", newSpec([]Question{logo}, 0, false).check(tabState{cursor: 1}), "scrollback-negative", true},
		{"stale dialog above a permission prompt", newSpec([]Question{colorPlain}, 0, false).check(tabState{cursor: 1}), "stale-negative", false},
		{"answered, idle", newSpec([]Question{colorPlain}, 0, false).check(tabState{cursor: 1}), "plain-done", false},
		{"plain expected, tabbed shown", newSpec([]Question{colorTab}, 0, false).check(tabState{cursor: 1}), "tab-q1", false},
		{"tabbed expected, plain shown", newSpec([]Question{colorPlain, toppings}, 0, true).check(tabState{cursor: 1}), "plain-q", false},
		{"multi cursor not on row 1", newSpec(tabbed, 1, true).check(tabState{cursor: 1}), "tab-q2-cursor-row2", false},
		{"multi pre-checked box", newSpec(tabbed, 1, true).check(tabState{cursor: 1}), "tab-q2-olives", false},
		{"last tab shows Submit", newSpec(tabbed, 1, true).check(tabState{cursor: 1}), "tab-q2", true},
		{"last tab expected, Next shown", newSpec([]Question{colorTab, toppings}, 1, true).check(tabState{cursor: 1}), "mfirst-q1", false},
		{"non-last tab shows Next", newSpec(mfirst, 0, true).check(tabState{cursor: 1}), "mfirst-q1", true},
		{"non-last tab expected, Submit shown", newSpec(mfirst, 0, true).check(tabState{cursor: 1}), "tab-q2", false},
		{"single multi-select question is tabbed", newSpec([]Question{toppings}, 0, true).check(tabState{cursor: 1}), "smulti-q", true},
		{"markdown shown verbatim", newSpec([]Question{race}, 0, false).check(tabState{cursor: 1}), "md-q", true},
		{"wrapped question and label", newSpec([]Question{notes, toppings}, 0, true).check(tabState{cursor: 1}), "wrap-q1", true},
		{"wrapped review", reviewCheck([]Question{notes, toppings}, []Answer{{Options: []int{0}}, {Options: []int{1}}}), "wrap-review", true},
		{"review with another answer", reviewCheck([]Question{notes, toppings}, []Answer{{Options: []int{1}}, {Options: []int{1}}}), "wrap-review", false},
		{"review, ANSI", reviewCheck(tabbed, []Answer{{Options: []int{1}}, {Options: []int{2, 0}}}), "tab-review@ansi", true},
		{"typed text must match exactly", newSpec([]Question{colorPlain}, 0, false).check(tabState{cursor: 4, other: Normalize("2 mor")}), "plain-other-typed", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			name, ansi := strings.CutSuffix(tt.fixture, "@ansi")
			capture := fixture(t, name)
			if ansi {
				capture = colorize(capture)
			}
			if got := tt.check(capture); got != tt.want {
				t.Errorf("check(%s) = %v, want %v", tt.fixture, got, tt.want)
			}
		})
	}
}

// TestPlanWalk replays each answer through the frames the pane shows along
// the way: every step's Expect passes on its own frame and on no other
// fixture.
func TestPlanWalk(t *testing.T) {
	tests := []struct {
		name   string
		qs     []Question
		as     []Answer
		frames []string
	}{
		{
			name:   "plain option",
			qs:     []Question{colorPlain},
			as:     []Answer{{Options: []int{1}}},
			frames: []string{"plain-q"},
		},
		{
			name:   "plain Other with a leading digit",
			qs:     []Question{colorPlain},
			as:     []Answer{{Text: "2 more"}},
			frames: []string{"plain-q", "plain-other-cursor", "plain-other-typed"},
		},
		{
			name:   "tabbed options",
			qs:     []Question{colorTab, toppings},
			as:     []Answer{{Options: []int{1}}, {Options: []int{2, 0}}},
			frames: []string{"tab-q1", "tab-q2", "tab-q2-checked", "tab-q2-checked-submit", "tab-review"},
		},
		{
			name: "tabbed Other on single and multi",
			qs:   []Question{colorTab, toppings},
			as:   []Answer{{Text: `Teal, "deep"; 1`}, {Options: []int{1}, Text: "Pineapple 2"}},
			frames: []string{
				"tab-q1", "tab-q1-other-cursor", "tab-q1-other-typed",
				"tab-q2", "tab-q2-olives", "tab-q2-other-cursor", "tab-q2-other-typed", "tab-q2-submit",
				"tab-review-other",
			},
		},
		{
			name: "multi-select first tab, Next row",
			qs:   []Question{toppings, colorTab},
			as: []Answer{
				{Options: []int{1}, Text: "3 anchovies and a very long free text answer that should wrap inside the field at sixty columns"},
				{Text: "1 shade darker"},
			},
			frames: []string{
				"mfirst-q1", "mfirst-q1-olives", "mfirst-q1-other-cursor", "mfirst-q1-other-typed", "mfirst-q1-next",
				"mfirst-q2", "mfirst-q2-other-cursor", "mfirst-q2-other-typed",
				"mfirst-review",
			},
		},
	}
	all := fixtureNames(t)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			steps, err := Plan(tt.qs, tt.as)
			if err != nil {
				t.Fatal(err)
			}
			if len(steps) != len(tt.frames) {
				t.Fatalf("%d steps, want %d", len(steps), len(tt.frames))
			}
			for i, s := range steps {
				for _, name := range all {
					want := name == tt.frames[i]
					if got := s.Expect(fixture(t, name)); got != want {
						t.Errorf("step %d Expect(%s) = %v, want %v", i, name, got, want)
					}
				}
			}
		})
	}
}

func TestDialogLinesIgnoresInputBox(t *testing.T) {
	for _, name := range []string{"plain-done", "perm-after", "perm-bash"} {
		if _, ok := dialogLines(fixture(t, name)); ok {
			t.Errorf("dialogLines(%s) found a question dialog", name)
		}
	}
}

// An option row must spell its label and description exactly.
func TestQuestionCheckOptionRowsExactly(t *testing.T) {
	with := func(q Question, k int, o Option) []Question {
		q.Options = append([]Option(nil), q.Options...)
		q.Options[k] = o
		return []Question{q}
	}
	tests := []struct {
		name    string
		qs      []Question
		tabbed  bool
		capture string
	}{
		{"label is a prefix of the row", []Question{colorPlain}, false,
			strings.Replace(fixture(t, "plain-q"), "❯ 1. Red\n", "❯ 1. Red-team the prod DB\n", 1)},
		{"another description", with(colorPlain, 0, Option{Label: "Red", Description: "Hot"}), false, fixture(t, "plain-q")},
		{"description missing from the call", with(colorPlain, 0, Option{Label: "Red"}), false, fixture(t, "plain-q")},
		{"multi-select label is a prefix", with(toppings, 1, Option{Label: "Olive", Description: "Salty"}), true, fixture(t, "smulti-q")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if newSpec(tt.qs, 0, tt.tabbed).check(tabState{cursor: 1})(tt.capture) {
				t.Error("check passed")
			}
		})
	}
	if !newSpec([]Question{toppings}, 0, true).check(tabState{cursor: 1})(fixture(t, "smulti-q")) {
		t.Error("smulti-q does not match its own question")
	}
}

// Only a dialog's own column-0 rule splits it, so an option described by a
// line of rule runes still parses.
func TestQuestionCheckIndentedRule(t *testing.T) {
	q := colorPlain
	q.Options = append([]Option(nil), q.Options...)
	q.Options[2].Description = "────────"
	capture := strings.Replace(fixture(t, "plain-q"), "     Natural\n", "     ────────\n", 1)
	if !newSpec([]Question{q}, 0, false).check(tabState{cursor: 1})(capture) {
		t.Error("check failed on an option described by a rule")
	}
}
