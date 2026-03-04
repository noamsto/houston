package claude

import (
	"fmt"
	"strings"
	"testing"

	"github.com/noamsto/houston/parser"
)

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

func TestExtractSuggestion(t *testing.T) {
	// Helper to build a realistic CC terminal bottom with separators and status
	makeCCBottom := func(promptLine string) string {
		sep := "\x1b[2m\x1b[38;2;136;136;136m" + strings.Repeat("─", 80)
		status := "\x1b[0m  \x1b[1m\x1b[34m🤖\x1b[0m Opus 4.6 | 📊 50k/200k"
		mode := "  \x1b[38;2;153;153;153m--\x1b[39m \x1b[38;2;153;153;153mINSERT\x1b[39m \x1b[38;2;153;153;153m--\x1b[39m"
		return fmt.Sprintf("some output\n%s\n%s\n%s\n%s\n%s\n", sep, promptLine, sep, status, mode)
	}

	tests := []struct {
		name   string
		output string
		want   string
	}{
		{
			name:   "dim suggestion text",
			output: makeCCBottom("\x1b[0m❯\u00a0\x1b[2mclean up the .bak files too\x1b[0m"),
			want:   "clean up the .bak files too",
		},
		{
			name:   "no suggestion (empty prompt)",
			output: makeCCBottom("\x1b[0m❯\u00a0"),
			want:   "",
		},
		{
			name:   "user typed input (not dim)",
			output: makeCCBottom("\x1b[0m❯ fix the build error"),
			want:   "",
		},
		{
			name:   "no prompt line at all",
			output: "just some output\nno prompt here\n",
			want:   "",
		},
		{
			name:   "suggestion with regular space after prompt",
			output: makeCCBottom("\x1b[0m❯ \x1b[2mrebuild naspi\x1b[0m"),
			want:   "rebuild naspi",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractSuggestion(tt.output)
			if got != tt.want {
				t.Errorf("ExtractSuggestion() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestExtractInputText(t *testing.T) {
	makeCCBottom := func(promptLine string) string {
		sep := "\x1b[2m\x1b[38;2;136;136;136m" + strings.Repeat("─", 80)
		status := "\x1b[0m  \x1b[1m\x1b[34m🤖\x1b[0m Opus 4.6 | 📊 50k/200k"
		mode := "  \x1b[38;2;153;153;153m--\x1b[39m \x1b[38;2;153;153;153mINSERT\x1b[39m \x1b[38;2;153;153;153m--\x1b[39m"
		return fmt.Sprintf("some output\n%s\n%s\n%s\n%s\n%s\n", sep, promptLine, sep, status, mode)
	}

	tests := []struct {
		name   string
		output string
		want   string
	}{
		{
			name:   "user typed input",
			output: makeCCBottom("\x1b[0m❯ fix the build error"),
			want:   "fix the build error",
		},
		{
			name:   "user typed input with NBSP",
			output: makeCCBottom("\x1b[0m❯\u00a0hello world"),
			want:   "hello world",
		},
		{
			name:   "empty prompt",
			output: makeCCBottom("\x1b[0m❯\u00a0"),
			want:   "",
		},
		{
			name:   "dim suggestion returns empty",
			output: makeCCBottom("\x1b[0m❯\u00a0\x1b[2mclean up the .bak files too\x1b[0m"),
			want:   "",
		},
		{
			name:   "no prompt line",
			output: "just some output\nno prompt here\n",
			want:   "",
		},
		{
			name:   "input with ANSI colors",
			output: makeCCBottom("\x1b[0m❯ \x1b[1m\x1b[32msome colored text\x1b[0m"),
			want:   "some colored text",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractInputText(tt.output)
			if got != tt.want {
				t.Errorf("ExtractInputText() = %q, want %q", got, tt.want)
			}
		})
	}
}
