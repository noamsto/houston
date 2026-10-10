package answer

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

const maxDetail = 2 << 10

const truncatedNote = "… (truncated — open the Terminal tab)"

// ruleRun is collapsed to one rune before hashing, so a pane resize that only
// redraws the rules at another width keeps the frame.
var ruleRun = regexp.MustCompile(`─+|╌+`)

// Prompt is a permission-style dialog: a question answered by one numbered
// choice.
type Prompt struct {
	Question string
	Choices  []string // labels of choices 1..len(Choices)
	Detail   string   // the dialog text above the question, ≤ 2 KiB, ending in truncatedNote when cut
	Frame    string   // hex sha256 of the dialog's frameText, command included
}

// PermissionPrompt parses the permission-style dialog at the bottom of
// capture: the lines after the last "─" dialog rule, ending in an "Esc to
// cancel" footer, with consecutive choices 1..m (2 ≤ m ≤ 9), one under the cursor,
// below a line ending in "?". A question dialog is never one.
func PermissionPrompt(capture string) (Prompt, bool) {
	lines := captureLines(capture)
	width := anchorWidth(lines)
	top := -1
	for i := len(lines) - 1; i >= 0; i-- {
		if isAnchorRule(lines[i], width) {
			top = i
			break
		}
	}
	if top < 0 {
		return Prompt{}, false
	}
	region := lines[top+1:]

	end := len(region)
	for end > 0 && isBlank(region[end-1]) {
		end--
	}
	footer := end
	for footer > 0 && !isBlank(region[footer-1]) {
		footer--
	}
	if n := end - footer; n == 0 || n > 2 || !strings.HasPrefix(Normalize(strings.Join(region[footer:end], "\n")), "Esctocancel") {
		return Prompt{}, false
	}
	choicesEnd := footer
	for choicesEnd > 0 && isBlank(region[choicesEnd-1]) {
		choicesEnd--
	}

	first := -1
	for i := choicesEnd - 1; i >= 0; i-- {
		if c, ok := parseChoice(region[i]); ok && c.num == 1 {
			first = i
			break
		}
	}
	if first < 0 {
		return Prompt{}, false
	}
	choices, ok := parseChoices(region[first:choicesEnd])
	if !ok {
		return Prompt{}, false
	}

	qEnd := first - 1
	for qEnd >= 0 && isBlank(region[qEnd]) {
		qEnd--
	}
	if qEnd < 0 || !strings.HasSuffix(strings.TrimSpace(region[qEnd]), "?") {
		return Prompt{}, false
	}
	qStart := qEnd
	for qStart > 0 && !isBlank(region[qStart-1]) && !isRuleLine(region[qStart-1]) {
		qStart--
	}

	sum := sha256.Sum256([]byte(frameText(region)))
	return Prompt{
		Question: joinTrimmed(region[qStart:qEnd+1], " "),
		Choices:  choices,
		Detail:   detail(region[:qStart]),
		Frame:    hex.EncodeToString(sum[:]),
	}, true
}

type choice struct {
	num    int
	label  string
	cursor bool
}

// parseChoice reads a "k. label" row, optionally under the "❯" cursor.
func parseChoice(line string) (choice, bool) {
	s := strings.TrimLeftFunc(line, unicode.IsSpace)
	rest, cursor := strings.CutPrefix(s, cursorMark)
	if cursor {
		rest = strings.TrimLeftFunc(rest, unicode.IsSpace)
	}
	if len(rest) < 3 || rest[0] < '1' || rest[0] > '9' || rest[1] != '.' {
		return choice{}, false
	}
	if r, _ := utf8.DecodeRuneInString(rest[2:]); !unicode.IsSpace(r) {
		return choice{}, false
	}
	label := strings.TrimSpace(rest[2:])
	if label == "" {
		return choice{}, false
	}
	return choice{num: int(rest[0] - '0'), label: label, cursor: cursor}, true
}

// parseChoices reads choices 1..m from lines, joining each wrapped
// continuation line onto its choice's label.
func parseChoices(lines []string) ([]string, bool) {
	labels := make([]string, 0, len(lines))
	cursors := 0
	for _, l := range lines {
		if isBlank(l) {
			return nil, false
		}
		c, ok := parseChoice(l)
		if !ok {
			if len(labels) == 0 || strings.Contains(l, cursorMark) {
				return nil, false
			}
			labels[len(labels)-1] += " " + strings.TrimSpace(l)
			continue
		}
		if c.num != len(labels)+1 {
			return nil, false
		}
		if c.cursor {
			cursors++
		}
		labels = append(labels, c.label)
	}
	if len(labels) < 2 || cursors != 1 {
		return nil, false
	}
	for _, l := range labels {
		if n := Normalize(l); strings.HasPrefix(n, "Typesomething") || n == "Chataboutthis" {
			return nil, false
		}
	}
	return labels, true
}

func isBlank(line string) bool { return strings.TrimSpace(line) == "" }

func isRuleLine(line string) bool { return isDialogRule(line, "─╌") }

// frameText is what the frame hashes: the region's non-blank lines, each
// line's leading "│" dropped and its words separated by single spaces, one
// line per row, so a space or line break that moves inside a command changes
// the frame.
func frameText(region []string) string {
	var lines []string
	for _, l := range region {
		l = strings.TrimPrefix(strings.TrimLeftFunc(l, unicode.IsSpace), "│")
		if words := strings.Fields(l); len(words) > 0 {
			lines = append(lines, strings.Join(words, " "))
		}
	}
	return ruleRun.ReplaceAllStringFunc(strings.Join(lines, "\n"), func(run string) string {
		r, _ := utf8.DecodeRuneInString(run)
		return string(r)
	})
}

func joinTrimmed(lines []string, sep string) string {
	parts := make([]string, 0, len(lines))
	for _, l := range lines {
		parts = append(parts, strings.TrimSpace(l))
	}
	return strings.Join(parts, sep)
}

// detail is the dialog text above the question without blank or rule lines,
// cut on a rune boundary to fit maxDetail with truncatedNote as its last line.
func detail(lines []string) string {
	var kept []string
	for _, l := range lines {
		if !isBlank(l) && !isRuleLine(l) {
			kept = append(kept, strings.TrimSpace(l))
		}
	}
	s := strings.Join(kept, "\n")
	if len(s) <= maxDetail {
		return s
	}
	cut := maxDetail - len(truncatedNote) - 1
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "\n" + truncatedNote
}
