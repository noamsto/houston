package claude

import (
	"strings"
	"testing"

	"github.com/noamsto/houston/parser"
)

func TestIsStatusLine(t *testing.T) {
	tests := []struct {
		line string
		want bool
	}{
		{"-- INSERT --", true},
		{"-- NORMAL --", true},
		{"🤖 Sonnet 4.5", true},
		{"📊 50k/200k", true},
		{"⏱️ 0.5h", true},
		{"💬 43 msgs", true},
		{"❄ impure", true},
		{"📂 ~/project", true},
		{"accept edits on", true},
		{"────────────────────────────────────────────────────────────────────────────────", true},
		{"Hello, how can I help?", false},
		{"$ ls -la", false},
		{"", false},
		{"   ", false},
	}

	for _, tt := range tests {
		t.Run(tt.line, func(t *testing.T) {
			got := IsStatusLine(tt.line)
			if got != tt.want {
				t.Errorf("IsStatusLine(%q) = %v, want %v", tt.line, got, tt.want)
			}
		})
	}
}

func TestFilterStatusBar(t *testing.T) {
	input := `Some content here
More content
────────────────────────────────────────────────────────────────────────────────
❄ impure 📂 ~/path  🤖 Sonnet 4.5
-- INSERT --`

	output := FilterStatusBar(input)

	// Should contain the content
	if !strings.Contains(output, "Some content here") {
		t.Error("Expected output to contain 'Some content here'")
	}
	if !strings.Contains(output, "More content") {
		t.Error("Expected output to contain 'More content'")
	}

	// Should not contain status bar elements
	if strings.Contains(output, "-- INSERT --") {
		t.Error("Expected output to not contain '-- INSERT --'")
	}
	if strings.Contains(output, "🤖") {
		t.Error("Expected output to not contain model emoji")
	}
	if strings.Contains(output, "─────────────────────────") {
		t.Error("Expected output to not contain separator line")
	}
}

func TestDetectMode(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   parser.Mode
	}{
		{
			name:   "insert mode",
			output: "content\n-- INSERT --",
			want:   parser.ModeInsert,
		},
		{
			name:   "normal mode",
			output: "content\n-- NORMAL --",
			want:   parser.ModeNormal,
		},
		{
			name:   "no mode indicator defaults to normal",
			output: "just some content\nno mode here",
			want:   parser.ModeNormal,
		},
		{
			name:   "insert mode not at bottom - still detected in last 5 lines",
			output: "line1\nline2\nline3\n-- INSERT --\nlast line",
			want:   parser.ModeInsert,
		},
		{
			name:   "insert mode with ANSI color codes",
			output: "content\n\x1b[38;2;153;153;153m--\x1b[39m \x1b[38;2;153;153;153mINSERT\x1b[39m \x1b[38;2;153;153;153m--\x1b[39m",
			want:   parser.ModeInsert,
		},
		{
			name:   "normal mode with ANSI color codes",
			output: "content\n\x1b[38;2;153;153;153m--\x1b[39m \x1b[38;2;153;153;153mNORMAL\x1b[39m \x1b[38;2;153;153;153m--\x1b[39m",
			want:   parser.ModeNormal,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DetectMode(tt.output)
			if got != tt.want {
				t.Errorf("DetectMode() = %v, want %v", got, tt.want)
			}
		})
	}
}
