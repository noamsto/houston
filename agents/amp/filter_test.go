package amp

import (
	"strings"
	"testing"
)

func TestFilterStatusBar(t *testing.T) {
	input := `Some content here
More content
╭─37% of 168k · $1.24 (free)─────────────────────────────────smart─╮
│                                                                  │
╰─────────────────────────────────~/Data/git/tmux-dashboard (main)─╯`

	output := FilterStatusBar(input)

	// Should contain the content
	if !strings.Contains(output, "Some content here") {
		t.Error("Expected output to contain 'Some content here'")
	}
	if !strings.Contains(output, "More content") {
		t.Error("Expected output to contain 'More content'")
	}

	// Should not contain box elements
	if strings.Contains(output, "╭─") {
		t.Error("Expected output to not contain box top")
	}
	if strings.Contains(output, "╰─") {
		t.Error("Expected output to not contain box bottom")
	}
	if strings.Contains(output, "smart") {
		t.Error("Expected output to not contain 'smart' from status box")
	}
}

func TestFilterStatusBar_NoBox(t *testing.T) {
	input := "Just regular content\nNo box here"
	output := FilterStatusBar(input)

	if output != input {
		t.Errorf("Expected output to equal input when no box present\ngot: %q\nwant: %q", output, input)
	}
}

func TestExtractStatusLine(t *testing.T) {
	input := `Some content
╭─37% of 168k · $1.24 (free)─────smart─╮
│                                      │
╰─────────────~/Data/git/project (main)─╯
more content after`

	output := ExtractStatusLine(input)

	// Should contain box elements
	if !strings.Contains(output, "╭─") {
		t.Error("Expected status line to contain box top")
	}
	if !strings.Contains(output, "╰─") {
		t.Error("Expected status line to contain box bottom")
	}
	if !strings.Contains(output, "37% of 168k") {
		t.Error("Expected status line to contain token info")
	}

	// Should not contain content outside box
	if strings.Contains(output, "Some content") {
		t.Error("Expected status line to not contain content before box")
	}
	if strings.Contains(output, "more content after") {
		t.Error("Expected status line to not contain content after box")
	}
}
