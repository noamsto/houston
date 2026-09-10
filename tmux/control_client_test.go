package tmux

import (
	"bufio"
	"io"
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
}

type recordedConn struct {
	pw  *io.PipeWriter
	mu  sync.Mutex
	buf strings.Builder
}

func (d *recordingDialer) dial() (io.ReadCloser, io.Writer, func() error, error) {
	pr, pw := io.Pipe()
	c := &recordedConn{pw: pw}
	d.mu.Lock()
	d.conns = append(d.conns, c)
	d.mu.Unlock()
	return pr, c, func() error { return pw.Close() }, nil
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
	return c.buf.Write(p)
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
func (c *recordedConn) answerCommand(t *testing.T) {
	t.Helper()
	c.feedLine(t, "%begin 1700000000 1 0")
	c.feedLine(t, "%end 1700000000 1 0")
}

// answerCommandError releases a RunCommand caller with tmux's refusal.
func (c *recordedConn) answerCommandError(t *testing.T) {
	t.Helper()
	c.feedLine(t, "%begin 1700000000 1 0")
	c.feedLine(t, "can't find pane")
	c.feedLine(t, "%error 1700000000 1 0")
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

const resume1 = "refresh-client -A %1:continue"

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

	conn.feedLine(t, "%pause %1")
	waitForWrite(t, conn, resume1)
	conn.answerCommandError(t)

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

	conn.feedLine(t, "%pause %1")
	waitForWrite(t, conn, resume1)
	conn.answerCommandError(t)

	time.Sleep(5 * cc.gapDeadline)
	if n := cc.gapCount(); n != 0 {
		t.Fatalf("%d gaps armed for a pane nobody subscribes to, want 0", n)
	}
	if n := strings.Count(conn.written(), resume1); n != 1 {
		t.Fatalf("wrote %d resumes with no subscribers, want exactly 1", n)
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
