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

func ruleWidth(line string) int {
	return utf8.RuneCountInString(strings.TrimRightFunc(line, unicode.IsSpace))
}

// isAnchorRule reports whether a capture line is a dialog's "─" rule: drawn
// from column 0 across the whole pane. A dialog's own rule spans the pane, while
// a short rule run spilled into its text falls short of it, so no shorter rule
// may stand in: a capture with none (a dialog taller than the capture) holds no
// dialog. Rule lines hold only "─", so their rune count is their width in cells.
func isAnchorRule(line string, width int) bool {
	return width > 0 && isDialogRule(line, "─") && ruleWidth(line) >= width
}

// isSessionRule reports whether a capture line is the top rule of the input
// box of a named session (claude --name): rule runes, then the name between
// spaces, then a closing " ─". A narrow pane shrinks the leading rule to
// nothing and cuts the name with "…". It takes the ANSI-stripped line, not
// Normalize's output, which has no spaces left.
func isSessionRule(line string) bool {
	body, ok := strings.CutSuffix(strings.TrimRightFunc(line, unicode.IsSpace), " ─")
	if !ok {
		return false
	}
	name, ok := strings.CutPrefix(strings.TrimLeft(body, "─"), " ")
	if !ok || name == "" {
		return false
	}
	r, _ := utf8.DecodeRuneInString(name)
	return !unicode.IsSpace(r) && r != '─'
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
// Each later dialog rule's norm is dialogRule. width is the pane's width in
// cells. A named session's input-box rule (see isSessionRule) as the last line
// of the capture belongs to no dialog and is dropped.
func dialogLines(capture string, width int) ([]dialogLine, bool) {
	raw := captureLines(capture)
	lines := make([]dialogLine, 0, len(raw))
	for _, l := range raw {
		if isAnchorRule(l, width) {
			lines = append(lines, dialogLine{l, dialogRule})
		} else if n := Normalize(l); n != "" {
			lines = append(lines, dialogLine{l, n})
		}
	}
	if n := len(lines); n > 0 && isSessionRule(lines[n-1].raw) {
		lines = lines[:n-1]
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

// previewLeft returns the label column of a preview layout's option area:
// each line cut at the preview box's left border (a box rune after
// whitespace), so the preview itself is never compared, and the empty notes
// hint dropped as the area's last line. Typed notes stay and fail the match.
func previewLeft(area []dialogLine) []dialogLine {
	var out []dialogLine
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
			out = append(out, dialogLine{left, n})
		}
	}
	if len(out) > 0 && out[len(out)-1].norm == previewNotes {
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
	marked  []string // labels with the commit mark "✔" after the label, before the description
	multi   bool
	preview bool   // the preview layout: label rows, no Other row, unnumbered "Chat about this"
	index   int    // position of the question's tab
	final   string // the unnumbered row of a multi-select tab: "Next" or "Submit"
}

func newSpec(qs []Question, i int) questionSpec {
	q := qs[i]
	s := questionSpec{
		text: Normalize(q.Text), labels: optionRows(q), multi: q.MultiSelect, preview: q.PreviewLayout(),
		index: i, final: "Next",
	}
	if i == len(qs)-1 {
		s.final = "Submit"
	}
	for k, o := range q.Options {
		s.marked = append(s.marked, Normalize(o.Label)+"✔"+strings.TrimPrefix(s.labels[k], Normalize(o.Label)))
	}
	return s
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

// isTail reports whether lines are the "Chat about this" row and a footer of
// one to four lines (a narrow pane wraps it; see isFooter). The preview layout
// leaves the row unnumbered.
func (s questionSpec) isTail(lines []string) bool {
	chat := strconv.Itoa(len(s.labels)+2) + ".Chataboutthis"
	if s.preview {
		chat = "Chataboutthis"
	}
	if len(lines) < 2 || len(lines) > 5 || lines[0] != chat {
		return false
	}
	return isFooter(strings.Join(lines[1:], ""))
}

// isFooter reports whether a normalized footer has one of the two shapes a
// question tab shows. Normal: "Enter to select · <nav>[ · n to add notes]
// [ · Tab to switch questions][ · ctrl+g to edit in <E>] · Esc to cancel".
// Vim editor mode while the Other field has focus: "-- INSERT -- · Enter to
// select · <nav>[ · ctrl+g to edit in <E>]". Any other mode (vim's
// "-- NORMAL --") changes what keys do, so it fails.
func isFooter(footer string) bool {
	if rest, ok := strings.CutPrefix(footer, "--INSERT--·Entertoselect·"); ok {
		rest, ok = cutNav(rest)
		return ok && (rest == "" || isEditHint(rest))
	}
	rest, ok := strings.CutPrefix(footer, "Entertoselect·")
	if !ok {
		return false
	}
	if rest, ok = cutNav(rest); !ok {
		return false
	}
	if rest, ok = strings.CutSuffix(rest, "·Esctocancel"); !ok {
		return false
	}
	rest = strings.TrimPrefix(rest, "·ntoaddnotes")
	rest = strings.TrimPrefix(rest, "·Tabtoswitchquestions")
	return rest == "" || isEditHint(rest)
}

func cutNav(s string) (string, bool) {
	for _, nav := range []string{"Tab/Arrowkeystonavigate", "↑/↓tonavigate"} {
		if rest, ok := strings.CutPrefix(s, nav); ok {
			return rest, true
		}
	}
	return "", false
}

// isEditHint reports whether s is "· ctrl+g to edit in <E>", normalized, for
// one editor name E.
func isEditHint(s string) bool {
	editor, ok := strings.CutPrefix(s, "·ctrl+gtoeditin")
	return ok && editor != "" && !strings.Contains(editor, "·")
}

func sortedOptions(a Answer) []int {
	opts := slices.Clone(a.Options)
	slices.Sort(opts)
	return opts
}

// commitMark follows the label of a tab's committed option, or ends a line of
// its committed Other row, once the tab is re-entered.
const commitMark = "✔"

// View is what the pane shows of one AskUserQuestion call: one question tab,
// or the review of a tabbed dialog.
type View struct {
	Step    int      // the tab shown: an index into the questions, or len(qs) on the review
	Cursor  int      // tab: the 1-based row under the cursor (Other is n+1), 0 = a multi-select's Next/Submit row; review: 1 Submit, 2 Cancel
	Options []int    // committed (single-select, preview: the ✔ row) or ticked (multi-select) options, ascending
	Text    string   // committed Other text, wrapped lines joined by one space; "" for none
	Review  []string // on the review: per question, the answer after "→", wrapped lines joined by one space

	boxes      []bool // per question, whether its tab-bar box is ticked; nil on a plain dialog
	other      string // the Other row's visible text, normalized, committed or not; "" for the placeholder
	otherLines int    // lines the Other row spans; the focused field wraps narrower, so it varies
	committed  bool   // the Other row is the tab's answer, possibly empty ("Type something. ✔")
}

// ReadView reads the question tab or review of qs at the bottom of capture.
// It fails unless the dialog is exactly one of qs's tabs or their review:
// each row spelled exactly, one cursor, at most one commit mark and a known
// footer (see isFooter). A tab is identified by its text and rows, so callers
// must refuse questions that repeat (see Repeats).
func ReadView(qs []Question, capture string, width int) (View, bool) {
	lines, ok := dialogLines(capture, width)
	if !ok || isTabBar(lines[0].norm) != Tabbed(qs) {
		return View{}, false
	}
	var boxes []bool
	if Tabbed(qs) {
		if boxes = tabBoxes(lines[0].norm); len(boxes) != len(qs) {
			return View{}, false
		}
		if len(lines) > 1 && lines[1].norm == "Reviewyouranswers" {
			return readReview(qs, lines[2:], boxes)
		}
	}
	for i := range qs {
		if v, ok := readTab(newSpec(qs, i), lines, boxes); ok {
			return v, true
		}
	}
	return View{}, false
}

// readTab reads lines (a tab bar or header line first) as the tab of s.
func readTab(s questionSpec, lines []dialogLine, boxes []bool) (View, bool) {
	body, ok := consumeText(norms(lines[1:]), s.text)
	if !ok {
		return View{}, false
	}
	rule := slices.Index(body, dialogRule)
	if rule < 0 || !s.isTail(body[rule+1:]) {
		return View{}, false
	}
	start := len(lines) - len(body)
	area := lines[start : start+rule]
	v := View{Step: s.index, boxes: boxes}
	if s.preview {
		ok = s.readPreview(previewLeft(area), &v)
	} else {
		ok = s.readRows(area, &v)
	}
	return v, ok
}

// readPreview reads the preview layout's label column: label rows, each
// "label" or "label ✔".
func (s questionSpec) readPreview(area []dialogLine, v *View) bool {
	rows, ok := numberedRows(area, len(s.labels))
	if !ok {
		return false
	}
	cursors := 0
	for k, lines := range rows {
		r := rowText(lines, k+1)
		if r.cursor {
			cursors++
			v.Cursor = k + 1
		}
		switch r.text {
		case s.labels[k]:
		case s.marked[k]:
			v.Options = append(v.Options, k)
		default:
			return false
		}
	}
	return cursors == 1 && len(v.Options) <= 1
}

// readRows reads a tab's option rows, its Other row and, for a multi-select,
// its Next/Submit row.
func (s questionSpec) readRows(area []dialogLine, v *View) bool {
	cursors := 0
	if s.multi {
		if len(area) == 0 {
			return false
		}
		final := cursorRow(area[len(area)-1].norm)
		if final.text != s.final {
			return false
		}
		if final.cursor {
			cursors++
		}
		area = area[:len(area)-1]
	}
	n := len(s.labels)
	rows, ok := numberedRows(area, n+1)
	if !ok {
		return false
	}
	for k, label := range s.labels {
		r := rowText(rows[k], k+1)
		if r.cursor {
			cursors++
			v.Cursor = k + 1
		}
		if s.multi {
			ticked, text, ok := cutBox(r.text)
			if !ok || text != label {
				return false
			}
			if ticked {
				v.Options = append(v.Options, k)
			}
			continue
		}
		switch r.text {
		case label:
		case s.marked[k]:
			v.Options = append(v.Options, k)
		default:
			return false
		}
	}
	if rowText(rows[n], n+1).cursor {
		cursors++
		v.Cursor = n + 1
	}
	text, ok := s.readOther(rows[n], len(v.Options) > 0, v)
	if !ok || cursors != 1 {
		return false
	}
	if v.committed && v.other != "" {
		v.Text = text
	}
	return true
}

// readOther reads the Other row (row n+1): the placeholder or free text,
// behind a box on a multi-select, with a commit mark ending one of its lines on
// a single-select. It sets v's Other fields and returns the visible text as
// typed, each line trimmed and joined by one space. marked reports whether a
// single-select option row carries the commit mark.
func (s questionSpec) readOther(lines []dialogLine, marked bool, v *View) (string, bool) {
	ticked, mark := false, false
	parts := make([]string, len(lines))
	for j, l := range lines {
		p := strings.TrimSpace(l.raw)
		if j == 0 {
			p = strings.TrimSpace(strings.TrimPrefix(p, cursorMark))
			p = strings.TrimSpace(strings.TrimPrefix(p, strconv.Itoa(len(s.labels)+1)+"."))
			if s.multi {
				var ok bool
				if ticked, p, ok = cutBox(p); !ok {
					return "", false
				}
				p = strings.TrimSpace(p)
			}
		}
		if cut, ok := strings.CutSuffix(p, commitMark); ok && !s.multi {
			if mark || marked {
				return "", false
			}
			mark, p = true, strings.TrimSpace(cut)
		}
		parts[j] = p
	}
	text := strings.Join(slices.DeleteFunc(parts, func(p string) bool { return p == "" }), " ")
	placeholder := "Typesomething."
	if s.multi {
		placeholder = "Typesomething"
	}
	switch norm := Normalize(text); norm {
	case "":
		return "", false
	case placeholder:
		text = ""
	default:
		v.other = norm
	}
	v.otherLines = len(lines)
	switch {
	case s.multi:
		v.committed = ticked
	case mark:
		v.committed = true
	default:
		// A wrapped committed Other loses its mark on re-entry; the tab's
		// ticked box with no option marked still shows it is the answer.
		v.committed = v.boxes != nil && v.boxes[s.index] && !marked && v.other != ""
	}
	return text, true
}

// cutBox cuts a multi-select row's leading box, "[ ]" or "[✔]" (normalized
// "[]" when empty), and reports whether it is ticked.
func cutBox(text string) (ticked bool, rest string, ok bool) {
	if rest, ok := strings.CutPrefix(text, "[✔]"); ok {
		return true, rest, true
	}
	for _, empty := range []string{"[]", "[ ]"} {
		if rest, ok := strings.CutPrefix(text, empty); ok {
			return false, rest, true
		}
	}
	return false, "", false
}

// numberedRows groups area into count rows, each starting "N." after an
// optional cursor, with the lines that wrap or describe it.
func numberedRows(area []dialogLine, count int) ([][]dialogLine, bool) {
	rows := make([][]dialogLine, 0, count)
	for _, l := range area {
		if len(rows) < count && strings.HasPrefix(cursorRow(l.norm).text, strconv.Itoa(len(rows)+1)+".") {
			rows = append(rows, []dialogLine{l})
			continue
		}
		if len(rows) == 0 {
			return nil, false
		}
		rows[len(rows)-1] = append(rows[len(rows)-1], l)
	}
	return rows, len(rows) == count
}

// rowText is row k's normalized text across its lines, without its cursor
// and number.
func rowText(lines []dialogLine, k int) row {
	r := cursorRow(lines[0].norm)
	r.text = strings.TrimPrefix(r.text, strconv.Itoa(k)+".")
	for _, l := range lines[1:] {
		r.text += l.norm
	}
	return r
}

// readReview reads the review below its tab bar: every box ticked, each
// question in order with its answer after "→", and the submit choices.
func readReview(qs []Question, lines []dialogLine, boxes []bool) (View, bool) {
	if slices.Contains(boxes, false) {
		return View{}, false
	}
	const ready = "Readytosubmityouranswers?"
	v := View{Step: len(qs), boxes: boxes}
	for _, q := range qs {
		rest, ok := consumeText(norms(lines), "●"+Normalize(q.Text))
		if !ok {
			return View{}, false
		}
		lines = lines[len(lines)-len(rest):]
		end := slices.IndexFunc(lines, func(l dialogLine) bool { return strings.HasPrefix(l.norm, "●") || l.norm == ready })
		if end < 1 || !strings.HasPrefix(lines[0].norm, "→") {
			return View{}, false
		}
		parts := make([]string, end)
		for j, l := range lines[:end] {
			parts[j] = strings.TrimSpace(l.raw)
		}
		parts[0] = strings.TrimSpace(strings.TrimPrefix(parts[0], "→"))
		v.Review = append(v.Review, strings.Join(slices.DeleteFunc(parts, func(p string) bool { return p == "" }), " "))
		lines = lines[end:]
	}
	if len(lines) != 3 || lines[0].norm != ready {
		return View{}, false
	}
	submit, cancel := cursorRow(lines[1].norm), cursorRow(lines[2].norm)
	if submit.text != "1.Submitanswers" || cancel.text != "2.Cancel" || submit.cursor == cancel.cursor {
		return View{}, false
	}
	v.Cursor = 1
	if cancel.cursor {
		v.Cursor = 2
	}
	return v, true
}

// viewIs checks that the pane shows exactly v: every field but the Other
// row's line count, which changes as the field gains or loses focus. Text and
// review answers compare normalized, since a focused field wraps narrower.
func viewIs(qs []Question, v View) Check {
	return func(capture string, width int) bool {
		got, ok := ReadView(qs, capture, width)
		return ok && sameView(got, v)
	}
}

func sameView(got, want View) bool {
	return got.Step == want.Step && got.Cursor == want.Cursor && slices.Equal(got.Options, want.Options) &&
		Normalize(got.Text) == Normalize(want.Text) &&
		slices.EqualFunc(got.Review, want.Review, func(g, w string) bool { return Normalize(g) == Normalize(w) }) &&
		slices.Equal(got.boxes, want.boxes) && got.other == want.other && got.committed == want.committed
}
