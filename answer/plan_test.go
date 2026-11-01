package answer

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestPlanStepDoesNotReorderCallerOptions(t *testing.T) {
	a := Answer{Options: []int{2, 0}}
	if _, err := PlanStep(m3, 1, a, readFixture(t, m3, "2.1.310/m3-q2-after-blue")); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a.Options, []int{2, 0}) {
		t.Errorf("options reordered to %v", a.Options)
	}
}

func TestValidate(t *testing.T) {
	nine := Question{Text: "Pick", Options: opts("a", "", "b", "", "c", "", "d", "", "e", "", "f", "", "g", "", "h", "", "i", "")}
	tests := []struct {
		name string
		q    Question
		a    Answer
	}{
		{"option out of range", colorTab, Answer{Options: []int{2}}},
		{"negative option", colorTab, Answer{Options: []int{-1}}},
		{"duplicate option", toppings, Answer{Options: []int{1, 1}}},
		{"single with two options", colorPlain, Answer{Options: []int{0, 1}}},
		{"single with option and text", colorPlain, Answer{Options: []int{0}, Text: "x"}},
		{"single empty", colorPlain, Answer{}},
		{"multi empty", toppings, Answer{}},
		{"more than 8 options", nine, Answer{Options: []int{0}}},
		{"no options", Question{Text: "Pick"}, Answer{Text: "x"}},
		{"empty question", Question{Text: " ", Options: opts("a", "")}, Answer{Options: []int{0}}},
		{"empty label", Question{Text: "Pick", Options: opts("a", "", " ", "x")}, Answer{Options: []int{0}}},
		{"blank text", colorPlain, Answer{Text: "  "}},
		{"newline in text", colorPlain, Answer{Text: "a\nb"}},
		{"escape in text", toppings, Answer{Text: "a\x1b[A"}},
		{"commit mark in text", toppings, Answer{Text: "ham ✔"}},
		{"text on a preview question", layout, Answer{Text: "Masonry"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := validate(tt.q, tt.a); err == nil {
				t.Error("validate passed, want an error")
			}
		})
	}
}

func TestParseQuestions(t *testing.T) {
	raw := json.RawMessage(`{"questions":[
		{"question":"Which color do you prefer?","header":"Color","multiSelect":false,
		 "options":[{"label":"Red","description":"Warm"},{"label":"Blue","description":"Cool"}]},
		{"question":"Which toppings do you want?","header":"Toppings","multiSelect":true,
		 "options":[{"label":"Cheese","description":"Melty"},{"label":"Olives","description":"Salty"},{"label":"Basil","description":"Fresh"}]}
	]}`)
	got, err := ParseQuestions(raw)
	if err != nil {
		t.Fatal(err)
	}
	color, tops := colorTab, toppings
	color.Header, tops.Header = "Color", "Toppings"
	if want := []Question{color, tops}; !reflect.DeepEqual(got, want) {
		t.Errorf("ParseQuestions = %+v, want %+v", got, want)
	}
	got, err = ParseQuestions(json.RawMessage(`{"questions":[{"question":"Pick a size?","multiSelect":false,
		"options":[{"label":"Small","description":"Compact","preview":"+-+\n|s|"},{"label":"Large","description":"Roomy","preview":" "}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Options[0].Preview != "+-+\n|s|" || !got[0].PreviewLayout() {
		t.Errorf("ParseQuestions = %+v, want a preview question", got)
	}
	for _, q := range []Question{colorPlain, {Text: "Pick", Options: []Option{{Label: "a", Preview: " \n "}}}, {Text: "Pick", MultiSelect: true, Options: previews(opts("a", ""))}} {
		if q.PreviewLayout() {
			t.Errorf("%+v renders in the preview layout", q)
		}
	}
	for _, bad := range []string{`{"questions":[]}`, `{}`, `[`, `{"questions":"x"}`} {
		if _, err := ParseQuestions(json.RawMessage(bad)); err == nil {
			t.Errorf("ParseQuestions(%s): want an error", bad)
		}
	}
}

// stepLog spells each step's keys the way keys takes them.
func stepLog(steps []Step) [][]string {
	out := make([][]string, len(steps))
	for i, s := range steps {
		out[i] = []string{}
		for _, k := range s.Keys {
			if k.Text != "" {
				out[i] = append(out[i], "text:"+k.Text)
				continue
			}
			out[i] = append(out[i], k.Special)
		}
	}
	return out
}

func readFixture(t *testing.T, qs []Question, name string) View {
	t.Helper()
	v, ok := ReadView(qs, fixture(t, name), fixtureWidth)
	if !ok {
		t.Fatalf("%s does not read", name)
	}
	return v
}

func rep(key string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = key
	}
	return out
}

type planFunc func(qs []Question, v View) ([]Step, error)

func step(index int, a Answer) planFunc {
	return func(qs []Question, v View) ([]Step, error) { return PlanStep(qs, index, a, v) }
}

func back(index int) planFunc {
	return func(qs []Question, v View) ([]Step, error) { return PlanBack(qs, index, v) }
}

func submit(review ...string) planFunc {
	return func(qs []Question, v View) ([]Step, error) { return PlanSubmit(qs, review, v) }
}

// TestPlanSteps checks each plan's keys, batch by batch, and replays it
// against the 2.1.310 recording: frames[0] is the pane the plan is built
// from, frames[i] the pane after steps[i-1]'s keys, which steps[i].Expect must
// pass; "" where nothing was recorded. A tabbed step or back ends with a
// keyless landing step.
func TestPlanSteps(t *testing.T) {
	tests := []struct {
		name   string
		qs     []Question
		plan   planFunc
		keys   [][]string
		frames []string
		never  string // a key the plan must not send
	}{
		{
			name:   "single option on a fresh tab",
			qs:     m3,
			plan:   step(0, Answer{Options: []int{1}}),
			keys:   [][]string{{"2"}, {}},
			frames: []string{"m3-q1", "m3-q2-after-blue"},
		},
		{
			name:   "re-selection after Back",
			qs:     m3,
			plan:   step(0, Answer{Options: []int{0}}),
			keys:   [][]string{{"1"}, {}},
			frames: []string{"m3-q1-back-left", "m3-q2-after-red"},
		},
		{
			name: "single Other over committed Other text",
			qs:   m3,
			plan: step(0, Answer{Text: "navy"}),
			keys: [][]string{{"Down", "Down"}, {"C-u", "C-u", "C-u"}, {"text:navy"}, {"Enter"}, {}},
			frames: []string{
				"m3-q1-back-other-answered", "m3-q1-down-to-other-answered", "m3-q1-other-cleared",
				"m3-q1-other-navy", "m3-q2-after-navy",
			},
			never: "3",
		},
		{
			name:   "single Other on a fresh tab, vim footer",
			qs:     v2,
			plan:   step(0, Answer{Text: longTeal}),
			keys:   [][]string{{"Down", "Down"}, {"text:" + longTeal}, {"Enter"}, {}},
			frames: []string{"v2-q1", "v2-q1-down-other", "v2-q1-other-long", ""},
			never:  "3",
		},
		{
			name:   "wrapped committed Other is cleared line by line",
			qs:     v2,
			plan:   step(0, Answer{Text: "teal"}),
			keys:   [][]string{{"Down", "Down"}, rep("C-u", 5), {"text:teal"}, {"Enter"}, {}},
			frames: []string{"v2-q1-back-long", "", "v2-q1-long-cleared2", "", ""},
			never:  "3",
		},
		{
			name:   "plain preview",
			qs:     []Question{size3},
			plan:   step(0, Answer{Options: []int{1}}),
			keys:   [][]string{{"2"}, {"Enter"}},
			frames: []string{"prev-q", "prev-q-large"},
		},
		{
			name:   "preview tab lands on the review",
			qs:     m3,
			plan:   step(2, Answer{Options: []int{1}}),
			keys:   [][]string{{"2"}, {"Enter"}, {}},
			frames: []string{"m3-q3", "m3-q3-large", "m3-review"},
		},
		{
			name:   "multi toggles only the difference",
			qs:     m3,
			plan:   step(1, Answer{Options: []int{2}}),
			keys:   [][]string{{"1"}, rep("Down", 4), {"Enter"}, {}},
			frames: []string{"m3-q2-reenter-toggled", "", "", ""},
		},
		{
			name:   "multi Other",
			qs:     m3,
			plan:   step(1, Answer{Options: []int{2, 0}, Text: "extra pepper"}),
			keys:   [][]string{rep("Down", 3), {"text:extra pepper"}, {"Down"}, {"Enter"}, {}},
			frames: []string{"m3-q2-cheese-basil", "m3-q2-other-cursor", "m3-q2-other-typed", "m3-q2-other-next", "m3-q3"},
			never:  "4",
		},
		{
			name: "multi Other retyped after Back",
			qs:   m3,
			plan: step(1, Answer{Options: []int{0, 2}, Text: "no onions"}),
			keys: [][]string{rep("Down", 3), rep("C-u", 3), {"text:no onions"}, {"Down"}, {"Enter"}, {}},
			frames: []string{
				"m3-q2-back-from-q3", "m3-q2-back-other-cursor", "m3-q2-other-cu", "m3-q2-other-retyped", "", "",
			},
			never: "4",
		},
		{
			name:   "multi with an empty Other",
			qs:     v2,
			plan:   step(1, Answer{Options: []int{1}}),
			keys:   [][]string{rep("Down", 4), {"Enter"}, {}},
			frames: []string{"v2-q2-olives", "v2-q2-next-empty-other", ""},
		},
		{
			name:   "cursor prelude from row 2",
			qs:     v2,
			plan:   step(0, Answer{Options: []int{0}}),
			keys:   [][]string{{"Up"}, {"1"}, {}},
			frames: []string{"v3-q1-up-from-other", "v3-q1-up2", ""},
		},
		{
			name:   "cursor prelude from the Submit row",
			qs:     v2,
			plan:   step(1, Answer{Options: []int{1}}),
			keys:   [][]string{rep("Up", 4), {"2"}, rep("Down", 4), {"Enter"}, {}},
			frames: []string{"v3-q2-at-submit", "v3-q2-up4", "", "", ""},
		},
		{
			name:   "back from a tab",
			qs:     m3,
			plan:   back(1),
			keys:   [][]string{{"Left"}, {}},
			frames: []string{"m3-q2-after-blue", "m3-q1-back-left"},
		},
		{
			name:   "back from the review",
			qs:     m3,
			plan:   back(3),
			keys:   [][]string{{"Left"}, {}},
			frames: []string{"m3-review", "m3-q3-back-from-review"},
		},
		{
			name:   "submit from Cancel",
			qs:     v2,
			plan:   submit("Red", "Olives"),
			keys:   [][]string{{"Up"}, {"1"}},
			frames: []string{"v3-review-cancel", "v3-review-up"},
		},
		{
			name:   "submit compares the review normalized",
			qs:     m3,
			plan:   submit("Red", "Cheese,Basil,  extra\npepper", "Large"),
			keys:   [][]string{{"1"}},
			frames: []string{"m3-review"},
		},
		{
			name:   "plain option submits",
			qs:     []Question{colorPlain},
			plan:   step(0, Answer{Options: []int{1}}),
			keys:   [][]string{{"2"}},
			frames: []string{"plain-q"},
		},
		{
			name:   "plain Other",
			qs:     []Question{colorPlain},
			plan:   step(0, Answer{Text: "dark teal"}),
			keys:   [][]string{rep("Down", 3), {"text:dark teal"}, {"Enter"}},
			frames: []string{"plain-q", "plain-other-cursor", "plain-other-typed"},
			never:  "4",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := readFixture(t, tt.qs, "2.1.310/"+tt.frames[0])
			steps, err := tt.plan(tt.qs, v)
			if err != nil {
				t.Fatal(err)
			}
			got := stepLog(steps)
			if !reflect.DeepEqual(got, tt.keys) {
				t.Fatalf("keys = %q, want %q", got, tt.keys)
			}
			for _, batch := range got {
				if tt.never != "" && slices.Contains(batch, tt.never) {
					t.Errorf("plan sends %q", tt.never)
				}
			}
			for i, f := range tt.frames {
				if f != "" && !steps[i].Expect(fixture(t, "2.1.310/"+f), fixtureWidth) {
					t.Errorf("step %d Expect fails on %s", i, f)
				}
			}
		})
	}
}

// The first Expect pins the view the plan was built from, so a pane that
// moved on types nothing.
func TestPlanFirstExpectPinsView(t *testing.T) {
	steps, err := PlanStep(m3, 1, Answer{Options: []int{0}}, readFixture(t, m3, "2.1.310/m3-q2-after-blue"))
	if err != nil {
		t.Fatal(err)
	}
	if steps[0].Expect(fixture(t, "2.1.310/m3-q1"), fixtureWidth) {
		t.Error("first Expect passes on another tab")
	}
	if steps[0].Expect(fixture(t, "2.1.310/m3-q2-cheese-basil"), fixtureWidth) {
		t.Error("first Expect passes on the same tab with other boxes ticked")
	}

	steps, err = PlanSubmit(v2, []string{"Blue", "Olives"}, readFixture(t, v2, "2.1.310/v3-review-cancel"))
	if err != nil {
		t.Fatal(err)
	}
	if steps[0].Expect(fixture(t, "2.1.310/v3-review-cancel"), fixtureWidth) {
		t.Error("submit's first Expect passes with another review answer")
	}
}

// C-u clears one visual line, so one press is not enough for a wrapped text.
func TestPlanStepClearsWrappedOther(t *testing.T) {
	steps, err := PlanStep(v2, 0, Answer{Text: "teal"}, readFixture(t, v2, "2.1.310/v2-q1-back-long"))
	if err != nil {
		t.Fatal(err)
	}
	placeholder := steps[2].Expect
	if placeholder(fixture(t, "2.1.310/v2-q1-long-cleared"), fixtureWidth) {
		t.Error("placeholder Expect passes with one line left")
	}
	if !placeholder(fixture(t, "2.1.310/v2-q1-long-cleared2"), fixtureWidth) {
		t.Error("placeholder Expect fails on the cleared field")
	}
}

// A forward landing needs the answered tab's bar box ticked.
func TestPlanStepLandingNeedsTickedBox(t *testing.T) {
	steps, err := PlanStep(m3, 0, Answer{Options: []int{1}}, readFixture(t, m3, "2.1.310/m3-q1"))
	if err != nil {
		t.Fatal(err)
	}
	landing := steps[len(steps)-1].Expect
	after := fixture(t, "2.1.310/m3-q2-after-blue")
	if !landing(after, fixtureWidth) {
		t.Fatal("landing fails on m3-q2-after-blue")
	}
	if landing(strings.Replace(after, "☒ Color", "☐ Color", 1), fixtureWidth) {
		t.Error("landing passes with the answered tab's box clear")
	}
	if landing(fixture(t, "2.1.310/m3-q1"), fixtureWidth) {
		t.Error("landing passes on the tab it left")
	}
}

// Re-selecting after Back replaces the committed option: the tab shows one.
func TestReselectionLeavesOneOption(t *testing.T) {
	if v := readFixture(t, m3, "2.1.310/m3-q1-back-btab"); !slices.Equal(v.Options, []int{0}) {
		t.Errorf("m3-q1-back-btab options = %v, want [0]", v.Options)
	}
}

func TestPlanStepBackSubmitErrors(t *testing.T) {
	q1 := readFixture(t, m3, "2.1.310/m3-q1")
	q2 := readFixture(t, m3, "2.1.310/m3-q2-after-blue")
	review := readFixture(t, m3, "2.1.310/m3-review")
	plain := readFixture(t, []Question{colorPlain}, "2.1.310/plain-q")
	repeated := []Question{colorTab, toppings, colorTab}
	tests := []struct {
		name string
		plan func() ([]Step, error)
	}{
		{"step on another tab", func() ([]Step, error) { return PlanStep(m3, 1, Answer{Options: []int{0}}, q1) }},
		{"step on the review", func() ([]Step, error) { return PlanStep(m3, 2, Answer{Options: []int{0}}, review) }},
		{"step index past the tabs", func() ([]Step, error) { return PlanStep(m3, 3, Answer{Options: []int{0}}, review) }},
		{"step negative index", func() ([]Step, error) { return PlanStep(m3, -1, Answer{Options: []int{0}}, q1) }},
		{"step invalid answer", func() ([]Step, error) { return PlanStep(m3, 0, Answer{Options: []int{2}}, q1) }},
		{"step text with the commit mark", func() ([]Step, error) { return PlanStep(m3, 0, Answer{Text: "teal ✔"}, q1) }},
		{"step on repeating questions", func() ([]Step, error) { return PlanStep(repeated, 0, Answer{Options: []int{0}}, q1) }},
		{"back from the first tab", func() ([]Step, error) { return PlanBack(m3, 0, q1) }},
		{"back from another tab", func() ([]Step, error) { return PlanBack(m3, 2, q2) }},
		{"back past the review", func() ([]Step, error) { return PlanBack(m3, 4, review) }},
		{"back from a tab while on the review", func() ([]Step, error) { return PlanBack(m3, 2, review) }},
		{"back on a plain dialog", func() ([]Step, error) { return PlanBack([]Question{colorPlain}, 1, plain) }},
		{"back on repeating questions", func() ([]Step, error) { return PlanBack(repeated, 1, q2) }},
		{"submit from a tab", func() ([]Step, error) { return PlanSubmit(m3, []string{"Red", "Cheese", "Large"}, q2) }},
		{"submit with too few answers", func() ([]Step, error) { return PlanSubmit(m3, []string{"Red", "Large"}, review) }},
		{"submit a plain dialog", func() ([]Step, error) { return PlanSubmit([]Question{colorPlain}, []string{"Red"}, plain) }},
		{"submit repeating questions", func() ([]Step, error) {
			return PlanSubmit(repeated, []string{"Red", "Cheese", "Red"}, review)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if steps, err := tt.plan(); err == nil {
				t.Errorf("plan = %d steps, want an error", len(steps))
			}
		})
	}
}
