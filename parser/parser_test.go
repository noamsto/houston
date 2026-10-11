// parser/parser_test.go
package parser

import (
	"testing"
)

func TestDetectChoices(t *testing.T) {
	output := `What approach should we use?

1. Option A - do this
2. Option B - do that
3. Option C - something else
4. All of the above`

	result := Parse(output)

	if result.Type != TypeChoice {
		t.Errorf("expected TypeChoice, got %v", result.Type)
	}
	if len(result.Choices) != 4 {
		t.Errorf("expected 4 choices, got %d", len(result.Choices))
	}
	if result.Question != "What approach should we use?" {
		t.Errorf("unexpected question: %q", result.Question)
	}
}

func TestDetectChoicesWithCursor(t *testing.T) {
	output := `Do you want to proceed?
❯ 1. Yes
2. Type here to tell Claude what to do differently`

	result := Parse(output)

	if result.Type != TypeChoice {
		t.Errorf("expected TypeChoice, got %v", result.Type)
	}
	if len(result.Choices) != 2 {
		t.Errorf("expected 2 choices, got %d: %v", len(result.Choices), result.Choices)
	}
	if result.Question != "Do you want to proceed?" {
		t.Errorf("unexpected question: %q", result.Question)
	}
}

func TestDetectError(t *testing.T) {
	output := `Running build...
Error: missing dependency xyz
Build failed`

	result := Parse(output)

	if result.Type != TypeError {
		t.Errorf("expected TypeError, got %v", result.Type)
	}
	if result.ErrorSnippet == "" {
		t.Error("expected error snippet")
	}
}

func TestDetectQuestion(t *testing.T) {
	output := `I've made the changes.

Does this look right?`

	result := Parse(output)

	if result.Type != TypeQuestion {
		t.Errorf("expected TypeQuestion, got %v", result.Type)
	}
}

func TestDetectIdle(t *testing.T) {
	output := `$ echo done
done
$`

	result := Parse(output)

	if result.Type != TypeIdle {
		t.Errorf("expected TypeIdle, got %v", result.Type)
	}
}

func TestDetectChoicesColoured(t *testing.T) {
	output := "Do you want to proceed?\n" +
		"\x1b[38;5;246m1. \x1b[38;5;153mYes\x1b[0m\n" +
		"\x1b[38;5;246m2. \x1b[38;5;153mYes, and don't ask again\x1b[0m\n" +
		"\x1b[38;5;246m3. \x1b[38;5;153mType here to tell Claude\x1b[0m\n" +
		"\x1b[38;5;246m4. \x1b[38;5;153mNo\x1b[0m\n"

	result := Parse(output)

	if result.Type != TypeChoice {
		t.Fatalf("expected TypeChoice, got %v", result.Type)
	}
	if len(result.Choices) != 4 {
		t.Errorf("expected 4 choices, got %d: %q", len(result.Choices), result.Choices)
	}
}
