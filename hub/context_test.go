package hub

import "testing"

func replay(t *testing.T, path string) *Session {
	t.Helper()
	evs, _, err := ReadTranscriptFrom(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	s := &Session{}
	for _, ev := range evs {
		applyTranscriptEvent(s, ev)
	}
	return s
}

func TestContextIsCacheInclusiveAndLatestWins(t *testing.T) {
	s := replay(t, "testdata/context_claude.jsonl")
	// The first message used 100003; the latest 21002 must win, not the max.
	if s.view.ContextUsed != 21002 {
		t.Errorf("ContextUsed = %d, want 21002", s.view.ContextUsed)
	}
	if s.view.ContextLimit != 200_000 {
		t.Errorf("ContextLimit = %d, want 200000", s.view.ContextLimit)
	}
}

func TestClaudeHasNoSpend(t *testing.T) {
	s := replay(t, "testdata/context_claude.jsonl")
	if s.view.SpendUSD != nil {
		t.Errorf("SpendUSD = %v, want nil", *s.view.SpendUSD)
	}
}

func TestPiContextAndSpend(t *testing.T) {
	s := replay(t, "testdata/context_pi.jsonl")
	if s.view.ContextUsed != 6500 {
		t.Errorf("ContextUsed = %d, want 6500 (latest message, aborted one included)", s.view.ContextUsed)
	}
	if s.view.ContextLimit != 0 {
		t.Errorf("ContextLimit = %d, want 0 for an unknown model", s.view.ContextLimit)
	}
	if s.view.SpendUSD == nil || *s.view.SpendUSD != 0.75 {
		t.Errorf("SpendUSD = %v, want 0.75", s.view.SpendUSD)
	}
}

func TestContextLimit(t *testing.T) {
	cases := []struct {
		model string
		used  int
		want  int
	}{
		{"claude-opus-4-1", 1000, 200_000},
		{"claude-sonnet-4-5", 1000, 200_000},
		{"claude-opus-4-1[1m]", 500_000, 1_000_000},
		{"claude-opus-4-1", 250_000, 0}, // window smaller than use: wrong for this session
		{"deepseek-v4-pro", 1000, 0},
		{"", 1000, 0},
	}
	for _, c := range cases {
		if got := contextLimit(c.model, c.used); got != c.want {
			t.Errorf("contextLimit(%q, %d) = %d, want %d", c.model, c.used, got, c.want)
		}
	}
}
