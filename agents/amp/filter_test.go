package amp

import (
	"strings"
	"testing"
)

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
