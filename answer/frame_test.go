package answer

import (
	"os"
	"path/filepath"
	"slices"
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
	// The preview-layout questions behind the prev-q* and prevtab-* frames.
	layout = Question{Text: "Which layout do you prefer?", Options: previews(opts("Grid", "Cards in rows", "List", "One per line", "Split", "Two panes"))}
	size   = Question{Text: "Pick a size?", Options: previews(opts("Small", "Compact", "Large", "Roomy"))}
	speed  = Question{Text: "Pick a speed?", Options: opts("Slow", "Careful", "Fast", "Quick")}
	deploy = Question{
		Text: "Which deployment strategy should we use for the new service rollout?",
		Options: previews(opts(
			"Blue green deployment with full traffic switch", "Two identical fleets",
			"Canary", "Gradual percentage rollout across regions over several hours",
		)),
	}
)

// opts pairs up labels and descriptions: label, description, label, ….
func opts(pairs ...string) []Option {
	var out []Option
	for i := 0; i < len(pairs); i += 2 {
		out = append(out, Option{Label: pairs[i], Description: pairs[i+1]})
	}
	return out
}

// previews gives every option a preview sketch.
func previews(os []Option) []Option {
	for i := range os {
		os[i].Preview = "+--+\n|" + os[i].Label + "|\n+--+"
	}
	return os
}

// fixtureWidth is the pane width the testdata captures were recorded at.
const fixtureWidth = 60

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name+".txt"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// fixtureNames lists the fixtures in testdata/<dir>, named as fixture takes
// them.
func fixtureNames(t *testing.T, dir string) []string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join("testdata", dir, "*.txt"))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, p := range paths {
		name := strings.TrimSuffix(filepath.Base(p), ".txt")
		if dir != "" {
			name = dir + "/" + name
		}
		names = append(names, name)
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
		{"preview wrapped label", newSpec([]Question{deploy}, 0, false).check(tabState{cursor: 1}), "prev-q-wrap", true},
		{"preview expected, non-preview shown", newSpec([]Question{size, speed}, 1, true).check(tabState{cursor: 1}), "prevtab-q2", true},
		{"preview cursor on another row", newSpec([]Question{layout}, 0, false).check(tabState{cursor: 1}), "prev-q-list", false},
		{"preview cursor row 2", newSpec([]Question{layout}, 0, false).check(tabState{cursor: 2, touched: true}), "prev-q-list", true},
		{"299 plain initial", newSpec([]Question{colorPlain}, 0, false).check(tabState{cursor: 1}), "2.1.299/plain-q", true},
		{"299 last tab shows Submit", newSpec(tabbed, 1, true).check(tabState{cursor: 1}), "2.1.299/tab-q2", true},
		{"299 non-last tab shows Next", newSpec(mfirst, 0, true).check(tabState{cursor: 1}), "2.1.299/mfirst-q1", true},
		{"299 review", reviewCheck(tabbed, []Answer{{Options: []int{1}}, {Options: []int{2, 0}}}), "2.1.299/tab-review", true},
		{"299 preview cursor row 2", newSpec([]Question{layout}, 0, false).check(tabState{cursor: 2, touched: true}), "2.1.299/prev-q-list", true},
		{"299 smulti", newSpec([]Question{toppings}, 0, true).check(tabState{cursor: 1}), "2.1.299/smulti-q", true},
		{"299 markdown", newSpec([]Question{race}, 0, false).check(tabState{cursor: 1}), "2.1.299/md-q", true},
		{"299 wrapped", newSpec([]Question{notes, toppings}, 0, true).check(tabState{cursor: 1}), "2.1.299/wrap-q1", true},
		{"299 wrapped review", reviewCheck([]Question{notes, toppings}, []Answer{{Options: []int{0}}, {Options: []int{1}}}), "2.1.299/wrap-review", true},
		{"299 multi cursor not on row 1", newSpec(tabbed, 1, true).check(tabState{cursor: 1}), "2.1.299/tab-q2-cursor-row2", false},
		{"named plain initial", newSpec([]Question{colorPlain}, 0, false).check(tabState{cursor: 1}), "2.1.299-named/plain-q", true},
		{"named plain initial, ANSI", newSpec([]Question{colorPlain}, 0, false).check(tabState{cursor: 1}), "2.1.299-named/plain-q@ansi", true},
		{"named plain, tabbed expected", newSpec([]Question{colorPlain, toppings}, 0, true).check(tabState{cursor: 1}), "2.1.299-named/plain-q", false},
		{"named last tab shows Submit", newSpec(tabbed, 1, true).check(tabState{cursor: 1}), "2.1.299-named/tab-q2", true},
		{"named review", reviewCheck(tabbed, []Answer{{Options: []int{1}}, {Options: []int{2, 0}}}), "2.1.299-named/tab-review", true},
		{"named review, ANSI", reviewCheck(tabbed, []Answer{{Options: []int{1}}, {Options: []int{2, 0}}}), "2.1.299-named/tab-review@ansi", true},
		{"named review with another answer", reviewCheck(tabbed, []Answer{{Options: []int{0}}, {Options: []int{2, 0}}}), "2.1.299-named/tab-review", false},
		{"named preview", newSpec([]Question{layout}, 0, false).check(tabState{cursor: 1}), "2.1.299-named/prev-q", true},
		{"typed text must match exactly", newSpec([]Question{colorPlain}, 0, false).check(tabState{cursor: 4, other: Normalize("2 mor")}), "plain-other-typed", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			name, ansi := strings.CutSuffix(tt.fixture, "@ansi")
			capture := fixture(t, name)
			if ansi {
				capture = colorize(capture)
			}
			if got := tt.check(capture, fixtureWidth); got != tt.want {
				t.Errorf("check(%s) = %v, want %v", tt.fixture, got, tt.want)
			}
		})
	}
}

// The 2.1.310 calls: three questions (single, multi, preview) and two (single,
// multi).
var (
	size3 = Question{Text: "Pick a size?", Options: previews(opts("Small", "Compact", "Large", "Roomy"))}
	m3    = []Question{colorTab, toppings, size3}
	v2    = []Question{colorTab, toppings}
)

const longTeal = "a fairly long custom colour answer that keeps going well past the edge of this narrow pane"

func TestReadView(t *testing.T) {
	tests := []struct {
		fixture string
		qs      []Question
		want    View
		other   string // the normalized visible Other text, when the row checks it
	}{
		{"2.1.310/m3-q1", m3, View{Step: 0, Cursor: 1}, ""},
		{"2.1.310/m3-q1-back-left", m3, View{Step: 0, Cursor: 1, Options: []int{1}}, ""},
		{"2.1.310/m3-q1-back-btab", m3, View{Step: 0, Cursor: 1, Options: []int{0}}, ""},
		{"2.1.310/m3-q2-cheese-basil", m3, View{Step: 1, Cursor: 1, Options: []int{0, 2}}, ""},
		{"2.1.310/m3-q2-other-typed", m3, View{Step: 1, Cursor: 4, Options: []int{0, 2}, Text: "extra pepper"}, "extrapepper"},
		{"2.1.310/m3-q2-other-next", m3, View{Step: 1, Cursor: 0, Options: []int{0, 2}, Text: "extra pepper"}, "extrapepper"},
		{"2.1.310/m3-q3", m3, View{Step: 2, Cursor: 1}, ""},
		{"2.1.310/m3-q3-large", m3, View{Step: 2, Cursor: 2}, ""},
		{"2.1.310/m3-q3-back-from-review", m3, View{Step: 2, Cursor: 1, Options: []int{1}}, ""},
		{"2.1.310/m3-q1-back-other-answered", m3, View{Step: 0, Cursor: 1, Text: "teal"}, "teal"},
		{"2.1.310/m3-q1-back-blue2", m3, View{Step: 0, Cursor: 1, Options: []int{1}}, "navy"},
		{"2.1.310/m3-review", m3, View{Step: 3, Cursor: 1, Review: []string{"Red", "Cheese, Basil, extra pepper", "Large"}}, ""},
		{"2.1.310/v3-review-cancel", v2, View{Step: 2, Cursor: 2, Review: []string{"Red", "Olives"}}, ""},
		{"2.1.310/v2-q1-back-long", v2, View{Step: 0, Cursor: 1, Text: longTeal}, Normalize(longTeal)},
		{"2.1.310/plain-q", []Question{colorPlain}, View{Step: 0, Cursor: 1}, ""},
		{"2.1.310/plain-other-typed", []Question{colorPlain}, View{Step: 0, Cursor: 4}, "darkteal"},
		{"2.1.310/smulti-review", []Question{toppings}, View{Step: 1, Cursor: 1, Review: []string{"Olives, ham"}}, ""},
		{"2.1.310/prev-q-large", []Question{size3}, View{Step: 0, Cursor: 2}, ""},
		{"2.1.310-normal/q1", v2, View{Step: 0, Cursor: 1}, ""},
		{"2.1.310-normal/q1-other-typed", v2, View{Step: 0, Cursor: 3}, "teal"},
		{"2.1.310-normal/q1-back", v2, View{Step: 0, Cursor: 1, Text: "teal"}, "teal"},
		{"2.1.310-normal/review", v2, View{Step: 2, Cursor: 1, Review: []string{"navy", "ham"}}, ""},
		{"mfirst-review", []Question{toppings, colorTab}, View{Step: 2, Cursor: 1, Review: []string{
			"Olives, 3 anchovies and a very long free text answer that should wrap inside the field at sixty columns",
			"1 shade darker",
		}}, ""},
		{"mfirst-q1-next", []Question{toppings, colorTab}, View{Step: 0, Cursor: 0, Options: []int{1},
			Text: "3 anchovies and a very long free text answer that should wrap inside the field at sixty columns"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.fixture, func(t *testing.T) {
			got, ok := ReadView(tt.qs, fixture(t, tt.fixture), fixtureWidth)
			if !ok {
				t.Fatal("ReadView refused")
			}
			if got.Step != tt.want.Step || got.Cursor != tt.want.Cursor || !slices.Equal(got.Options, tt.want.Options) ||
				got.Text != tt.want.Text || !slices.Equal(got.Review, tt.want.Review) {
				t.Errorf("ReadView = %+v, want %+v", got, tt.want)
			}
			if tt.other != "" && got.other != tt.other {
				t.Errorf("other = %q, want %q", got.other, tt.other)
			}
		})
	}
}

// The unexported fields a step plan works from.
func TestReadViewOtherRow(t *testing.T) {
	tests := []struct {
		fixture   string
		qs        []Question
		boxes     []bool
		lines     int
		committed bool
	}{
		{"2.1.310/v2-q1-back-long", v2, []bool{true, false}, 2, true},
		{"2.1.310/v2-q1-long-cleared", v2, []bool{true, false}, 1, true},
		{"2.1.310/m3-q1-other-cleared", m3, []bool{true, true, true}, 1, true},
		{"2.1.310/m3-q1-back-blue2", m3, []bool{true, true, true}, 1, false},
		{"2.1.310/v3-q1-wrapped-up1", v2, []bool{true, true}, 2, false},
		{"2.1.310/m3-q2-cheese-basil", m3, []bool{true, true, false}, 1, false},
		{"mfirst-q1-other-typed", []Question{toppings, colorTab}, []bool{true, false}, 2, true},
		{"2.1.310/plain-other-typed", []Question{colorPlain}, nil, 1, false},
	}
	for _, tt := range tests {
		t.Run(tt.fixture, func(t *testing.T) {
			got, ok := ReadView(tt.qs, fixture(t, tt.fixture), fixtureWidth)
			if !ok {
				t.Fatal("ReadView refused")
			}
			if !slices.Equal(got.boxes, tt.boxes) || got.otherLines != tt.lines || got.committed != tt.committed {
				t.Errorf("boxes %v lines %d committed %v, want %v %d %v", got.boxes, got.otherLines, got.committed, tt.boxes, tt.lines, tt.committed)
			}
		})
	}
	// A cleared committed Other keeps its mark and has no text.
	v, _ := ReadView(m3, fixture(t, "2.1.310/m3-q1-other-cleared"), fixtureWidth)
	if v.Text != "" || v.other != "" || len(v.Options) != 0 {
		t.Errorf("m3-q1-other-cleared = %+v, want a committed empty Other", v)
	}
}

func TestReadViewRefuses(t *testing.T) {
	down := fixture(t, "2.1.310/v2-q1-down-other")
	if _, ok := ReadView(v2, down, fixtureWidth); !ok {
		t.Fatal("v2-q1-down-other does not read")
	}
	vimNormal := strings.Replace(down,
		"-- INSERT -- · Enter to select · Tab/Arrow keys to navigate\n· ctrl+g to edit in Nvim",
		"-- NORMAL -- · Enter to select · Tab/Arrow keys to navigate", 1)
	if vimNormal == down {
		t.Fatal("footer not rewritten")
	}
	tests := []struct {
		name    string
		qs      []Question
		capture string
	}{
		{"stale dialog above a permission prompt", []Question{colorPlain}, fixture(t, "stale-negative")},
		{"scrollback quotes the question above another dialog", []Question{colorPlain}, fixture(t, "scrollback-negative")},
		{"answered, idle", []Question{colorPlain}, fixture(t, "plain-done")},
		{"permission prompt", []Question{colorPlain}, fixture(t, "perm-bash")},
		{"vim normal mode footer", v2, vimNormal},
		{"plain expected, tabbed shown", []Question{colorTab}, fixture(t, "2.1.310/v2-q1")},
		{"tabbed expected, plain shown", []Question{colorPlain, toppings}, fixture(t, "2.1.310/plain-q")},
		{"tab bar counts another number of questions", m3, fixture(t, "2.1.310/v2-q1")},
		{"second mark", v2, lastReplace(fixture(t, "2.1.310/v2-q1-red-committed"), "3. teal", "3. teal ✔")},
		{"label is a prefix of the row", []Question{colorPlain}, strings.Replace(fixture(t, "plain-q"), "❯ 1. Red\n", "❯ 1. Red-team the prod DB\n", 1)},
		{"two cursors", v2, lastReplace(fixture(t, "2.1.310/v2-q1"), "  2. Blue", "❯ 2. Blue")},
		{"no cursor", v2, lastReplace(fixture(t, "2.1.310/v2-q1"), "❯ 1. Red", "  1. Red")},
		{"review answer missing", v2, lastReplace(fixture(t, "2.1.310/v2-review"), "   → Olives\n", "")},
		{"review with an unticked box", v2, lastReplace(fixture(t, "2.1.310/v2-review"), "☒ Toppings", "☐ Toppings")},
		{"review choices without a cursor", v2, lastReplace(fixture(t, "2.1.310/v2-review"), "❯ 1. Submit", "  1. Submit")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if v, ok := ReadView(tt.qs, tt.capture, fixtureWidth); ok {
				t.Errorf("ReadView = %+v, want a refusal", v)
			}
		})
	}
}

// TestReadViewVimFooter covers the footer vim editor mode shows while the
// Other field has focus: "-- INSERT --" in place of "Esc to cancel".
func TestReadViewVimFooter(t *testing.T) {
	tests := []struct {
		fixture string
		qs      []Question
		cursor  int
		options []int
	}{
		{"2.1.310/repro-other-q1", v2, 3, nil},
		{"2.1.310/plain-other-cursor", []Question{colorPlain}, 4, nil},
		{"2.1.310/m3-q2-other-cursor", m3, 4, []int{0, 2}},
		{"2.1.310/plain-other-typed", []Question{colorPlain}, 4, nil},
	}
	for _, tt := range tests {
		v, ok := ReadView(tt.qs, fixture(t, tt.fixture), fixtureWidth)
		if !ok || v.Cursor != tt.cursor || !slices.Equal(v.Options, tt.options) {
			t.Errorf("ReadView(%s) = %+v, %v; want cursor %d options %v", tt.fixture, v, ok, tt.cursor, tt.options)
		}
	}
}

func TestFooterShapes(t *testing.T) {
	tests := []struct {
		footer string
		want   bool
	}{
		{"Enter to select · Tab/Arrow keys to navigate · Esc to cancel", true},
		{"Enter to select · ↑/↓ to navigate · n to add notes · Tab to switch questions · Esc to cancel", true},
		{"Enter to select · ↑/↓ to navigate · ctrl+g to edit in Vim · Esc to cancel", true},
		{"-- INSERT -- · Enter to select · Tab/Arrow keys to navigate · ctrl+g to edit in Nvim", true},
		{"-- INSERT -- · Enter to select · ↑/↓ to navigate", true},
		{"-- INSERT -- · Enter to select · Tab/Arrow keys to navigate · Esc to cancel", false},
		{"-- NORMAL -- · Enter to select · Tab/Arrow keys to navigate", false},
		{"Enter to select · Tab/Arrow keys to navigate", false},
		{"Enter to select · Tab/Arrow keys to navigate · ctrl+g to edit in · Esc to cancel", false},
		{"Enter to select · Tab/Arrow keys to navigate · ctrl+g to edit in A · B · Esc to cancel", false},
		{"Enter to select · Tab to switch questions · ↑/↓ to navigate · Esc to cancel", false},
		{"Enter to select · ↑/↓ to navigate · Tab to switch questions · n to add notes · Esc to cancel", false},
	}
	for _, tt := range tests {
		if got := isFooter(Normalize(tt.footer)); got != tt.want {
			t.Errorf("isFooter(%q) = %v, want %v", tt.footer, got, tt.want)
		}
	}
}

// fixtureQuestions returns the call behind a question fixture, or nil for a
// frame that shows no question tab or review.
func fixtureQuestions(t *testing.T, dir, base string) []Question {
	t.Helper()
	recent := strings.HasPrefix(dir, "2.1.310")
	switch {
	case dir == "2.1.310-normal":
		return v2
	case base == "plain-done", base == "stale-negative", base == "scrollback-negative", strings.HasPrefix(base, "perm-"):
		return nil
	case strings.HasPrefix(base, "m3-"):
		return m3
	case strings.HasPrefix(base, "v2-"), strings.HasPrefix(base, "v3-"), strings.HasPrefix(base, "repro-"), strings.HasPrefix(base, "tab-"):
		return v2
	case strings.HasPrefix(base, "plain-"):
		return []Question{colorPlain}
	case strings.HasPrefix(base, "smulti-"):
		return []Question{toppings}
	case base == "prev-q-wrap":
		return []Question{deploy}
	case strings.HasPrefix(base, "prev-q") && recent:
		return []Question{size3}
	case strings.HasPrefix(base, "prev-q"):
		return []Question{layout}
	case strings.HasPrefix(base, "prevtab-"):
		return []Question{size, speed}
	case strings.HasPrefix(base, "mfirst-"):
		return []Question{toppings, colorTab}
	case strings.HasPrefix(base, "wrap-"):
		return []Question{notes, toppings}
	case base == "md-q":
		return []Question{race}
	}
	t.Fatalf("no questions known for %s/%s", dir, base)
	return nil
}

// Every question tab and review in every fixture set reads for its call, and
// the view it reads passes viewIs on its own frame only.
func TestReadViewFixtures(t *testing.T) {
	for _, dir := range []string{"", "2.1.299", "2.1.299-named", "2.1.310", "2.1.310-normal"} {
		for _, name := range fixtureNames(t, dir) {
			qs := fixtureQuestions(t, dir, filepath.Base(name))
			capture := fixture(t, name)
			v, ok := ReadView(qs, capture, fixtureWidth)
			if qs == nil {
				if ok {
					t.Errorf("%s: ReadView read a question", name)
				}
				continue
			}
			if !ok {
				t.Errorf("%s: ReadView refused", name)
				continue
			}
			if !viewIs(qs, v)(capture, fixtureWidth) {
				t.Errorf("%s: viewIs fails on its own frame", name)
			}
			moved := v
			moved.Cursor = (v.Cursor + 1) % 3
			if viewIs(qs, moved)(capture, fixtureWidth) {
				t.Errorf("%s: viewIs passes with another cursor", name)
			}
		}
	}
}

func TestViewIsIgnoresOtherLines(t *testing.T) {
	v, ok := ReadView(v2, fixture(t, "2.1.310/v3-q1-wrapped-up2"), fixtureWidth)
	if !ok {
		t.Fatal("v3-q1-wrapped-up2 does not read")
	}
	v.otherLines++
	if !viewIs(v2, v)(fixture(t, "2.1.310/v3-q1-wrapped-up2"), fixtureWidth) {
		t.Error("viewIs compares the Other line count")
	}
	v.other = "teal"
	if viewIs(v2, v)(fixture(t, "2.1.310/v3-q1-wrapped-up2"), fixtureWidth) {
		t.Error("viewIs ignores the Other text")
	}

	// The focused field wraps narrower, so committed text and review answers
	// compare normalized.
	long := fixture(t, "2.1.310/v2-q1-back-long")
	v = readFixture(t, v2, "2.1.310/v2-q1-back-long")
	v.Text = strings.Replace(longTeal, " going ", " going\n  ", 1)
	if !viewIs(v2, v)(long, fixtureWidth) {
		t.Error("viewIs compares the committed text raw")
	}
	v.Text = "teal"
	if viewIs(v2, v)(long, fixtureWidth) {
		t.Error("viewIs ignores the committed text")
	}
	review := fixture(t, "2.1.310/m3-review")
	v = readFixture(t, m3, "2.1.310/m3-review")
	v.Review = []string{"Red", "Cheese,Basil, extra  pepper", "Large"}
	if !viewIs(m3, v)(review, fixtureWidth) {
		t.Error("viewIs compares review answers raw")
	}
	v.Review = []string{"Blue", "Cheese, Basil, extra pepper", "Large"}
	if viewIs(m3, v)(review, fixtureWidth) {
		t.Error("viewIs ignores review answers")
	}
}

func TestTabbedRepeats(t *testing.T) {
	if Tabbed([]Question{colorPlain}) || !Tabbed([]Question{toppings}) || !Tabbed(v2) {
		t.Error("Tabbed")
	}
	reworded := colorTab
	reworded.Text = "Which  color do you\nprefer?"
	described := colorTab
	described.Options = opts("Red", "Hot", "Blue", "Cool")
	previewed := size3
	previewed.Options = previews(opts("Small", "Tiny", "Large", "Huge"))
	tests := []struct {
		name string
		qs   []Question
		want bool
	}{
		{"distinct", m3, false},
		{"same text and rows after normalizing", []Question{colorTab, toppings, reworded}, true},
		{"another description", []Question{colorTab, described}, false},
		{"preview rows ignore descriptions", []Question{size3, previewed}, true},
		{"one question", []Question{colorTab}, false},
	}
	for _, tt := range tests {
		if got := Repeats(tt.qs); got != tt.want {
			t.Errorf("Repeats(%s) = %v, want %v", tt.name, got, tt.want)
		}
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
		named  bool
	}{
		{
			name:   "plain option",
			qs:     []Question{colorPlain},
			as:     []Answer{{Options: []int{1}}},
			frames: []string{"plain-q"},
			named:  true,
		},
		{
			name:   "plain Other with a leading digit",
			qs:     []Question{colorPlain},
			as:     []Answer{{Text: "2 more"}},
			frames: []string{"plain-q", "plain-other-cursor", "plain-other-typed"},
			named:  true,
		},
		{
			name:   "tabbed options",
			qs:     []Question{colorTab, toppings},
			as:     []Answer{{Options: []int{1}}, {Options: []int{2, 0}}},
			frames: []string{"tab-q1", "tab-q2", "tab-q2-checked", "tab-q2-checked-submit", "tab-review"},
			named:  true,
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
		{
			name:   "preview plain",
			qs:     []Question{layout},
			as:     []Answer{{Options: []int{2}}},
			frames: []string{"prev-q", "prev-q-split"},
			named:  true,
		},
		{
			name:   "preview tabbed mixed",
			qs:     []Question{size, speed},
			as:     []Answer{{Options: []int{1}}, {Options: []int{0}}},
			frames: []string{"prevtab-q1", "prevtab-q1-large", "prevtab-q2", "prevtab-review"},
		},
	}
	for _, dir := range []string{"", "2.1.299", "2.1.299-named"} {
		all := fixtureNames(t, dir)
		have := map[string]bool{}
		for _, name := range all {
			have[name] = true
		}
		prefix := ""
		if dir != "" {
			prefix = dir + "/"
		}
		for _, tt := range tests {
			if dir == "2.1.299-named" && !tt.named {
				continue
			}
			frames := make([]string, len(tt.frames))
			for i, f := range tt.frames {
				frames[i] = prefix + f
				if !have[frames[i]] {
					t.Fatalf("%s: fixture %s missing", prefix+tt.name, frames[i])
				}
			}
			t.Run(prefix+tt.name, func(t *testing.T) {
				steps, err := Plan(tt.qs, tt.as)
				if err != nil {
					t.Fatal(err)
				}
				if len(steps) != len(frames) {
					t.Fatalf("%d steps, want %d", len(steps), len(frames))
				}
				for i, s := range steps {
					for _, name := range all {
						want := name == frames[i]
						if got := s.Expect(fixture(t, name), fixtureWidth); got != want {
							t.Errorf("step %d Expect(%s) = %v, want %v", i, name, got, want)
						}
					}
					if dir == "" {
						continue
					}
					for _, other := range []string{"stale-negative", "scrollback-negative"} {
						if s.Expect(fixture(t, other), fixtureWidth) {
							t.Errorf("step %d Expect(%s) = true", i, other)
						}
					}
				}
			})
		}
	}
}

func TestDialogLinesIgnoresInputBox(t *testing.T) {
	for _, name := range []string{"plain-done", "perm-after", "perm-bash"} {
		if _, ok := dialogLines(fixture(t, name), fixtureWidth); ok {
			t.Errorf("dialogLines(%s) found a question dialog", name)
		}
	}
}

// lastReplace swaps the last occurrence of old, the one inside the dialog
// rather than in scrollback.
func lastReplace(s, old, repl string) string {
	i := strings.LastIndex(s, old)
	return s[:i] + repl + s[i+len(old):]
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
		{"299 extra description word", []Question{colorPlain}, false,
			strings.Replace(fixture(t, "2.1.299/plain-q"), "     Warm\n", "     Warm sun\n", 1)},
		{"299 named extra description word", []Question{colorPlain}, false,
			strings.Replace(fixture(t, "2.1.299-named/plain-q"), "     Warm\n", "     Warm sun\n", 1)},
		{"299 named label differs by a word", []Question{colorPlain}, false,
			lastReplace(fixture(t, "2.1.299-named/plain-q"), "Which color do you prefer?", "Which color do you really prefer?")},
		{"299 label differs by a word", []Question{colorPlain}, false,
			lastReplace(fixture(t, "2.1.299/plain-q"), "Which color do you prefer?", "Which color do you really prefer?")},
		{"multi-select label is a prefix", with(toppings, 1, Option{Label: "Olive", Description: "Salty"}), true, fixture(t, "smulti-q")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if newSpec(tt.qs, 0, tt.tabbed).check(tabState{cursor: 1})(tt.capture, fixtureWidth) {
				t.Error("check passed")
			}
		})
	}
	if !newSpec([]Question{toppings}, 0, true).check(tabState{cursor: 1})(fixture(t, "smulti-q"), fixtureWidth) {
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
	if !newSpec([]Question{q}, 0, false).check(tabState{cursor: 1})(capture, fixtureWidth) {
		t.Error("check failed on an option described by a rule")
	}
}

// Notes the user typed under the preview are not the empty-notes hint, so the
// dialog no longer matches.
func TestQuestionCheckPreviewNotes(t *testing.T) {
	check := newSpec([]Question{layout}, 0, false).check(tabState{cursor: 1})
	base := fixture(t, "prev-q")
	if !check(base, fixtureWidth) {
		t.Fatal("prev-q does not match its own question")
	}
	typed := strings.Replace(base, "Notes: press n to add notes", "Notes: rows of two", 1)
	if check(typed, fixtureWidth) {
		t.Error("check passed with typed notes")
	}
}

// A dialog rule spans the pane, so a question line made of rule runes is text.
func TestQuestionCheckShortRuleIsText(t *testing.T) {
	q := colorPlain
	q.Text = "Pick one:\n────\nWhich color do you prefer?"
	capture := strings.Replace(fixture(t, "plain-q"), "\nWhich color do you prefer?\n", "\nPick one:\n────\nWhich color do you prefer?\n", 1)
	if !newSpec([]Question{q}, 0, false).check(tabState{cursor: 1})(capture, fixtureWidth) {
		t.Error("check failed on a question holding a short rule")
	}
}

// The anchor width comes from the pane, so a full-width scrollback
// line holding more runes than cells (tmux draws a ZWJ family emoji in 2 cells
// and NFD Hangul medial/final jamo in none) still lets the dialog anchor.
func TestAnchorIgnoresWideRuneLines(t *testing.T) {
	tests := []struct{ name, line string }{
		{"zwj emoji", strings.Repeat("a", 58) + "\U0001F468\u200d\U0001F469\u200d\U0001F467"},
		{"nfd hangul", strings.Repeat("\u1100\u1161\u11a8", 10) + strings.Repeat("a", 40)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !newSpec([]Question{colorPlain}, 0, false).check(tabState{cursor: 1})(tt.line+"\n"+fixture(t, "plain-q"), fixtureWidth) {
				t.Error("question check failed below the line")
			}
			if _, ok := PermissionPrompt(tt.line+"\n"+fixture(t, "perm-bash"), fixtureWidth); !ok {
				t.Error("no permission prompt below the line")
			}
		})
	}
}

// A question dialog taller than the capture loses its own border, so a short
// column-0 rule spilled above its header must not anchor it.
func TestQuestionRefusesWithoutFullWidthRule(t *testing.T) {
	check := newSpec([]Question{colorPlain}, 0, false).check(tabState{cursor: 1})
	base := fixture(t, "plain-q")
	if !check(base, fixtureWidth) {
		t.Fatal("unmodified fixture failed")
	}
	_, below, ok := strings.Cut(base, strings.Repeat("─", fixtureWidth)+"\n ☐ Color")
	if !ok {
		t.Fatal("plain-q dialog border not found")
	}
	cut := "──\n ☐ Color" + below
	if check(cut, fixtureWidth) {
		t.Error("check anchored on a short rule")
	}
	if check(base, fixtureWidth+1) {
		t.Error("check passed on a pane wider than the rule")
	}
}

func TestSessionRule(t *testing.T) {
	tests := []struct {
		line string
		want bool
	}{
		{"────────────── dispatcher · qa · 10-15 17:40 ─", true},
		{" dispatcher · qa … ─", true},
		{" dispatcher · qa · 10-1… ─", true},
		{"── x ─", true},
		{"────────────────────", false},
		{"  5. Chat about this", false},
		{"Enter to select · Esc to cancel", false},
		{"  indented name ─", false},
		{"────── dispatcher", false},
		{"──────x ─", false},
		{"─── ─ ─", false},
		{"──── ─", false},
		{" ─", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := isSessionRule(tt.line); got != tt.want {
			t.Errorf("isSessionRule(%q) = %v, want %v", tt.line, got, tt.want)
		}
	}
}

// Only the pane's last line may be the session-name rule: anything else after
// the footer, or text wedged above the rule, keeps the dialog from matching.
func TestQuestionCheckSessionRuleOnlyAtBottom(t *testing.T) {
	check := newSpec([]Question{colorPlain}, 0, false).check(tabState{cursor: 1})
	base := fixture(t, "2.1.299-named/plain-q")
	if !check(base, fixtureWidth) {
		t.Fatal("named plain-q does not match its own question")
	}
	lines := strings.Split(strings.TrimRight(base, "\n"), "\n")
	rule := lines[len(lines)-1]
	if !isSessionRule(rule) {
		t.Fatalf("last fixture line %q is not a session rule", rule)
	}
	tests := []struct{ name, capture string }{
		{"text after the rule", base + "\nsomething else\n"},
		{"second rule after the rule", base + "\n" + rule + "\n"},
		{"extra option above the rule", strings.Replace(base, "\n"+rule, "\n  6. Extra\n"+rule, 1)},
		{"extra line below the footer, above the rule", strings.Replace(base, "\n"+rule, "\nrm -rf /\n"+rule, 1)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if check(tt.capture, fixtureWidth) {
				t.Error("check passed")
			}
		})
	}
}
