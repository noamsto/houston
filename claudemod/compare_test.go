package claudemod_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/noamsto/houston/claudemod"
	"github.com/noamsto/houston/hook"
)

// A PostToolUse that lands after Stop leaves the hook path stuck on
// thinking; the mod path orders by seq and ends in the right state.
func TestLateToolEndHookVersusMod(t *testing.T) {
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")
	const sid = "cmp1"
	dir := t.TempDir()

	for _, ev := range []struct{ name, tool string }{
		{hook.EventUserPromptSubmit, ""},
		{hook.EventPreToolUse, "Bash"},
		{hook.EventStop, ""},
		{hook.EventPostToolUse, "Bash"},
	} {
		payload := map[string]any{"hook_event_name": ev.name, "session_id": sid}
		if ev.tool != "" {
			payload["tool_name"] = ev.tool
		}
		b, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		if err := hook.Dispatch(ev.name, dir, strings.NewReader(string(b))); err != nil {
			t.Fatalf("Dispatch %s: %v", ev.name, err)
		}
	}
	got, err := hook.Read(hook.Path(dir, sid))
	if err != nil {
		t.Fatal(err)
	}
	if got.State != hook.StateThinking {
		t.Fatalf("hook state = %q, want %q (the late PostToolUse race)", got.State, hook.StateThinking)
	}

	store := claudemod.NewStore()
	rec := func(seq int64, ev string) claudemod.Record {
		return claudemod.Record{V: 1, SID: sid, Boot: "b", Seq: seq, Ev: ev, ID: "t1", Tool: "Bash"}
	}
	for _, r := range []claudemod.Record{rec(1, "turn.start"), rec(2, "tool.start"), rec(4, "turn.complete")} {
		store.Apply(r)
	}
	if !store.Apply(rec(3, "tool.end")) {
		t.Fatal("late tool.end rejected")
	}
	snap := store.Snapshot()
	if len(snap) != 1 || snap[0].State != hook.StateWaiting || snap[0].Reordered != 1 {
		t.Fatalf("mod session = %+v, want state waiting with 1 reordered", snap)
	}
}
