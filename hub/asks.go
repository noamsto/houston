package hub

import "strings"

const maxAsksRunes = 200

// questionTail returns the final line of an assistant message when it ends in
// a question mark, else "". Only the last line counts: a question followed by
// a closing line is deliberately missed rather than guessed at.
func questionTail(text string) string {
	text = strings.TrimSpace(text)
	if i := strings.LastIndexByte(text, '\n'); i >= 0 {
		text = text[i+1:]
	}
	text = strings.TrimPrefix(strings.TrimSpace(text), "- ")
	text = strings.Trim(text, "*_` \t")
	if !strings.HasSuffix(text, "?") {
		return ""
	}
	if r := []rune(text); len(r) > maxAsksRunes {
		return string(r[:maxAsksRunes-1]) + "…"
	}
	return text
}
