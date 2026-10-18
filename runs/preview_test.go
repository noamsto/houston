package runs

import (
	"testing"

	"github.com/noamsto/houston/tmux"
)

func TestIsInjectedTurn(t *testing.T) {
	tests := []struct {
		text string
		want bool
	}{
		{`<agent-message from="x"> [Sub…`, true},
		{"<task-notification>done</task-notification>", true},
		{"<system-reminder>x</system-reminder>", true},
		{"<command-name>/clear</command-name>", true},
		{"  \n <task-notification>", true},
		{"<br/>", true},
		{"[Request interrupted by user]", true},
		{"  [Request interrupted by user for tool use]", true},
		{"fix the bug", false},
		{"use <b> tags", false},
		{"a < b", false},
		{"<3 this", false},
		{"[1] first item", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := isInjectedTurn(tt.text); got != tt.want {
			t.Errorf("isInjectedTurn(%q) = %v, want %v", tt.text, got, tt.want)
		}
	}
}

func TestPreviewTaskSkipsInjectedAndEmpty(t *testing.T) {
	if got := previewTask("", "<task-notification>x", "fix it", "later"); got != "fix it" {
		t.Errorf("previewTask = %q, want fix it", got)
	}
	if got := previewTask("<system-reminder>x"); got != "" {
		t.Errorf("previewTask = %q, want \"\"", got)
	}
}

func TestDeltasFromTmuxPreviewIgnoresInjectedPrompt(t *testing.T) {
	wins := []tmux.WindowOptions{{Session: "s", Window: 1, Task: "real task"}}
	panes := []tmux.PaneOptions{{
		PaneID: "%1", Target: "s:1", ClaudeStatus: "idle 1 ",
		ClaudeTask: "<task-notification>done</task-notification>",
	}}
	got := deltasFromTmux(wins, panes, fakeProject)
	if len(got) != 1 || got[0].Run.Activity.Task != "real task" {
		t.Fatalf("Task = %+v, want the human window task", got)
	}

	wins[0].Task = "<agent-message from=\"x\">"
	got = deltasFromTmux(wins, panes, fakeProject)
	if got[0].Run.Activity.Task != "" {
		t.Errorf("Task = %q, want \"\" when every candidate is injected", got[0].Run.Activity.Task)
	}
}
