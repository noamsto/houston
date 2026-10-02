package hub

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/noamsto/houston/chat"
	"github.com/noamsto/houston/hook"
)

// Hand-written Claude transcript lines, in the shapes chat/testdata/claude
// pins. Inputs are plain ASCII so fmt quoting is valid JSON.

func claudeHuman(text string) string {
	return fmt.Sprintf(`{"type":"user","timestamp":"2024-01-01T00:00:00.000Z","origin":{"kind":"human"},"message":{"role":"user","content":%q}}`, text) + "\n"
}

func claudeText(msgID, text string) string {
	return fmt.Sprintf(`{"type":"assistant","timestamp":"2024-01-01T00:00:01.000Z","message":{"id":%q,"role":"assistant","content":[{"type":"text","text":%q}]}}`, msgID, text) + "\n"
}

func claudeToolUse(msgID, id, name, input string) string {
	return fmt.Sprintf(`{"type":"assistant","timestamp":"2024-01-01T00:00:02.000Z","message":{"id":%q,"role":"assistant","content":[{"type":"tool_use","id":%q,"name":%q,"input":%s}]}}`, msgID, id, name, input) + "\n"
}

func claudeToolResult(id, out string) string {
	return fmt.Sprintf(`{"type":"user","timestamp":"2024-01-01T00:00:03.000Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":%q,"content":%q,"is_error":false}]}}`, id, out) + "\n"
}

// chatFixture is a running hub with one hook session pointing at a transcript.
type chatFixture struct {
	t          *testing.T
	dir        string
	transcript string
	state      hook.SessionState
	h          *Hub
}

func newChatFixture(t *testing.T, sid, agent, body string) *chatFixture {
	t.Helper()
	dir := t.TempDir()
	transcript := filepath.Join(t.TempDir(), sid+".jsonl")
	if err := os.WriteFile(transcript, []byte(body), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	f := &chatFixture{
		t:          t,
		dir:        dir,
		transcript: transcript,
		state: hook.SessionState{
			SessionID:      sid,
			TranscriptPath: transcript,
			State:          hook.StateThinking,
			Agent:          agent,
			Since:          1,
			UpdatedAt:      time.Now().Unix(),
		},
	}
	writeState(t, dir, f.state)
	f.h = NewWithOptions(dir, Options{ClaudeProjectsDir: "-"}, silentLog())
	startHub(t, f.h)
	waitUntil(t, "session loaded", func() bool { return findSession(f.h, sid) != nil })
	return f
}

// poke rewrites the state file, which drives loadStateFile → refreshTranscript.
func (f *chatFixture) poke() {
	f.t.Helper()
	f.state.Since++
	writeState(f.t, f.dir, f.state)
}

func (f *chatFixture) appendAndPoke(lines string) {
	f.t.Helper()
	fh, err := os.OpenFile(f.transcript, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		f.t.Fatalf("open transcript: %v", err)
	}
	if _, err := fh.WriteString(lines); err != nil {
		f.t.Fatalf("append transcript: %v", err)
	}
	if err := fh.Close(); err != nil {
		f.t.Fatalf("close transcript: %v", err)
	}
	f.poke()
}

func (f *chatFixture) newest() uint64 {
	p, err := f.h.ChatPage(f.state.SessionID, 0, 1)
	if err != nil || len(p.Updates) == 0 {
		return 0
	}
	return p.Updates[len(p.Updates)-1].Seq
}

func (f *chatFixture) waitNewest(want uint64) {
	f.t.Helper()
	waitUntil(f.t, fmt.Sprintf("newest seq %d", want), func() bool { return f.newest() == want })
}

func (f *chatFixture) epoch() string {
	f.t.Helper()
	e, err := f.h.ChatEpoch(f.state.SessionID)
	if err != nil {
		f.t.Fatalf("ChatEpoch: %v", err)
	}
	return e
}

func (f *chatFixture) ring() []chat.Update {
	f.h.mu.RLock()
	defer f.h.mu.RUnlock()
	sess := f.h.sessions[f.state.SessionID]
	if sess == nil || sess.chat == nil {
		return nil
	}
	return append([]chat.Update(nil), sess.chat.ring...)
}

func humans(from, to int) string {
	var b strings.Builder
	for i := from; i <= to; i++ {
		b.WriteString(claudeHuman(fmt.Sprintf("prompt %04d", i)))
	}
	return b.String()
}

func seqs(ups []chat.Update) []uint64 {
	out := make([]uint64, len(ups))
	for i, u := range ups {
		out[i] = u.Seq
	}
	return out
}

func wantSeqs(t *testing.T, what string, ups []chat.Update, from, to uint64) {
	t.Helper()
	if uint64(len(ups)) != to-from+1 {
		t.Fatalf("%s: seqs %v, want %d..%d", what, seqs(ups), from, to)
	}
	for i, u := range ups {
		if u.Seq != from+uint64(i) {
			t.Fatalf("%s: seqs %v, want %d..%d", what, seqs(ups), from, to)
		}
	}
}

func TestChatPageAfterLoad(t *testing.T) {
	body := claudeHuman("fix the test") +
		claudeText("msg_1", "on it") +
		claudeToolUse("msg_1", "toolu_1", "Read", `{"file_path":"/tmp/x/main.go"}`) +
		claudeToolResult("toolu_1", "package main")
	f := newChatFixture(t, "chat-a", "", body)
	f.waitNewest(4)

	p, err := f.h.ChatPage("chat-a", 0, 50)
	if err != nil {
		t.Fatalf("ChatPage: %v", err)
	}
	wantSeqs(t, "page", p.Updates, 1, 4)
	if p.More {
		t.Error("More = true, want false")
	}
	if p.Epoch == "" || p.Epoch == "chat-a" || len(p.Epoch) != 12 {
		t.Errorf("Epoch = %q, want 12 opaque hex chars", p.Epoch)
	}
	kinds := []string{chat.SessionUpdateUserMessageChunk, chat.SessionUpdateAgentMessageChunk, chat.SessionUpdateToolCall, chat.SessionUpdateToolCallUpdate}
	for i, u := range p.Updates {
		if u.SessionUpdate != kinds[i] {
			t.Errorf("update %d kind = %q, want %q", i, u.SessionUpdate, kinds[i])
		}
	}
	if p.Updates[2].Title != "main.go" {
		t.Errorf("tool_call title = %q, want main.go", p.Updates[2].Title)
	}
}

func TestChatForNormalizedClaude(t *testing.T) {
	for _, engine := range []string{"claude", "claude-code"} {
		if chat.For(engine) == nil {
			t.Errorf("chat.For(%q) = nil", engine)
		}
	}
	// An empty Agent (native Claude hook payload) is normalized to claude
	// and must get chat.
	f := newChatFixture(t, "chat-native", "", claudeHuman("hi"))
	f.waitNewest(1)
}

func TestChatSinceAfterAppend(t *testing.T) {
	f := newChatFixture(t, "chat-b", "", humans(1, 3))
	f.waitNewest(3)
	epoch := f.epoch()

	f.appendAndPoke(humans(4, 6))
	f.waitNewest(6)

	ups, ok, err := f.h.ChatSince("chat-b", epoch, 3)
	if err != nil || !ok {
		t.Fatalf("ChatSince = ok %v err %v", ok, err)
	}
	wantSeqs(t, "since 3", ups, 4, 6)

	ups, ok, _ = f.h.ChatSince("chat-b", epoch, 6)
	if !ok || ups == nil || len(ups) != 0 {
		t.Errorf("ChatSince(newest) = %v ok %v, want empty non-nil, ok", ups, ok)
	}
	if _, ok, _ := f.h.ChatSince("chat-b", epoch, 7); ok {
		t.Error("ChatSince(after > total) ok, want false")
	}
	if _, ok, _ := f.h.ChatSince("chat-b", "000000000000", 3); ok {
		t.Error("ChatSince(wrong epoch) ok, want false")
	}
}

func TestChatRingCapAndPages(t *testing.T) {
	line := claudeHuman("prompt 0001")
	f := newChatFixture(t, "chat-c", "", humans(1, 520))
	f.waitNewest(520)

	ring := f.ring()
	if len(ring) != chatRingSize || ring[0].Seq != 21 || ring[len(ring)-1].Seq != 520 {
		t.Fatalf("ring holds %d updates %d..%d, want 500 updates 21..520", len(ring), ring[0].Seq, ring[len(ring)-1].Seq)
	}
	ringIDs := map[uint64]string{}
	for _, u := range ring {
		ringIDs[u.Seq] = u.ID
	}

	for _, tc := range []struct {
		before   uint64
		from, to uint64
	}{
		{30, 20, 29}, // straddles the ring's start
		{15, 5, 14},  // wholly below it
	} {
		p, err := f.h.ChatPage("chat-c", tc.before, 10)
		if err != nil {
			t.Fatalf("ChatPage(%d): %v", tc.before, err)
		}
		wantSeqs(t, fmt.Sprintf("before %d", tc.before), p.Updates, tc.from, tc.to)
		if !p.More {
			t.Errorf("before %d: More = false, want true", tc.before)
		}
		for _, u := range p.Updates {
			if want := fmt.Sprintf("%d:0", (u.Seq-1)*uint64(len(line))); u.ID != want {
				t.Errorf("seq %d ID = %q, want %q", u.Seq, u.ID, want)
			}
			if id, ok := ringIDs[u.Seq]; ok && id != u.ID {
				t.Errorf("seq %d ID = %q, ring holds %q", u.Seq, u.ID, id)
			}
		}
	}

	p, _ := f.h.ChatPage("chat-c", 11, 10)
	wantSeqs(t, "before 11", p.Updates, 1, 10)
	if p.More {
		t.Error("before 11: More = true, want false")
	}

	epoch := f.epoch()
	if _, ok, _ := f.h.ChatSince("chat-c", epoch, 19); ok {
		t.Error("ChatSince(19) ok with ring starting at 21, want false (gap)")
	}
	ups, ok, _ := f.h.ChatSince("chat-c", epoch, 20)
	if !ok {
		t.Fatal("ChatSince(20) not ok")
	}
	wantSeqs(t, "since 20", ups, 21, 520)
}

func TestChatPageLimitAndBeforeEdges(t *testing.T) {
	f := newChatFixture(t, "chat-d", "", humans(1, 150))
	f.waitNewest(150)

	for _, tc := range []struct {
		name     string
		before   uint64
		limit    int
		from, to uint64
	}{
		{"default limit", 0, 0, 101, 150},
		{"limit clamped to 100", 0, 1000, 51, 150},
		{"limit clamped to 1", 0, -3, 150, 150},
		{"before beyond newest", 9999, 10, 141, 150},
		{"before one past newest", 151, 10, 141, 150},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := f.h.ChatPage("chat-d", tc.before, tc.limit)
			if err != nil {
				t.Fatalf("ChatPage: %v", err)
			}
			wantSeqs(t, tc.name, p.Updates, tc.from, tc.to)
			if !p.More {
				t.Error("More = false, want true")
			}
		})
	}

	p, err := f.h.ChatPage("chat-d", 1, 10)
	if err != nil {
		t.Fatalf("ChatPage(before 1): %v", err)
	}
	if p.More || p.Updates == nil || len(p.Updates) != 0 {
		t.Errorf("before 1: %+v, want empty non-nil, More false", p)
	}
	b, _ := json.Marshal(p)
	if !strings.Contains(string(b), `"updates":[]`) {
		t.Errorf("empty page JSON = %s, want updates []", b)
	}
}

func TestChatSubscribeCoalesces(t *testing.T) {
	f := newChatFixture(t, "chat-e", "", humans(1, 1))
	f.waitNewest(1)
	epoch := f.epoch()

	ch, unsub, err := f.h.ChatSubscribe("chat-e")
	if err != nil {
		t.Fatalf("ChatSubscribe: %v", err)
	}
	defer unsub()

	f.appendAndPoke(humans(2, 2))
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("no notify after append")
	}
	f.waitNewest(2)

	for i := 3; i <= 5; i++ {
		f.appendAndPoke(humans(i, i))
		f.waitNewest(uint64(i))
	}
	if n := len(ch); n != 1 {
		t.Errorf("pending notifies = %d, want 1", n)
	}
	ups, ok, _ := f.h.ChatSince("chat-e", epoch, 0)
	if !ok {
		t.Fatal("ChatSince(0) not ok")
	}
	wantSeqs(t, "since 0", ups, 1, 5)
}

func TestChatTruncationResets(t *testing.T) {
	f := newChatFixture(t, "chat-f", "", humans(1, 5))
	f.waitNewest(5)
	epoch := f.epoch()

	ch, unsub, err := f.h.ChatSubscribe("chat-f")
	if err != nil {
		t.Fatalf("ChatSubscribe: %v", err)
	}
	defer unsub()

	if err := os.WriteFile(f.transcript, []byte(claudeHuman("fresh")), 0o644); err != nil {
		t.Fatalf("truncate transcript: %v", err)
	}
	f.poke()

	waitUntil(t, "epoch change", func() bool { return f.epoch() != epoch })
	f.waitNewest(1)
	p, _ := f.h.ChatPage("chat-f", 0, 50)
	wantSeqs(t, "after truncation", p.Updates, 1, 1)
	if p.Updates[0].ID != "0:0" {
		t.Errorf("ID = %q, want 0:0", p.Updates[0].ID)
	}
	if _, ok, _ := f.h.ChatSince("chat-f", epoch, 5); ok {
		t.Error("ChatSince(old epoch) ok, want false")
	}
	select {
	case <-ch:
	default:
		t.Error("subscriber not notified of truncation")
	}
}

func TestChatTranscriptPathChangeResets(t *testing.T) {
	f := newChatFixture(t, "chat-p", "", humans(1, 3))
	f.waitNewest(3)
	epoch := f.epoch()

	ch, unsub, err := f.h.ChatSubscribe("chat-p")
	if err != nil {
		t.Fatalf("ChatSubscribe: %v", err)
	}
	defer unsub()

	next := filepath.Join(t.TempDir(), "next.jsonl")
	if err := os.WriteFile(next, []byte(claudeHuman("other session")), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	f.transcript = next
	f.state.TranscriptPath = next
	f.poke()

	waitUntil(t, "epoch change", func() bool { return f.epoch() != epoch })
	f.waitNewest(1)
	p, _ := f.h.ChatPage("chat-p", 0, 50)
	if len(p.Updates) != 1 || len(p.Updates[0].Content) == 0 || p.Updates[0].Content[0].Content.Text != "other session" {
		t.Errorf("page after path change = %+v", p.Updates)
	}
	select {
	case <-ch:
	default:
		t.Error("subscriber not notified of path change")
	}
}

// epochAfterRestart starts a fresh hub over f's state dir, as a houston
// restart would, and returns its epoch once it has read up to seq want.
func (f *chatFixture) epochAfterRestart(want uint64) string {
	f.t.Helper()
	h := NewWithOptions(f.dir, Options{ClaudeProjectsDir: "-"}, silentLog())
	startHub(f.t, h)
	sid := f.state.SessionID
	waitUntil(f.t, fmt.Sprintf("restarted hub at seq %d", want), func() bool {
		p, err := h.ChatPage(sid, 0, 1)
		return err == nil && len(p.Updates) == 1 && p.Updates[0].Seq == want
	})
	e, err := h.ChatEpoch(sid)
	if err != nil {
		f.t.Fatalf("restarted hub ChatEpoch: %v", err)
	}
	return e
}

func TestChatEpochSurvivesRestartOfAnUnchangedFile(t *testing.T) {
	f := newChatFixture(t, "chat-r1", "", humans(1, 3))
	f.waitNewest(3)
	if got, want := f.epochAfterRestart(3), f.epoch(); got != want {
		t.Errorf("epoch after restart = %s, want %s", got, want)
	}
}

// A stale client's epoch must not name different content after a restart:
// the in-process renumber moved this hub to generation 1, and a fresh hub
// starts back at generation 0 over the rewritten file.
func TestChatEpochAfterRenumberAndRestartIsNew(t *testing.T) {
	f := newChatFixture(t, "chat-r2", "", humans(1, 5))
	f.waitNewest(5)
	stale := f.epoch()

	if err := os.WriteFile(f.transcript, []byte(humans(6, 7)), 0o644); err != nil {
		t.Fatalf("rewrite transcript: %v", err)
	}
	f.poke()
	waitUntil(t, "epoch change", func() bool { return f.epoch() != stale })
	f.waitNewest(2)

	if got := f.epochAfterRestart(2); got == stale {
		t.Errorf("restarted hub reuses the stale epoch %s for different content", got)
	}
}

func TestChatEpochNamesTheTranscriptPath(t *testing.T) {
	body := humans(1, 2)
	f := newChatFixture(t, "chat-r3", "", body)
	f.waitNewest(2)
	before := f.epoch()

	moved := filepath.Join(t.TempDir(), "moved.jsonl")
	if err := os.WriteFile(moved, []byte(body), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	f.state.TranscriptPath = moved
	f.poke()

	if got := f.epochAfterRestart(2); got == before {
		t.Errorf("epoch %s unchanged after the transcript path changed", got)
	}
}

// A brand-new session's file has no complete line yet, so its identity is
// unknown; the first line makes it known, which resets the stream once.
func TestChatEmptySessionResetsOnceOnItsFirstLine(t *testing.T) {
	f := newChatFixture(t, "chat-r4", "", "")
	waitUntil(t, "chat state", func() bool { _, err := f.h.ChatEpoch("chat-r4"); return err == nil })
	empty := f.epoch()

	ch, unsub, err := f.h.ChatSubscribe("chat-r4")
	if err != nil {
		t.Fatalf("ChatSubscribe: %v", err)
	}
	defer unsub()

	f.appendAndPoke(humans(1, 1))
	f.waitNewest(1)
	known := f.epoch()
	if known == empty {
		t.Fatalf("epoch %s unchanged once the first line arrived", known)
	}
	select {
	case <-ch:
	default:
		t.Error("subscriber not notified")
	}

	f.appendAndPoke(humans(2, 2))
	f.waitNewest(2)
	if e := f.epoch(); e != known {
		t.Errorf("epoch changed on a later append: %s → %s", known, e)
	}
	if got := f.epochAfterRestart(2); got != known {
		t.Errorf("epoch after restart = %s, want %s", got, known)
	}
}

// zeroUpdateReader yields no updates, letting a test isolate refreshChat's
// head-change notify from the new-update path.
type zeroUpdateReader struct{}

func (zeroUpdateReader) Engine() string { return "zero" }

func (zeroUpdateReader) Read(_ string, from chat.Cursor) ([]chat.Update, chat.Cursor, bool, error) {
	return nil, from, false, nil
}

func (zeroUpdateReader) Tool(string, string) (*chat.Update, error) {
	return nil, chat.ErrToolNotFound
}

// The first line makes the transcript's identity known, changing the epoch
// even though the reader turns it into no update: refreshChat must still
// notify subscribers, or they stay stale until the SSE handler's 2s check.
func TestChatHeadChangeNotifiesWithoutUpdates(t *testing.T) {
	f := newChatFixture(t, "chat-h1", "", "")
	waitUntil(t, "chat state", func() bool { _, err := f.h.ChatEpoch("chat-h1"); return err == nil })
	empty := f.epoch()

	ch, unsub, err := f.h.ChatSubscribe("chat-h1")
	if err != nil {
		t.Fatalf("ChatSubscribe: %v", err)
	}
	defer unsub()

	// Swap in the no-update reader while the file is still empty, so every
	// later refresh consumes the first line without producing an update.
	f.h.mu.Lock()
	f.h.sessions["chat-h1"].chat.reader = zeroUpdateReader{}
	f.h.mu.Unlock()

	if err := os.WriteFile(f.transcript, []byte(`{"type":"summary"}`+"\n"), 0o644); err != nil {
		t.Fatalf("write first line: %v", err)
	}
	f.h.refreshChat("chat-h1")

	if known := f.epoch(); known == empty {
		t.Fatalf("epoch %s unchanged once the first line became known", known)
	}
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Error("subscriber not notified of the head change with no updates")
	}
}

func TestChatNoChat(t *testing.T) {
	f := newChatFixture(t, "chat-pi", "pi", humans(1, 2))
	for _, sid := range []string{"chat-pi", "no-such-session"} {
		if _, err := f.h.ChatPage(sid, 0, 50); !errors.Is(err, ErrNoChat) {
			t.Errorf("%s: ChatPage err = %v, want ErrNoChat", sid, err)
		}
		if _, _, err := f.h.ChatSince(sid, "x", 0); !errors.Is(err, ErrNoChat) {
			t.Errorf("%s: ChatSince err = %v, want ErrNoChat", sid, err)
		}
		if _, err := f.h.ChatEpoch(sid); !errors.Is(err, ErrNoChat) {
			t.Errorf("%s: ChatEpoch err = %v, want ErrNoChat", sid, err)
		}
		if _, _, err := f.h.ChatSubscribe(sid); !errors.Is(err, ErrNoChat) {
			t.Errorf("%s: ChatSubscribe err = %v, want ErrNoChat", sid, err)
		}
		if _, err := f.h.ChatTool(sid, "toolu_1"); !errors.Is(err, ErrNoChat) {
			t.Errorf("%s: ChatTool err = %v, want ErrNoChat", sid, err)
		}
	}
}

func TestChatPartialLineExcludedUntilComplete(t *testing.T) {
	last := claudeHuman("third")
	f := newChatFixture(t, "chat-i", "", humans(1, 2)+last[:10])
	f.waitNewest(2)
	f.poke()
	time.Sleep(50 * time.Millisecond)
	if n := f.newest(); n != 2 {
		t.Fatalf("newest seq with a partial line = %d, want 2", n)
	}

	f.appendAndPoke(last[10:])
	f.waitNewest(3)
	p, _ := f.h.ChatPage("chat-i", 0, 50)
	if len(p.Updates) < 3 || len(p.Updates[2].Content) == 0 {
		t.Fatalf("page = %+v, want 3 updates with content", p.Updates)
	}
	if got := p.Updates[2].Content[0].Content.Text; got != "third" {
		t.Errorf("completed line text = %q, want third", got)
	}
}

func TestChatSubscriberClosedOnRemove(t *testing.T) {
	f := newChatFixture(t, "chat-j", "", humans(1, 1))
	f.waitNewest(1)

	ch, unsub, err := f.h.ChatSubscribe("chat-j")
	if err != nil {
		t.Fatalf("ChatSubscribe: %v", err)
	}
	if err := os.Remove(hook.Path(f.dir, "chat-j")); err != nil {
		t.Fatalf("remove state: %v", err)
	}

	deadline := time.After(5 * time.Second)
	for closed := false; !closed; {
		select {
		case _, ok := <-ch:
			closed = !ok
		case <-deadline:
			t.Fatal("subscriber channel not closed after remove")
		}
	}
	unsub()
	unsub()
}

func TestChatUnsubscribeTwice(t *testing.T) {
	f := newChatFixture(t, "chat-u", "", humans(1, 1))
	_, unsub, err := f.h.ChatSubscribe("chat-u")
	if err != nil {
		t.Fatalf("ChatSubscribe: %v", err)
	}
	unsub()
	unsub()
}

func TestChatTool(t *testing.T) {
	body := claudeToolUse("msg_1", "toolu_1", "Bash", `{"command":"go test ./...","description":"run tests"}`) +
		claudeToolResult("toolu_1", "ok")
	f := newChatFixture(t, "chat-t", "", body)
	f.waitNewest(2)

	u, err := f.h.ChatTool("chat-t", "toolu_1")
	if err != nil {
		t.Fatalf("ChatTool: %v", err)
	}
	if u.Status != chat.StatusCompleted || u.Title != "run tests" || len(u.RawInput) == 0 {
		t.Errorf("ChatTool = %+v", u)
	}
	if _, err := f.h.ChatTool("chat-t", "toolu_nope"); !errors.Is(err, chat.ErrToolNotFound) {
		t.Errorf("unknown call err = %v, want chat.ErrToolNotFound", err)
	}
}
