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

func TestStaleModFallsBack(t *testing.T) {
	dir := t.TempDir()
	store := claudemod.NewStore()
	h := NewWithOptions(dir, Options{ClaudeProjectsDir: "-", Mod: store}, silentLog())
	now := time.Now()
	sec := now.Unix()
	writeState(t, dir, hook.SessionState{SessionID: "s1", State: hook.StateThinking, UpdatedAt: sec - 1})
	h.loadStateFile(hook.Path(dir, "s1"))

	// The mod's records ended 50 s ago: past the window, so it stopped.
	f := newModFeed(t, store, "s1")
	f.at(sec-51, "turn.start", nil)
	f.at(sec-50, "turn.complete", func(r *claudemod.Record) { r.Reason = "answer" })
	h.applyModDirty()
	h.tickMod(now)
	v := findSession(h, "s1")
	if v == nil || v.State != hook.StateThinking || v.UpdatedAt != sec-1 {
		t.Fatalf("view = %+v, want the hook's thinking at %d", v, sec-1)
	}

	// A record 30 s ago is inside the window: the mod is live again.
	f.at(sec-30, "heartbeat", nil)
	h.applyModDirty()
	h.tickMod(now)
	if v := findSession(h, "s1"); v == nil || v.State != hook.StateWaiting {
		t.Fatalf("view = %+v, want the mod's waiting", v)
	}
}

func TestModLiveWindowBoundary(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	s := &Session{hasMod: true, mod: claudemod.Session{Known: true}}
	s.mod.Updated = now.Add(-44 * time.Second).UnixMilli()
	if !s.modLive(now) {
		t.Error("mod retired 44 s after its newest record")
	}
	s.mod.Updated = now.Add(-modLiveWindow).UnixMilli()
	if s.modLive(now) {
		t.Error("mod still live once the window passed")
	}
}

func TestStreamOfHookWritesDoesNotKeepDeadModLive(t *testing.T) {
	const now = int64(1_000_000)
	rec := now - 60
	s := &Session{hasMod: true, mod: claudemod.Session{Known: true, Updated: rec * 1000}}
	// Hook events keep landing 3 s apart after the mod's last record; once the
	// window has passed, none of them brings the mod back.
	for sec := rec + 3; sec <= now; sec += 3 {
		s.hookState = hook.SessionState{SessionID: "s1", State: hook.StateThinking, UpdatedAt: sec}
		for _, at := range []int64{sec, sec + 1, sec + 2} {
			if at-rec < int64(modLiveWindow/time.Second) {
				continue
			}
			if s.modLive(time.Unix(at, 0)) {
				t.Fatalf("dead mod live at %d after a hook write at %d", at, sec)
			}
		}
	}
}

func TestLateHookWriteDoesNotRetireIdleMod(t *testing.T) {
	dir := t.TempDir()
	store := claudemod.NewStore()
	h := NewWithOptions(dir, Options{ClaudeProjectsDir: "-", Mod: store}, silentLog())
	now := time.Now()
	sec := now.Unix()

	f := newModFeed(t, store, "s1")
	f.at(sec-22, "turn.start", nil)
	f.at(sec-20, "turn.complete", func(r *claudemod.Record) { r.Reason = "answer" })
	// A detached post_tool lands after Stop and leaves the hook file thinking.
	writeState(t, dir, hook.SessionState{SessionID: "s1", State: hook.StateThinking, UpdatedAt: sec - 18})
	h.loadStateFile(hook.Path(dir, "s1"))
	h.applyModDirty()

	// 13 s after the late write, before the next heartbeat.
	h.tickMod(time.Unix(sec-5, 0))
	if v := findSession(h, "s1"); v == nil || v.State != hook.StateWaiting {
		t.Fatalf("view = %+v, want the mod's waiting", v)
	}

	f.at(sec-5, "heartbeat", nil)
	h.applyModDirty()
	h.tickMod(now)
	if v := findSession(h, "s1"); v == nil || v.State != hook.StateWaiting {
		t.Fatalf("view = %+v, want the mod's waiting", v)
	}
}

func TestHookEndedRetiresMod(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().Unix()
	writeState(t, dir, hook.SessionState{SessionID: "s1", State: hook.StateThinking, UpdatedAt: now - 6})
	store := claudemod.NewStore()
	h := startModHub(t, dir, store)

	f := newModFeed(t, store, "s1")
	f.at(now-5, "turn.start", nil)
	f.at(now-2, "turn.complete", func(r *claudemod.Record) { r.Reason = "answer" })
	waitView(t, h, "s1", "mod waiting", stateIs(hook.StateWaiting))

	// SessionEnd fired, the mod's session.end was lost at exit.
	writeState(t, dir, hook.SessionState{SessionID: "s1", State: hook.StateEnded, UpdatedAt: now})
	waitView(t, h, "s1", "hook ended", stateIs(hook.StateEnded))
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

func TestHeartbeatKeepsHookPermissionOverride(t *testing.T) {
	dir := t.TempDir()
	store := claudemod.NewStore()
	h := NewWithOptions(dir, Options{ClaudeProjectsDir: "-", Mod: store}, silentLog())
	now := time.Now()
	sec := now.Unix()

	f := newModFeed(t, store, "s1")
	f.at(sec-9, "turn.start", nil)
	f.at(sec-8, "tool.start", func(r *claudemod.Record) { r.Tool, r.ID = "Bash", "tu1" })
	writeState(t, dir, hook.SessionState{
		SessionID: "s1", State: hook.StatePermission, Tool: "Bash",
		LastMessage: "Claude needs your permission to use Bash", UpdatedAt: sec - 2,
	})
	h.loadStateFile(hook.Path(dir, "s1"))
	h.applyModDirty()
	h.tickMod(now)
	if v := findSession(h, "s1"); v == nil || v.State != hook.StatePermission {
		t.Fatalf("view = %+v, want the hook's permission", v)
	}

	// The heartbeat proves the mod alive; it is not main-loop activity.
	f.at(sec, "heartbeat", nil)
	h.applyModDirty()
	h.tickMod(now)
	v := findSession(h, "s1")
	if v == nil || v.State != hook.StatePermission || v.Tool != "Bash" ||
		v.LastMessage != "Claude needs your permission to use Bash" {
		t.Fatalf("view = %+v, want the hook's permission after a heartbeat", v)
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
	rec := now - 5
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

	// No record for the whole window: the mod stopped, so the transcript's list
	// returns.
	h.tickMod(time.Unix(rec, 0).Add(modLiveWindow))
	if v := findSession(h, "s1"); v == nil || v.State != hook.StateThinking || !backgroundIs("bshA")(*v) {
		t.Errorf("view = %+v, want the hook's thinking and bshA", v)
	}
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
	f.at(sec+40, "heartbeat", nil)
	h.applyModDirty()
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
