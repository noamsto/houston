package answer

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func keys(specials ...string) []Key {
	ks := make([]Key, 0, len(specials))
	for _, s := range specials {
		if t, ok := strings.CutPrefix(s, "text:"); ok {
			ks = append(ks, Key{Text: t})
			continue
		}
		ks = append(ks, Key{Special: s})
	}
	return ks
}

func TestPlanKeys(t *testing.T) {
	tests := []struct {
		name string
		qs   []Question
		as   []Answer
		want [][]Key
	}{
		{
			name: "plain option submits at once",
			qs:   []Question{colorPlain},
			as:   []Answer{{Options: []int{1}}},
			want: [][]Key{keys("2")},
		},
		{
			name: "plain Other",
			qs:   []Question{colorPlain},
			as:   []Answer{{Text: "Mauve"}},
			want: [][]Key{keys("4"), keys("text:Mauve"), keys("Enter")},
		},
		{
			name: "leading-digit Other text is typed, not pressed",
			qs:   []Question{colorPlain},
			as:   []Answer{{Text: "2 more"}},
			want: [][]Key{keys("4"), keys("text:2 more"), keys("Enter")},
		},
		{
			name: "tabbed two-question, single option + multi options",
			qs:   []Question{colorTab, toppings},
			as:   []Answer{{Options: []int{1}}, {Options: []int{2, 0}}},
			want: [][]Key{
				keys("2"),
				keys("1", "3"),
				keys("Down", "Down", "Down", "Down"),
				keys("Enter"),
				keys("1"),
			},
		},
		{
			name: "tabbed two-question, single Other + multi option and Other",
			qs:   []Question{colorTab, toppings},
			as:   []Answer{{Text: "Teal"}, {Options: []int{1}, Text: "Pineapple 2"}},
			want: [][]Key{
				keys("3"), keys("text:Teal"), keys("Enter"),
				keys("2"), keys("Down", "Down", "Down"), keys("text:Pineapple 2"), keys("Down"), keys("Enter"),
				keys("1"),
			},
		},
		{
			name: "multi on the first tab, Other only",
			qs:   []Question{toppings, colorTab},
			as:   []Answer{{Text: "Ham"}, {Options: []int{0}}},
			want: [][]Key{
				keys("Down", "Down", "Down"), keys("text:Ham"), keys("Down"), keys("Enter"),
				keys("1"),
				keys("1"),
			},
		},
		{
			name: "single multi-select question still reviews",
			qs:   []Question{toppings},
			as:   []Answer{{Options: []int{0}}},
			want: [][]Key{keys("1"), keys("Down", "Down", "Down", "Down"), keys("Enter"), keys("1")},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			steps, err := Plan(tt.qs, tt.as)
			if err != nil {
				t.Fatal(err)
			}
			got := make([][]Key, 0, len(steps))
			for _, s := range steps {
				if s.Expect == nil {
					t.Fatal("step without Expect")
				}
				got = append(got, s.Keys)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("keys = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPlanDoesNotReorderCallerOptions(t *testing.T) {
	as := []Answer{{Options: []int{2, 0}}}
	if _, err := Plan([]Question{toppings}, as); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(as[0].Options, []int{2, 0}) {
		t.Errorf("options reordered to %v", as[0].Options)
	}
}

func TestPlanErrors(t *testing.T) {
	nine := Question{Text: "Pick", Options: []string{"a", "b", "c", "d", "e", "f", "g", "h", "i"}}
	tests := []struct {
		name string
		qs   []Question
		as   []Answer
	}{
		{"no questions", nil, nil},
		{"fewer answers", []Question{colorTab, toppings}, []Answer{{Options: []int{0}}}},
		{"option out of range", []Question{colorTab}, []Answer{{Options: []int{2}}}},
		{"negative option", []Question{colorTab}, []Answer{{Options: []int{-1}}}},
		{"duplicate option", []Question{toppings}, []Answer{{Options: []int{1, 1}}}},
		{"single with two options", []Question{colorPlain}, []Answer{{Options: []int{0, 1}}}},
		{"single with option and text", []Question{colorPlain}, []Answer{{Options: []int{0}, Text: "x"}}},
		{"single empty", []Question{colorPlain}, []Answer{{}}},
		{"multi empty", []Question{toppings}, []Answer{{}}},
		{"more than 8 options", []Question{nine}, []Answer{{Options: []int{0}}}},
		{"no options", []Question{{Text: "Pick"}}, []Answer{{Text: "x"}}},
		{"empty question", []Question{{Text: " ", Options: []string{"a"}}}, []Answer{{Options: []int{0}}}},
		{"empty label", []Question{{Text: "Pick", Options: []string{"a", " "}}}, []Answer{{Options: []int{0}}}},
		{"blank text", []Question{colorPlain}, []Answer{{Text: "  "}}},
		{"newline in text", []Question{colorPlain}, []Answer{{Text: "a\nb"}}},
		{"escape in text", []Question{toppings}, []Answer{{Text: "a\x1b[A"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if steps, err := Plan(tt.qs, tt.as); err == nil {
				t.Errorf("Plan = %d steps, want an error", len(steps))
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
	if want := []Question{colorTab, toppings}; !reflect.DeepEqual(got, want) {
		t.Errorf("ParseQuestions = %+v, want %+v", got, want)
	}
	for _, bad := range []string{`{"questions":[]}`, `{}`, `[`, `{"questions":"x"}`} {
		if _, err := ParseQuestions(json.RawMessage(bad)); err == nil {
			t.Errorf("ParseQuestions(%s): want an error", bad)
		}
	}
}
