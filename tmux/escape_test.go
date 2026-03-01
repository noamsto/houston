package tmux

import "testing"

func TestUnescapeOctal(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"plain text", "hello world", "hello world"},
		{"newline CR LF", `hello\015\012world`, "hello\r\nworld"},
		{"backslash", `path\134to\134file`, `path\to\file`},
		{"tab", `col1\011col2`, "col1\tcol2"},
		{"escape char", `\033[32mgreen\033[0m`, "\033[32mgreen\033[0m"},
		{"mixed", `ls /\015\015\012bin/ dev/`, "ls /\r\r\nbin/ dev/"},
		{"empty", "", ""},
		{"no escapes", "just plain text 123", "just plain text 123"},
		{"backslash at end", `trailing\134`, `trailing\`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := UnescapeOctal(tt.input)
			if got != tt.want {
				t.Errorf("UnescapeOctal(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
