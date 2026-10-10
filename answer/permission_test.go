package answer

import (
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestPermissionPromptBash(t *testing.T) {
	p, ok := PermissionPrompt(fixture(t, "perm-bash"), fixtureWidth)
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
		p, ok := PermissionPrompt(capture, fixtureWidth)
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
	p, ok := PermissionPrompt(fixture(t, "stale-negative"), fixtureWidth)
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
		if p, ok := PermissionPrompt(fixture(t, name), fixtureWidth); ok {
			t.Errorf("PermissionPrompt(%s) = %+v, want none", name, p)
		}
	}
}

func TestPermissionPromptDetailCap(t *testing.T) {
	rule := strings.Repeat("─", 40)
	capture := rule + "\n" + strings.Repeat(" é long description line\n", 200) +
		"\n Do you want to proceed?\n ❯ 1. Yes\n   2. No\n\n Esc to cancel\n"
	p, ok := PermissionPrompt(capture, len([]rune(rule)))
	if !ok {
		t.Fatal("no prompt")
	}
	if len(p.Detail) > maxDetail || !utf8.ValidString(p.Detail) || !strings.HasPrefix(p.Detail, "é long") {
		t.Errorf("Detail is %d bytes, valid UTF-8 %v", len(p.Detail), utf8.ValidString(p.Detail))
	}
}

// withCommand swaps perm-bash's command for lines, as Claude indents them
// between the "╌" delimiters.
func withCommand(t *testing.T, lines ...string) string {
	t.Helper()
	const cmd = "\n touch colours.txt && ln -s colours.txt toppings.txt\n╌"
	base := fixture(t, "perm-bash")
	if strings.Count(base, cmd) != 1 {
		t.Fatal("perm-bash command line not found")
	}
	return strings.Replace(base, cmd, "\n "+strings.Join(lines, "\n ")+"\n╌", 1)
}

func TestPermissionPromptIndentedRuleInCommand(t *testing.T) {
	heredoc := func(head string) Prompt {
		t.Helper()
		p, ok := PermissionPrompt(withCommand(t, head, "────────", "X"), fixtureWidth)
		if !ok {
			t.Fatal("no prompt")
		}
		return p
	}
	rm, ls := heredoc("rm -rf ~/important && cat <<X"), heredoc("ls && cat <<X")
	if rm.Frame == ls.Frame {
		t.Error("frame unchanged when the command above an indented rule changed")
	}
	if !strings.HasPrefix(rm.Detail, "Bash command\n") || !strings.Contains(rm.Detail, "rm -rf ~/important && cat <<X\n────────\nX\n") {
		t.Errorf("Detail = %q, want the whole dialog text", rm.Detail)
	}
}

func TestPermissionPromptFrameKeepsWhitespace(t *testing.T) {
	frame := func(cmd ...string) string {
		t.Helper()
		p, ok := PermissionPrompt(withCommand(t, cmd...), fixtureWidth)
		if !ok {
			t.Fatal("no prompt")
		}
		return p.Frame
	}
	if frame("rm -rf /tmp/x") == frame("rm -rf / tmp/x") {
		t.Error("frame unchanged when a space moved into the command")
	}
	if frame("rm -rf /tmp/x") == frame("rm -rf", "/tmp/x") {
		t.Error("frame unchanged when a line break moved into the command")
	}
	if frame("rm  -rf   /tmp/x") != frame("rm -rf /tmp/x") {
		t.Error("frame changed by a run of spaces")
	}
}

func TestPermissionPromptDetailTruncationNote(t *testing.T) {
	rule := strings.Repeat("─", 40)
	long := rule + "\n" + strings.Repeat(" é long description line\n", 200) +
		"\n Do you want to proceed?\n ❯ 1. Yes\n   2. No\n\n Esc to cancel\n"
	p, ok := PermissionPrompt(long, len([]rune(rule)))
	if !ok {
		t.Fatal("no prompt")
	}
	if !strings.HasSuffix(p.Detail, "\n"+truncatedNote) {
		t.Errorf("Detail ends %q, want the truncation note", p.Detail[max(0, len(p.Detail)-80):])
	}
	short, ok := PermissionPrompt(fixture(t, "perm-bash"), fixtureWidth)
	if !ok || strings.Contains(short.Detail, truncatedNote) {
		t.Errorf("Detail = %q, want no truncation note", short.Detail)
	}
}

// A dialog rule spans the pane, so a short column-0 rule run in the dialog's
// text never anchors it.
func TestPermissionPromptShortRuleDoesNotAnchor(t *testing.T) {
	base := fixture(t, "perm-bash")
	want, ok := PermissionPrompt(base, fixtureWidth)
	if !ok {
		t.Fatal("no prompt")
	}
	capture := strings.Replace(base, "\n This command requires approval", "\n──\n This command requires approval", 1)
	p, ok := PermissionPrompt(capture, fixtureWidth)
	if !ok {
		t.Fatal("no prompt")
	}
	if p.Detail != want.Detail {
		t.Errorf("Detail = %q, want %q", p.Detail, want.Detail)
	}
}

// A dialog taller than the capture loses its own border, so a short column-0
// rule spilled into its command must not anchor it: the Detail and Frame would
// cover only the command's tail.
func TestPermissionPromptRefusesWithoutFullWidthRule(t *testing.T) {
	base := fixture(t, "perm-bash")
	_, below, ok := strings.Cut(base, "─\n Bash command")
	if !ok {
		t.Fatal("perm-bash border not found")
	}
	cut := strings.Replace(" Bash command"+below, "\n This command requires approval", "\n──\n This command requires approval", 1)
	if p, ok := PermissionPrompt(cut, fixtureWidth); ok {
		t.Errorf("PermissionPrompt anchored on a short rule: %+v", p)
	}
}

func TestPermissionPromptRefusesNarrowerThanPane(t *testing.T) {
	for _, width := range []int{fixtureWidth + 1, 0} {
		if p, ok := PermissionPrompt(fixture(t, "perm-bash"), width); ok {
			t.Errorf("width %d: PermissionPrompt = %+v, want none", width, p)
		}
	}
}
