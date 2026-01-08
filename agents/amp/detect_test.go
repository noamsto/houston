package amp

import (
	"testing"

	"github.com/noamsto/houston/parser"
)

func TestDetectFromOutput(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   bool
	}{
		{
			name:   "cogitated thinking",
			output: "some output\n✻ Cogitated for 1m 30s",
			want:   true,
		},
		{
			name:   "baked thinking",
			output: "✻ Baked for 45s",
			want:   true,
		},
		{
			name:   "post tool use hooks",
			output: "Running PostToolUse hooks…",
			want:   true,
		},
		{
			name:   "box status with smart mode",
			output: "╭─37% of 168k · $1.24 (free)─────smart─╮",
			want:   true,
		},
		{
			name:   "box status with token format",
			output: "╭─50% of 168k─╮",
			want:   true,
		},
		{
			name:   "box status with free indicator",
			output: "╭─$0.00 (free)─╮",
			want:   true,
		},
		{
			name:   "claude output - should not match",
			output: "-- INSERT --\n🤖 Sonnet 4.5 | 📊 50k/200k",
			want:   false,
		},
		{
			name:   "generic shell - should not match",
			output: "$ ls -la\ntotal 42",
			want:   false,
		},
		{
			name:   "box without amp content - should not match",
			output: "╭─────────────╮",
			want:   false,
		},
		{
			name:   "empty output",
			output: "",
			want:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DetectFromOutput(tt.output)
			if got != tt.want {
				t.Errorf("DetectFromOutput() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestParseOutput_AmpChoices(t *testing.T) {
	input := `╭───────────────────────────────────────────────────────────────────────────────╮
│ Run this command?                                                             │
│                                                                               │
│ git push                                                                      │
│                                                                               │
│ (Matches built-in permissions rule 0: ask Bash --cmd '*git*push*')            │
│                                                                               │
│ ‣ Yes                                                                         │
│   Allow All for This Session                                                  │
│   Allow All for Every Session                                                 │
│   No                                                                          │
╰───────────────────────────────────────────────────────────────────────────────╯`

	result := ParseOutput(input)

	if result.Type != parser.TypeChoice {
		t.Errorf("expected TypeChoice, got %v", result.Type)
	}

	if result.Question != "Run this command?" {
		t.Errorf("expected question 'Run this command?', got %q", result.Question)
	}

	expectedChoices := []string{"Yes", "Allow All for This Session", "Allow All for Every Session", "No"}
	if len(result.Choices) != len(expectedChoices) {
		t.Errorf("expected %d choices, got %d: %v", len(expectedChoices), len(result.Choices), result.Choices)
		return
	}

	for i, want := range expectedChoices {
		if result.Choices[i] != want {
			t.Errorf("choice[%d] = %q, want %q", i, result.Choices[i], want)
		}
	}
}

func TestParseOutput_AmpChoices_DifferentSelection(t *testing.T) {
	input := `│ Run this command?                                                             │
│   Yes                                                                         │
│ ‣ Allow All for This Session                                                  │
│   No                                                                          │`

	result := ParseOutput(input)

	if result.Type != parser.TypeChoice {
		t.Errorf("expected TypeChoice, got %v", result.Type)
	}

	// Selected item should be first
	if len(result.Choices) < 1 || result.Choices[0] != "Allow All for This Session" {
		t.Errorf("expected first choice to be selected item, got %v", result.Choices)
	}
}

func TestParseOutput_ActivityPatterns(t *testing.T) {
	tests := []struct {
		name         string
		input        string
		wantType     parser.ResultType
		wantActivity string
	}{
		{
			name: "braille spinner thinking",
			input: `╰── ⣳ Thinking ▶
          ╰── Analyzing code structure`,
			wantType:     parser.TypeWorking,
			wantActivity: "Thinking",
		},
		{
			name: "running tools status",
			input: `├── ✓ Read file.go
╰── ⣳ Thinking ▶
 ≋ Running tools...  Esc to cancel`,
			wantType:     parser.TypeWorking,
			wantActivity: "Running tools",
		},
		{
			name: "tool invocation without parens",
			input: `● Grep CreateAPIKey
    some results here`,
			wantType:     parser.TypeWorking,
			wantActivity: "Searching",
		},
		{
			name: "cogitated thinking",
			input: `✻ Cogitated for 3m 7s

❯ Some response`,
			wantType:     parser.TypeWorking,
			wantActivity: "Thinking",
		},
		{
			name: "completed tool still working",
			input: `    ├── ✓ Grep CreateAPIKey
    ├── ✓ Read services/identity/pkg/identity/service.go`,
			wantType:     parser.TypeWorking,
			wantActivity: "Searching", // First completed tool found in text
		},
		{
			name: "waiting for response",
			input: `  ✓ Thinking ▶

╭─54% of 168k · $3.10─────────────────────────────────────────┬──────────────────────────────────────────────────────smart─╮
│                                                             │ TODOs                                                      │
╰─────────────────────────────────────────────────────────────┴───────────────────────────~/Data/git/tmux-dashboard (main)─╯
 ∼ Waiting for response...  Esc to cancel`,
			wantType:     parser.TypeWorking,
			wantActivity: "Waiting for response",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ParseOutput(tt.input)
			if result.Type != tt.wantType {
				t.Errorf("Type = %v, want %v", result.Type, tt.wantType)
			}
			if result.Activity != tt.wantActivity {
				t.Errorf("Activity = %q, want %q", result.Activity, tt.wantActivity)
			}
		})
	}
}
