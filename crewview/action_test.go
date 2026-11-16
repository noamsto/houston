package crewview

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

func goldenItems(t *testing.T) map[string]Item {
	t.Helper()
	lines := readLines(t, "overview.ndjson")
	if len(lines) == 0 {
		t.Fatal("golden overview.ndjson is empty")
	}
	ov, err := Decode(lines[0])
	if err != nil {
		t.Fatal(err)
	}
	items := make(map[string]Item, len(ov.Attention))
	for _, it := range ov.Attention {
		items[it.ID] = it
	}
	return items
}

func golden(t *testing.T, id string) Item {
	t.Helper()
	it, ok := goldenItems(t)[id]
	if !ok {
		t.Fatalf("golden has no item %q", id)
	}
	return it
}

// withArgv returns a copy of it whose action carries argv, leaving the golden
// item's slices untouched.
func withArgv(it Item, argv ...string) Item {
	var a Action
	if it.Action != nil {
		a = *it.Action
	}
	a.Argv = argv
	it.Action = &a
	return it
}

func withPR(it Item, pr *int) Item {
	var a Action
	if it.Action != nil {
		a = *it.Action
	}
	a.PR = pr
	it.Action = &a
	return it
}

func intp(n int) *int { return &n }

func withCrew(it Item, crew string) Item     { it.Crew = crew; return it }
func withBranch(it Item, branch string) Item { it.Branch = branch; return it }
func withWorker(it Item, worker string) Item { it.Worker = worker; return it }

func TestActionGoldenExecutable(t *testing.T) {
	tests := []struct {
		id   string
		text string
		want []string
	}{
		{"failed:c1:feat/h", "", []string{"where", "--crew", "c1", "--", "feat/h"}},
		{"stall:c1:feat/e", "", []string{"nudge", "--crew", "c1", "--", "feat/e"}},
		{"blocked:c1:feat/c", "go ahead", []string{"reply", "--crew", "c1", "--", "worker:feat/c", "go ahead"}},
		{"blocked:c1:feat/r", "go ahead", []string{"reply", "--crew", "c1", "--", "worker:feat/r", "go ahead"}},
		{"merge_ready:c1:feat/b", "", []string{"merge", "12", "--crew", "c1"}},
	}
	for _, tc := range tests {
		t.Run(tc.id, func(t *testing.T) {
			it := golden(t, tc.id)
			if err := Check(it); err != nil {
				t.Fatalf("Check = %v", err)
			}
			if !Executable(it) {
				t.Fatal("Executable = false")
			}
			got, err := Args(it, tc.text)
			if err != nil {
				t.Fatalf("Args = %v", err)
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("Args = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestActionArgsFreshSlice(t *testing.T) {
	it := golden(t, "failed:c1:feat/h")
	got, err := Args(it, "")
	if err != nil {
		t.Fatal(err)
	}
	got[0] = "nudge"
	a := it.Action
	if a == nil || len(a.Argv) < 2 {
		t.Fatal("golden item has no action argv")
	}
	if a.Argv[1] != "where" {
		t.Errorf("Args aliases the action argv: argv[1] = %q", a.Argv[1])
	}
}

func TestActionReplyText(t *testing.T) {
	reply := golden(t, "blocked:c1:feat/c")
	peek := golden(t, "failed:c1:feat/h")
	tests := []struct {
		name    string
		it      Item
		text    string
		want    []string
		wantErr error
	}{
		{"shell metacharacters stay one element", reply, "-rf; $(x)", []string{"reply", "--crew", "c1", "--", "worker:feat/c", "-rf; $(x)"}, nil},
		{"empty", reply, "", nil, ErrTextRequired},
		{"whitespace only", reply, " \t\n ", nil, ErrTextRequired},
		{"at limit", reply, strings.Repeat("a", MaxReplyText), []string{"reply", "--crew", "c1", "--", "worker:feat/c", strings.Repeat("a", MaxReplyText)}, nil},
		{"over limit", reply, strings.Repeat("a", 8<<10+1), nil, ErrTextRequired},
		{"non-reply ignores text", peek, "-rf; $(x)", []string{"where", "--crew", "c1", "--", "feat/h"}, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Args(tc.it, tc.text)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Args err = %v, want %v", err, tc.wantErr)
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("Args = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestActionNotExecutable(t *testing.T) {
	peek := golden(t, "failed:c1:feat/h")
	nudge := golden(t, "stall:c1:feat/e")
	reply := golden(t, "blocked:c1:feat/c")
	merge := golden(t, "merge_ready:c1:feat/b")

	peer := peek
	peer.Origin = OriginPeer
	noOrigin := peek
	noOrigin.Origin = ""
	noAction := peek
	noAction.Action = nil
	peekVerbNudgeSub := withArgv(peek, "crew", "nudge", "--crew", "c1", "--", "feat/h")
	replyVerbWrongPlaceholder := withArgv(reply, "crew", "reply", "--crew", "c1", "--", "worker:feat/c", "hi")
	if nudge.Action == nil {
		t.Fatal("golden nudge item has no action")
	}
	nudgeAsMergeVerb := nudge
	nudgeAsMergeVerb.Action = &Action{Verb: "merge", Argv: nudge.Action.Argv, PR: intp(12)}
	codename := withWorker(nudge, "otter")

	tests := []struct {
		name string
		it   Item
	}{
		{"inspect lane", golden(t, "lane_dead:c2:")},
		{"inspect hold", golden(t, "hold_matured:c1:h-due")},
		{"golden peer item", golden(t, "blocked:c1:feat/p")},
		{"peer origin", peer},
		{"unknown origin", noOrigin},
		{"nil action", noAction},
		{"argv0 bash", withArgv(peek, "bash", "where", "--crew", "c1", "--", "feat/h")},
		{"argv0 crewx", withArgv(peek, "crewx", "where", "--crew", "c1", "--", "feat/h")},
		{"argv0 empty", withArgv(peek, "", "where", "--crew", "c1", "--", "feat/h")},
		{"argv0 relative", withArgv(peek, "./crew", "where", "--crew", "c1", "--", "feat/h")},
		{"argv0 relative dir", withArgv(peek, "bin/crew", "where", "--crew", "c1", "--", "feat/h")},
		{"argv0 abs other base", withArgv(peek, "/usr/bin/bash", "where", "--crew", "c1", "--", "feat/h")},
		{"bash wrapper", withArgv(peek, "bash", "-euo", "pipefail", "crew", "where", "--crew", "c1", "--", "feat/h")},
		{"empty argv", withArgv(peek)},
		{"peek verb nudge subcommand", peekVerbNudgeSub},
		{"merge verb on nudge argv", nudgeAsMergeVerb},
		{"peek crew mismatch", withArgv(peek, "crew", "where", "--crew", "c2", "--", "feat/h")},
		{"nudge crew mismatch", withArgv(nudge, "crew", "nudge", "--crew", "c2", "--", "feat/e")},
		{"reply crew mismatch", withArgv(reply, "crew", "reply", "--crew", "c2", "--", "worker:feat/c", "<text>")},
		{"merge crew mismatch", withArgv(merge, "crew", "merge", "12", "--crew", "c2")},
		{"peek branch mismatch", withArgv(peek, "crew", "where", "--crew", "c1", "--", "feat/x")},
		{"reply recipient mismatch", withArgv(reply, "crew", "reply", "--crew", "c1", "--", "worker:feat/x", "<text>")},
		{"reply recipient without worker prefix", withArgv(reply, "crew", "reply", "--crew", "c1", "--", "feat/c", "<text>")},
		{"reply placeholder replaced", replyVerbWrongPlaceholder},
		{"nudge target not worker", withArgv(nudge, "crew", "nudge", "--crew", "c1", "--", "feat/x")},
		{"nudge target branch when worker is codename", withArgv(codename, "crew", "nudge", "--crew", "c1", "--", "feat/e")},
		{"nudge target empty", withArgv(withWorker(nudge, ""), "crew", "nudge", "--crew", "c1", "--", "")},
		{"nudge target flag", withArgv(withWorker(nudge, "-x"), "crew", "nudge", "--crew", "c1", "--", "-x")},
		{"peek empty crew", withArgv(withCrew(peek, ""), "crew", "where", "--crew", "", "--", "feat/h")},
		{"nudge empty crew", withArgv(withCrew(nudge, ""), "crew", "nudge", "--crew", "", "--", "feat/e")},
		{"reply empty crew", withArgv(withCrew(reply, ""), "crew", "reply", "--crew", "", "--", "worker:feat/c", "<text>")},
		{"merge empty crew", withArgv(withCrew(merge, ""), "crew", "merge", "12", "--crew", "")},
		{"peek empty branch", withArgv(withBranch(peek, ""), "crew", "where", "--crew", "c1", "--", "")},
		{"reply empty branch", withArgv(withBranch(reply, ""), "crew", "reply", "--crew", "c1", "--", "worker:", "<text>")},
		{"merge N with letters", withArgv(merge, "crew", "merge", "12a", "--crew", "c1")},
		{"merge N leading zero", withArgv(merge, "crew", "merge", "012", "--crew", "c1")},
		{"merge N not pr", withArgv(merge, "crew", "merge", "13", "--crew", "c1")},
		{"merge pr nil", withPR(merge, nil)},
		{"peek extra trailing element", withArgv(peek, "crew", "where", "--crew", "c1", "--", "feat/h", "extra")},
		{"nudge extra trailing element", withArgv(nudge, "crew", "nudge", "--crew", "c1", "--", "feat/e", "extra")},
		{"reply extra trailing element", withArgv(reply, "crew", "reply", "--crew", "c1", "--", "worker:feat/c", "<text>", "extra")},
		{"merge extra trailing element", withArgv(merge, "crew", "merge", "12", "--crew", "c1", "--yes")},
		{"peek missing separator", withArgv(peek, "crew", "where", "--crew", "c1", "feat/h")},
		{"peek separator replaced", withArgv(peek, "crew", "where", "--crew", "c1", "-x", "feat/h")},
		{"peek crew flag replaced", withArgv(peek, "crew", "where", "--crw", "c1", "--", "feat/h")},
		{"merge crew flag replaced", withArgv(merge, "crew", "merge", "12", "--all", "c1")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := Check(tc.it); !errors.Is(err, ErrNotExecutable) {
				t.Errorf("Check = %v, want ErrNotExecutable", err)
			}
			if Executable(tc.it) {
				t.Error("Executable = true")
			}
			if got, err := Args(tc.it, "hi"); !errors.Is(err, ErrNotExecutable) || got != nil {
				t.Errorf("Args = %q, %v; want nil, ErrNotExecutable", got, err)
			}
		})
	}
}

func TestActionNudgeCodename(t *testing.T) {
	it := withArgv(withWorker(golden(t, "stall:c1:feat/e"), "otter"), "crew", "nudge", "--crew", "c1", "--", "otter")
	got, err := Args(it, "")
	if err != nil {
		t.Fatalf("Args = %v", err)
	}
	if want := []string{"nudge", "--crew", "c1", "--", "otter"}; !slices.Equal(got, want) {
		t.Errorf("Args = %q, want %q", got, want)
	}
}

func TestActionArgv0Accepted(t *testing.T) {
	it := withArgv(golden(t, "failed:c1:feat/h"), "/nix/store/x/bin/crew", "where", "--crew", "c1", "--", "feat/h")
	got, err := Args(it, "")
	if err != nil {
		t.Fatalf("Args = %v", err)
	}
	if want := []string{"where", "--crew", "c1", "--", "feat/h"}; !slices.Equal(got, want) {
		t.Errorf("Args = %q, want %q", got, want)
	}
}
