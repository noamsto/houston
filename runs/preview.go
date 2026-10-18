package runs

import "strings"

// isInjectedTurn reports whether a "last user prompt" is text the harness or
// another agent injected rather than something a human typed: an XML-ish
// envelope (<task-notification>, <agent-message …>, <system-reminder>,
// <command-name>) or an interrupt marker. A human prompt that itself starts
// with an HTML-like tag is filtered too.
func isInjectedTurn(text string) bool {
	text = strings.TrimSpace(text)
	if strings.HasPrefix(text, "[Request interrupted") {
		return true
	}
	rest, ok := strings.CutPrefix(text, "<")
	if !ok || rest == "" || !isASCIILetter(rest[0]) {
		return false
	}
	n := 1
	for n < len(rest) && (isASCIILetter(rest[n]) || rest[n] >= '0' && rest[n] <= '9' || rest[n] == '_' || rest[n] == '-') {
		n++
	}
	if n == len(rest) {
		return false
	}
	switch rest[n] {
	case ' ', '>', '/':
		return true
	}
	return false
}

func isASCIILetter(b byte) bool { return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' }

// previewTask is the first candidate that is non-empty and human-typed.
func previewTask(vals ...string) string {
	for _, v := range vals {
		if v != "" && !isInjectedTurn(v) {
			return v
		}
	}
	return ""
}
