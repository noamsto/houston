package tmux

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSubscribeDeliversOutput(t *testing.T) {
	cc := NewControlClient("test")
	sub := cc.Subscribe("%1")

	cc.dispatch("%1", []byte("hello"))

	select {
	case ev := <-sub.C():
		if ev.Dirty {
			t.Fatalf("got dirty event, want data")
		}
		if string(ev.Data) != "hello" {
			t.Fatalf("got %q, want %q", ev.Data, "hello")
		}
	default:
		t.Fatal("no event delivered")
	}
}

func TestUnsubscribeStopsDelivery(t *testing.T) {
	cc := NewControlClient("test")
	sub := cc.Subscribe("%1")
	cc.Unsubscribe("%1", sub)

	cc.dispatch("%1", []byte("hello"))

	select {
	case ev := <-sub.C():
		t.Fatalf("got event %+v after unsubscribe", ev)
	default:
	}
}

func TestUnsubscribeRemovesOnlyThatSubscriber(t *testing.T) {
	cc := NewControlClient("test")
	a := cc.Subscribe("%1")
	b := cc.Subscribe("%1")
	cc.Unsubscribe("%1", a)

	cc.dispatch("%1", []byte("hello"))

	if len(a.C()) != 0 {
		t.Fatal("unsubscribed handle still received output")
	}
	if len(b.C()) != 1 {
		t.Fatalf("remaining subscriber got %d events, want 1", len(b.C()))
	}
}

// fillSub saturates a subscriber's buffer so the next dispatch must drop.
func fillSub(cc *ControlClient, paneID string, s *PaneSub) {
	for i := 0; i < cap(s.ch); i++ {
		cc.dispatch(paneID, []byte("x"))
	}
}

func TestDropMarksDirtyAndDiscardsStaleBuffer(t *testing.T) {
	cc := NewControlClient("test")
	sub := cc.Subscribe("%1")

	fillSub(cc, "%1", sub)
	cc.dispatch("%1", []byte("this one cannot fit"))

	ev := <-sub.C()
	if !ev.Dirty {
		t.Fatalf("first event after a drop = %+v, want Dirty", ev)
	}
	if len(sub.C()) != 0 {
		t.Fatalf("%d stale events survived the drop, want 0", len(sub.C()))
	}
}

func TestDirtySuppressesDeliveryUntilAcked(t *testing.T) {
	cc := NewControlClient("test")
	sub := cc.Subscribe("%1")

	fillSub(cc, "%1", sub)
	cc.dispatch("%1", []byte("overflow"))
	<-sub.C() // consume the Dirty marker

	cc.dispatch("%1", []byte("still suppressed"))
	if len(sub.C()) != 0 {
		t.Fatal("delivered output while dirty; subscriber has not re-seeded yet")
	}

	cc.AckReseed(sub)
	cc.dispatch("%1", []byte("after reseed"))

	ev := <-sub.C()
	if string(ev.Data) != "after reseed" {
		t.Fatalf("got %q, want %q", ev.Data, "after reseed")
	}
}

func TestMarkPendingReseedReArmsExactlyOnce(t *testing.T) {
	cc := NewControlClient("test")
	sub := cc.Subscribe("%1")

	// Drive the subscriber dirty, then consume the marker the way the
	// WebSocket write loop's coalescing branch does.
	fillSub(cc, "%1", sub)
	cc.dispatch("%1", []byte("overflow"))
	if ev := <-sub.C(); !ev.Dirty {
		t.Fatalf("setup: expected Dirty, got %+v", ev)
	}

	cc.MarkPendingReseed(sub)

	if got := len(sub.C()); got != 1 {
		t.Fatalf("after re-arm, %d events queued, want exactly 1", got)
	}
	if ev := <-sub.C(); !ev.Dirty {
		t.Fatalf("re-armed event = %+v, want Dirty", ev)
	}

	// Still suppressed until the consumer acks.
	cc.dispatch("%1", []byte("suppressed"))
	if len(sub.C()) != 0 {
		t.Fatal("delivered output while still dirty")
	}
	cc.AckReseed(sub)
	cc.dispatch("%1", []byte("after ack"))
	if ev := <-sub.C(); string(ev.Data) != "after ack" {
		t.Fatalf("got %q, want %q", ev.Data, "after ack")
	}
}

func TestDropOnOneSubscriberDoesNotAffectAnother(t *testing.T) {
	cc := NewControlClient("test")
	slow := cc.Subscribe("%1")
	fast := cc.Subscribe("%1")

	fillSub(cc, "%1", slow) // also fills fast; drain fast so it has room
	for len(fast.C()) > 0 {
		<-fast.C()
	}

	cc.dispatch("%1", []byte("live"))

	if ev := <-slow.C(); !ev.Dirty {
		t.Fatalf("slow subscriber = %+v, want Dirty", ev)
	}
	if ev := <-fast.C(); ev.Dirty {
		t.Fatal("fast subscriber was marked dirty by its neighbour's drop")
	}
}

// feed runs the read loop over a canned control-mode transcript.
func feed(cc *ControlClient, transcript string) {
	cc.readLoop(bufio.NewReader(strings.NewReader(transcript)))
}

// gapCount and gapFor read cc.gaps under cc.mu — the read loop and every
// armed deadline write that map concurrently with the test goroutine.
func (cc *ControlClient) gapCount() int {
	cc.mu.RLock()
	defer cc.mu.RUnlock()
	return len(cc.gaps)
}

func (cc *ControlClient) gapFor(paneID string) *gap {
	cc.mu.RLock()
	defer cc.mu.RUnlock()
	return cc.gaps[paneID]
}

// isDirty reports whether a subscriber is currently withholding delivery. A
// test asserting that nothing reaches a subscriber needs it: dispatch drops
// everything for a dirty subscriber, so silence alone cannot tell "tmux sent
// nothing" apart from "we refused to deliver it".
func (cc *ControlClient) isDirty(s *PaneSub) bool {
	cc.mu.RLock()
	defer cc.mu.RUnlock()
	return s.dirty
}

// TestPauseDefersDirtyUntilContinue proves that %pause alone must queue no
// Dirty: a capture triggered at %pause predates the output tmux discards
// during the gap, so re-seeding then paints onto a stale screen. The Dirty
// belongs at %continue, once the gap has actually closed.
func TestPauseDefersDirtyUntilContinue(t *testing.T) {
	cc := NewControlClient("test")
	sub := cc.Subscribe("%1")

	feed(cc, "%output %1 before\\015\\012\n%pause %1\n")

	ev := <-sub.C()
	if ev.Dirty {
		t.Fatalf("got Dirty at %%pause, want the pre-gap data")
	}
	if string(ev.Data) != "before\r\n" {
		t.Fatalf("got %q, want %q", ev.Data, "before\r\n")
	}
	if len(sub.C()) != 0 {
		t.Fatalf("%d events queued after %%pause, want 0", len(sub.C()))
	}

	feed(cc, "%output %1 during\\015\\012\n%continue %1\n")

	ev = <-sub.C()
	if !ev.Dirty {
		t.Fatalf("first event after %%continue = %+v, want Dirty", ev)
	}
	if len(sub.C()) != 0 {
		t.Fatalf("%d events survived the gap, want 0 — the backlog is discarded", len(sub.C()))
	}
}

func TestContinueDoesNotMarkAMidGapSubscriber(t *testing.T) {
	cc := NewControlClient("test")

	feed(cc, "%pause %1\n")
	sub := cc.Subscribe("%1")
	feed(cc, "%continue %1\n")

	if len(sub.C()) != 0 {
		t.Fatal("continue marked a subscriber that attached during the gap")
	}
}

func TestPauseOnAnotherPaneIsIgnored(t *testing.T) {
	cc := NewControlClient("test")
	other := cc.Subscribe("%2")
	mine := cc.Subscribe("%1")

	feed(cc, "%pause %2\n%continue %2\n")

	ev := <-other.C()
	if !ev.Dirty {
		t.Fatalf("subscriber on the paused pane got %+v, want Dirty", ev)
	}
	if len(mine.C()) != 0 {
		t.Fatalf("a pause on %%2 marked %%1 dirty (%d events)", len(mine.C()))
	}
}

func TestRepeatedPauseIsANoOp(t *testing.T) {
	cc := NewControlClient("test")
	t.Cleanup(cc.clearGaps) // nothing closes this gap; disarm its deadline
	sub := cc.Subscribe("%1")

	feed(cc, "%pause %1\n")
	first := cc.gapFor("%1")
	feed(cc, "%pause %1\n")

	if len(sub.C()) != 0 {
		t.Fatalf("%d events queued after a repeated %%pause, want 0", len(sub.C()))
	}
	// Identity, not count: overwriting the record leaves one entry too, while
	// orphaning the first gap's armed timer.
	if got := cc.gapFor("%1"); got != first {
		t.Fatalf("repeated %%pause replaced the gap record (%p → %p)", first, got)
	}
}

func TestOrphanContinueMarksNobody(t *testing.T) {
	cc := NewControlClient("test")
	sub := cc.Subscribe("%1")

	feed(cc, "%continue %1\n")

	if len(sub.C()) != 0 {
		t.Fatal("continue with no open gap marked a subscriber dirty")
	}
}

// TestPauseWithAQuotedPaneIDOpensNoGap proves the injection is closed
// upstream of expireGap's command string: a %pause whose pane-ID field isn't
// shaped like a real pane ID must not be treated as a notification at all, or
// that field would reach refresh-client -A '<id>:continue' verbatim.
func TestPauseWithAQuotedPaneIDOpensNoGap(t *testing.T) {
	cc := NewControlClient("test")
	t.Cleanup(cc.clearGaps) // a failed fix would open a gap nothing closes

	feed(cc, "%pause %0';kill-server;'x\n")

	if n := cc.gapCount(); n != 0 {
		t.Fatalf("%d gaps open after a malformed %%pause, want 0", n)
	}
}

// scriptedDialer hands out one canned transcript per dial, so a test can drive
// the client through a disconnect and a re-attach without running tmux.
type scriptedDialer struct {
	mu          sync.Mutex
	transcripts []string
	dials       int
}

func (d *scriptedDialer) dial() (io.ReadCloser, io.Writer, func() error, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	i := d.dials
	d.dials++
	if i >= len(d.transcripts) {
		// Keep the connection open but silent so the client stops re-dialling.
		pr, pw := io.Pipe()
		return pr, io.Discard, func() error { return pw.Close() }, nil
	}
	return io.NopCloser(strings.NewReader(d.transcripts[i])), io.Discard,
		func() error { return nil }, nil
}

func (d *scriptedDialer) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.dials
}

// recordingDialer hands out a live connection per dial: the test writes
// control lines into it one at a time and reads back what the client wrote.
// scriptedDialer can do neither — it replays a fixed transcript to EOF and
// throws every write away, so it cannot answer a command's %begin/%end, and an
// unanswered command parks RunCommand until Close().
type recordingDialer struct {
	mu    sync.Mutex
	conns []*recordedConn
	// onDial arms a connection before it is handed out. It takes the index
	// because arming every connection hot-loops the dialer.
	onDial func(i int, c *recordedConn)
	// baseline replaces baselineBlock on every connection.
	baseline string
}

type recordedConn struct {
	pw       *io.PipeWriter
	mu       sync.Mutex
	buf      strings.Builder
	blocks   int   // command numbers for the blocks this connection answers
	writeErr error // when set, every stdin write fails; nothing else here can
}

// baselineBlock is what a real tmux -CC answers its own attach-session command
// with, before houston has written anything. The fake owes it because blocks
// bind to commands by order: without it every block on the connection answers
// the command one place ahead of the right one. Byte-faithful down to the
// control-mode introducer and the CRLFs, because the introducer is exactly what
// decides whether the baseline's %begin parses at all.
const baselineBlock = "\x1bP1000p%begin 1700000000 1 0\r\n%end 1700000000 1 0\r\n"

func (d *recordingDialer) dial() (io.ReadCloser, io.Writer, func() error, error) {
	pr, pw := io.Pipe()
	c := &recordedConn{pw: pw}
	d.mu.Lock()
	if d.onDial != nil {
		// Called before the connection is visible to connAt, so a test can arm
		// it ahead of attach's own write.
		d.onDial(len(d.conns), c)
	}
	d.conns = append(d.conns, c)
	baseline := d.baseline
	d.mu.Unlock()
	if baseline == "" {
		baseline = baselineBlock
	}
	// Prefixed onto the reader rather than written into pw: readLoop does not
	// run until attach returns, so a write here would deadlock Start().
	return io.NopCloser(io.MultiReader(strings.NewReader(baseline), pr)),
		c, func() error { return pw.Close() }, nil
}

// connAt waits for the i-th dial and returns its connection. Dial i>0 happens
// on supervise's goroutine, so it cannot simply be indexed.
func (d *recordingDialer) connAt(t *testing.T, i int) *recordedConn {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		d.mu.Lock()
		n := len(d.conns)
		var c *recordedConn
		if i < n {
			c = d.conns[i]
		}
		d.mu.Unlock()
		if c != nil {
			return c
		}
		select {
		case <-deadline:
			t.Fatalf("connection %d never dialled; %d dials so far", i, n)
		case <-time.After(time.Millisecond):
		}
	}
}

func (c *recordedConn) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.writeErr != nil {
		return 0, c.writeErr
	}
	return c.buf.Write(p)
}

// nextBlock numbers the next block this connection emits.
func (c *recordedConn) nextBlock() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.blocks++
	return c.blocks
}

func (c *recordedConn) written() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.String()
}

// feedLine delivers one control-mode line. The write returns once readLoop has
// copied the bytes, which is before it has dispatched them — see sync().
func (c *recordedConn) feedLine(t *testing.T, line string) {
	t.Helper()
	if _, err := io.WriteString(c.pw, line+"\n"); err != nil {
		t.Fatalf("feed %q: %v", line, err)
	}
}

// sync blocks until everything fed before it has been dispatched. readLoop is
// strictly sequential, so a sentinel on its own pane clearing proves the lines
// ahead of it were handled. No dialer-fed negative assertion is sound without
// it.
func (c *recordedConn) sync(t *testing.T, sentinel *PaneSub) {
	t.Helper()
	c.feedLine(t, "%output %9 sync")
	select {
	case ev := <-sentinel.C():
		if ev.Dirty {
			t.Fatalf("sentinel subscriber went dirty: %+v", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("sentinel never cleared — readLoop is not consuming")
	}
}

// waitForWrite blocks until want appears in what the client wrote. On the
// deadline path this is the only sound barrier: the resume comes from the
// timer goroutine, not readLoop, and expireGap writes inside RunCommand and
// marks only after it returns.
func waitForWrite(t *testing.T, c *recordedConn, want string) {
	t.Helper()
	waitForWriteCount(t, c, want, 1)
}

func waitForWriteCount(t *testing.T, c *recordedConn, want string, n int) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		got := strings.Count(c.written(), want)
		if got >= n {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("connection got %q %d times, want %d; wrote %q", want, got, n, c.written())
		case <-time.After(time.Millisecond):
		}
	}
}

// answerCommand releases a RunCommand caller parked on its response block.
func (c *recordedConn) answerCommand(t *testing.T, body ...string) {
	t.Helper()
	c.emitBlock(t, "%end", body)
}

// answerCommandError releases a RunCommand caller with tmux's refusal.
func (c *recordedConn) answerCommandError(t *testing.T, body string) {
	t.Helper()
	c.emitBlock(t, "%error", []string{body})
}

// emitBlock writes one block. Both ends carry the same command number: a
// mismatch desynchronises the connection instead of delivering.
func (c *recordedConn) emitBlock(t *testing.T, terminator string, body []string) {
	t.Helper()
	num := c.nextBlock()
	c.feedLine(t, fmt.Sprintf("%%begin 1700000000 %d 0", num))
	for _, line := range body {
		c.feedLine(t, line)
	}
	c.feedLine(t, fmt.Sprintf("%s 1700000000 %d 0", terminator, num))
}

// ackAttach answers the ignore-size command attach writes. Every block is
// bound in order to the command it answers, so a test that leaves ignore-size
// outstanding binds its own command's reply to the attach write and parks.
func (c *recordedConn) ackAttach(t *testing.T) {
	t.Helper()
	waitForWrite(t, c, ignoreSizeCmd)
	c.answerCommand(t)
}

func expectDirty(t *testing.T, s *PaneSub, who string) {
	t.Helper()
	select {
	case ev := <-s.C():
		if !ev.Dirty {
			t.Fatalf("%s got %+v, want Dirty", who, ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("%s never got Dirty", who)
	}
}

const resume1 = "refresh-client -A '%1:continue'"

// pinnedDial hands out d's first connection and fails every later dial.
//
// Every negative assertion about gap state needs this pin. Unpinned, supervise
// re-dials inside its backoff and the re-attach runs markAllDirty →
// clearGapsLocked, which deletes the very gap the test is examining: gapCount()
// then reads 0 whether the re-arm under test happened or not, and a gap that
// did get re-armed fires its deadline against a connection the test no longer
// holds. Tests that want the re-dial say so and use d.dial directly.
func pinnedDial(d *recordingDialer) func() (io.ReadCloser, io.Writer, func() error, error) {
	first := true
	return func() (io.ReadCloser, io.Writer, func() error, error) {
		if !first {
			return nil, nil, nil, errors.New("pinned to one connection")
		}
		first = false // Start()'s own call; every later one runs in supervise's goroutine, so the two never race
		return d.dial()
	}
}

// waitRetired blocks until supervise has retired the live connection. connOK
// and gone are cleared in the same critical section, so this is the barrier for
// "the next RunCommand cannot even write".
func waitRetired(t *testing.T, cc *ControlClient) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for cc.Connected() {
		select {
		case <-deadline:
			t.Fatal("connection was never retired")
		case <-time.After(time.Millisecond):
		}
	}
}

// logCapture collects slog output. The buffer carries its own lock: the lines
// under test are written from expireGap's goroutine and from supervise's.
type logCapture struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *logCapture) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *logCapture) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

// captureLogs routes slog to a buffer for the rest of the test. slog.SetDefault
// is process-global, so a test using it must never call t.Parallel(): it would
// clobber, and be clobbered by, anything logging beside it. Debug is enabled so
// a test can prove the quiet path ran at all instead of reading an empty buffer
// as success.
func captureLogs(t *testing.T) *logCapture {
	t.Helper()
	lc := &logCapture{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(lc, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return lc
}

// assertNoWarn proves a failure was logged quietly. The positive half carries
// the test: an empty buffer satisfies "no warning" even when expireGap never
// ran at all.
func assertNoWarn(t *testing.T, logs *logCapture, wantDebug string) {
	t.Helper()
	got := logs.String()
	if !strings.Contains(got, wantDebug) {
		t.Fatalf("no %q line logged; got %q", wantDebug, got)
	}
	if strings.Contains(got, "level=WARN") {
		t.Fatalf("warned about a failure the reconnect already recovers from: %q", got)
	}
}

func TestGapDeadlineResumesAndMarksEverySubscriber(t *testing.T) {
	d := &recordingDialer{}
	cc := NewControlClient("test")
	cc.dial = d.dial
	cc.backoff = time.Millisecond
	// The late subscriber must land inside the gap. Nothing enforces that
	// against a running clock, so the deadline is put out of reach and
	// expireGap invoked by hand; the timer path is covered by the tests below.
	cc.gapDeadline = time.Hour

	sentinel := cc.Subscribe("%9")
	early := cc.Subscribe("%1")

	if err := cc.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = cc.Close() }()
	conn := d.connAt(t, 0)
	conn.ackAttach(t)

	conn.feedLine(t, "%pause %1")
	conn.sync(t, sentinel) // the gap is open
	late := cc.Subscribe("%1")

	g := cc.gapFor("%1")
	if g == nil {
		t.Fatalf("no gap open after %%pause")
	}
	// On its own goroutine: expireGap parks in RunCommand until answered.
	go cc.expireGap("%1", g)

	waitForWrite(t, conn, resume1)
	conn.answerCommand(t)

	expectDirty(t, early, "subscriber present at %pause")
	expectDirty(t, late, "subscriber that attached mid-gap")

	if n := strings.Count(conn.written(), resume1); n != 1 {
		t.Fatalf("wrote %d resumes, want exactly 1: %q", n, conn.written())
	}
}

func TestGapDeadlineResumesBeforeMarking(t *testing.T) {
	d := &recordingDialer{}
	cc := NewControlClient("test")
	cc.dial = d.dial
	cc.backoff = time.Millisecond
	cc.gapDeadline = 75 * time.Millisecond

	sub := cc.Subscribe("%1")

	if err := cc.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = cc.Close() }()
	conn := d.connAt(t, 0)
	conn.ackAttach(t)

	conn.feedLine(t, "%pause %1")
	waitForWrite(t, conn, resume1)

	// The resume is on the wire but unanswered, so the pane is still paused.
	// A Dirty here sends the consumer into capture-pane against a pane tmux is
	// still discarding output for, and having acked it never re-seeds again.
	if n := len(sub.C()); n != 0 {
		t.Fatalf("%d events queued before the resume completed, want 0", n)
	}

	conn.answerCommand(t)
	expectDirty(t, sub, "subscriber")
}

// TestRefusedResumeReArmsTheGap proves a resume tmux refuses does not abandon
// the pane. %pause is edge-triggered, so tmux never re-announces a pane it
// already considers paused: drop the gap on a failed resume and nothing is
// left to retry it, and the pane's output is discarded forever. The refusal
// must also mark nobody — a Dirty against a still-paused pane both seeds the
// pre-gap screen and swallows the retry's own mark.
func TestRefusedResumeReArmsTheGap(t *testing.T) {
	d := &recordingDialer{}
	cc := NewControlClient("test")
	cc.dial = d.dial
	cc.backoff = time.Millisecond
	cc.gapDeadline = 75 * time.Millisecond

	sub := cc.Subscribe("%1")

	if err := cc.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = cc.Close() }()
	conn := d.connAt(t, 0)
	conn.ackAttach(t)

	conn.feedLine(t, "%pause %1")
	waitForWrite(t, conn, resume1)
	conn.answerCommandError(t, "can't find pane")

	waitForWriteCount(t, conn, resume1, 2)
	if n := len(sub.C()); n != 0 {
		t.Fatalf("%d events queued off a refused resume, want 0", n)
	}

	conn.answerCommand(t)
	expectDirty(t, sub, "subscriber on the refused pane")
}

// TestRefusedResumeWithNoSubscribersStopsRetrying pins the other half: a
// retry loop with nobody watching would run for the life of the client.
func TestRefusedResumeWithNoSubscribersStopsRetrying(t *testing.T) {
	d := &recordingDialer{}
	cc := NewControlClient("test")
	cc.dial = d.dial
	cc.backoff = time.Millisecond
	cc.gapDeadline = 20 * time.Millisecond

	if err := cc.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = cc.Close() }()
	conn := d.connAt(t, 0)
	conn.ackAttach(t)

	conn.feedLine(t, "%pause %1")
	waitForWrite(t, conn, resume1)
	conn.answerCommandError(t, "can't find pane")

	time.Sleep(5 * cc.gapDeadline)
	if n := cc.gapCount(); n != 0 {
		t.Fatalf("%d gaps armed for a pane nobody subscribes to, want 0", n)
	}
	if n := strings.Count(conn.written(), resume1); n != 1 {
		t.Fatalf("wrote %d resumes with no subscribers, want exactly 1", n)
	}
}

// The three tests below fix the other half of the refusal rule: only a refusal
// tmux actually sent may re-arm the gap. Every other failure is already on its
// way to a re-attach, and that sweeps the gaps and re-seeds every subscriber,
// so a deadline re-armed there resumes nothing and only logs about it.

// TestUnwritableResumeDoesNotReArm covers the failure that never reached tmux:
// the connection was retired before the write, so RunCommand returns without
// writing. Nothing is paused as far as tmux is concerned that a retry could fix.
func TestUnwritableResumeDoesNotReArm(t *testing.T) {
	logs := captureLogs(t) // process-global: never t.Parallel() this test
	d := &recordingDialer{}
	cc := NewControlClient("test")
	cc.dial = pinnedDial(d)
	cc.backoff = time.Millisecond
	// Out of reach, so the gap under test is the one the test opened and
	// expireGap runs exactly once, by hand.
	cc.gapDeadline = time.Hour

	sentinel := cc.Subscribe("%9")
	sub := cc.Subscribe("%1")

	if err := cc.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = cc.Close() }()
	conn := d.connAt(t, 0)
	conn.ackAttach(t)

	conn.feedLine(t, "%pause %1")
	conn.sync(t, sentinel)
	g := cc.gapFor("%1")
	if g == nil {
		t.Fatalf("no gap open after %%pause")
	}

	_ = conn.pw.Close()
	waitRetired(t, cc)

	cc.expireGap("%1", g) // returns without parking: RunCommand finds gone nil

	if n := cc.gapCount(); n != 0 {
		t.Fatalf("%d gaps armed after a resume that was never written, want 0", n)
	}
	if strings.Contains(conn.written(), resume1) {
		t.Fatalf("wrote a resume onto a retired connection: %q", conn.written())
	}
	if n := len(sub.C()); n != 0 {
		t.Fatalf("%d events queued off a failed resume, want 0", n)
	}
	assertNoWarn(t, logs, "gap deadline resume abandoned")
}

// TestResumeLostWithItsConnectionDoesNotReArm covers the other connection-lost
// shape: the resume was written, tmux may well have acted on it, and only the
// reply is missing. The re-attach is what recovers the pane either way.
func TestResumeLostWithItsConnectionDoesNotReArm(t *testing.T) {
	logs := captureLogs(t) // process-global: never t.Parallel() this test
	d := &recordingDialer{}
	cc := NewControlClient("test")
	cc.dial = pinnedDial(d)
	cc.backoff = time.Millisecond
	cc.gapDeadline = time.Hour

	sentinel := cc.Subscribe("%9")
	sub := cc.Subscribe("%1")

	if err := cc.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = cc.Close() }()
	conn := d.connAt(t, 0)
	conn.ackAttach(t)

	conn.feedLine(t, "%pause %1")
	conn.sync(t, sentinel)
	g := cc.gapFor("%1")
	if g == nil {
		t.Fatalf("no gap open after %%pause")
	}

	// On its own goroutine, and the channel is the barrier: expireGap parks in
	// RunCommand until the connection dies and re-arms only after it returns.
	// Asserting any earlier reads the window where the gap is legitimately
	// deleted, and passes with the re-arm still in place.
	returned := make(chan struct{})
	go func() { defer close(returned); cc.expireGap("%1", g) }()

	waitForWrite(t, conn, resume1) // on the wire, unanswered
	_ = conn.pw.Close()

	select {
	case <-returned:
	case <-time.After(2 * time.Second):
		t.Fatal("expireGap stayed parked after its connection died")
	}

	if n := cc.gapCount(); n != 0 {
		t.Fatalf("%d gaps armed after the resume's connection died, want 0", n)
	}
	if n := len(sub.C()); n != 0 {
		t.Fatalf("%d events queued off a failed resume, want 0", n)
	}
	assertNoWarn(t, logs, "gap deadline resume abandoned")
}

// TestDesynchronisedResumeDoesNotReArm covers the queue refusing the write.
// cmdDesync is set directly because every way of provoking it for real — a
// mismatched terminator, a failed stdin write — also tears the connection down,
// which lands expireGap on the connection-lost path the tests above already
// cover. The window being modelled is real: desync() sets the flag, then
// dropConnection starts a teardown supervise has not observed yet, so gone is
// still live and the write is refused by the queue rather than by the transport.
func TestDesynchronisedResumeDoesNotReArm(t *testing.T) {
	d := &recordingDialer{}
	cc := NewControlClient("test")
	cc.dial = pinnedDial(d)
	cc.backoff = time.Millisecond
	cc.gapDeadline = time.Hour

	sentinel := cc.Subscribe("%9")
	sub := cc.Subscribe("%1")

	if err := cc.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = cc.Close() }()
	conn := d.connAt(t, 0)
	conn.ackAttach(t)

	conn.feedLine(t, "%pause %1")
	conn.sync(t, sentinel)
	g := cc.gapFor("%1")
	if g == nil {
		t.Fatalf("no gap open after %%pause")
	}

	cc.cmdQMu.Lock()
	cc.cmdDesync = true
	cc.cmdQMu.Unlock()

	cc.expireGap("%1", g) // returns without parking: the queue refuses the write

	if n := cc.gapCount(); n != 0 {
		t.Fatalf("%d gaps armed after a resume the queue refused, want 0", n)
	}
	if strings.Contains(conn.written(), resume1) {
		t.Fatalf("a desynchronised queue still let a resume onto the wire: %q", conn.written())
	}
	if n := len(sub.C()); n != 0 {
		t.Fatalf("%d events queued off a failed resume, want 0", n)
	}
}

// TestStaleDeadlineDoesNotClaimANewerGap covers expireGap's pointer-identity
// check. A deadline whose Stop lost the race wakes up holding the gap it armed;
// without the check it resumes a pane a newer handshake legitimately paused,
// deletes the newer record so nothing bounds it any more, and marks every
// subscriber — a re-seed storm off a gap that was never its own.
//
// Losing a Stop race is not something a test can arrange on a real clock, so
// the two gaps are driven by %pause/%continue and the stale deadline is fired
// by hand.
func TestStaleDeadlineDoesNotClaimANewerGap(t *testing.T) {
	d := &recordingDialer{}
	cc := NewControlClient("test")
	cc.dial = pinnedDial(d)
	cc.backoff = time.Millisecond
	cc.gapDeadline = time.Hour

	sentinel := cc.Subscribe("%9")
	sub := cc.Subscribe("%1")

	if err := cc.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = cc.Close() }()
	conn := d.connAt(t, 0)
	conn.ackAttach(t)

	conn.feedLine(t, "%pause %1")
	conn.sync(t, sentinel)
	stale := cc.gapFor("%1")
	if stale == nil {
		t.Fatalf("no gap open after the first %%pause")
	}

	conn.feedLine(t, "%continue %1")
	conn.sync(t, sentinel)
	expectDirty(t, sub, "subscriber at the first %continue")
	cc.AckReseed(sub)

	conn.feedLine(t, "%pause %1")
	conn.sync(t, sentinel)
	newer := cc.gapFor("%1")
	if newer == nil || newer == stale {
		t.Fatalf("the second %%pause opened no fresh gap: %p", newer)
	}

	// With the identity check this returns at once. Without it the stale
	// deadline writes a resume nothing will ever answer and parks inside
	// RunCommand, which is the first thing the failure looks like.
	returned := make(chan struct{})
	go func() { defer close(returned); cc.expireGap("%1", stale) }()
	select {
	case <-returned:
	case <-time.After(2 * time.Second):
		t.Fatal("a stale deadline resumed a pane whose gap it never armed")
	}

	if g := cc.gapFor("%1"); g != newer {
		t.Fatalf("stale deadline dropped the newer gap: have %p, want %p", g, newer)
	}
	if strings.Contains(conn.written(), resume1) {
		t.Fatalf("stale deadline wrote a resume: %q", conn.written())
	}
	if n := len(sub.C()); n != 0 {
		t.Fatalf("%d events queued by a stale deadline, want 0", n)
	}
}

func TestContinueWithinDeadlineSendsNoResume(t *testing.T) {
	d := &recordingDialer{}
	cc := NewControlClient("test")
	cc.dial = d.dial
	cc.backoff = time.Millisecond
	// Default gapDeadline: the %continue below is what must close this gap.

	sentinel := cc.Subscribe("%9")
	sub := cc.Subscribe("%1")

	if err := cc.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = cc.Close() }()
	conn := d.connAt(t, 0)

	conn.feedLine(t, "%pause %1")
	conn.feedLine(t, "%continue %1")
	conn.sync(t, sentinel)

	expectDirty(t, sub, "subscriber present at %pause")
	if strings.Contains(conn.written(), resume1) {
		t.Fatalf("fought houston's own handshake with a resume: %q", conn.written())
	}
}

func TestReadLoopKeepsDispatchingWhileResumeOutstanding(t *testing.T) {
	d := &recordingDialer{}
	cc := NewControlClient("test")
	cc.dial = d.dial
	cc.backoff = time.Millisecond
	cc.gapDeadline = 75 * time.Millisecond

	other := cc.Subscribe("%2")

	if err := cc.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = cc.Close() }()
	conn := d.connAt(t, 0)
	conn.ackAttach(t)

	conn.feedLine(t, "%pause %1")
	waitForWrite(t, conn, resume1)

	conn.feedLine(t, `%output %2 live\015\012`)
	select {
	case ev := <-other.C():
		if string(ev.Data) != "live\r\n" {
			t.Fatalf("got %+v, want %q", ev, "live\r\n")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("readLoop stalled while a resume was in flight")
	}

	conn.answerCommand(t)
}

// TestReplyGoesToTheCommandThatAskedForIt pins which command a block answers.
// tmux answers every write with a block of its own, waited on or not, so a
// caller that takes the next block to arrive takes whatever the last unwaited
// writer provoked — and a resume that tmux refused reads back as a success.
func TestReplyGoesToTheCommandThatAskedForIt(t *testing.T) {
	d := &recordingDialer{}
	cc := NewControlClient("test")
	cc.dial = d.dial
	cc.backoff = time.Millisecond

	sentinel := cc.Subscribe("%9")

	if err := cc.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = cc.Close() }()
	conn := d.connAt(t, 0)
	conn.ackAttach(t)

	if err := cc.SendSpecialKey("%1", "C-c"); err != nil {
		t.Fatalf("SendSpecialKey: %v", err)
	}
	waitForWrite(t, conn, "send-keys -t %1 C-c")

	const command = "display-message -p mine"
	type result struct {
		out string
		err error
	}
	got := make(chan result, 1)
	go func() {
		out, err := cc.RunCommand(command)
		got <- result{out: out, err: err}
	}()
	waitForWrite(t, conn, command)

	// The send-keys above is on nobody's hook, but tmux still answers it.
	conn.answerCommand(t, "theirs")
	conn.sync(t, sentinel)

	select {
	case r := <-got:
		t.Fatalf("RunCommand returned (%q, %v) off another writer's block", r.out, r.err)
	default:
	}

	conn.answerCommand(t, "mine")

	select {
	case r := <-got:
		if r.err != nil {
			t.Fatalf("RunCommand: %v", r.err)
		}
		if r.out != "mine" {
			t.Fatalf("RunCommand = %q, want %q", r.out, "mine")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RunCommand never got the block its own write provoked")
	}
}

// TestOrphanBaselineTerminatorKeepsTheQueueAligned covers a stream introduced
// by something ParseControlLine does not strip, so the baseline's %begin parses
// as data and only its terminator arrives. Nothing was bound, so the right
// answer is to drop the orphan; treating it as a desync instead failed every
// command and re-dialled forever.
func TestOrphanBaselineTerminatorKeepsTheQueueAligned(t *testing.T) {
	d := &recordingDialer{baseline: "\x1bP1001p%begin 1700000000 1 0\r\n%end 1700000000 1 0\r\n"}
	cc := NewControlClient("test")
	cc.dial = d.dial
	cc.backoff = time.Millisecond

	sentinel := cc.Subscribe("%9")

	if err := cc.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = cc.Close() }()
	conn := d.connAt(t, 0)
	conn.ackAttach(t)

	const command = "display-message -p mine"
	got := make(chan commandResponse, 1)
	go func() {
		out, err := cc.RunCommand(command)
		got <- commandResponse{output: out, err: err}
	}()
	waitForWrite(t, conn, command)
	conn.answerCommand(t, "mine")

	select {
	case r := <-got:
		if r.err != nil {
			t.Fatalf("RunCommand: %v", r.err)
		}
		if r.output != "mine" {
			t.Fatalf("RunCommand = %q, want %q", r.output, "mine")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RunCommand never got the block its own write provoked")
	}

	conn.sync(t, sentinel)
	d.mu.Lock()
	dials := len(d.conns)
	d.mu.Unlock()
	if dials != 1 {
		t.Fatalf("dialled %d times; the orphan terminator dropped the connection", dials)
	}
}

// TestFirstBlockOfAConnectionBindsToNoCommand proves a fresh connection owes
// three blocks and that the first of them answers nobody: tmux replies to the
// dialer's own attach-session before houston has written a byte. Every block
// carries a body naming the command it belongs to, because bodiless %ends are
// interchangeable — with empty bodies an off-by-one delivers the wrong block and
// still reads as a pass.
func TestFirstBlockOfAConnectionBindsToNoCommand(t *testing.T) {
	d := &recordingDialer{baseline: "\x1bP1000p%begin 1700000000 1 0\r\nbaseline\r\n%end 1700000000 1 0\r\n"}
	cc := NewControlClient("test")
	cc.dial = d.dial
	cc.backoff = time.Millisecond

	if err := cc.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = cc.Close() }()

	conn := d.connAt(t, 0)
	waitForWrite(t, conn, ignoreSizeCmd)

	// Enrolled while ignore-size is still outstanding, so all three blocks are
	// owed at once. Answering ignore-size first would empty the queue ahead of
	// this write, and then a baseline that wrongly claimed an entry would
	// still leave every later block on the right one.
	const command = "display-message -p mine"
	got := make(chan commandResponse, 1)
	go func() {
		out, err := cc.RunCommand(command)
		got <- commandResponse{output: out, err: err}
	}()
	waitForWrite(t, conn, command)

	conn.answerCommand(t, "ignore-size")
	conn.answerCommand(t, "mine")

	select {
	case r := <-got:
		if r.err != nil {
			t.Fatalf("RunCommand: %v", r.err)
		}
		if r.output != "mine" {
			t.Fatalf("RunCommand = %q, want %q — the connection's unowed first block was bound to a command", r.output, "mine")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RunCommand never got the block its own write provoked")
	}
}

// TestSecondConnectionAlsoDropsItsBaselineBlock runs the same three-block drive
// against connection 1. It is the only test that fails when the baseline marker
// is a ControlClient field rather than a readLoop local — a per-client marker is
// already set by connection 0, so every connection after the first stays one
// block out of step while every other test in this file still passes. Connection
// 0 is dropped holding two unanswered enrollments, so this also proves the new
// connection's queue starts empty instead of inheriting them.
func TestSecondConnectionAlsoDropsItsBaselineBlock(t *testing.T) {
	d := &recordingDialer{baseline: "\x1bP1000p%begin 1700000000 1 0\r\nbaseline\r\n%end 1700000000 1 0\r\n"}
	cc := NewControlClient("test")
	cc.dial = d.dial
	cc.backoff = time.Millisecond

	if err := cc.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = cc.Close() }()

	first := d.connAt(t, 0)
	waitForWrite(t, first, ignoreSizeCmd)
	if err := cc.SendSpecialKey("%1", "C-c"); err != nil {
		t.Fatalf("SendSpecialKey: %v", err)
	}
	waitForWrite(t, first, "send-keys -t %1 C-c")
	_ = first.pw.Close() // neither block answered; supervise re-dials

	conn := d.connAt(t, 1)
	waitForWrite(t, conn, ignoreSizeCmd)

	// Enrolled while ignore-size is still outstanding, so the queue holds two
	// entries when the blocks arrive. Answering ignore-size first would empty
	// the queue ahead of this write and let an off-by-one pass.
	const command = "display-message -p mine"
	got := make(chan commandResponse, 1)
	go func() {
		out, err := cc.RunCommand(command)
		got <- commandResponse{output: out, err: err}
	}()
	waitForWrite(t, conn, command)

	conn.answerCommand(t, "ignore-size")
	conn.answerCommand(t, "mine")

	select {
	case r := <-got:
		if r.err != nil {
			t.Fatalf("RunCommand: %v", r.err)
		}
		if r.output != "mine" {
			t.Fatalf("RunCommand = %q, want %q — connection 1 bound its baseline block to a command", r.output, "mine")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RunCommand never got its own block — connection 1 carried connection 0's queue over")
	}
}

// TestMismatchedTerminatorReleasesTheWaiter feeds a block whose %end disagrees
// with its %begin, which makes the queue head's owner unknowable. The waiter
// must come back with an error rather than stay parked: parked here is
// expireGap's timer goroutine never returning, so the gap is never re-armed and
// never marked, and the pane stays dark — the failure this whole change exists
// to prevent. The connection has to go too, since a desynchronised stream is
// terminal until a re-attach resets the queue.
func TestMismatchedTerminatorReleasesTheWaiter(t *testing.T) {
	d := &recordingDialer{}
	cc := NewControlClient("test")
	cc.dial = d.dial
	cc.backoff = time.Millisecond

	if err := cc.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = cc.Close() }()

	conn := d.connAt(t, 0)
	conn.ackAttach(t)

	const command = "display-message -p mine"
	got := make(chan commandResponse, 1)
	go func() {
		out, err := cc.RunCommand(command)
		got <- commandResponse{output: out, err: err}
	}()
	waitForWrite(t, conn, command)

	num := conn.nextBlock()
	conn.feedLine(t, fmt.Sprintf("%%begin 1700000000 %d 0", num))
	conn.feedLine(t, "mine")
	conn.feedLine(t, fmt.Sprintf("%%end 1700000000 %d 0", num+1))

	select {
	case r := <-got:
		if r.err == nil {
			t.Fatalf("RunCommand returned (%q, nil) off a block whose terminator did not match its %%begin", r.output)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RunCommand stayed parked on a mismatched block")
	}

	d.connAt(t, 1) // the desynchronised connection must be torn down and re-dialled
}

// TestDesyncRecoversOnTheNextConnection follows that teardown through to the
// end: a desynchronised stream is terminal until a re-attach resets the queue,
// so the subscribers must re-seed and the fresh connection must accept commands
// again.
//
// The desync is driven through a plain command rather than a paused pane on
// purpose. A paused pane would put expireGap's re-arm in a race with
// markAllDirty's sweep and whichever won would decide the assertion, so do not
// "restore" that variant.
func TestDesyncRecoversOnTheNextConnection(t *testing.T) {
	d := &recordingDialer{}
	cc := NewControlClient("test")
	cc.dial = d.dial
	cc.backoff = time.Millisecond

	sub := cc.Subscribe("%1")

	if err := cc.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = cc.Close() }()

	first := d.connAt(t, 0)
	first.ackAttach(t)

	const command = "display-message -p mine"
	got := make(chan commandResponse, 1)
	go func() {
		out, err := cc.RunCommand(command)
		got <- commandResponse{output: out, err: err}
	}()
	waitForWrite(t, first, command)

	num := first.nextBlock()
	first.feedLine(t, fmt.Sprintf("%%begin 1700000000 %d 0", num))
	first.feedLine(t, fmt.Sprintf("%%end 1700000000 %d 0", num+1))

	// Only that it failed: desync() answers the waiter and then drops the
	// connection, so whether this reads errCmdDesync or errConnLost is a real
	// race between the reply and the close of gone.
	select {
	case r := <-got:
		if r.err == nil {
			t.Fatalf("RunCommand returned (%q, nil) off a desynchronised connection", r.output)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RunCommand stayed parked on a desynchronised connection")
	}

	expectDirty(t, sub, "subscriber after the desynchronised connection was replaced")

	second := d.connAt(t, 1)
	second.ackAttach(t)

	go func() {
		out, err := cc.RunCommand(command)
		got <- commandResponse{output: out, err: err}
	}()
	waitForWrite(t, second, command)
	second.answerCommand(t, "mine")

	select {
	case r := <-got:
		if r.err != nil || r.output != "mine" {
			t.Fatalf("RunCommand on the fresh connection = (%q, %v), want (%q, nil)", r.output, r.err, "mine")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the fresh connection never answered a command")
	}
}

// TestFailedWriteLeavesNoPhantomEnrollment covers the entry writeCommandLocked
// deliberately leaves in the queue when the write fails: a short write may have
// delivered half a command line, so the queue can no longer be reasoned about
// and every later command must fail instead of being answered from a block it
// did not provoke.
//
// The re-dial is suppressed on purpose. Left alone, the teardown installs a
// fresh connection with an empty queue, the second command writes to it and
// parks in RunCommand — which has no timeout — so the test would hang the whole
// run instead of failing. TestMismatchedTerminatorReleasesTheWaiter wants that
// re-dial; this one must not have it.
func TestFailedWriteLeavesNoPhantomEnrollment(t *testing.T) {
	d := &recordingDialer{}
	cc := NewControlClient("test")
	cc.backoff = time.Millisecond
	first := true
	cc.dial = func() (io.ReadCloser, io.Writer, func() error, error) {
		if !first {
			return nil, nil, nil, errors.New("no reconnect in this test")
		}
		first = false // Start()'s own call; every later one runs in supervise's goroutine, so the two never race
		return d.dial()
	}

	if err := cc.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = cc.Close() }()

	conn := d.connAt(t, 0)
	conn.ackAttach(t)

	conn.mu.Lock()
	conn.writeErr = errors.New("stdin is gone")
	conn.mu.Unlock()

	if err := cc.SendSpecialKey("%1", "C-c"); err == nil {
		t.Fatal("SendSpecialKey reported success against a failing stdin")
	}

	// The entry is only harmless because the queue now reports itself
	// unreasonable: the next writer is turned away before it can append an entry
	// the phantom would answer ahead of. A raw stdin error here instead of
	// errCmdDesync means nothing recorded the failure and the queue is still
	// being treated as aligned.
	if err := cc.SendSpecialKey("%1", "C-c"); !errors.Is(err, errCmdDesync) {
		t.Fatalf("second send after a failed write = %v, want %v", err, errCmdDesync)
	}

	if out, err := cc.RunCommand("display-message -p mine"); err == nil {
		t.Fatalf("RunCommand returned (%q, nil) after a failed write left the queue unreasonable", out)
	}
}

// TestConcurrentWriterNeverStealsAReply is the reviewer's own reproduction,
// scaled down: an unwaited writer racing a stream of RunCommand calls, each
// asserting it got its own body back. cmdMu does not serialize the unwaited
// writer, so before the queue existed a RunCommand holding cmdMu could be
// handed a send-keys' block.
//
// The answerer owes a block to every line the client wrote, ignore-size
// included, because replies are bound by order and a skipped line stalls every
// reply behind it. It tracks a cursor into the buffer rather than diffing
// written(), and it reports failures over a channel: t.Fatalf from a non-test
// goroutine does not stop the test.
func TestConcurrentWriterNeverStealsAReply(t *testing.T) {
	d := &recordingDialer{}
	cc := NewControlClient("test")
	cc.dial = d.dial
	cc.backoff = time.Millisecond

	if err := cc.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = cc.Close() }()
	conn := d.connAt(t, 0)

	stop := make(chan struct{})
	fail := make(chan string, 1)
	answererDone := make(chan struct{})
	writerDone := make(chan struct{})

	go func() {
		defer close(answererDone)
		cursor := 0
		for {
			select {
			case <-stop:
				return
			default:
			}
			buf := conn.written()
			nl := strings.IndexByte(buf[cursor:], '\n')
			if nl < 0 {
				time.Sleep(100 * time.Microsecond)
				continue
			}
			line := buf[cursor : cursor+nl]
			cursor += nl + 1

			// Only a display-message is waited on, so only it needs a body the
			// caller can recognise; the rest just need their block.
			body, _ := strings.CutPrefix(line, "display-message -p ")
			num := conn.nextBlock()
			block := fmt.Sprintf("%%begin 1700000000 %d 0\n", num)
			if body != line {
				block += body + "\n"
			}
			block += fmt.Sprintf("%%end 1700000000 %d 0\n", num)
			if _, err := io.WriteString(conn.pw, block); err != nil {
				select {
				case fail <- fmt.Sprintf("answering %q: %v", line, err):
				default:
				}
				return
			}
		}
	}()

	go func() {
		defer close(writerDone)
		for {
			select {
			case <-stop:
				return
			default:
			}
			if err := cc.SendSpecialKey("%1", "C-c"); err != nil {
				select {
				case fail <- fmt.Sprintf("SendSpecialKey: %v", err):
				default:
				}
				return
			}
			time.Sleep(100 * time.Microsecond)
		}
	}()

	for i := range 200 {
		want := fmt.Sprintf("r%d", i)
		out, err := cc.RunCommand("display-message -p " + want)
		if err != nil {
			t.Fatalf("RunCommand %d: %v", i, err)
		}
		if out != want {
			t.Fatalf("RunCommand %d = %q, want %q — a concurrent writer's block was delivered to it", i, out, want)
		}
	}

	close(stop)
	<-writerDone
	<-answererDone
	select {
	case msg := <-fail:
		t.Fatal(msg)
	default:
	}
}

// TestIgnoreSizeIsFirstWriteOnEveryAttach characterizes a rule the CC client
// already keeps: it must never influence the session's window size. The
// assertion that server/pane_ws_protocol_test.go says lives in tmux/.
func TestIgnoreSizeIsFirstWriteOnEveryAttach(t *testing.T) {
	d := &recordingDialer{}
	cc := NewControlClient("test")
	cc.dial = d.dial
	cc.backoff = time.Millisecond

	if err := cc.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = cc.Close() }()

	first := d.connAt(t, 0)
	assertIgnoreSizeFirst(t, first)

	_ = first.pw.Close() // drop the connection; supervise re-dials
	assertIgnoreSizeFirst(t, d.connAt(t, 1))
}

func assertIgnoreSizeFirst(t *testing.T, c *recordedConn) {
	t.Helper()
	const want = "refresh-client -f ignore-size\n"
	waitForWrite(t, c, want)
	if got := c.written(); !strings.HasPrefix(got, want) {
		t.Fatalf("first write on the connection = %q, want %q first", got, want)
	}
}

// TestAttachWriteFailureTearsDownItsOwnConnection pins why attach installs
// closeCur, connOK and gone before its own ignore-size write. That write can
// fail, and the teardown behind it must close the connection being installed;
// reached through the previous connection's closeCur it would close a spent
// sync.Once — nil on the very first attach — and leave this connection
// installed, connOK true, and nothing ever re-dialling.
//
// What tells the two apart is a second connection appearing at all, with
// ignore-size written on it — though on the very first attach the wrong order
// does not get that far: closeCur is still nil and the teardown panics on it.
func TestAttachWriteFailureTearsDownItsOwnConnection(t *testing.T) {
	d := &recordingDialer{
		onDial: func(i int, c *recordedConn) {
			if i == 0 { // connection 0 only: arming every connection hot-loops the dialer
				c.writeErr = errors.New("stdin is gone")
			}
		},
	}
	cc := NewControlClient("test")
	cc.dial = d.dial
	cc.backoff = time.Millisecond

	if err := cc.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = cc.Close() }()

	if got := d.connAt(t, 0).written(); got != "" {
		t.Fatalf("the failing connection recorded %q, want nothing", got)
	}
	assertIgnoreSizeFirst(t, d.connAt(t, 1))
}

func TestReconnectMarksSubscribersDirty(t *testing.T) {
	d := &scriptedDialer{transcripts: []string{
		"%output %1 first\\015\\012\n", // EOF here == connection dropped
	}}
	cc := NewControlClient("test")
	cc.dial = d.dial
	cc.backoff = time.Millisecond

	// Subscribe before Start so the reconnect cannot fire with no
	// subscriber present.
	sub := cc.Subscribe("%1")

	if err := cc.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = cc.Close() }()

	// The pre-drop Data event may or may not be observed: markDirtyLocked
	// drains the backlog on re-attach, so whether "first" survives depends
	// on which side wins the race. Both interleavings are correct. What must
	// happen is that Dirty arrives.
	deadline := time.After(2 * time.Second)
	for {
		select {
		case ev := <-sub.C():
			if ev.Dirty {
				return
			}
		case <-deadline:
			t.Fatalf("no Dirty event after reconnect; dials=%d", d.count())
		}
	}
}

// TestReattachClearsGapState covers R12: a gap outstanding on a connection
// that has since died must not let an unrelated %continue on the new
// connection re-seed anybody.
func TestReattachClearsGapState(t *testing.T) {
	d := &recordingDialer{}
	cc := NewControlClient("test")
	cc.dial = d.dial
	cc.backoff = time.Millisecond

	sub := cc.Subscribe("%1")

	if err := cc.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = cc.Close() }()

	first := d.connAt(t, 0)
	first.feedLine(t, "%pause %1")
	_ = first.pw.Close() // drop the connection; supervise re-dials

	// markAllDirty fires on every re-attach, so sub is already dirty and a
	// second Dirty from the stale gap would be invisible behind it. Drain and
	// ack before looking for one.
	expectDirty(t, sub, "subscriber after re-attach")
	cc.AckReseed(sub)

	second := d.connAt(t, 1)
	// Subscribed only now: one subscribed before the re-attach would itself
	// be marked dirty by markAllDirty, and dispatch skips dirty subscribers —
	// the barrier could never clear.
	sentinel := cc.Subscribe("%9")

	second.feedLine(t, "%continue %1")
	second.sync(t, sentinel)

	if len(sub.C()) != 0 {
		t.Fatal("continue on the new connection re-seeded a subscriber left over from the dropped connection's gap")
	}
}

// TestClearGapsDisarmsEveryDeadline is necessarily white-box, because
// clearGapsLocked's timer.Stop() loop has no behavioural witness and
// structurally cannot have one: once the map entry is deleted, a surviving
// timer's *gap can never again equal cc.gaps[paneID] — a later gap on the pane
// is a fresh allocation — so it loses expireGap's identity check and does
// nothing observable. Removing the loop costs one leaked timer per swept pane
// for up to gapDeadline, and that is all this test can speak to.
//
// The hour deadline is a precondition, not a style choice: Stop() also reports
// false for a timer that has already fired, so a short deadline would let a
// real firing satisfy the assertion.
func TestClearGapsDisarmsEveryDeadline(t *testing.T) {
	cc := NewControlClient("test")
	cc.gapDeadline = time.Hour

	cc.openGap("%1")
	cc.openGap("%2")

	gaps := []*gap{cc.gapFor("%1"), cc.gapFor("%2")}
	for i, g := range gaps {
		if g == nil {
			t.Fatalf("gap %d never opened", i)
		}
	}

	cc.clearGaps()

	if n := cc.gapCount(); n != 0 {
		t.Fatalf("%d gaps survived clearGaps, want 0", n)
	}
	for i, g := range gaps {
		if g.timer.Stop() {
			t.Fatalf("gap %d's deadline was still armed after clearGaps", i)
		}
	}
}

// TestReconnectDisarmsAnOpenGapsDeadline is the behavioural tripwire for the
// two guards in the gap lifecycle, and measurement says it witnesses only their
// conjunction: removing either one alone leaves it green. Without the Stop()
// loop the timer does fire, but it loses expireGap's identity check and writes
// nothing; without the identity check the timer was stopped and never fires at
// all. Remove both and a deadline the re-attach dropped resumes the pane on the
// fresh connection, which is what this catches. Neither guard is covered
// individually here — TestClearGapsDisarmsEveryDeadline and
// TestStaleDeadlineDoesNotClaimANewerGap are. What it adds over
// TestReattachClearsGapState is the timer path: that one proves only that a
// %continue on the new connection re-seeds nobody.
//
// The deadline has to be short enough to fire inside the test, or the assertion
// is about a timer that was never going to do anything.
func TestReconnectDisarmsAnOpenGapsDeadline(t *testing.T) {
	d := &recordingDialer{}
	cc := NewControlClient("test")
	cc.dial = d.dial // the re-dial is the point here; no pin
	cc.backoff = time.Millisecond
	cc.gapDeadline = 150 * time.Millisecond

	sentinel := cc.Subscribe("%9")
	sub := cc.Subscribe("%1")

	if err := cc.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = cc.Close() }()

	first := d.connAt(t, 0)
	first.ackAttach(t)

	first.feedLine(t, "%pause %1")
	first.sync(t, sentinel)
	if cc.gapFor("%1") == nil {
		t.Fatalf("no gap open after %%pause")
	}

	_ = first.pw.Close() // dropped well inside the deadline
	expectDirty(t, sub, "subscriber after re-attach")

	second := d.connAt(t, 1)
	second.ackAttach(t)

	time.Sleep(3 * cc.gapDeadline)
	if n := cc.gapCount(); n != 0 {
		t.Fatalf("%d gaps survived the re-attach, want 0", n)
	}
	if strings.Contains(second.written(), resume1) {
		t.Fatalf("a gap the re-attach dropped still resumed its pane: %q", second.written())
	}
	if strings.Contains(first.written(), resume1) {
		t.Fatalf("resume written onto the dead connection: %q", first.written())
	}
}

// TestCloseRefusesALatePause covers R13: readLoop keeps parsing buffered
// lines after Close() returns, so a %pause handled after close must not arm
// a timer that outlives the client.
func TestCloseRefusesALatePause(t *testing.T) {
	cc := NewControlClient("test")
	cc.gapDeadline = 10 * time.Millisecond

	sub := cc.Subscribe("%1")

	if err := cc.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	feed(cc, "%pause %1\n")

	if n := cc.gapCount(); n != 0 {
		t.Fatalf("%d gaps open after a %%pause following Close(), want 0", n)
	}

	time.Sleep(3 * cc.gapDeadline)
	if len(sub.C()) != 0 {
		t.Fatal("Dirty queued after Close() from a pause that should never have armed a timer")
	}
}

// TestCloseSweepsAnOpenGap is the other half of R13: TestCloseRefusesALatePause
// covers the guard against a pause arriving after Close, this covers the sweep
// of one already open when Close runs.
func TestCloseSweepsAnOpenGap(t *testing.T) {
	d := &recordingDialer{}
	cc := NewControlClient("test")
	cc.dial = d.dial
	cc.backoff = time.Millisecond
	cc.gapDeadline = 20 * time.Millisecond

	sentinel := cc.Subscribe("%9")
	sub := cc.Subscribe("%1")

	if err := cc.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	conn := d.connAt(t, 0)

	conn.feedLine(t, "%pause %1")
	conn.sync(t, sentinel)

	if err := cc.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if n := cc.gapCount(); n != 0 {
		t.Fatalf("%d gaps survived Close(), want 0", n)
	}

	// The gap count above is what bites; this is a tripwire, since after Close
	// a surviving timer's RunCommand returns before it can write or mark.
	time.Sleep(3 * cc.gapDeadline)
	if len(sub.C()) != 0 {
		t.Fatal("a swept gap still marked its subscriber dirty")
	}
}

func TestDoneDoesNotFireOnDisconnect(t *testing.T) {
	d := &scriptedDialer{transcripts: []string{""}}
	cc := NewControlClient("test")
	cc.dial = d.dial
	cc.backoff = time.Millisecond
	if err := cc.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = cc.Close() }()

	select {
	case <-cc.Done():
		t.Fatal("Done() fired on a transport drop; it must mean closed")
	case <-time.After(200 * time.Millisecond):
	}

	if d.count() < 2 {
		t.Fatalf("dials=%d, want at least 2 (the client never re-dialled)", d.count())
	}
}

func TestCloseFiresDone(t *testing.T) {
	d := &scriptedDialer{}
	cc := NewControlClient("test")
	cc.dial = d.dial
	cc.backoff = time.Millisecond
	if err := cc.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	_ = cc.Close()

	select {
	case <-cc.Done():
	case <-time.After(time.Second):
		t.Fatal("Done() did not fire after Close()")
	}
}

func TestCloseDuringDialDoesNotLeak(t *testing.T) {
	dialStarted := make(chan struct{})
	release := make(chan struct{})
	closedConn := make(chan struct{}, 1)
	var once sync.Once
	first := true

	cc := NewControlClient("test")
	cc.backoff = time.Millisecond
	cc.dial = func() (io.ReadCloser, io.Writer, func() error, error) {
		if first {
			first = false // this first dial is Start()'s own call; every later one runs in supervise's goroutine, so the two never race
			return io.NopCloser(strings.NewReader("")), io.Discard,
				func() error { return nil }, nil
		}
		once.Do(func() { close(dialStarted) })
		<-release // hold this dial in flight until the test has called Close()
		return io.NopCloser(strings.NewReader("")), io.Discard,
			func() error { closedConn <- struct{}{}; return nil }, nil
	}

	if err := cc.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	<-dialStarted
	_ = cc.Close()
	close(release)

	select {
	case <-closedConn:
	case <-time.After(2 * time.Second):
		t.Fatal("connection dialled during Close() was never closed — leaked")
	}
	select {
	case <-cc.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("Done() never fired after Close()")
	}
}

func TestReconnectBacksOffWhenConnectionsDieImmediately(t *testing.T) {
	var dials int32

	cc := NewControlClient("test")
	cc.backoff = 20 * time.Millisecond
	cc.dial = func() (io.ReadCloser, io.Writer, func() error, error) {
		atomic.AddInt32(&dials, 1)
		// dial succeeds, but the connection is dead on arrival
		return io.NopCloser(strings.NewReader("")), io.Discard,
			func() error { return nil }, nil
	}

	if err := cc.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = cc.Close() }()

	time.Sleep(300 * time.Millisecond)

	n := atomic.LoadInt32(&dials)
	// 20ms doubling gives roughly 20+40+80+160 -> about 5 dials in 300ms.
	// With no backoff on this path it would be thousands.
	if n > 12 {
		t.Fatalf("dialled %d times in 300ms — reconnect is hot-spinning", n)
	}
	if n < 2 {
		t.Fatalf("dialled %d times — never retried at all", n)
	}
}
