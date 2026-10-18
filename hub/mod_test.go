package hub

import (
	"fmt"
	"testing"
	"time"

	"github.com/noamsto/houston/claudemod"
	"github.com/noamsto/houston/hook"
)

// modFeed applies records of one session's boot to a store, in seq order.
type modFeed struct {
	t     *testing.T
	store *claudemod.Store
	sid   string
	seq   int64
}

func newModFeed(t *testing.T, store *claudemod.Store, sid string) *modFeed {
	return &modFeed{t: t, store: store, sid: sid}
}

// at applies an ev record stamped at unix second sec; set fills its fields.
func (f *modFeed) at(sec int64, ev string, set func(*claudemod.Record)) {
	f.t.Helper()
	f.seq++
	rec := claudemod.Record{V: 1, SID: f.sid, Boot: "boot-1", Seq: f.seq, TS: sec * 1000, Ev: ev}
	if set != nil {
		set(&rec)
	}
	if !f.store.Apply(rec) {
		f.t.Fatalf("record %s seq %d not applied", ev, f.seq)
	}
}

func startModHub(t *testing.T, dir string, store *claudemod.Store) *Hub {
	t.Helper()
	h := NewWithOptions(dir, Options{ClaudeProjectsDir: "-", Mod: store}, silentLog())
	startHub(t, h)
	return h
}

func waitView(t *testing.T, h *Hub, sid, what string, cond func(v SessionView) bool) SessionView {
	t.Helper()
	var got SessionView
	waitUntil(t, what, func() bool {
		v := findSession(h, sid)
		if v == nil {
			return false
		}
		got = *v
		return cond(got)
	})
	return got
}

func stateIs(st hook.State) func(SessionView) bool {
	return func(v SessionView) bool { return v.State == st }
}

// backgroundIs matches a view listing exactly one task, id.
func backgroundIs(id string) func(SessionView) bool {
	return func(v SessionView) bool { return len(v.Background) == 1 && v.Background[0].ID == id }
}

func TestModOverlayWinsWhileLive(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().Unix()
	// A late PostToolUse left the hook file thinking after the turn ended.
	writeState(t, dir, hook.SessionState{SessionID: "s1", State: hook.StateThinking, Turn: 1, UpdatedAt: now - 1})
	store := claudemod.NewStore()
	h := startModHub(t, dir, store)

	f := newModFeed(t, store, "s1")
	f.at(now-5, "turn.start", nil)
	f.at(now-4, "tool.start", func(r *claudemod.Record) { r.Tool, r.ID = "Bash", "tu1" })
	f.at(now-2, "turn.complete", func(r *claudemod.Record) { r.Reason = "answer" })

	v := waitView(t, h, "s1", "mod waiting", stateIs(hook.StateWaiting))
	if v.Tool != "" || v.Since != now-2 || v.Turn != 1 {
		t.Errorf("view = %+v, want no tool, since %d, the hook's turn", v, now-2)
	}
}

func TestModLiveSkipsTranscriptHeal(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().Unix()
	// The transcript proves the dialog was declined, so the hook path heals it.
	transcript := writeJSONL(t,
		bgStartLine("tu1", "Bash", `{"command":"rm x"}`, "2026-01-01T00:00:00Z")+
			bgResultLine("tu1", "The user doesn't want to proceed with this tool use.", true))
	writeState(t, dir, hook.SessionState{
		SessionID: "s1", State: hook.StatePermission, Tool: "Bash", Turn: 1,
		TranscriptPath: transcript, UpdatedAt: now - 20,
	})
	store := claudemod.NewStore()
	h := startModHub(t, dir, store)
	waitView(t, h, "s1", "hook path heals the dialog", stateIs(hook.StateWaiting))

	// The mod has a newer call in flight: the heal must not apply over it.
	f := newModFeed(t, store, "s1")
	f.at(now-3, "turn.start", nil)
	f.at(now-1, "tool.start", func(r *claudemod.Record) {
		r.Tool, r.ID = "Bash", "tu2"
		r.Input = &claudemod.ToolInput{Command: "go test ./..."}
	})
	v := waitView(t, h, "s1", "mod tool-running", stateIs(hook.StateToolRunning))
	if v.Tool != "Bash" || v.ToolInputHint != "go test ./..." {
		t.Errorf("tool = %q hint = %q", v.Tool, v.ToolInputHint)
	}

	// Esc on a running tool arrives as one turn.complete.
	f.at(now, "turn.complete", func(r *claudemod.Record) { r.Reason = "aborted" })
	waitView(t, h, "s1", "mod waiting", stateIs(hook.StateWaiting))
}

func TestHookAheadFallsBack(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().Unix()
	writeState(t, dir, hook.SessionState{SessionID: "s1", State: hook.StateThinking, UpdatedAt: now - 26})
	store := claudemod.NewStore()
	h := startModHub(t, dir, store)

	f := newModFeed(t, store, "s1")
	f.at(now-26, "turn.start", nil)
	f.at(now-25, "turn.complete", nil)
	waitView(t, h, "s1", "mod live", stateIs(hook.StateWaiting))

	// 13 s past the newest record, and the mod has not answered it in 12 s:
	// the mod stopped posting.
	writeState(t, dir, hook.SessionState{SessionID: "s1", State: hook.StateThinking, UpdatedAt: now - 12})
	v := waitView(t, h, "s1", "hook state", stateIs(hook.StateThinking))
	if v.UpdatedAt != now-12 {
		t.Errorf("UpdatedAt = %d, want the hook's %d", v.UpdatedAt, now-12)
	}
}

func TestFreshHookWriteDoesNotRetireLiveMod(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().Unix()
	writeState(t, dir, hook.SessionState{SessionID: "s1", State: hook.StateWaiting, UpdatedAt: now - 20})
	store := claudemod.NewStore()
	h := startModHub(t, dir, store)

	f := newModFeed(t, store, "s1")
	f.at(now-21, "turn.start", nil)
	f.at(now-20, "turn.complete", func(r *claudemod.Record) { r.Reason = "answer" })
	waitView(t, h, "s1", "mod waiting", stateIs(hook.StateWaiting))

	// A UserPromptSubmit after an idle spell lands before the mod's queued
	// turn.start: the mod has not had the grace to answer it yet.
	writeState(t, dir, hook.SessionState{SessionID: "s1", State: hook.StateThinking, Turn: 2, UpdatedAt: now})
	v := waitView(t, h, "s1", "hook state loaded", func(v SessionView) bool { return v.Turn == 2 })
	if v.State != hook.StateWaiting {
		t.Fatalf("state = %q, want the mod's waiting", v.State)
	}

	f.at(now, "turn.start", nil)
	v = waitView(t, h, "s1", "mod thinking", stateIs(hook.StateThinking))
	if v.Since != now {
		t.Errorf("Since = %d, want the mod's %d", v.Since, now)
	}
}

func TestModLiveGraceBoundary(t *testing.T) {
	const rec = int64(1_000_000)
	s := &Session{
		hasMod:    true,
		mod:       claudemod.Session{Known: true, Updated: rec * 1000},
		hookState: hook.SessionState{SessionID: "s1", UpdatedAt: rec + 5},
	}
	if !s.modLive(time.Unix(rec+5+modLagGrace-1, 0)) {
		t.Error("mod retired before the grace passed")
	}
	if s.modLive(time.Unix(rec+5+modLagGrace, 0)) {
		t.Error("mod still live once the grace passed")
	}

	// A hook write one second past the record is the same event across a
	// second boundary, never ahead.
	s.hookState.UpdatedAt = rec + 1
	if !s.modLive(time.Unix(rec+1+modLagGrace*10, 0)) {
		t.Error("hook write within the truncation second retired the mod")
	}
}

func TestHookPermissionWinsWhileModToolRunning(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().Unix()
	store := claudemod.NewStore()
	h := startModHub(t, dir, store)

	f := newModFeed(t, store, "s1")
	f.at(now-3, "turn.start", nil)
	f.at(now-2, "tool.start", func(r *claudemod.Record) {
		r.Tool, r.ID = "Bash", "tu1"
		r.Input = &claudemod.ToolInput{Command: "rm x"}
	})
	waitView(t, h, "s1", "mod tool-running", stateIs(hook.StateToolRunning))

	// Managed Claude Code: no mod permission record, only the hook's Notification.
	writeState(t, dir, hook.SessionState{
		SessionID: "s1", State: hook.StatePermission, Tool: "Bash", ToolInputHint: "rm x",
		LastMessage: "Claude needs your permission to use Bash", UpdatedAt: now,
	})
	v := waitView(t, h, "s1", "hook permission", stateIs(hook.StatePermission))
	if v.Tool != "Bash" || v.ToolInputHint != "rm x" || v.LastMessage != "Claude needs your permission to use Bash" {
		t.Errorf("view = %+v", v)
	}
}

func TestHookPermissionIgnoredAfterModTurnComplete(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().Unix()
	store := claudemod.NewStore()
	h := startModHub(t, dir, store)

	f := newModFeed(t, store, "s1")
	f.at(now-3, "turn.start", nil)
	f.at(now-2, "tool.start", func(r *claudemod.Record) { r.Tool, r.ID = "Bash", "tu1" })
	writeState(t, dir, hook.SessionState{
		SessionID: "s1", State: hook.StatePermission, Tool: "Bash",
		LastMessage: "Claude needs your permission to use Bash", UpdatedAt: now,
	})
	waitView(t, h, "s1", "hook permission", stateIs(hook.StatePermission))

	// The dialog was dismissed: the mod saw the turn end after the hook wrote.
	f.at(now+1, "turn.complete", func(r *claudemod.Record) { r.Reason = "answer" })
	v := waitView(t, h, "s1", "mod waiting", stateIs(hook.StateWaiting))
	if v.Tool != "" || v.LastMessage != "" {
		t.Errorf("tool = %q message = %q, want none", v.Tool, v.LastMessage)
	}
}

func TestModOnlySessionCreatedAndJoinedByPane(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().Unix()
	transcript := writeJSONL(t,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"tu_1","name":"Edit","input":{"file_path":"x.go"}}]}}`+"\n")
	store := claudemod.NewStore()
	h := startModHub(t, dir, store)

	f := newModFeed(t, store, "m1")
	f.at(now, "session.start", func(r *claudemod.Record) {
		r.CWD, r.Transcript, r.Pane, r.TmuxServer = "/repo", transcript, "%5", "1234"
	})
	v := waitView(t, h, "m1", "transcript tailed", func(v SessionView) bool { return len(v.Trail) == 1 })
	want := SessionView{
		SessionID: "m1", CWD: "/repo", TmuxPane: "%5", TmuxServer: "1234", TranscriptPath: transcript,
		Agent: hook.AgentClaude, State: hook.StateStarting, UpdatedAt: now, Since: now,
	}
	if v.SessionID != want.SessionID || v.CWD != want.CWD || v.TmuxPane != want.TmuxPane ||
		v.TmuxServer != want.TmuxServer || v.TranscriptPath != want.TranscriptPath || v.Agent != want.Agent ||
		v.State != want.State || v.UpdatedAt != want.UpdatedAt || v.Since != want.Since || v.Turn != 0 {
		t.Errorf("view = %+v, want coordinates of %+v", v, want)
	}

	f.at(now+1, "turn.start", nil)
	v = waitView(t, h, "m1", "mod turn", stateIs(hook.StateThinking))
	if v.Turn != 1 {
		t.Errorf("Turn = %d, want the mod's 1", v.Turn)
	}
}

func TestModOnlySessionNotCreatedUntilKnown(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().Unix()
	store := claudemod.NewStore()
	h := startModHub(t, dir, store)

	f := newModFeed(t, store, "m1")
	// A tool.end of a call this houston never saw defines no state.
	f.at(now, "tool.end", func(r *claudemod.Record) { r.Tool, r.ID, r.Pane = "Bash", "tu0", "%5" })
	// m1 was dirty before m2's record, so it is folded by the time m2 shows.
	newModFeed(t, store, "m2").at(now, "session.start", nil)
	waitView(t, h, "m2", "known session", stateIs(hook.StateStarting))
	if v := findSession(h, "m1"); v != nil {
		t.Fatalf("session created from an unknown state: %+v", v)
	}

	f.at(now+1, "turn.start", nil)
	v := waitView(t, h, "m1", "known session", stateIs(hook.StateThinking))
	if v.TmuxPane != "%5" {
		t.Errorf("TmuxPane = %q, want the one learnt before it was known", v.TmuxPane)
	}
}

func TestBackgroundSwitchesWithoutDoubleCount(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().Unix()
	// The mod's records sit far enough back for a hook write to lead them for
	// the whole grace.
	rec := now - modLagGrace - 15
	transcript := writeJSONL(t,
		bgStartLine("tu1", "Bash", shellInput, "2026-01-01T00:00:00Z")+
			bgResultLine("tu1", fmt.Sprintf(shellResult, "bshA"), false))
	writeState(t, dir, hook.SessionState{
		SessionID: "s1", State: hook.StateThinking, TranscriptPath: transcript, UpdatedAt: rec - 1,
	})
	store := claudemod.NewStore()
	h := startModHub(t, dir, store)
	waitView(t, h, "s1", "transcript background", backgroundIs("bshA"))

	// Live, but the mod's list is not known yet: the transcript's stands.
	f := newModFeed(t, store, "s1")
	f.at(rec, "turn.start", nil)
	f.at(rec, "tool.start", func(r *claudemod.Record) { r.Tool, r.ID = "Read", "tu2" })
	v := waitView(t, h, "s1", "mod live", stateIs(hook.StateToolRunning))
	if !backgroundIs("bshA")(v) {
		t.Errorf("background before the mod's list is known = %+v, want bshA", v.Background)
	}

	f.at(rec, "stop", func(r *claudemod.Record) {
		r.Tasks = []claudemod.Task{{ID: "bshB", Type: "shell", Status: "running", Description: "B"}}
	})
	v = waitView(t, h, "s1", "mod background", backgroundIs("bshB"))
	if len(v.Background) != 1 {
		t.Fatalf("background = %+v", v.Background)
	}
	if v.Background[0].Kind != BackgroundShell || v.Background[0].Hint != "B" || v.Background[0].Since != rec {
		t.Errorf("mod task = %+v", v.Background[0])
	}

	// The hook runs ahead: the mod stopped, so the transcript's list returns.
	writeState(t, dir, hook.SessionState{
		SessionID: "s1", State: hook.StateThinking, TranscriptPath: transcript, UpdatedAt: now - modLagGrace - 2,
	})
	waitView(t, h, "s1", "transcript background again", func(v SessionView) bool {
		return v.State == hook.StateThinking && backgroundIs("bshA")(v)
	})
}

func TestMonitorExpiresOnTick(t *testing.T) {
	store := claudemod.NewStore()
	h := NewWithOptions(t.TempDir(), Options{ClaudeProjectsDir: "-", Mod: store}, silentLog())
	now := time.Now()
	sec := now.Unix()

	f := newModFeed(t, store, "m1")
	f.at(sec, "session.start", nil)
	f.at(sec, "tool.start", func(r *claudemod.Record) {
		r.Tool, r.ID = "Monitor", "tu1"
		r.Input = &claudemod.ToolInput{Description: "CI checks"}
	})
	f.at(sec, "tool.end", func(r *claudemod.Record) { r.Tool, r.ID, r.Task, r.TimeoutMS = "Monitor", "tu1", "bmon1", 60_000 })
	f.at(sec, "stop", func(r *claudemod.Record) {
		r.Tasks = []claudemod.Task{{ID: "bmon1", Type: "shell", Status: "running"}}
	})
	h.applyModDirty()

	v := findSession(h, "m1")
	if v == nil || len(v.Background) != 1 {
		t.Fatalf("view = %+v, want monitor bmon1", v)
	}
	if got := v.Background[0]; got.ID != "bmon1" || got.Kind != BackgroundMonitor {
		t.Fatalf("task = %+v, want monitor bmon1", got)
	}
	h.tickMod(now.Add(30 * time.Second))
	if v := findSession(h, "m1"); v == nil || len(v.Background) != 1 {
		t.Fatalf("monitor gone before its timeout: %+v", v)
	}
	h.tickMod(now.Add(61 * time.Second))
	if v := findSession(h, "m1"); v == nil || v.Background != nil {
		t.Fatalf("monitor kept past its timeout: %+v", v)
	}
}

func TestEvictionDeletesModOnlySession(t *testing.T) {
	dir := t.TempDir()
	store := claudemod.NewStore()
	h := NewWithOptions(dir, Options{ClaudeProjectsDir: "-", Mod: store}, silentLog())
	sec := time.Now().Unix()

	newModFeed(t, store, "m1").at(sec, "session.start", nil)
	writeState(t, dir, hook.SessionState{SessionID: "h1", State: hook.StateThinking, UpdatedAt: sec - 1})
	h.loadStateFile(hook.Path(dir, "h1"))
	newModFeed(t, store, "h1").at(sec, "turn.complete", nil)
	h.applyModDirty()
	if v := findSession(h, "m1"); v == nil {
		t.Fatal("mod-only session not created")
	}
	if v := findSession(h, "h1"); v == nil || v.State != hook.StateWaiting {
		t.Fatalf("h1 = %+v, want the mod's waiting", v)
	}

	// The store evicts its least recently applied sessions past its cap.
	for i := 0; ; i++ {
		_, m1 := store.Session("m1")
		_, h1 := store.Session("h1")
		if !m1 && !h1 {
			break
		}
		if i > 10_000 {
			t.Fatal("store never evicted")
		}
		newModFeed(t, store, fmt.Sprintf("f%05d", i)).at(sec, "tool.end", func(r *claudemod.Record) { r.ID = "tu0" })
	}
	h.applyModDirty()

	if v := findSession(h, "m1"); v != nil {
		t.Errorf("evicted mod-only session kept: %+v", v)
	}
	if v := findSession(h, "h1"); v == nil || v.State != hook.StateThinking {
		t.Errorf("h1 = %+v, want its hook state back", v)
	}
	if n := len(h.Snapshot()); n != 1 {
		t.Errorf("%d sessions, want h1 only (no unknown-state filler)", n)
	}
}

func TestModAsksFromStopLast(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().Unix()
	store := claudemod.NewStore()
	h := startModHub(t, dir, store)

	f := newModFeed(t, store, "m1")
	f.at(now, "session.start", nil)
	f.at(now, "turn.start", nil)
	f.at(now+1, "stop", func(r *claudemod.Record) { r.Last = "All green.\nShall I push?" })
	f.at(now+1, "turn.complete", func(r *claudemod.Record) { r.Reason = "answer" })
	waitView(t, h, "m1", "asks from stop", func(v SessionView) bool {
		return v.State == hook.StateWaiting && v.Asks == "Shall I push?"
	})

	f.at(now+2, "turn.start", nil)
	waitView(t, h, "m1", "asks cleared", func(v SessionView) bool {
		return v.State == hook.StateThinking && v.Asks == ""
	})
}

func TestDiscoverySeededSessionGetsModBackground(t *testing.T) {
	body := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Ready."}]}}`
	projects := fakeProjectsDir(t, map[string]string{"sess-dsc.jsonl": body})
	store := claudemod.NewStore()
	h := NewWithOptions(t.TempDir(), Options{ClaudeProjectsDir: projects, Mod: store}, silentLog())
	startHub(t, h)
	seeded := waitView(t, h, "sess-dsc", "seeded session", func(v SessionView) bool { return true })

	now := time.Now().Unix()
	f := newModFeed(t, store, "sess-dsc")
	f.at(now, "session.start", nil)
	f.at(now, "turn.start", nil)
	f.at(now, "tool.start", func(r *claudemod.Record) {
		r.Tool, r.ID, r.BG = "Bash", "tu1", true
		r.Input = &claudemod.ToolInput{Description: "Watch logs", Command: "tail -f x"}
	})
	f.at(now, "tool.end", func(r *claudemod.Record) { r.Tool, r.ID, r.Task = "Bash", "tu1", "b1" })
	f.at(now, "stop", func(r *claudemod.Record) {
		r.Tasks = []claudemod.Task{{ID: "b1", Type: "shell", Status: "running"}}
	})
	v := waitView(t, h, "sess-dsc", "mod background", backgroundIs("b1"))
	if len(v.Background) != 1 {
		t.Fatalf("background = %+v", v.Background)
	}
	if v.Background[0].Hint != "Watch logs" {
		t.Errorf("hint = %q", v.Background[0].Hint)
	}
	if v.TranscriptPath != seeded.TranscriptPath || v.CWD != seeded.CWD {
		t.Errorf("seeded coordinates lost: transcript %q cwd %q, want %q %q",
			v.TranscriptPath, v.CWD, seeded.TranscriptPath, seeded.CWD)
	}
}
