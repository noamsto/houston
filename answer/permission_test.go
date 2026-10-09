package answer

import (
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestPermissionPromptBash(t *testing.T) {
	p, ok := PermissionPrompt(fixture(t, "perm-bash"))
	if !ok {
		t.Fatal("no prompt")
	}
	if p.Question != "Do you want to proceed?" {
		t.Errorf("Question = %q", p.Question)
	}
	wantChoices := []string{
		"Yes",
		"Yes, and don't ask again for touch and ln -s commands in this folder",
		"Yes, and switch to auto mode · auto mode handles these prompts for you",
		"No",
	}
	if !reflect.DeepEqual(p.Choices, wantChoices) {
		t.Errorf("Choices = %q, want %q", p.Choices, wantChoices)
	}
	wantDetail := strings.Join([]string{
		"Bash command",
		"Tip: auto mode handles these prompts for you — choose",
		`"switch to auto mode" below`,
		"Link toppings.txt to colours.txt",
		"touch colours.txt && ln -s colours.txt toppings.txt",
		"This command requires approval",
	}, "\n")
	if p.Detail != wantDetail {
		t.Errorf("Detail = %q, want %q", p.Detail, wantDetail)
	}
	if len(p.Frame) != 64 {
		t.Errorf("Frame = %q, want 64 hex chars", p.Frame)
	}
}

func TestPermissionPromptFrame(t *testing.T) {
	frame := func(capture string) string {
		t.Helper()
		p, ok := PermissionPrompt(capture)
		if !ok {
			t.Fatal("no prompt")
		}
		return p.Frame
	}
	base := fixture(t, "perm-bash")
	if frame(base) == frame(fixture(t, "perm-bash-cmd2")) {
		t.Error("frame unchanged when only the command changed")
	}
	if frame(base) != frame(colorize(base)) {
		t.Error("frame changed by ANSI escapes")
	}
	wider := strings.ReplaceAll(strings.ReplaceAll(base, "──", "───"), "╌╌", "╌╌╌")
	if frame(base) != frame(wider) {
		t.Error("frame changed by rule width")
	}
	moved := strings.Replace(strings.Replace(base, " ❯ 1. Yes", "   1. Yes", 1), "   4. No", " ❯ 4. No", 1)
	if frame(base) == frame(moved) {
		t.Error("frame unchanged when the cursor moved")
	}
}

func TestPermissionPromptBelowStaleQuestion(t *testing.T) {
	p, ok := PermissionPrompt(fixture(t, "stale-negative"))
	if !ok {
		t.Fatal("no prompt")
	}
	if !reflect.DeepEqual(p.Choices, []string{"Yes", "No"}) || p.Question != "Do you want to proceed?" {
		t.Errorf("prompt = %+v", p)
	}
	if want := "Bash command\nCreate blue.txt\ntouch blue.txt\nThis command requires approval"; p.Detail != want {
		t.Errorf("Detail = %q, want %q", p.Detail, want)
	}
}

func TestPermissionPromptRefuses(t *testing.T) {
	for _, name := range []string{
		"perm-inactive", "perm-after", "plain-done",
		"plain-q", "plain-other-cursor", "tab-q1", "tab-q2", "mfirst-q1-other-typed", "tab-review", "wrap-review",
	} {
		if p, ok := PermissionPrompt(fixture(t, name)); ok {
			t.Errorf("PermissionPrompt(%s) = %+v, want none", name, p)
		}
	}
}

func TestPermissionPromptDetailCap(t *testing.T) {
	rule := strings.Repeat("─", 40)
	capture := rule + "\n" + strings.Repeat(" é long description line\n", 200) +
		"\n Do you want to proceed?\n ❯ 1. Yes\n   2. No\n\n Esc to cancel\n"
	p, ok := PermissionPrompt(capture)
	if !ok {
		t.Fatal("no prompt")
	}
	if len(p.Detail) > maxDetail || !utf8.ValidString(p.Detail) || !strings.HasPrefix(p.Detail, "é long") {
		t.Errorf("Detail is %d bytes, valid UTF-8 %v", len(p.Detail), utf8.ValidString(p.Detail))
	}
}
