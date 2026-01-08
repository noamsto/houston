package ansi

import "testing"

func TestStrip(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "full ANSI sequence",
			input:    "\x1b[32mgreen text\x1b[0m",
			expected: "green text",
		},
		{
			name:     "tmux ESC symbol sequence",
			input:    "␛[32mgreen text␛[0m",
			expected: "green text",
		},
		{
			name:     "mixed ESC formats",
			input:    "\x1b[1mbold␛[0m normal",
			expected: "bold normal",
		},
		{
			name:     "no ANSI codes",
			input:    "plain text [not ansi]",
			expected: "plain text [not ansi]",
		},
		{
			name:     "brackets with letters (not SGR)",
			input:    "[A] option [B] choice",
			expected: "[A] option [B] choice",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Strip(tt.input)
			if got != tt.expected {
				t.Errorf("Strip(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestStripOrphaned(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "full ANSI sequence",
			input:    "\x1b[32mgreen text\x1b[0m",
			expected: "green text",
		},
		{
			name:     "tmux ESC symbol sequence",
			input:    "␛[32mgreen text␛[0m",
			expected: "green text",
		},
		{
			name:     "orphaned SGR sequence",
			input:    "[39m Done...",
			expected: " Done...",
		},
		{
			name:     "mixed sequences",
			input:    "\x1b[1mbold[0m normal [39mtext",
			expected: "bold normal text",
		},
		{
			name:     "mixed with tmux symbol",
			input:    "␛[2m␛[38;2;205;214;244m text ␛[0m",
			expected: " text ",
		},
		{
			name:     "complex color codes",
			input:    "[0;1;32m bright green [0m reset",
			expected: " bright green  reset",
		},
		{
			name:     "no ANSI codes",
			input:    "plain text [not ansi]",
			expected: "plain text [not ansi]",
		},
		{
			name:     "brackets with letters (not SGR)",
			input:    "[A] option [B] choice",
			expected: "[A] option [B] choice",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := StripOrphaned(tt.input)
			if got != tt.expected {
				t.Errorf("StripOrphaned(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}
