// Package answer models Claude Code's question and permission dialogs as they
// appear in a tmux capture, and plans the keystrokes that answer them.
//
// Every check compares normalized text (see Normalize) inside the dialog at the
// bottom of the pane, never against scrollback, which routinely repeats the
// question text in the user's prompt and in earlier answer summaries.
package answer

import (
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/noamsto/houston/internal/ansi"
)

const cursorMark = "❯"

// dialogRule stands for a dialog's own rule among dialogLines' normalized
// lines. Normalize deletes every space, so no capture line can spell it.
const dialogRule = "── dialog rule ──"

// Normalize drops a leading "│" from each line and deletes every Unicode
// whitespace rune, so Claude's hard wrapping never affects a comparison.
func Normalize(s string) string {
	var b strings.Builder
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimLeftFunc(line, unicode.IsSpace)
		line = strings.TrimPrefix(line, "│")
		for _, r := range line {
			if !unicode.IsSpace(r) {
				b.WriteRune(r)
			}
		}
	}
	return b.String()
}

func captureLines(capture string) []string {
	return strings.Split(ansi.Strip(ansi.OSC8Pattern.ReplaceAllString(capture, "")), "\n")
}

func isRule(line, runes string) bool {
	return line != "" && strings.Trim(line, runes) == ""
}

// isDialogRule reports whether a capture line is one of a dialog's own rules,
// which Claude draws from column 0. The text a dialog frames is indented, so a
// command or description line made of rule runes never passes.
func isDialogRule(line, runes string) bool {
	return isRule(strings.TrimRightFunc(line, unicode.IsSpace), runes)
}

// anchorWidth is the widest column-0 "─" rule in a capture, in runes. A
// dialog's own rule spans the pane, so it sets the width, while a short rule
// run spilled into its text falls short of it. Rule lines hold only "─", so
// their rune count is their width in cells.
func anchorWidth(lines []string) int {
	w := 0
	for _, l := range lines {
		if isDialogRule(l, "─") {
			w = max(w, ruleWidth(l))
		}
	}
	return w
}

func ruleWidth(line string) int {
	return utf8.RuneCountInString(strings.TrimRightFunc(line, unicode.IsSpace))
}

// isAnchorRule reports whether a capture line is a dialog's "─" rule: drawn
// from column 0 as wide as the capture's widest rule.
func isAnchorRule(line string, width int) bool {
	return isDialogRule(line, "─") && ruleWidth(line) >= width
}

func isTabBar(line string) bool {
	return strings.HasPrefix(line, "←") && strings.HasSuffix(line, "✔Submit→")
}

func isHeader(line string) bool {
	return strings.HasPrefix(line, "☐") || strings.HasPrefix(line, "☒")
}

// dialogLine is one non-blank line of a dialog: the capture line and its
// normalized text.
type dialogLine struct {
	raw, norm string
}

// dialogLines returns the non-blank lines of the question dialog at the
// bottom of capture, starting with its tab bar or header line: the lines after
// the last "─" dialog rule (see isAnchorRule) that is directly followed by one.
// Each later dialog rule's norm is dialogRule.
func dialogLines(capture string) ([]dialogLine, bool) {
	raw := captureLines(capture)
	width := anchorWidth(raw)
	lines := make([]dialogLine, 0, len(raw))
	for _, l := range raw {
		if isAnchorRule(l, width) {
			lines = append(lines, dialogLine{l, dialogRule})
		} else if n := Normalize(l); n != "" {
			lines = append(lines, dialogLine{l, n})
		}
	}
	for i := len(lines) - 2; i >= 0; i-- {
		if lines[i].norm == dialogRule && (isTabBar(lines[i+1].norm) || isHeader(lines[i+1].norm)) {
			return lines[i+1:], true
		}
	}
	return nil, false
}

func norms(lines []dialogLine) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = l.norm
	}
	return out
}

// previewNotes is the hint under an empty preview notes field.
const previewNotes = "Notes:pressntoaddnotes"

// previewLeft returns the normalized label column of a preview layout's
// option area: each line cut at the preview box's left border (a box rune
// after whitespace), so the preview itself is never compared, and the empty
// notes hint dropped as the area's last line. Typed notes stay and fail the
// match.
func previewLeft(area []dialogLine) []string {
	var out []string
	for _, l := range area {
		left := l.raw
		var prev rune
		for i, r := range l.raw {
			if strings.ContainsRune("┌│└", r) && unicode.IsSpace(prev) {
				left = l.raw[:i]
				break
			}
			prev = r
		}
		if n := Normalize(left); n != "" {
			out = append(out, n)
		}
	}
	if len(out) > 0 && out[len(out)-1] == previewNotes {
		out = out[:len(out)-1]
	}
	return out
}

// row is one numbered option row (or the unnumbered Next/Submit row) with its
// wrapped continuation and description lines appended.
type row struct {
	cursor bool
	text   string
}

func cursorRow(line string) row {
	text, ok := strings.CutPrefix(line, cursorMark)
	return row{cursor: ok, text: text}
}

// questionSpec is one question as its tab must render it, normalized.
type questionSpec struct {
	text    string
	labels  []string // each option's label followed by its description (label only in the preview layout)
	multi   bool
	preview bool // the preview layout: label rows, no Other row, unnumbered "Chat about this"
	tabbed  bool
	index   int    // position of the question's tab
	count   int    // number of question tabs
	final   string // the unnumbered row of a multi-select tab: "Next" or "Submit"
}

// tabState is what a question tab must show at one point of the answer.
type tabState struct {
	cursor  int    // numbered row under the cursor (1-based); 0 = the Next/Submit row
	checked []int  // 0-based options whose box is ticked (multi-select)
	other   string // normalized text in the Other field; "" = empty
	touched bool   // keys were sent on this tab, so a multi-select tab-bar box may be ticked
}

func newSpec(qs []Question, i int, tabbed bool) questionSpec {
	q := qs[i]
	s := questionSpec{text: Normalize(q.Text), multi: q.MultiSelect, preview: q.PreviewLayout(), tabbed: tabbed, index: i, count: len(qs), final: "Next"}
	if i == len(qs)-1 {
		s.final = "Submit"
	}
	for _, o := range q.Options {
		label := Normalize(o.Label)
		if !s.preview {
			label += Normalize(o.Description)
		}
		s.labels = append(s.labels, label)
	}
	return s
}

func (s questionSpec) check(want tabState) Check {
	return func(capture string) bool {
		bar, rows, final, ok := s.parse(capture)
		return ok && s.barMatches(bar, want.touched) && s.matches(rows, final, want)
	}
}

// parse splits the tab into its tab bar or header line, its option rows
// (options, then Other unless in the preview layout) and, for a multi-select,
// the Next/Submit row. The tab must hold exactly this question, followed by a
// rule, the "Chat about this" row and the footer, so a dialog that is not at
// the bottom of the pane never parses.
func (s questionSpec) parse(capture string) (string, []row, row, bool) {
	lines, ok := dialogLines(capture)
	if !ok || isTabBar(lines[0].norm) != s.tabbed {
		return "", nil, row{}, false
	}
	body, ok := consumeText(norms(lines[1:]), s.text)
	if !ok {
		return "", nil, row{}, false
	}
	rule := slices.Index(body, dialogRule)
	if rule < 0 || !s.isTail(body[rule+1:]) {
		return "", nil, row{}, false
	}
	if s.preview {
		start := len(lines) - len(body)
		rows, ok := splitRows(previewLeft(lines[start:start+rule]), len(s.labels))
		return lines[0].norm, rows, row{}, ok
	}
	area := body[:rule]
	var final row
	if s.multi {
		if len(area) == 0 {
			return "", nil, row{}, false
		}
		final = cursorRow(area[len(area)-1])
		area = area[:len(area)-1]
	}
	rows, ok := splitRows(area, len(s.labels)+1)
	return lines[0].norm, rows, final, ok
}

// barMatches checks the tab bar's answered boxes: one per question, ticked
// before this tab and clear after it, so a dialog whose questions merely look
// alike (same text, another position or count) does not match. This tab's own
// box stays clear while a single-select is answered, but ticks once a
// multi-select box is, so there it is only required clear before any key.
func (s questionSpec) barMatches(bar string, touched bool) bool {
	if !s.tabbed {
		return true
	}
	boxes := tabBoxes(bar)
	if len(boxes) != s.count {
		return false
	}
	for j, answered := range boxes {
		if (j < s.index && !answered) || (j > s.index && answered) || (j == s.index && answered && (!touched || !s.multi)) {
			return false
		}
	}
	return true
}

// tabBoxes returns, per tab of a tab bar, whether its box is ticked.
func tabBoxes(bar string) []bool {
	var boxes []bool
	for _, r := range bar {
		switch r {
		case '☐':
			boxes = append(boxes, false)
		case '☒':
			boxes = append(boxes, true)
		}
	}
	return boxes
}

// consumeText consumes the lines that spell want exactly, so a wrapped
// question matches however Claude broke it.
func consumeText(lines []string, want string) ([]string, bool) {
	got := ""
	for i, l := range lines {
		got += l
		if got == want {
			return lines[i+1:], true
		}
		if !strings.HasPrefix(want, got) {
			return nil, false
		}
	}
	return nil, false
}

// isTail reports whether lines are the "Chat about this" row and a footer
// ending in "Esc to cancel" — at most three lines, since a narrow pane wraps it.
// The preview layout leaves the row unnumbered.
func (s questionSpec) isTail(lines []string) bool {
	chat := strconv.Itoa(len(s.labels)+2) + ".Chataboutthis"
	if s.preview {
		chat = "Chataboutthis"
	}
	if len(lines) < 2 || len(lines) > 4 || lines[0] != chat {
		return false
	}
	footer := strings.Join(lines[1:], "")
	return strings.HasPrefix(footer, "Entertoselect") && strings.HasSuffix(footer, "Esctocancel")
}

func splitRows(area []string, count int) ([]row, bool) {
	var rows []row
	for _, l := range area {
		r := cursorRow(l)
		if len(rows) < count {
			if text, ok := strings.CutPrefix(r.text, strconv.Itoa(len(rows)+1)+"."); ok {
				rows = append(rows, row{cursor: r.cursor, text: text})
				continue
			}
		}
		if len(rows) == 0 {
			return nil, false
		}
		rows[len(rows)-1].text += l
	}
	return rows, len(rows) == count
}

func (s questionSpec) matches(rows []row, final row, want tabState) bool {
	for k, label := range s.labels {
		text := rows[k].text
		if s.multi {
			box := "[]"
			if slices.Contains(want.checked, k) {
				box = "[✔]"
			}
			var ok bool
			if text, ok = strings.CutPrefix(text, box); !ok {
				return false
			}
		}
		if rows[k].cursor != (want.cursor == k+1) || text != label {
			return false
		}
	}
	n := len(s.labels)
	if s.preview {
		return true
	}
	if rows[n].cursor != (want.cursor == n+1) || rows[n].text != s.otherRow(want.other) {
		return false
	}
	return !s.multi || (final.cursor == (want.cursor == 0) && final.text == s.final)
}

// otherRow is the Other row's text: a placeholder while the field is empty,
// and a ticked box once typing has filled it (multi-select).
func (s questionSpec) otherRow(typed string) string {
	switch {
	case !s.multi && typed == "":
		return "Typesomething."
	case !s.multi:
		return typed
	case typed == "":
		return "[]Typesomething"
	default:
		return "[✔]" + typed
	}
}

// reviewCheck matches the review step of a tabbed dialog: every question
// followed by its answer as Claude summarises it, and the submit choices.
func reviewCheck(qs []Question, as []Answer) Check {
	var b strings.Builder
	b.WriteString("Reviewyouranswers")
	for i, q := range qs {
		b.WriteString("●" + Normalize(q.Text) + "→" + Normalize(reviewAnswer(q, as[i])))
	}
	b.WriteString("Readytosubmityouranswers?" + cursorMark + "1.Submitanswers2.Cancel")
	want := b.String()
	answered := slices.Repeat([]bool{true}, len(qs))
	return func(capture string) bool {
		lines, ok := dialogLines(capture)
		return ok && isTabBar(lines[0].norm) && slices.Equal(tabBoxes(lines[0].norm), answered) &&
			strings.Join(norms(lines[1:]), "") == want
	}
}

// reviewAnswer is the chosen labels in option order, then the Other text.
func reviewAnswer(q Question, a Answer) string {
	var parts []string
	for _, o := range sortedOptions(a) {
		parts = append(parts, q.Options[o].Label)
	}
	if a.Text != "" {
		parts = append(parts, a.Text)
	}
	return strings.Join(parts, ", ")
}

func sortedOptions(a Answer) []int {
	opts := slices.Clone(a.Options)
	slices.Sort(opts)
	return opts
}
