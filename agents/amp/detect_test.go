package amp

import "testing"

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
