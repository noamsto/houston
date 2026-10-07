package hub

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestQuestionTail(t *testing.T) {
	long := strings.Repeat("é", 299) + "?"
	cases := []struct{ name, in, want string }{
		{"plain", "Ship it?", "Ship it?"},
		{"last line of several", "Done.\n\nWant A or B?", "Want A or B?"},
		{"bold", "**Proceed?**", "Proceed?"},
		{"list marker", "- Merge now?", "Merge now?"},
		{"emphasis and code", "_`Retry?`_", "Retry?"},
		{"question followed by more text", "Which one? Let me know.", ""},
		{"statement", "Fixed it.", ""},
		{"empty", "", ""},
		{"trailing whitespace", "Ship it?  \n\n", "Ship it?"},
		{"question not on the last line", "Ship it?\nNo rush.", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := questionTail(c.in); got != c.want {
				t.Errorf("questionTail(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}

	t.Run("capped at 200 runes", func(t *testing.T) {
		got := questionTail(long)
		if n := utf8.RuneCountInString(got); n != 200 {
			t.Errorf("rune count = %d, want 200", n)
		}
		if !strings.HasSuffix(got, "…") || !utf8.ValidString(got) {
			t.Errorf("got %q, want valid UTF-8 ending in an ellipsis", got)
		}
	})
}

func TestViewSignatureSeesAsks(t *testing.T) {
	a := SessionView{SessionID: "s", Asks: "A or B?"}
	b := SessionView{SessionID: "s"}
	if viewSignature(a) == viewSignature(b) {
		t.Error("views differing only in Asks share a signature")
	}
}
