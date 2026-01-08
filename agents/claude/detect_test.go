package claude

import "testing"

func TestDetectFromOutput(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   bool
	}{
		{
			name:   "insert mode",
			output: "some text\n-- INSERT --",
			want:   true,
		},
		{
			name:   "normal mode",
			output: "some text\n-- NORMAL --",
			want:   true,
		},
		{
			name:   "model emoji",
			output: "🤖 Sonnet 4.5 | 📊 50k/200k",
			want:   true,
		},
		{
			name:   "stats emoji",
			output: "📊 151k/200k (75.5%)",
			want:   true,
		},
		{
			name:   "messages emoji",
			output: "💬 43 msgs",
			want:   true,
		},
		{
			name:   "claude prompt",
			output: "Claude: Hello, how can I help?",
			want:   true,
		},
		{
			name:   "human prompt",
			output: "Human: Please help me",
			want:   true,
		},
		{
			name:   "yes no prompt",
			output: "Continue? [Y/n]",
			want:   true,
		},
		{
			name:   "amp output - should not match",
			output: "╭─37% of 168k · $1.24 (free)─────smart─╮\n✻ Cogitated for 1m 30s",
			want:   false,
		},
		{
			name:   "generic shell - should not match",
			output: "$ ls -la\ntotal 42\ndrwxr-xr-x 5 user user 4096 Jan 1 00:00 .",
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
