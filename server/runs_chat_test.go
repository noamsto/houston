package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/noamsto/houston/chat"
	"github.com/noamsto/houston/hook"
	"github.com/noamsto/houston/hub"
	"github.com/noamsto/houston/mode"
	"github.com/noamsto/houston/runs"
)

const (
	chatSID   = "s1"
	chatEpoch = "ep1"
	chatKey   = "claude/s1"
)

// fakeChat is an in-memory chatSource for one session, with the hub's
// servability rules: ChatSince is ok only for the current epoch and a cursor
// within [first-1, total].
type fakeChat struct {
	mu    sync.Mutex
	epoch string
	ups   []chat.Update // seq-ascending, contiguous
	total uint64
	tools map[string]*chat.Update
	err   error
	subs  map[chan struct{}]struct{}
}

func newFakeChat(n int) *fakeChat {
	f := &fakeChat{epoch: chatEpoch, tools: map[string]*chat.Update{}, subs: map[chan struct{}]struct{}{}}
	f.appendN(n)
	return f
}

// dropOldest evicts the oldest n updates, as the hub's ring does.
func (f *fakeChat) dropOldest(n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ups = f.ups[n:]
}

func (f *fakeChat) appendN(n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for range n {
		f.total++
		f.ups = append(f.ups, chat.Update{
			ID:            "u" + strconv.FormatUint(f.total, 10),
			Seq:           f.total,
			SessionUpdate: chat.SessionUpdateAgentMessageChunk,
		})
	}
	for ch := range f.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func (f *fakeChat) setEpoch(e string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.epoch = e
}

func (f *fakeChat) closeSubs() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for ch := range f.subs {
		delete(f.subs, ch)
		close(ch)
	}
}

func (f *fakeChat) subCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.subs)
}

func (f *fakeChat) check(sid string) error {
	if f.err != nil {
		return f.err
	}
	if sid != chatSID {
		return hub.ErrNoChat
	}
	return nil
}

func (f *fakeChat) first() uint64 {
	if len(f.ups) == 0 {
		return f.total + 1
	}
	return f.ups[0].Seq
}

func (f *fakeChat) slice(from, to uint64) []chat.Update {
	fst := f.first()
	return append([]chat.Update{}, f.ups[from-fst:to-fst]...)
}

func (f *fakeChat) ChatPage(sid string, before uint64, limit int) (hub.ChatPage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.check(sid); err != nil {
		return hub.ChatPage{}, err
	}
	if limit == 0 {
		limit = 50
	}
	if before == 0 || before > f.total+1 {
		before = f.total + 1
	}
	from := max(f.first(), 1)
	if before > uint64(limit) {
		from = max(from, before-uint64(limit))
	}
	return hub.ChatPage{Epoch: f.epoch, Updates: f.slice(from, before), More: from > 1}, nil
}

func (f *fakeChat) ChatSince(sid, epoch string, after uint64) ([]chat.Update, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.check(sid); err != nil {
		return nil, false, err
	}
	if epoch != f.epoch || after > f.total || after+1 < f.first() {
		return nil, false, nil
	}
	return f.slice(after+1, f.total+1), true, nil
}

func (f *fakeChat) ChatEpoch(sid string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.check(sid); err != nil {
		return "", err
	}
	return f.epoch, nil
}

func (f *fakeChat) ChatSubscribe(sid string) (<-chan struct{}, func(), error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.check(sid); err != nil {
		return nil, nil, err
	}
	ch := make(chan struct{}, 1)
	f.subs[ch] = struct{}{}
	return ch, func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		if _, ok := f.subs[ch]; ok {
			delete(f.subs, ch)
			close(ch)
		}
	}, nil
}

func (f *fakeChat) ChatTool(sid, callID string) (*chat.Update, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.check(sid); err != nil {
		return nil, err
	}
	u, ok := f.tools[callID]
	if !ok {
		return nil, chat.ErrToolNotFound
	}
	return u, nil
}

func chatDelta(session string) runs.Delta {
	return runs.Delta{Source: "hooks", Key: chatKey, Run: runs.Run{Agent: "claude", State: runs.StateRunning, Session: session}}
}

func newChatServer(t *testing.T, src chatSource, session string) *Server {
	t.Helper()
	s := newReplyServer(t, nil, chatDelta(session))
	s.chat = src
	return s
}

func chatRunPath(t *testing.T, s *Server) string {
	t.Helper()
	snap := s.runs.Snapshot()
	if len(snap) != 1 {
		t.Fatalf("registry holds %d listed runs, want 1", len(snap))
	}
	return "/api/runs/" + snap[0].ID + "/chat"
}

func doChat(t *testing.T, s *Server, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, replyRequest("GET", path, ""))
	return rec
}

func wantRefusal(t *testing.T, rec *httptest.ResponseRecorder, code int, body string) {
	t.Helper()
	if rec.Code != code || strings.TrimSpace(rec.Body.String()) != body {
		t.Fatalf("got %d %q, want %d %q", rec.Code, rec.Body.String(), code, body)
	}
}

func TestRunChatWithoutRegistryIs503(t *testing.T) {
	s := &Server{}
	for _, path := range []string{"/api/runs/x/chat", "/api/runs/x/chat/stream", "/api/runs/x/chat/tool/y"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", path, nil)
		req.SetPathValue("id", "x")
		req.SetPathValue("callId", "y")
		switch {
		case strings.HasSuffix(path, "/stream"):
			s.handleRunChatStream(rec, req)
		case strings.Contains(path, "/tool/"):
			s.handleRunChatTool(rec, req)
		default:
			s.handleRunChat(rec, req)
		}
		wantRefusal(t, rec, http.StatusServiceUnavailable, "run registry not started")
	}
}

func TestRunChatLadder(t *testing.T) {
	cases := []struct {
		name    string
		session string
		src     func() chatSource
		path    func(base string) string
		code    int
		body    string
	}{
		{"unknown run", chatSID, func() chatSource { return newFakeChat(1) }, func(string) string { return "/api/runs/nope/chat" }, 404, "no such run"},
		{"unknown run stream", chatSID, func() chatSource { return newFakeChat(1) }, func(string) string { return "/api/runs/nope/chat/stream" }, 404, "no such run"},
		{"unknown run tool", chatSID, func() chatSource { return newFakeChat(1) }, func(string) string { return "/api/runs/nope/chat/tool/t" }, 404, "no such run"},
		{"no session", "", func() chatSource { return newFakeChat(1) }, func(b string) string { return b }, 404, "no chat"},
		{"no session stream", "", func() chatSource { return newFakeChat(1) }, func(b string) string { return b + "/stream" }, 404, "no chat"},
		{"no session tool", "", func() chatSource { return newFakeChat(1) }, func(b string) string { return b + "/tool/t" }, 404, "no chat"},
		{"no source", chatSID, func() chatSource { return nil }, func(b string) string { return b }, 404, "no chat"},
		{"source has no chat", "other", func() chatSource { return newFakeChat(1) }, func(b string) string { return b }, 404, "no chat"},
		{"source has no chat stream", "other", func() chatSource { return newFakeChat(1) }, func(b string) string { return b + "/stream" }, 404, "no chat"},
		{"source has no chat tool", "other", func() chatSource { return newFakeChat(1) }, func(b string) string { return b + "/tool/t" }, 404, "no chat"},
		{"source error", chatSID, func() chatSource { f := newFakeChat(1); f.err = errors.New("disk on fire"); return f }, func(b string) string { return b }, 500, "chat unavailable"},
		{"bad before", chatSID, func() chatSource { return newFakeChat(1) }, func(b string) string { return b + "?before=x" }, 400, "bad before"},
		{"negative before", chatSID, func() chatSource { return newFakeChat(1) }, func(b string) string { return b + "?before=-1" }, 400, "bad before"},
		{"bad limit", chatSID, func() chatSource { return newFakeChat(1) }, func(b string) string { return b + "?limit=ten" }, 400, "bad limit"},
		{"unknown tool", chatSID, func() chatSource { return newFakeChat(1) }, func(b string) string { return b + "/tool/missing" }, 404, "no such tool call"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newChatServer(t, tc.src(), tc.session)
			wantRefusal(t, doChat(t, s, tc.path(chatRunPath(t, s))), tc.code, tc.body)
		})
	}
}

func TestRunChatPageShape(t *testing.T) {
	s := newChatServer(t, newFakeChat(7), chatSID)
	rec := doChat(t, s, chatRunPath(t, s)+"?before=6&limit=3")
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q", ct)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode: %v; body=%s", err, rec.Body)
	}
	for _, k := range []string{"epoch", "updates", "more"} {
		if _, ok := raw[k]; !ok {
			t.Errorf("page lacks %q: %s", k, rec.Body)
		}
	}
	var page hub.ChatPage
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if page.Epoch != chatEpoch || !page.More || seqsOf(page.Updates) != "3,4,5" {
		t.Fatalf("page = %+v (seqs %s), want epoch %s, more, seqs 3,4,5", page, seqsOf(page.Updates), chatEpoch)
	}
}

func seqsOf(ups []chat.Update) string {
	parts := make([]string, len(ups))
	for i, u := range ups {
		parts[i] = strconv.FormatUint(u.Seq, 10)
	}
	return strings.Join(parts, ",")
}

// --- SSE ---

type sseFrame struct {
	id, event, data string
	comment         bool
}

type sseConn struct {
	resp   *http.Response
	frames <-chan sseFrame // closed at EOF
	cancel context.CancelFunc
}

func openChatStream(t *testing.T, ts *httptest.Server, path string, lastEventID string) *sseConn {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, "GET", ts.URL+path, nil)
	if err != nil {
		cancel()
		t.Fatalf("request: %v", err)
	}
	req.AddCookie(&http.Cookie{Name: authCookie, Value: replyToken})
	if lastEventID != "" {
		req.Header.Set("Last-Event-ID", lastEventID)
	}
	resp, err := ts.Client().Do(req) //nolint:bodyclose // body is closed by the reader goroutine below
	if err != nil {
		cancel()
		t.Fatalf("GET %s: %v", path, err)
	}
	frames := make(chan sseFrame, 64)
	go func() {
		defer close(frames)
		br := bufio.NewReader(resp.Body)
		var f sseFrame
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimSuffix(line, "\n")
			switch {
			case line == "":
				frames <- f
				f = sseFrame{}
			case strings.HasPrefix(line, ":"):
				f.comment = true
			case strings.HasPrefix(line, "id: "):
				f.id = strings.TrimPrefix(line, "id: ")
			case strings.HasPrefix(line, "event: "):
				f.event = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				f.data = strings.TrimPrefix(line, "data: ")
			}
		}
	}()
	c := &sseConn{resp: resp, frames: frames, cancel: cancel}
	t.Cleanup(c.close)
	return c
}

func (c *sseConn) close() {
	c.cancel()
	_ = c.resp.Body.Close()
}

// next returns the next frame that is neither a comment nor a ping.
func (c *sseConn) next(t *testing.T) sseFrame {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case f, ok := <-c.frames:
			if !ok {
				t.Fatal("stream ended, want a frame")
			}
			if f.comment || f.event == "ping" {
				continue
			}
			return f
		case <-deadline:
			t.Fatal("no frame within 3s")
		}
	}
}

func (c *sseConn) wantEOF(t *testing.T) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case f, ok := <-c.frames:
			if !ok {
				return
			}
			if !f.comment && f.event != "ping" {
				t.Fatalf("frame %+v after the end, want EOF", f)
			}
		case <-deadline:
			t.Fatal("stream still open after 3s, want EOF")
		}
	}
}

func (c *sseConn) wantReset(t *testing.T) {
	t.Helper()
	if f := c.next(t); f.event != "reset" || f.data != "{}" {
		t.Fatalf("frame %+v, want event: reset", f)
	}
	c.wantEOF(t)
}

func frameSeqs(t *testing.T, f sseFrame) []uint64 {
	t.Helper()
	if f.event != "updates" {
		t.Fatalf("frame %+v, want event: updates", f)
	}
	var ups []chat.Update
	if err := json.Unmarshal([]byte(f.data), &ups); err != nil {
		t.Fatalf("decode frame data: %v; %q", err, f.data)
	}
	out := make([]uint64, len(ups))
	for i, u := range ups {
		out[i] = u.Seq
	}
	if want := fmt.Sprintf("%s.%d", chatEpoch, out[len(out)-1]); f.id != want {
		t.Fatalf("frame id %q, want %q", f.id, want)
	}
	return out
}

func wantSeqs(t *testing.T, got []uint64, from, to uint64) {
	t.Helper()
	var want []uint64
	for s := from; s <= to; s++ {
		want = append(want, s)
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("seqs %v, want %v", got, want)
	}
}

// newChatStreamServer serves s over a real listener, so SSE frames flush.
// tune runs before the listener starts, keeping field writes race-free.
func newChatStreamServer(t *testing.T, f *fakeChat, tune ...func(*Server)) (*Server, *httptest.Server, string) {
	t.Helper()
	s := newChatServer(t, f, chatSID)
	for _, fn := range tune {
		fn(s)
	}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return s, ts, chatRunPath(t, s) + "/stream"
}

func TestRunChatStreamResumesAfterCursorAndFollowsAppends(t *testing.T) {
	f := newFakeChat(5)
	_, ts, path := newChatStreamServer(t, f)

	c := openChatStream(t, ts, path+"?after="+chatEpoch+".2", "")
	if c.resp.StatusCode != 200 {
		t.Fatalf("status %d", c.resp.StatusCode)
	}
	for k, v := range map[string]string{"Content-Type": "text/event-stream", "Cache-Control": "no-cache", "X-Accel-Buffering": "no"} {
		if got := c.resp.Header.Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
	wantSeqs(t, frameSeqs(t, c.next(t)), 3, 5)

	f.appendN(2)
	wantSeqs(t, frameSeqs(t, c.next(t)), 6, 7)
}

func TestRunChatStreamSendsAnEmptyFirstEventWhenCaughtUp(t *testing.T) {
	f := newFakeChat(3)
	_, ts, path := newChatStreamServer(t, f)

	c := openChatStream(t, ts, path+"?after="+chatEpoch+".3", "")
	first := c.next(t)
	if first.event != "updates" || first.data != "[]" || first.id != chatEpoch+".3" {
		t.Fatalf("first frame %+v, want an empty updates event at the cursor", first)
	}

	f.appendN(1)
	wantSeqs(t, frameSeqs(t, c.next(t)), 4, 4)
}

func TestRunChatStreamReconnectHasNoGapOrDuplicate(t *testing.T) {
	f := newFakeChat(3)
	_, ts, path := newChatStreamServer(t, f)

	var seen []uint64
	c := openChatStream(t, ts, path+"?after="+chatEpoch+".0", "")
	seen = append(seen, frameSeqs(t, c.next(t))...)
	f.appendN(2)
	last := c.next(t)
	seen = append(seen, frameSeqs(t, last)...)
	c.close()

	f.appendN(3) // lands while disconnected

	// Last-Event-ID wins over the stale after= the page URL still carries.
	c2 := openChatStream(t, ts, path+"?after="+chatEpoch+".0", last.id)
	seen = append(seen, frameSeqs(t, c2.next(t))...)
	f.appendN(1)
	seen = append(seen, frameSeqs(t, c2.next(t))...)

	wantSeqs(t, seen, 1, 9)
}

func TestRunChatStreamWithoutCursorSendsTheWholeRing(t *testing.T) {
	f := newFakeChat(4)
	_, ts, path := newChatStreamServer(t, f)
	c := openChatStream(t, ts, path, "")
	wantSeqs(t, frameSeqs(t, c.next(t)), 1, 4)
}

func TestRunChatStreamWithoutCursorOnATrimmedRingStartsAtItsNewestPage(t *testing.T) {
	f := newFakeChat(8)
	f.dropOldest(4)
	_, ts, path := newChatStreamServer(t, f)
	c := openChatStream(t, ts, path, "")
	wantSeqs(t, frameSeqs(t, c.next(t)), 5, 8)
}

func TestRunChatStreamResets(t *testing.T) {
	cases := []struct {
		name   string
		cursor string
		setup  func(*fakeChat)
	}{
		{"epoch mismatch", "old.2", nil},
		{"cursor older than the ring", chatEpoch + ".1", func(f *fakeChat) { f.dropOldest(3) }},
		{"cursor newer than the newest", chatEpoch + ".99", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeChat(5)
			if tc.setup != nil {
				tc.setup(f)
			}
			_, ts, path := newChatStreamServer(t, f)
			openChatStream(t, ts, path+"?after="+tc.cursor, "").wantReset(t)
		})
	}
}

func TestRunChatStreamMalformedCursorIs400(t *testing.T) {
	f := newFakeChat(2)
	s, _, path := newChatStreamServer(t, f)
	for _, cursor := range []string{"nodot", ".3", "ep.", "ep.x", "ep.-1"} {
		rec := doChat(t, s, path+"?after="+cursor)
		if rec.Code != http.StatusBadRequest || rec.Header().Get("Content-Type") == "text/event-stream" {
			t.Errorf("after=%q: %d %q (%s), want 400 before SSE", cursor, rec.Code, rec.Body, rec.Header().Get("Content-Type"))
		}
		req := replyRequest("GET", path, "")
		req.Header.Set("Last-Event-ID", cursor)
		rec = httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("Last-Event-ID %q: %d, want 400", cursor, rec.Code)
		}
	}
	if n := f.subCount(); n != 0 {
		t.Errorf("%d subscriptions left open", n)
	}
}

func TestRunChatStreamEpochChangeWhileOpenResets(t *testing.T) {
	f := newFakeChat(2)
	_, ts, path := newChatStreamServer(t, f, func(s *Server) { s.chatCheck = 20 * time.Millisecond })
	c := openChatStream(t, ts, path, "")
	frameSeqs(t, c.next(t))
	f.setEpoch("ep2") // no notify: only the check ticker can see it
	c.wantReset(t)
}

func TestRunChatStreamSessionFlipResets(t *testing.T) {
	f := newFakeChat(2)
	s, ts, path := newChatStreamServer(t, f, func(s *Server) { s.chatCheck = 50 * time.Millisecond })
	c := openChatStream(t, ts, path, "")
	frameSeqs(t, c.next(t))

	s.runs.Apply(chatDelta("s2"))
	c.wantReset(t)
}

func TestRunChatStreamClosedNotifyResetsAndReturns(t *testing.T) {
	f := newFakeChat(2)
	_, ts, path := newChatStreamServer(t, f)
	c := openChatStream(t, ts, path, "")
	frameSeqs(t, c.next(t))
	f.closeSubs()

	if fr := c.next(t); fr.event != "reset" {
		t.Fatalf("frame %+v, want reset", fr)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range c.frames {
		}
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("handler did not end the stream within 1s of the notify channel closing")
	}
}

func TestRunChatStreamUnsubscribesOnDisconnect(t *testing.T) {
	f := newFakeChat(1)
	_, ts, path := newChatStreamServer(t, f)
	c := openChatStream(t, ts, path, "")
	frameSeqs(t, c.next(t))
	if n := f.subCount(); n != 1 {
		t.Fatalf("%d subscriptions while open, want 1", n)
	}
	c.close()
	deadline := time.Now().Add(3 * time.Second)
	for f.subCount() != 0 {
		if time.Now().After(deadline) {
			t.Fatal("subscription still held after the client left")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestRunChatStreamPings(t *testing.T) {
	f := newFakeChat(1)
	_, ts, path := newChatStreamServer(t, f, func(s *Server) { s.chatPing = 20 * time.Millisecond })
	c := openChatStream(t, ts, path, "")
	frameSeqs(t, c.next(t))
	deadline := time.After(3 * time.Second)
	for {
		select {
		case fr, ok := <-c.frames:
			if !ok {
				t.Fatal("stream ended before a ping")
			}
			if fr.event == "ping" {
				return
			}
		case <-deadline:
			t.Fatal("no ping within 3s")
		}
	}
}

// --- tool detail ---

func TestRunChatToolShape(t *testing.T) {
	f := newFakeChat(1)
	f.tools["toolu_1"] = &chat.Update{
		SessionUpdate: chat.SessionUpdateToolCallUpdate,
		ToolCallID:    "toolu_1",
		Title:         "main.go",
		Kind:          chat.KindEdit,
		Status:        chat.StatusCompleted,
		RawInput:      json.RawMessage(`{"file_path":"/w/main.go"}`),
		Meta:          map[string]any{"tool": "Edit"},
		Content: []chat.Content{
			{Type: "content", Content: &chat.ContentBlock{Type: "text", Text: "line one"}},
			{Type: "content", Content: &chat.ContentBlock{Type: "text", Text: "line two"}},
			{Type: "diff", Path: "/w/main.go", OldText: "a", NewText: "b"},
		},
	}
	s := newChatServer(t, f, chatSID)
	rec := doChat(t, s, chatRunPath(t, s)+"/tool/toolu_1")
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q", ct)
	}
	want := `{"toolCallId":"toolu_1","name":"Edit","title":"main.go","kind":"edit","status":"completed",` +
		`"input":{"file_path":"/w/main.go"},"output":"line one\nline two","truncated":false,` +
		`"diff":{"path":"/w/main.go","oldText":"a","newText":"b"}}`
	var got, exp any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	_ = json.Unmarshal([]byte(want), &exp)
	if fmt.Sprint(got) != fmt.Sprint(exp) {
		t.Fatalf("body = %s\nwant %s", rec.Body, want)
	}
}

func TestRunChatToolOmitsEmptyInputAndDiff(t *testing.T) {
	f := newFakeChat(1)
	f.tools["t"] = &chat.Update{ToolCallID: "t", Status: chat.StatusInProgress, Meta: map[string]any{"tool": "Bash"}}
	s := newChatServer(t, f, chatSID)
	rec := doChat(t, s, chatRunPath(t, s)+"/tool/t")
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v; %s", err, rec.Body)
	}
	for _, k := range []string{"input", "diff"} {
		if _, ok := got[k]; ok {
			t.Errorf("%q present: %s", k, rec.Body)
		}
	}
	if got["name"] != "Bash" || got["output"] != "" {
		t.Errorf("body = %s", rec.Body)
	}
}

func TestRunChatToolCapsTextOnARuneBoundary(t *testing.T) {
	big := strings.Repeat("é", maxChatToolText) // 2 bytes each: twice the cap
	f := newFakeChat(1)
	f.tools["t"] = &chat.Update{
		ToolCallID: "t",
		Meta:       map[string]any{"tool": "Write"},
		Content: []chat.Content{
			{Type: "content", Content: &chat.ContentBlock{Type: "text", Text: "x" + big}},
			{Type: "diff", Path: "/p", OldText: "x" + big, NewText: big},
		},
	}
	s := newChatServer(t, f, chatSID)
	rec := doChat(t, s, chatRunPath(t, s)+"/tool/t")
	var got struct {
		Output    string `json:"output"`
		Truncated bool   `json:"truncated"`
		Diff      struct {
			OldText string `json:"oldText"`
			NewText string `json:"newText"`
		} `json:"diff"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !got.Truncated {
		t.Error("truncated = false")
	}
	// A leading "x" puts every rune boundary on an odd offset, so a cut at
	// exactly the cap would split an "é".
	for name, v := range map[string]string{"output": got.Output, "oldText": got.Diff.OldText, "newText": got.Diff.NewText} {
		if len(v) > maxChatToolText || len(v) < maxChatToolText-utf8.UTFMax {
			t.Errorf("%s: %d bytes, want just under %d", name, len(v), maxChatToolText)
		}
		if !utf8.ValidString(v) || strings.ContainsRune(v, utf8.RuneError) {
			t.Errorf("%s: invalid UTF-8 after the cut", name)
		}
	}
}

func TestRunChatToolUnderTheCapIsNotTruncated(t *testing.T) {
	f := newFakeChat(1)
	f.tools["t"] = &chat.Update{ToolCallID: "t", Content: []chat.Content{
		{Type: "content", Content: &chat.ContentBlock{Type: "text", Text: strings.Repeat("a", maxChatToolText)}},
	}}
	s := newChatServer(t, f, chatSID)
	rec := doChat(t, s, chatRunPath(t, s)+"/tool/t")
	if strings.Contains(rec.Body.String(), `"truncated":true`) {
		t.Fatalf("exactly-at-cap output marked truncated")
	}
}

func TestRunChatToolFlagsAreIndependent(t *testing.T) {
	oversized, _ := json.Marshal(strings.Repeat("a", 64<<10))
	long := strings.Repeat("b", maxChatToolText+1)
	f := newFakeChat(1)
	f.tools["inputOnly"] = &chat.Update{ToolCallID: "inputOnly", Meta: map[string]any{"tool": "Write"},
		RawInput: json.RawMessage(`{"file_path":"/p","content":` + string(oversized) + `}`),
		Content:  []chat.Content{{Type: "content", Content: &chat.ContentBlock{Type: "text", Text: "ok"}}}}
	f.tools["outputOnly"] = &chat.Update{ToolCallID: "outputOnly", Meta: map[string]any{"tool": "Write"},
		RawInput: json.RawMessage(`{"file_path":"/p","content":"x"}`),
		Content:  []chat.Content{{Type: "content", Content: &chat.ContentBlock{Type: "text", Text: long}}}}
	f.tools["both"] = &chat.Update{ToolCallID: "both", Meta: map[string]any{"tool": "Write"},
		RawInput: json.RawMessage(`{"file_path":"/p","content":` + string(oversized) + `}`),
		Content:  []chat.Content{{Type: "content", Content: &chat.ContentBlock{Type: "text", Text: long}}}}
	s := newChatServer(t, f, chatSID)

	for _, tc := range []struct {
		id               string
		wantInput        bool
		wantTruncated    bool
		wantInputOmitted bool
	}{
		{"inputOnly", false, false, true},
		{"outputOnly", true, true, false},
		{"both", false, true, true},
	} {
		rec := doChat(t, s, chatRunPath(t, s)+"/tool/"+tc.id)
		var got map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("%s: decode: %v", tc.id, err)
		}
		if _, ok := got["input"]; ok != tc.wantInput {
			t.Errorf("%s: input present = %v, want %v (%d bytes)", tc.id, ok, tc.wantInput, rec.Body.Len())
		}
		// truncated has no omitempty (always present); inputOmitted does,
		// so its key is absent when the input was kept.
		if got["truncated"] != tc.wantTruncated {
			t.Errorf("%s: truncated = %v, want %v", tc.id, got["truncated"], tc.wantTruncated)
		}
		if _, ok := got["inputOmitted"]; ok != tc.wantInputOmitted {
			t.Errorf("%s: inputOmitted present = %v, want %v", tc.id, ok, tc.wantInputOmitted)
		}
	}
}

// --- auth ---

func TestRunChatRoutesAreBehindTheAuthGate(t *testing.T) {
	s := newFullServer(t, Config{StatusDir: t.TempDir(), AuthEnabled: true, Mode: mode.Dispatcher})
	for _, path := range []string{"/api/runs/x/chat", "/api/runs/x/chat/stream", "/api/runs/x/chat/tool/y"} {
		req := httptest.NewRequest("GET", "http://127.0.0.1"+path, nil)
		req.Host = "127.0.0.1"
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("GET %s: %d, want 401", path, rec.Code)
		}
	}
}

// --- real hub pipeline ---

// startHubPipeline runs the real hub and hook source over stateDir and
// returns a server wired to both, as New wires them.
func startHubPipeline(t *testing.T, stateDir string) *Server {
	t.Helper()
	discard := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := hub.NewWithOptions(stateDir, hub.Options{ClaudeProjectsDir: "-"}, discard)

	ctx, cancel := context.WithCancel(context.Background())
	hubDone := make(chan struct{})
	go func() {
		defer close(hubDone)
		_ = h.Run(ctx)
	}()

	reg := runs.NewRegistry(runs.DefaultOrder)
	deltas := make(chan runs.Delta, 16)
	pumpDone := make(chan struct{})
	go func() {
		defer close(pumpDone)
		for d := range deltas {
			reg.Apply(d)
		}
	}()

	src := runs.NewHookSource(h, nil)
	srcDone := make(chan struct{})
	go func() {
		defer close(srcDone)
		_ = src.Run(ctx, deltas)
	}()

	t.Cleanup(func() {
		cancel()
		waitClosed := func(what string, ch <-chan struct{}) {
			select {
			case <-ch:
			case <-time.After(5 * time.Second):
				t.Errorf("%s did not stop after cancel", what)
			}
		}
		waitClosed("hub", hubDone)
		waitClosed("hook source", srcDone)
		close(deltas)
		waitClosed("delta pump", pumpDone)
	})

	return &Server{runs: reg, chat: h}
}

// pollChat GETs the single run's chat page until want(code) holds or 5s pass.
func pollChat(t *testing.T, s *Server, want func(int, []byte) bool) (int, []byte) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var code int
	var body []byte
	for time.Now().Before(deadline) {
		if snap := s.runs.Snapshot(); len(snap) == 1 {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest("GET", "/api/runs/"+snap[0].ID+"/chat", nil)
			req.SetPathValue("id", snap[0].ID)
			s.handleRunChat(rec, req)
			code, body = rec.Code, rec.Body.Bytes()
			if want(code, body) {
				return code, body
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	return code, body
}

func TestRunChatForACodexRunIsNoChat(t *testing.T) {
	t.Setenv("TMUX_PANE", "")
	t.Setenv("TMUX", "")

	transcript := filepath.Join(t.TempDir(), "codex.jsonl")
	if err := os.WriteFile(transcript, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stateDir := t.TempDir()
	payload, err := json.Marshal(map[string]any{
		"engine":          "codex",
		"canonical_event": "session_start",
		"native_event":    "SessionStart",
		"session_id":      "codex-s1",
		"cwd":             "/w",
		"native": map[string]any{
			"cwd":             "/w",
			"hook_event_name": "SessionStart",
			"session_id":      "codex-s1",
			"transcript_path": transcript,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := hook.Dispatch("", stateDir, bytes.NewReader(payload)); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	// The 404 below must come from the missing reader, not a missing Session.
	st, err := hook.Read(hook.Path(stateDir, "codex-s1"))
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	if st.Agent != "codex" || st.TranscriptPath != transcript {
		t.Fatalf("state agent=%q transcript_path=%q, want codex and %q", st.Agent, st.TranscriptPath, transcript)
	}

	s := startHubPipeline(t, stateDir)
	code, body := pollChat(t, s, func(code int, _ []byte) bool { return code != http.StatusServiceUnavailable })
	if code != http.StatusNotFound || strings.TrimSpace(string(body)) != "no chat" {
		t.Fatalf("got %d %q, want 404 no chat", code, body)
	}
}

func TestRunChatForANativeClaudeRunServesItsTranscript(t *testing.T) {
	t.Setenv("TMUX_PANE", "")
	t.Setenv("TMUX", "")

	transcript := filepath.Join(t.TempDir(), "claude-s1.jsonl")
	lines := strings.Join([]string{
		`{"type":"user","uuid":"u1","timestamp":"2024-01-01T00:00:00.000Z","origin":{"kind":"human"},"message":{"role":"user","content":"fix the bug"}}`,
		`{"type":"assistant","uuid":"a1","timestamp":"2024-01-01T00:00:01.000Z","message":{"id":"msg_1","role":"assistant","content":[{"type":"text","text":"Reading the file"}]}}`,
		`{"type":"assistant","uuid":"a2","timestamp":"2024-01-01T00:00:02.000Z","message":{"id":"msg_1","role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"Read","input":{"file_path":"/w/main.go"}}]}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(transcript, []byte(lines), 0o600); err != nil {
		t.Fatal(err)
	}

	stateDir := t.TempDir()
	payload, err := json.Marshal(map[string]any{
		"hook_event_name": "SessionStart",
		"session_id":      "claude-s1",
		"cwd":             "/w",
		"transcript_path": transcript,
		"source":          "startup",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := hook.Dispatch("", stateDir, bytes.NewReader(payload)); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	s := startHubPipeline(t, stateDir)
	code, body := pollChat(t, s, func(code int, body []byte) bool {
		var p hub.ChatPage
		return code == 200 && json.Unmarshal(body, &p) == nil && len(p.Updates) == 3
	})
	if code != 200 {
		t.Fatalf("got %d %q, want 200", code, body)
	}
	var page hub.ChatPage
	if err := json.Unmarshal(body, &page); err != nil {
		t.Fatalf("decode: %v; %s", err, body)
	}
	if seqsOf(page.Updates) != "1,2,3" || page.Epoch == "" || page.More {
		t.Fatalf("page = %s, want 3 updates seqs 1..3", body)
	}
	kinds := []string{page.Updates[0].SessionUpdate, page.Updates[1].SessionUpdate, page.Updates[2].SessionUpdate}
	if fmt.Sprint(kinds) != fmt.Sprint([]string{chat.SessionUpdateUserMessageChunk, chat.SessionUpdateAgentMessageChunk, chat.SessionUpdateToolCall}) {
		t.Fatalf("kinds = %v", kinds)
	}
}

func TestRunChatForAPiRunServesItsTranscript(t *testing.T) {
	t.Setenv("TMUX_PANE", "")
	t.Setenv("TMUX", "")

	transcript := filepath.Join(t.TempDir(), "pi-s1.jsonl")
	lines := strings.Join([]string{
		`{"type":"session","version":3,"id":"pi-s1","timestamp":"2025-01-01T00:00:00.000Z","cwd":"/w"}`,
		`{"type":"message","id":"e1","parentId":null,"timestamp":"2025-01-01T00:00:01.000Z","message":{"role":"user","content":"read main.go","timestamp":1735689601000}}`,
		`{"type":"message","id":"e2","parentId":"e1","timestamp":"2025-01-01T00:00:02.000Z","message":{"role":"assistant","content":[{"type":"text","text":"Reading it"},{"type":"toolCall","id":"call_1","name":"read","arguments":{"path":"/w/main.go"}}],"timestamp":1735689602000}}`,
		`{"type":"message","id":"e3","parentId":"e2","timestamp":"2025-01-01T00:00:03.000Z","message":{"role":"toolResult","toolCallId":"call_1","toolName":"read","content":[{"type":"text","text":"package main"}],"isError":false,"timestamp":1735689603000}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(transcript, []byte(lines), 0o600); err != nil {
		t.Fatal(err)
	}

	stateDir := t.TempDir()
	payload, err := json.Marshal(map[string]any{
		"engine":          "pi",
		"canonical_event": "session_start",
		"native_event":    "session_start",
		"session_id":      "pi-s1",
		"cwd":             "/w",
		"native": map[string]any{
			"session_file": transcript,
			"reason":       "startup",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := hook.Dispatch("", stateDir, bytes.NewReader(payload)); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	s := startHubPipeline(t, stateDir)
	code, body := pollChat(t, s, func(code int, body []byte) bool {
		var p hub.ChatPage
		return code == 200 && json.Unmarshal(body, &p) == nil && len(p.Updates) == 4
	})
	if code != 200 {
		t.Fatalf("got %d %q, want 200", code, body)
	}
	var page hub.ChatPage
	if err := json.Unmarshal(body, &page); err != nil {
		t.Fatalf("decode: %v; %s", err, body)
	}
	var kinds []string
	for _, u := range page.Updates {
		kinds = append(kinds, u.SessionUpdate)
	}
	wantKinds := []string{chat.SessionUpdateUserMessageChunk, chat.SessionUpdateAgentMessageChunk, chat.SessionUpdateToolCall, chat.SessionUpdateToolCallUpdate}
	if fmt.Sprint(kinds) != fmt.Sprint(wantKinds) {
		t.Fatalf("kinds = %v, want %v", kinds, wantKinds)
	}

	snap := s.runs.Snapshot()
	if len(snap) != 1 {
		t.Fatalf("registry holds %d listed runs, want 1", len(snap))
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", chatRunPath(t, s)+"/tool/call_1", nil)
	req.SetPathValue("id", snap[0].ID)
	req.SetPathValue("callId", "call_1")
	s.handleRunChatTool(rec, req)
	if rec.Code != 200 {
		t.Fatalf("tool: status %d: %s", rec.Code, rec.Body)
	}
	var tool struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &tool); err != nil || tool.Name != "read" {
		t.Fatalf("tool body = %s (err %v), want name read", rec.Body, err)
	}
}
