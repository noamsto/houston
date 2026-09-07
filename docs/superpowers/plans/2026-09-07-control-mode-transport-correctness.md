# Control-mode transport correctness — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make houston's tmux control-mode feed survive dropped chunks, tmux-initiated pauses, and connection loss without silently rendering a corrupted terminal.

**Architecture:** A pane subscription becomes a typed stream of `PaneEvent`s rather than raw `[]byte`. Any gap in that stream — a drop caused by a slow consumer, a `%pause` from tmux, or a reconnect — is signalled in-band as a `Dirty` event, so a consumer re-seeds from `capture-pane` at exactly the right point in the sequence rather than applying bytes on top of a corrupted screen. Reconnection moves inside `ControlClient`, so existing subscriptions survive a re-dial instead of every socket dying with the transport.

**Tech Stack:** Go 1.x stdlib, `tmux -CC` control mode, `github.com/gorilla/websocket`. No new dependencies.

**Spec:** `docs/superpowers/specs/2026-09-07-houston-overhaul-design.md` — section "Control-mode transport".

## Global Constraints

- **Houston never resizes a pane.** `refresh-client -f ignore-size` at
  `tmux/control_client.go:81` stays. A human is attached to that pane in kitty.
- **`Done()` means closed, not disconnected.** After Task 4, `Done()` fires only
  on explicit `Close()`. A transport drop must leave consumers attached.
- **Single producer.** `dispatch` and all dirty-marking run on the `readLoop`
  goroutine while holding `cc.mu`. Do not introduce a second producer.
- **Go tests run with:** `go test ./tmux/... -v` from the repo root.
- **No new dependencies.** The repo has exactly one non-stdlib dep
  (`gorilla/websocket`); keep it that way.

---

## File Structure

| File | Responsibility | Change |
|---|---|---|
| `tmux/control_client.go` | The `-CC` connection: dial, read, demux, send, reconnect | Modify |
| `tmux/control_client_test.go` | Unit tests for demux, dirty-marking, reconnect | **Create** |
| `tmux/control_manager.go` | Ref-counted clients per session | Modify (small) |
| `server/pane_ws.go` | WebSocket bridge; owns the seed handshake | Modify |
| `tmux/control_test.go` | Existing `ParseControlLine` table test | Untouched |

`control_client_test.go` is new because `control_test.go` covers the pure parser and should stay that way — parser tests need no fixtures or goroutines, and these do.

---

### Task 1: Typed pane subscriptions

Replaces `chan []byte` with a handle carrying a typed channel. No behaviour
change yet — this is the shape the next three tasks need. It also removes the
pointer-comparison-by-`fmt.Sprintf("%p")` hack in `Unsubscribe`.

**Files:**
- Modify: `tmux/control_client.go:25-26` (struct field), `:145-180` (dispatch/Subscribe/Unsubscribe)
- Modify: `server/pane_ws.go:105` (subscribe), `:118` (unsubscribe), `:169-196` (write loop)
- Test: `tmux/control_client_test.go` (create)

**Interfaces:**
- Consumes: nothing from earlier tasks.
- Produces:
  - `type PaneEvent struct { Data []byte; Dirty bool }`
  - `type PaneSub struct { ... }` with `func (s *PaneSub) C() <-chan PaneEvent`
  - `func (cc *ControlClient) Subscribe(paneID string) *PaneSub`
  - `func (cc *ControlClient) Unsubscribe(paneID string, s *PaneSub)`

- [ ] **Step 1: Write the failing test**

Create `tmux/control_client_test.go`:

```go
package tmux

import "testing"

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
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./tmux/ -run TestSubscribe -v`
Expected: FAIL to compile — `sub.C undefined (type <-chan []byte has no field or method C)`.

- [ ] **Step 3: Write the implementation**

In `tmux/control_client.go`, replace the `subs` field declaration (currently
line 26) and the `dispatch`/`Subscribe`/`Unsubscribe` block (currently lines
145-180):

```go
// PaneEvent is one item in a pane subscription. A Dirty event marks a gap in
// the stream: everything buffered before it was discarded, and the subscriber
// must re-seed from capture-pane before applying any later Data. Data and
// Dirty are never both set.
type PaneEvent struct {
	Data  []byte
	Dirty bool
}

// PaneSub is one subscriber's handle on a pane's output.
type PaneSub struct {
	ch    chan PaneEvent
	dirty bool // guarded by ControlClient.mu
}

// C returns the event stream. Read it until the subscription is released.
func (s *PaneSub) C() <-chan PaneEvent { return s.ch }
```

Change the struct field:

```go
	mu   sync.RWMutex
	subs map[string][]*PaneSub // paneID → subscribers
```

Replace `dispatch`, `Subscribe` and `Unsubscribe`:

```go
func (cc *ControlClient) dispatch(paneID string, data []byte) {
	cc.mu.Lock()
	defer cc.mu.Unlock()
	for _, s := range cc.subs[paneID] {
		select {
		case s.ch <- PaneEvent{Data: data}:
		default:
		}
	}
}

// Subscribe returns a handle receiving output for the given pane.
func (cc *ControlClient) Subscribe(paneID string) *PaneSub {
	s := &PaneSub{ch: make(chan PaneEvent, 4096)}
	cc.mu.Lock()
	cc.subs[paneID] = append(cc.subs[paneID], s)
	cc.mu.Unlock()
	return s
}

// Unsubscribe releases a subscription handle.
func (cc *ControlClient) Unsubscribe(paneID string, s *PaneSub) {
	cc.mu.Lock()
	defer cc.mu.Unlock()
	subs := cc.subs[paneID]
	for i, existing := range subs {
		if existing == s {
			cc.subs[paneID] = append(subs[:i], subs[i+1:]...)
			break
		}
	}
	if len(cc.subs[paneID]) == 0 {
		delete(cc.subs, paneID)
	}
}
```

Note `dispatch` now takes the write lock rather than `RLock`, because Task 2
mutates `s.dirty` inside it. Update `NewControlClient` (line 45):

```go
		subs:    make(map[string][]*PaneSub),
```

- [ ] **Step 4: Update the caller in `server/pane_ws.go`**

At line 105, change:

```go
	sub := cc.Subscribe(paneID)
```

In the `defer` block at line 118, change `cc.Unsubscribe(paneID, outputCh)` to:

```go
		cc.Unsubscribe(paneID, sub)
```

At line 166, change the call to pass the handle:

```go
	s.paneWSWriteLoop(conn, cc, pane, sub)
```

Change the write loop signature (line 169) and its output case (lines 182-196):

```go
func (s *Server) paneWSWriteLoop(conn *websocket.Conn, cc *tmux.ControlClient, pane tmux.Pane, sub *tmux.PaneSub) {
```

```go
		case ev := <-sub.C():
			// Coalesce: drain all buffered chunks into one write
			// to keep the channel drained and reduce WS round-trips.
			buf := append([]byte(nil), ev.Data...)
			for {
				select {
				case more := <-sub.C():
					buf = append(buf, more.Data...)
				default:
					goto send
				}
			}
		send:
			outputJSON, _ := json.Marshal(WSOutput{Data: string(buf)})
			msg, _ := json.Marshal(WSMessage{Type: "output", Data: outputJSON})
			if err := conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				return
			}
```

Dirty events are ignored for now — Task 5 handles them. Coalescing a `Dirty`
event's empty `Data` is harmless.

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./tmux/... ./server/... -v`
Expected: PASS, including the pre-existing `TestParseControlLine`.

- [ ] **Step 6: Verify it builds end to end**

Run: `go build ./...`
Expected: no output.

- [ ] **Step 7: Commit**

```bash
git add tmux/control_client.go tmux/control_client_test.go server/pane_ws.go
git commit -m "refactor(tmux): typed pane subscriptions via PaneSub handle"
```

---

### Task 2: A dropped chunk marks the stream dirty

A full subscriber channel currently drops silently. A drop can cut an escape
sequence in half, which corrupts every glyph rendered afterwards, and the pane
stays wrong until the socket is reopened. The subscriber most likely to be slow
is a phone on bad LTE — houston's whole premise.

**Files:**
- Modify: `tmux/control_client.go` (`dispatch`, plus new `markDirtyLocked`, `AckReseed`)
- Test: `tmux/control_client_test.go`

**Interfaces:**
- Consumes: `PaneEvent`, `PaneSub` from Task 1.
- Produces: `func (cc *ControlClient) AckReseed(s *PaneSub)`.

- [ ] **Step 1: Write the failing test**

Append to `tmux/control_client_test.go`:

```go
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
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./tmux/ -run 'TestDrop|TestDirty' -v`
Expected: FAIL — `cc.AckReseed undefined`, and once that compiles,
`first event after a drop = {Data:[120] Dirty:false}, want Dirty`.

- [ ] **Step 3: Write the implementation**

In `tmux/control_client.go`, replace `dispatch` and add the two helpers below it:

```go
func (cc *ControlClient) dispatch(paneID string, data []byte) {
	cc.mu.Lock()
	defer cc.mu.Unlock()
	for _, s := range cc.subs[paneID] {
		// A dirty subscriber has a hole in its stream; nothing may be
		// delivered until it re-seeds and acks.
		if s.dirty {
			continue
		}
		select {
		case s.ch <- PaneEvent{Data: data}:
		default:
			// A drop can cut an escape sequence, so everything already
			// buffered is unusable too. Discard it and signal a re-seed.
			cc.markDirtyLocked(s)
		}
	}
}

// markDirtyLocked discards a subscriber's buffered output and leaves a single
// Dirty event in its place. Caller must hold cc.mu.
func (cc *ControlClient) markDirtyLocked(s *PaneSub) {
	if s.dirty {
		return
	}
	for {
		select {
		case <-s.ch:
		default:
			// Drained, and we are the only producer while holding cc.mu,
			// so this send cannot block.
			s.ch <- PaneEvent{Dirty: true}
			s.dirty = true
			return
		}
	}
}

// AckReseed resumes delivery after the subscriber has re-seeded from
// capture-pane in response to a Dirty event.
func (cc *ControlClient) AckReseed(s *PaneSub) {
	cc.mu.Lock()
	defer cc.mu.Unlock()
	s.dirty = false
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./tmux/... -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add tmux/control_client.go tmux/control_client_test.go
git commit -m "fix(tmux): a dropped output chunk marks the pane dirty for re-seed"
```

---

### Task 3: `%pause` marks the stream dirty

tmux discards output while a pane is paused, so resuming without a re-seed
leaves a corrupted screen. `tmux/control_client.go:110` currently logs
`%pause`/`%continue` and does nothing.

**Mark on `%pause` only, never on `%continue`.** This is load-bearing, not an
oversight: houston's own seed handshake in `server/pane_ws.go` issues
`refresh-client -A %pane:pause` *before* it subscribes, so its deliberate pause
reaches no subscriber and marks nothing. Marking on `%continue` would instead
fire a spurious re-seed immediately after every deliberate seed.

**Files:**
- Modify: `tmux/control_client.go:110-111` (the `EventPause, EventContinue` case)
- Test: `tmux/control_client_test.go`

**Interfaces:**
- Consumes: `markDirtyLocked` from Task 2.
- Produces: `func (cc *ControlClient) markPaneDirty(paneID string)`.

- [ ] **Step 1: Write the failing test**

Append to `tmux/control_client_test.go`:

```go
import (
	"bufio"
	"strings"
	"testing"
)

// feed runs the read loop over a canned control-mode transcript.
func feed(cc *ControlClient, transcript string) {
	cc.readLoop(bufio.NewReader(strings.NewReader(transcript)))
}

func TestPauseMarksDirty(t *testing.T) {
	cc := NewControlClient("test")
	sub := cc.Subscribe("%1")

	feed(cc, "%output %1 before\\015\\012\n%pause %1\n")

	if ev := <-sub.C(); ev.Dirty {
		t.Fatal("marked dirty before the pause arrived")
	}
	ev := <-sub.C()
	if !ev.Dirty {
		t.Fatalf("event after %%pause = %+v, want Dirty", ev)
	}
}

func TestContinueDoesNotMarkDirty(t *testing.T) {
	cc := NewControlClient("test")
	sub := cc.Subscribe("%1")

	feed(cc, "%continue %1\n")

	if len(sub.C()) != 0 {
		t.Fatal("%continue marked the pane dirty; only %pause may")
	}
}

func TestPauseOnAnotherPaneIsIgnored(t *testing.T) {
	cc := NewControlClient("test")
	sub := cc.Subscribe("%1")

	feed(cc, "%pause %2\n")

	if len(sub.C()) != 0 {
		t.Fatal("a pause on %2 marked %1 dirty")
	}
}
```

Note: `readLoop` calls `close(cc.done)` on return until Task 4 moves it. Each
test above uses a fresh client, so the double-close hazard does not arise.

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./tmux/ -run TestPause -v`
Expected: FAIL — `event after %pause = {Data:[] Dirty:false}, want Dirty`
(the channel is empty, so the receive blocks and the test panics on timeout).

- [ ] **Step 3: Write the implementation**

In `tmux/control_client.go`, replace the `EventPause, EventContinue` case
(lines 110-111):

```go
		case EventPause:
			// tmux discards output while paused, so resuming without a
			// re-seed would paint on top of a hole. Only %pause marks:
			// houston's own seed handshake pauses BEFORE it subscribes,
			// so its deliberate pause reaches nobody. Marking on
			// %continue instead would re-seed straight after every seed.
			cc.markPaneDirty(event.PaneID)

		case EventContinue:
			slog.Debug("control mode continue", "session", cc.session, "paneID", event.PaneID)
```

Add below `markDirtyLocked`:

```go
// markPaneDirty signals every subscriber of a pane that its stream has a gap.
func (cc *ControlClient) markPaneDirty(paneID string) {
	cc.mu.Lock()
	defer cc.mu.Unlock()
	for _, s := range cc.subs[paneID] {
		cc.markDirtyLocked(s)
	}
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./tmux/... -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add tmux/control_client.go tmux/control_client_test.go
git commit -m "fix(tmux): %pause marks subscribers dirty so %continue re-seeds"
```

---

### Task 4: Reconnect inside the client

`tmux/control_manager.go:47` deletes the client on `Done()` and never re-dials,
so a tmux server restart kills every attached WebSocket. Reconnection belongs in
`ControlClient` rather than the manager: the subscription map then survives the
re-dial, existing `*PaneSub` handles stay valid, and every subscriber is simply
marked dirty on re-attach. Per lazytmux `#482` — a transport drop must not
destroy state that is trivially re-derivable.

This also changes `Done()`: it now closes only on explicit `Close()`.

**Files:**
- Modify: `tmux/control_client.go:51-88` (`Start`), `:90-91` (`readLoop` prologue), `:353-362` (`Close`), struct fields
- Test: `tmux/control_client_test.go`

**Interfaces:**
- Consumes: `markPaneDirty` from Task 3.
- Produces:
  - `func (cc *ControlClient) Connected() bool`
  - unexported `dial func() (io.ReadCloser, io.Writer, func() error, error)` field, injectable by tests

- [ ] **Step 1: Write the failing test**

Append to `tmux/control_client_test.go` (add `"io"`, `"sync"`, `"time"` to the
import block):

```go
// scriptedDialer hands out one canned transcript per dial, so a test can drive
// the client through a disconnect and a re-attach without running tmux.
type scriptedDialer struct {
	mu         sync.Mutex
	transcripts []string
	dials      int
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

func TestReconnectMarksSubscribersDirty(t *testing.T) {
	d := &scriptedDialer{transcripts: []string{
		"%output %1 first\\015\\012\n", // EOF here == connection dropped
	}}
	cc := NewControlClient("test")
	cc.dial = d.dial
	cc.backoff = time.Millisecond
	if err := cc.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = cc.Close() }()

	sub := cc.Subscribe("%1")
	// The first transcript is consumed before Subscribe may run, so drain
	// whatever arrived and assert on the reconnect signal instead.
	deadline := time.After(2 * time.Second)
	for {
		select {
		case ev := <-sub.C():
			if ev.Dirty {
				return // re-attach signalled a re-seed: what we want
			}
		case <-deadline:
			t.Fatalf("no Dirty event after reconnect; dials=%d", d.count())
		}
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
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./tmux/ -run 'TestReconnect|TestDone|TestClose' -v`
Expected: FAIL to compile — `cc.dial undefined`, `cc.backoff undefined`.

- [ ] **Step 3: Write the implementation**

In `tmux/control_client.go`, add to the struct (after the `done` field):

```go
	// dial opens one control-mode connection. Overridable in tests.
	dial    func() (io.ReadCloser, io.Writer, func() error, error)
	backoff time.Duration // initial reconnect delay; doubles to backoffMax

	closeMu  sync.Mutex
	closed   bool
	connMu   sync.RWMutex
	connOK   bool
	closeCur func() error
```

Add `"time"` to the imports. Add the constant above `NewControlClient`:

```go
const backoffMax = 10 * time.Second
```

Set the defaults in `NewControlClient`:

```go
func NewControlClient(session string) *ControlClient {
	cc := &ControlClient{
		session: session,
		subs:    make(map[string][]*PaneSub),
		pending: make(chan commandResponse, 1),
		done:    make(chan struct{}),
		backoff: 250 * time.Millisecond,
	}
	cc.dial = cc.dialTmux
	return cc
}
```

Replace `Start` (lines 51-88) with the split below:

```go
// dialTmux opens the real control-mode connection over a PTY. tmux -CC needs a
// terminal for tcgetattr, so a plain pipe will not do.
func (cc *ControlClient) dialTmux() (io.ReadCloser, io.Writer, func() error, error) {
	master, slave, err := openPTY()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("open pty: %w", err)
	}

	cmd := exec.Command("tmux", "-CC", "attach-session", "-t", cc.session)
	cmd.Stdin = slave
	cmd.Stdout = slave
	cmd.Stderr = slave

	if err := cmd.Start(); err != nil {
		_ = master.Close()
		_ = slave.Close()
		return nil, nil, nil, fmt.Errorf("start tmux -CC: %w", err)
	}
	_ = slave.Close() // child inherited it

	closeFn := func() error {
		_ = master.Close()
		return cmd.Wait()
	}
	return master, master, closeFn, nil
}

func (cc *ControlClient) Start() error {
	r, w, closeFn, err := cc.dial()
	if err != nil {
		return err
	}
	cc.attach(w, closeFn)
	go cc.supervise(r)
	return nil
}

// attach installs a freshly dialled connection and re-asserts client options.
func (cc *ControlClient) attach(w io.Writer, closeFn func() error) {
	cc.connMu.Lock()
	cc.stdin = w
	cc.closeCur = closeFn
	cc.connOK = true
	cc.connMu.Unlock()

	// Exclude this CC client from window size calculations so it never
	// overrides kitty's dimensions (window-size=latest).
	cc.stdinMu.Lock()
	_, err := io.WriteString(w, "refresh-client -f ignore-size\n")
	cc.stdinMu.Unlock()
	if err != nil {
		slog.Warn("failed to set ignore-size on CC client", "error", err)
	}
}

// supervise runs the read loop, re-dialling until Close. Every successful
// re-attach marks all subscribers dirty: the pane painted on while we were
// away, so their screens are stale by definition.
func (cc *ControlClient) supervise(r io.ReadCloser) {
	defer close(cc.done)

	delay := cc.backoff
	for {
		cc.readLoop(bufio.NewReader(r))
		_ = r.Close()

		cc.connMu.Lock()
		cc.connOK = false
		cc.connMu.Unlock()

		if cc.isClosed() {
			return
		}
		slog.Info("control client disconnected, reconnecting", "session", cc.session)

		next, w, closeFn, err := cc.dial()
		if err != nil {
			if cc.isClosed() {
				return
			}
			time.Sleep(delay)
			if delay = delay * 2; delay > backoffMax {
				delay = backoffMax
			}
			continue
		}

		delay = cc.backoff
		r = next
		cc.attach(w, closeFn)
		cc.markAllDirty()
		slog.Info("control client reattached", "session", cc.session)
	}
}

func (cc *ControlClient) markAllDirty() {
	cc.mu.Lock()
	defer cc.mu.Unlock()
	for _, subs := range cc.subs {
		for _, s := range subs {
			cc.markDirtyLocked(s)
		}
	}
}

func (cc *ControlClient) isClosed() bool {
	cc.closeMu.Lock()
	defer cc.closeMu.Unlock()
	return cc.closed
}

// Connected reports whether a control connection is currently established.
// Consumers use it to mark their view stale rather than to tear it down.
func (cc *ControlClient) Connected() bool {
	cc.connMu.RLock()
	defer cc.connMu.RUnlock()
	return cc.connOK
}
```

Remove `defer close(cc.done)` from `readLoop` (line 91) — `supervise` owns it
now. Remove the now-unused `cmd`, `ptyMaster` and `ptySlave` struct fields.

Replace `Close` (lines 353-362):

```go
func (cc *ControlClient) Close() error {
	cc.closeMu.Lock()
	if cc.closed {
		cc.closeMu.Unlock()
		return nil
	}
	cc.closed = true
	cc.closeMu.Unlock()

	cc.connMu.RLock()
	closeFn := cc.closeCur
	cc.connMu.RUnlock()

	if closeFn != nil {
		return closeFn()
	}
	return nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./tmux/... -race -v`
Expected: PASS with no data races.

- [ ] **Step 5: Simplify the manager**

In `tmux/control_manager.go`, replace the exit-monitor goroutine (lines 45-52).
The client no longer exits on a drop, so the monitor should only clean up after
a real close:

```go
	m.clients[session] = &managedClient{client: cc, refCount: 1}
	slog.Info("started control client", "session", session)

	// The client reconnects on its own; Done() now fires only on Close.
	go func() {
		<-cc.Done()
		m.mu.Lock()
		delete(m.clients, session)
		m.mu.Unlock()
		slog.Info("control client closed", "session", session)
	}()
```

- [ ] **Step 6: Run the full suite and build**

Run: `go test ./... -race` then `go build ./...`
Expected: PASS, then no output.

- [ ] **Step 7: Commit**

```bash
git add tmux/control_client.go tmux/control_client_test.go tmux/control_manager.go
git commit -m "fix(tmux): reconnect inside the control client, keeping subscriptions"
```

---

### Task 5: Re-seed the WebSocket on a dirty event, and remove dead code

Wires the signal to the consumer that needs it, and clears the two artefacts the
spec flags: `SetClientSize` has no callers, and the comment at
`server/pane_ws.go:317` describes a `400x200` fixed size that the code does not
set.

**Files:**
- Modify: `server/pane_ws.go` (write loop, `resize` case comment)
- Modify: `tmux/control_client.go` (delete `SetClientSize`)
- Test: manual verification (the re-seed path needs a live tmux server)

**Interfaces:**
- Consumes: `PaneEvent.Dirty`, `AckReseed` from Tasks 1-2.
- Produces: nothing consumed by later tasks in this plan. Plan 2 consumes
  `ControlClient.Connected()` to mark runs stale.

- [ ] **Step 1: Extract the seed send so the re-seed path reuses it**

In `server/pane_ws.go`, add above `paneWSWriteLoop`:

```go
// sendSeed pushes a capture-pane snapshot as the authoritative screen state.
// Used on connect and again after any gap in the control stream.
func (s *Server) sendSeed(conn *websocket.Conn, pane tmux.Pane) error {
	seed, err := s.tmux.CapturePane(pane, 500)
	if err != nil || seed == "" {
		return err
	}
	outputJSON, _ := json.Marshal(WSOutput{Data: seed})
	msg, _ := json.Marshal(WSMessage{Type: "seed", Data: outputJSON})
	return conn.WriteMessage(websocket.TextMessage, msg)
}
```

Replace the inline seed block (currently lines 141-148) with:

```go
	// Seed: capture-pane provides scrollback history and initial visible
	// content. Pane is paused so no %output races with this seed.
	if err := s.sendSeed(conn, pane); err != nil {
		return
	}
```

- [ ] **Step 2: Handle the dirty event in the write loop**

Replace the output case added in Task 1 with:

```go
		case ev := <-sub.C():
			if ev.Dirty {
				// The stream has a hole. Everything buffered was discarded,
				// so capture-pane is the only trustworthy screen state.
				slog.Debug("pane stream dirty, re-seeding", "target", pane.Target())
				if err := s.sendSeed(conn, pane); err != nil {
					return
				}
				cc.AckReseed(sub)
				continue
			}
			// Coalesce: drain all buffered chunks into one write
			// to keep the channel drained and reduce WS round-trips.
			buf := append([]byte(nil), ev.Data...)
			for {
				select {
				case more := <-sub.C():
					if more.Dirty {
						// Rare: a drop while coalescing. Flush what we have,
						// then let the next iteration re-seed over it.
						cc.markPendingReseed(sub)
						goto send
					}
					buf = append(buf, more.Data...)
				default:
					goto send
				}
			}
		send:
```

Add to `tmux/control_client.go`, below `AckReseed`:

```go
// markPendingReseed re-arms a Dirty event that a consumer drained while
// coalescing, so it is not lost.
func (cc *ControlClient) markPendingReseed(s *PaneSub) {
	cc.mu.Lock()
	defer cc.mu.Unlock()
	s.dirty = false // markDirtyLocked is a no-op while already dirty
	cc.markDirtyLocked(s)
}
```

- [ ] **Step 3: Delete the dead code and fix the stale comment**

Delete `SetClientSize` from `tmux/control_client.go` (currently lines 301-308).

In `server/pane_ws.go`, replace the `resize` case comment (lines 316-318):

```go
		case "resize":
			// No-op by design: the CC client is created with
			// `refresh-client -f ignore-size`, so houston never resizes a
			// pane a human is attached to. The browser absorbs the mismatch.
```

- [ ] **Step 4: Verify nothing references the deleted symbol**

Run: `rg -n 'SetClientSize' --type go`
Expected: no matches.

Run: `go build ./... && go vet ./...`
Expected: no output from either.

- [ ] **Step 5: Run the full suite**

Run: `go test ./... -race`
Expected: PASS.

- [ ] **Step 6: Verify against a live tmux server**

```bash
go build -o /tmp/houston . && /tmp/houston -addr 127.0.0.1:9099 -debug
```

In another shell, open a pane in the browser at `http://127.0.0.1:9099`, then
force a reconnect:

```bash
tmux kill-server
```

Expected: the log prints `control client disconnected, reconnecting`, then
`control client reattached`; the browser pane does **not** close, and repaints
with current content once tmux is back. Before this plan the socket died.

To exercise the drop path, run something that floods a pane
(`yes | head -c 10000000`) while the browser tab is throttled (DevTools →
Network → offline for a second). Expected: `pane stream dirty, re-seeding` in
the log and a clean screen afterwards, not garbage.

- [ ] **Step 7: Commit**

```bash
git add server/pane_ws.go tmux/control_client.go
git commit -m "fix(server): re-seed the pane socket after a stream gap"
```

---

## Self-Review

**Spec coverage** — against "Control-mode transport" in the spec:

| Spec requirement | Task |
|---|---|
| `%pause`/`%continue` handled, re-seed on resume | 3, 5 |
| Drop marks dirty and triggers re-seed | 2, 5 |
| Reconnect with backoff; state kept, not destroyed | 4 |
| Keep `refresh-client -f ignore-size` | Global constraint; asserted in Task 5 Step 3 |
| Delete dead `SetClientSize` | 5 |
| Fix stale `400x200` comment | 5 |
| Runs go `Stale` while disconnected | **Deferred to Plan 2** — `Run` does not exist yet. Task 4 exposes `Connected()` as the seam. |

**Placeholder scan:** none — every step carries the code it needs.

**Type consistency:** `PaneEvent`, `PaneSub`, `PaneSub.C()`, `Subscribe`,
`Unsubscribe`, `AckReseed`, `markDirtyLocked`, `markPaneDirty`,
`markPendingReseed`, `Connected`, `dial`, `backoff` are spelled identically in
every task that names them. `dispatch` takes `cc.mu.Lock` from Task 1 onward
because Task 2 mutates `s.dirty` under it.
