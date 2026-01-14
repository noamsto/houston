package amp

import "testing"

func TestParseStatus(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected AmpStatus
	}{
		{
			name: "full status with free tier",
			input: `╭─37% of 168k · $1.24 (free)─────────────────────────────────smart─╮
│                                                                  │
╰─────────────────────────────────~/Data/git/tmux-dashboard (main)─╯`,
			expected: AmpStatus{
				TokenPercent: "37%",
				TokenLimit:   "168k",
				Cost:         "$1.24",
				CostNote:     "(free)",
				Mode:         "smart",
				Path:         "~/Data/git/tmux-dashboard",
				Branch:       "main",
			},
		},
		{
			name: "status without cost note",
			input: `╭─27% of 168k · $0.63─────────────────────────────────────────smart─╮
│                                                                    │
╰───────────────────────────────────────~/Data/git/houston (feature)─╯`,
			expected: AmpStatus{
				TokenPercent: "27%",
				TokenLimit:   "168k",
				Cost:         "$0.63",
				Mode:         "smart",
				Path:         "~/Data/git/houston",
				Branch:       "feature",
			},
		},
		{
			name: "rush mode",
			input: `╭─50% of 168k · $2.00──────────────────────────────────────────rush─╮
│                                                                    │
╰─────────────────────────────────────────────~/project (dev-branch)─╯`,
			expected: AmpStatus{
				TokenPercent: "50%",
				TokenLimit:   "168k",
				Cost:         "$2.00",
				Mode:         "rush",
				Path:         "~/project",
				Branch:       "dev-branch",
			},
		},
		{
			name: "no branch",
			input: `╭─10% of 168k · $0.10─────────────────────────────────────────auto─╮
│                                                                   │
╰───────────────────────────────────────────────────────~/Downloads─╯`,
			expected: AmpStatus{
				TokenPercent: "10%",
				TokenLimit:   "168k",
				Cost:         "$0.10",
				Mode:         "auto",
				Path:         "~/Downloads",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseStatus(tt.input)

			if got.TokenPercent != tt.expected.TokenPercent {
				t.Errorf("TokenPercent = %q, want %q", got.TokenPercent, tt.expected.TokenPercent)
			}
			if got.TokenLimit != tt.expected.TokenLimit {
				t.Errorf("TokenLimit = %q, want %q", got.TokenLimit, tt.expected.TokenLimit)
			}
			if got.Cost != tt.expected.Cost {
				t.Errorf("Cost = %q, want %q", got.Cost, tt.expected.Cost)
			}
			if got.CostNote != tt.expected.CostNote {
				t.Errorf("CostNote = %q, want %q", got.CostNote, tt.expected.CostNote)
			}
			if got.Mode != tt.expected.Mode {
				t.Errorf("Mode = %q, want %q", got.Mode, tt.expected.Mode)
			}
			if got.Path != tt.expected.Path {
				t.Errorf("Path = %q, want %q", got.Path, tt.expected.Path)
			}
			if got.Branch != tt.expected.Branch {
				t.Errorf("Branch = %q, want %q", got.Branch, tt.expected.Branch)
			}
		})
	}
}

func TestParseStatus_Empty(t *testing.T) {
	got := ParseStatus("")
	if got.TokenPercent != "" || got.Mode != "" {
		t.Errorf("Expected empty status for empty input, got %+v", got)
	}
}
