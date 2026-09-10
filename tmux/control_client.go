package tmux

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// ControlClient manages a tmux -CC connection to a session.
type ControlClient struct {
	session string
	stdin   io.Writer
	stdinMu sync.Mutex // serialize writes to stdin

	// dial opens one control-mode connection. Overridable in tests.
	dial    func() (io.ReadCloser, io.Writer, func() error, error)
	backoff time.Duration // initial reconnect delay; doubles to backoffMax

	closeMu  sync.Mutex
	closed   bool
	connMu   sync.RWMutex
	connOK   bool
	closeCur func() error
	// gone is closed when the CURRENT connection ends, so a caller blocked in
	// RunCommand is released at the end of that connection rather than waiting
	// for Close(). Replaced on every attach; nil means no live connection.
	// Guarded by connMu.
	gone chan struct{}

	mu   sync.RWMutex
	subs map[string][]*PaneSub // paneID → subscribers
	gaps map[string]*gap       // paneID → open pause gap, guarded by mu

	// gapDeadline bounds an open gap: past it the pane is resumed and every
	// subscriber on it re-seeds. Shortened by tests.
	gapDeadline time.Duration

	// Synchronous command support. tmux answers every write with exactly one
	// block, in the order it received the writes, so a reply is bound to its
	// command by queue position. The command number cannot be a lookup key:
	// it is a server-global counter, so houston never knows in advance what
	// its own write will be assigned.
	//
	// Lock order is stdinMu → cmdQMu. readLoop must never take stdinMu, or a
	// blocked PTY write and a blocked reader deadlock against each other.
	//
	// cmdMu only serializes RunCommand callers; it is cmdQ, not this lock, that
	// binds a block to the command that asked for it. An unwaited writer never
	// takes cmdMu, so a caller holding it can still be handed that writer's
	// block.
	cmdMu     sync.Mutex
	cmdQMu    sync.Mutex
	cmdQ      []chan commandResponse // commands awaiting their block; a nil entry is a writer that is not waiting
	cmdDesync bool

	done chan struct{}
}

type commandResponse struct {
	output string
	err    error
}

// gap represents one open %pause on a pane, from %pause to %continue.
type gap struct {
	timer *time.Timer // fires expireGap; guarded by ControlClient.mu
}

const backoffMax = 10 * time.Second

// Exclude this CC client from window size calculations so it never overrides
// kitty's dimensions (window-size=latest).
const ignoreSizeCmd = "refresh-client -f ignore-size"

var (
	errConnLost     = errors.New("control connection lost")
	errClientClosed = errors.New("control client closed")
	// errTmuxRefused wraps a refusal tmux actually sent, which callers branch
	// on to tell it apart from every other way a command can fail.
	errTmuxRefused = errors.New("tmux refused the command")
	errCmdDesync   = errors.New("control command stream desynchronised")
)

func NewControlClient(session string) *ControlClient {
	cc := &ControlClient{
		session: session,
		subs:    make(map[string][]*PaneSub),
		gaps:    make(map[string]*gap),
		done:    make(chan struct{}),
		backoff: 250 * time.Millisecond,
		// Comfortably beyond a healthy seed handshake, short enough that a
		// pane nothing resumes is not a minute of frozen screen.
		gapDeadline: 10 * time.Second,
	}
	cc.dial = cc.dialTmux
	return cc
}

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

	var once sync.Once
	closeFn := func() error {
		var err error
		once.Do(func() {
			_ = master.Close()
			// Closing the PTY alone can leave the tmux client alive for ~10s
			// before it notices, and Wait() would block that whole time —
			// stalling both Close() and supervise. Killing the CLIENT is safe:
			// the server and the session are untouched by it.
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
			err = cmd.Wait() // still reaps the child; no zombie
		})
		return err
	}
	return master, master, closeFn, nil
}

func (cc *ControlClient) Start() error {
	r, w, closeFn, err := cc.dial()
	if err != nil {
		return err
	}
	cc.attach(w, closeFn)
	go cc.supervise(r, closeFn)
	return nil
}

// attach installs a freshly dialled connection and re-asserts client options.
func (cc *ControlClient) attach(w io.Writer, closeFn func() error) {
	// Installed before the first enrolled write: a desync raised by that write
	// tears down whatever closeCur names, and the previous connection's is a
	// spent sync.Once — or nil on the first attach — which would leave this
	// connection installed and terminal. Write order is unaffected, since the
	// stdinMu section below spans both the cc.stdin assignment and the write.
	cc.connMu.Lock()
	cc.closeCur = closeFn
	cc.connOK = true
	cc.gone = make(chan struct{})
	cc.connMu.Unlock()

	// Every send path reads cc.stdin under stdinMu, so the assignment must be
	// guarded by the same lock, not connMu.
	cc.stdinMu.Lock()
	cc.stdin = w
	cc.cmdQMu.Lock()
	cc.cmdQ = nil
	cc.cmdDesync = false
	cc.cmdQMu.Unlock()
	err := cc.writeCommandLocked(ignoreSizeCmd, nil)
	cc.stdinMu.Unlock()
	if err != nil {
		slog.Warn("failed to set ignore-size on CC client", "error", err)
		cc.dropConnection("ignore-size write failed")
	}
}

// writeCommandLocked enrolls reply and writes command. The caller must hold
// stdinMu, which is what makes enrollment order equal write order.
func (cc *ControlClient) writeCommandLocked(command string, reply chan commandResponse) error {
	cc.cmdQMu.Lock()
	if cc.cmdDesync {
		cc.cmdQMu.Unlock()
		return errCmdDesync
	}
	// Enrolled before the write: tmux can answer the instant the bytes land,
	// and a block that finds an empty queue has nothing to bind to.
	cc.cmdQ = append(cc.cmdQ, reply)
	cc.cmdQMu.Unlock()

	_, err := io.WriteString(cc.stdin, command+"\n")
	if err != nil {
		// The entry is left in place: a short write may have delivered part of
		// a command line, so the queue can no longer be reasoned about, which
		// is exactly what cmdDesync records. Set inside the stdinMu section,
		// or a write failing against a dying connection could mark the
		// freshly-installed one desynchronised.
		cc.cmdQMu.Lock()
		cc.cmdDesync = true
		cc.cmdQMu.Unlock()
	}
	return err
}

func (cc *ControlClient) writeCommand(command string, reply chan commandResponse) error {
	cc.stdinMu.Lock()
	err := cc.writeCommandLocked(command, reply)
	cc.stdinMu.Unlock()
	// errCmdDesync means the stream was already desynchronised, and whoever
	// desynchronised it already dropped the connection; dropping again would log
	// a teardown per send for the whole window before the reconnect lands.
	if err != nil && !errors.Is(err, errCmdDesync) {
		cc.dropConnection("control command write failed")
	}
	return err
}

// claimCommand pops the command the next block answers.
func (cc *ControlClient) claimCommand() chan commandResponse {
	cc.cmdQMu.Lock()
	defer cc.cmdQMu.Unlock()
	if cc.cmdDesync || len(cc.cmdQ) == 0 {
		return nil
	}
	reply := cc.cmdQ[0]
	cc.cmdQ = cc.cmdQ[1:]
	return reply
}

// desync abandons the queue once its alignment stops being knowable, failing
// every enrolled waiter rather than answering it from another command's block.
// A false return means someone got here first, so the teardown runs once.
func (cc *ControlClient) desync() bool {
	cc.cmdQMu.Lock()
	defer cc.cmdQMu.Unlock()
	if cc.cmdDesync {
		return false
	}
	cc.cmdDesync = true
	for _, reply := range cc.cmdQ {
		if reply != nil {
			reply <- commandResponse{err: errCmdDesync}
		}
	}
	cc.cmdQ = nil
	return true
}

// dropConnection ends the current connection so supervise re-dials, attach
// resets the queue and markAllDirty re-seeds every subscriber. Without it a
// desynchronised but transport-healthy connection is terminal: readLoop keeps
// reading, connOK stays true, nothing re-dials, and every later command fails
// for the life of the client while a paused pane stays dark.
func (cc *ControlClient) dropConnection(reason string) {
	slog.Warn("dropping control connection", "session", cc.session, "reason", reason)
	// No nil guard: attach installs closeCur before any path that can drop
	// the connection exists.
	cc.connMu.RLock()
	closeFn := cc.closeCur
	cc.connMu.RUnlock()
	_ = closeFn()
}

// minHealthyConn is how long a connection must last to count as healthy. A
// connection that dies sooner is treated as a failed attempt for backoff
// purposes, even though dial() itself succeeded — a tmux server that is up
// but has no such session accepts the dial and drops it instantly.
const minHealthyConn = 5 * time.Second

// supervise runs the read loop, re-dialling until Close. Every successful
// re-attach marks all subscribers dirty: the pane painted on while we were
// away, so their screens are stale by definition.
func (cc *ControlClient) supervise(r io.ReadCloser, closeConn func() error) {
	defer close(cc.done)

	delay := cc.backoff
	for {
		start := time.Now()
		cc.readLoop(bufio.NewReader(r))
		// closeConn, not r.Close(): it closes the PTY master AND waits on the
		// tmux child. Closing only the reader leaves a zombie per reconnect.
		_ = closeConn()

		cc.connMu.Lock()
		cc.connOK = false
		if cc.gone != nil {
			close(cc.gone)
			cc.gone = nil // a dial-error lap must not close it twice
		}
		cc.connMu.Unlock()

		if cc.isClosed() {
			return
		}
		if time.Since(start) >= minHealthyConn {
			delay = cc.backoff
		}

		slog.Info("control client disconnected, reconnecting",
			"session", cc.session, "in", delay)
		time.Sleep(delay)
		if delay *= 2; delay > backoffMax {
			delay = backoffMax
		}
		if cc.isClosed() {
			return
		}

		next, w, closeFn, err := cc.dial()
		if err != nil {
			continue
		}
		// Close() may have run while dial() was in flight. It could only have
		// closed the previous connection, so this one is ours to clean up.
		if cc.isClosed() {
			_ = closeFn()
			return
		}

		r = next
		closeConn = closeFn
		cc.attach(w, closeFn)
		cc.markAllDirty()
		slog.Info("control client reattached", "session", cc.session)
	}
}

func (cc *ControlClient) markAllDirty() {
	cc.mu.Lock()
	defer cc.mu.Unlock()
	// Pause state belongs to the connection that raised it; a gap left open
	// by a dead connection must not let a later, unrelated %continue re-seed.
	cc.clearGapsLocked()
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

func (cc *ControlClient) readLoop(r *bufio.Reader) {
	var cmdBuf strings.Builder
	inBlock := false
	blockNum := 0
	var blockReply chan commandResponse
	// A real tmux -CC answers the dialer's own attach-session command before
	// houston has written anything, so exactly one block per connection is
	// owed to no write of ours. Per-connection state, not a field: a once-only
	// field would leave every connection after the first permanently one block
	// out of step.
	sawBaseline := false

	// endBlock hands a terminator to whoever asked for the block.
	endBlock := func(cmdNum int, resp commandResponse) {
		if !inBlock {
			// Binding happens at %begin, so a block whose %begin never reached
			// us claimed no queue entry: the queue is untouched and still
			// aligned, and dropping the orphan is all this case asks for. The
			// one %begin that can go missing is the baseline's — it is the only
			// line tmux glues its control-mode introducer onto — so absorb the
			// baseline here too, which keeps a stream introduced some other way
			// merely odd instead of permanently one block out of step.
			slog.Debug("control block terminator outside a block", "session", cc.session, "cmd", cmdNum)
			sawBaseline = true
			return
		}
		if cmdNum != blockNum {
			// Unlike the orphan above, this block was bound at its %begin and
			// then answered under a different command number, so which entry
			// the queue head now belongs to is unknowable and delivering would
			// answer the wrong caller.
			if cc.desync() {
				cc.dropConnection("block terminator does not match its %begin")
			}
			inBlock = false
			blockReply = nil
			return
		}
		if blockReply != nil {
			blockReply <- resp
		}
		inBlock = false
		blockReply = nil
	}

	for {
		line, err := r.ReadString('\n')
		if err != nil {
			slog.Debug("control mode read loop ended", "session", cc.session, "error", err)
			return
		}
		line = strings.TrimRight(line, "\r\n")

		event := ParseControlLine(line)

		switch event.Type {
		case EventOutput, EventExtendedOutput:
			cc.dispatch(event.PaneID, []byte(event.Data))

		case EventPause:
			// tmux discards output while paused, so the Dirty belongs at the
			// end of the gap, not here — see openGap.
			cc.openGap(event.PaneID)

		case EventContinue:
			cc.closeGap(event.PaneID)

		case EventBegin:
			inBlock = true
			cmdBuf.Reset()
			// Recorded for the baseline too, so its own terminator is a match
			// rather than a desynchronising mismatch.
			blockNum = event.CmdNumber
			if !sawBaseline {
				sawBaseline = true
				blockReply = nil
				break
			}
			// A block that finds an empty queue is one houston did not write:
			// it owns no queue position, so dropping it keeps the queue
			// aligned where desynchronising would not.
			blockReply = cc.claimCommand()

		case EventEnd:
			endBlock(event.CmdNumber, commandResponse{output: cmdBuf.String()})

		case EventError:
			endBlock(event.CmdNumber, commandResponse{err: fmt.Errorf("%w: %s", errTmuxRefused, cmdBuf.String())})

		case EventData:
			if inBlock {
				if cmdBuf.Len() > 0 {
					cmdBuf.WriteByte('\n')
				}
				cmdBuf.WriteString(event.Data)
			}

		default:
			slog.Debug("control mode notification", "session", cc.session, "type", event.Type, "data", event.Data)
		}
	}
}

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
	inGap bool // attached before the pane's open gap closed; guarded by ControlClient.mu
}

// C returns the event stream. Read it until the subscription is released.
func (s *PaneSub) C() <-chan PaneEvent { return s.ch }

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
			// Drained, and every writer holds cc.mu while the reader only
			// removes, so this send cannot block.
			s.ch <- PaneEvent{Dirty: true}
			s.dirty = true
			return
		}
	}
}

// markPaneDirty signals every subscriber of a pane that its stream has a gap.
func (cc *ControlClient) markPaneDirty(paneID string) {
	cc.mu.Lock()
	defer cc.mu.Unlock()
	for _, s := range cc.subs[paneID] {
		cc.markDirtyLocked(s)
	}
}

// openGap records that paneID has entered a pause. %pause is edge-triggered —
// tmux emits it only on the not-paused → paused transition — so a gap already
// open on the pane means this one is a repeat and is a no-op. Every current
// subscriber is flagged inGap so closeGap knows who was here before the gap,
// as opposed to a subscriber that attaches during it.
func (cc *ControlClient) openGap(paneID string) {
	cc.mu.Lock()
	defer cc.mu.Unlock()
	cc.openGapLocked(paneID)
}

// openGapLocked is openGap's body. Caller must hold cc.mu.
func (cc *ControlClient) openGapLocked(paneID string) {
	// readLoop keeps parsing already-buffered lines after Close(), so a late
	// %pause must not arm a full-length timer that outlives the client.
	if cc.isClosed() {
		return
	}
	if _, open := cc.gaps[paneID]; open {
		return
	}
	for _, s := range cc.subs[paneID] {
		s.inGap = true
	}
	g := &gap{}
	// Assigned inside the critical section: clearGapsLocked reads g.timer
	// under cc.mu from a Close() that can run concurrently with readLoop.
	g.timer = time.AfterFunc(cc.gapDeadline, func() { cc.expireGap(paneID, g) })
	cc.gaps[paneID] = g
}

// closeGap ends paneID's pause. Deleting the gap record is the claim: only
// the caller that removes it marks anyone dirty. An orphan %continue — no gap
// open — marks nobody. Skipping mid-gap arrivals is a tradeoff, not a
// guarantee: it spares every deliberate seed a spurious re-seed at its own
// handshake's %continue, and costs that subscriber whatever tmux discarded
// between its capture-pane and the %continue.
func (cc *ControlClient) closeGap(paneID string) {
	cc.mu.Lock()
	defer cc.mu.Unlock()
	g, open := cc.gaps[paneID]
	if !open {
		return
	}
	g.timer.Stop()
	delete(cc.gaps, paneID)
	for _, s := range cc.subs[paneID] {
		if s.inGap {
			s.inGap = false
			cc.markDirtyLocked(s)
		}
	}
}

// expireGap bounds a gap whose %continue never came: it resumes the pane and,
// once the resume has landed, marks every current subscriber dirty, mid-gap
// arrivals included — the pause is per control client and houston runs one per
// session, so one socket's stuck handshake freezes the pane for every socket
// on it.
func (cc *ControlClient) expireGap(paneID string, g *gap) {
	cc.mu.Lock()
	// A timer whose Stop lost the race may claim only the gap it armed, never
	// a newer one that has since opened on the pane.
	if cc.gaps[paneID] != g {
		cc.mu.Unlock()
		return
	}
	g.timer.Stop()
	delete(cc.gaps, paneID)
	// Cleared inside the claim, so a gap opening while the resume is in flight
	// owns its own flags and the marking below cannot wipe them.
	for _, s := range cc.subs[paneID] {
		s.inGap = false
	}
	cc.mu.Unlock()

	// Resume first, mark after. Marked first, the consumer would capture-pane
	// while tmux is still discarding: everything painted before the resume
	// lands is lost, and the subscriber, having acked, never re-seeds again.
	//
	// paneID must be quoted: tmux's command-string lexer rejects a bare token
	// that starts with '%' and contains ':', so %0:continue is a parse error.
	if _, err := cc.RunCommand(fmt.Sprintf("refresh-client -A '%s:continue'", paneID)); err != nil {
		// Only a refusal tmux sent means the pane may still be paused with
		// tmux still willing to talk. Every other failure — the connection
		// already gone before the write, dying after it, or a desynchronised
		// queue — ends in a re-attach, and that sweeps the gaps and re-seeds
		// every subscriber anyway. Re-arming there would retry against a
		// connection that cannot carry the write.
		if !errors.Is(err, errTmuxRefused) {
			slog.Debug("gap deadline resume abandoned to the reconnect",
				"session", cc.session, "pane", paneID, "error", err)
			return
		}
		slog.Warn("gap deadline resume refused",
			"session", cc.session, "pane", paneID, "error", err)
		// The pane may still be paused, and %pause is edge-triggered: tmux
		// will never announce one it already considers paused. Without a
		// fresh gap nothing would ever retry, and the pane would stay dark
		// for good. Nobody subscribed means nobody to retry for.
		cc.mu.Lock()
		if len(cc.subs[paneID]) > 0 {
			cc.openGapLocked(paneID)
		}
		cc.mu.Unlock()
		// Marking here would be worse than not marking: the capture would come
		// off a still-paused pane, and that stale dirty flag makes the retry's
		// own mark a no-op — the screen would never recover.
		return
	}
	cc.markPaneDirty(paneID)
}

// clearGapsLocked cancels every armed deadline and forgets all gap state.
// Caller must hold cc.mu.
func (cc *ControlClient) clearGapsLocked() {
	for _, g := range cc.gaps {
		g.timer.Stop()
	}
	clear(cc.gaps)
	for _, subs := range cc.subs {
		for _, s := range subs {
			s.inGap = false
		}
	}
}

func (cc *ControlClient) clearGaps() {
	cc.mu.Lock()
	defer cc.mu.Unlock()
	cc.clearGapsLocked()
}

// AckReseed resumes delivery after the subscriber has re-seeded from
// capture-pane in response to a Dirty event.
func (cc *ControlClient) AckReseed(s *PaneSub) {
	cc.mu.Lock()
	defer cc.mu.Unlock()
	s.dirty = false
}

// MarkPendingReseed re-arms a Dirty event that a consumer drained while
// coalescing, so it is not lost.
func (cc *ControlClient) MarkPendingReseed(s *PaneSub) {
	cc.mu.Lock()
	defer cc.mu.Unlock()
	s.dirty = false // markDirtyLocked is a no-op while already dirty
	cc.markDirtyLocked(s)
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

// SendKeys sends text to a pane via the control mode connection.
// Printable text uses -l (literal) which supports UTF-8. Control characters
// and escape sequences are mapped to tmux key names to avoid embedding
// raw control bytes in the CC command string.
func (cc *ControlClient) SendKeys(paneID, text string) error {
	// Fast path: all printable text — send as literal
	if isPrintable(text) {
		return cc.sendLiteral(paneID, text)
	}

	// Mixed content (e.g. paste with newlines): split into printable
	// segments and control characters, send each appropriately.
	i := 0
	for i < len(text) {
		b := text[i]
		if b == 0x1b && i+1 < len(text) {
			// Escape sequence — try to match and send as key name
			if seqLen, name := matchEscSeq(text[i:]); seqLen > 0 {
				if err := cc.SendSpecialKey(paneID, name); err != nil {
					return err
				}
				i += seqLen
				continue
			}
		}
		if b < 0x20 || b == 0x7f {
			if err := cc.sendControl(paneID, b); err != nil {
				return err
			}
			i++
		} else {
			// Collect run of printable bytes
			j := i + 1
			for j < len(text) && text[j] >= 0x20 && text[j] != 0x7f {
				j++
			}
			if err := cc.sendLiteral(paneID, text[i:j]); err != nil {
				return err
			}
			i = j
		}
	}
	return nil
}

func isPrintable(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] == 0x7f {
			return false
		}
	}
	return true
}

func (cc *ControlClient) sendLiteral(paneID, text string) error {
	escaped := strings.ReplaceAll(text, "'", "'\\''")
	return cc.writeCommand(fmt.Sprintf("send-keys -t %s -l '%s'", paneID, escaped), nil)
}

func (cc *ControlClient) sendControl(paneID string, b byte) error {
	var name string
	switch b {
	case '\r', '\n':
		name = "Enter"
	case '\t':
		name = "Tab"
	case 0x1b:
		name = "Escape"
	case 0x7f:
		name = "BSpace"
	default:
		if b >= 1 && b <= 26 {
			name = fmt.Sprintf("C-%c", 'a'+rune(b)-1)
		} else {
			// Rare control char — send as hex
			return cc.writeCommand(fmt.Sprintf("send-keys -t %s -H %02x", paneID, b), nil)
		}
	}
	return cc.SendSpecialKey(paneID, name)
}

// matchEscSeq tries to match a terminal escape sequence and returns its
// length and tmux key name. Returns (0, "") if no match.
func matchEscSeq(s string) (int, string) {
	seqs := map[string]string{
		"\x1b[A": "Up", "\x1b[B": "Down", "\x1b[C": "Right", "\x1b[D": "Left",
		"\x1b[H": "Home", "\x1b[F": "End",
		"\x1b[2~": "Insert", "\x1b[3~": "DC", "\x1b[5~": "PPage", "\x1b[6~": "NPage",
		"\x1b[Z": "BTab",
		"\x1bOP": "F1", "\x1bOQ": "F2", "\x1bOR": "F3", "\x1bOS": "F4",
		"\x1b[15~": "F5", "\x1b[17~": "F6", "\x1b[18~": "F7", "\x1b[19~": "F8",
		"\x1b[20~": "F9", "\x1b[21~": "F10", "\x1b[23~": "F11", "\x1b[24~": "F12",
	}
	// Try longest match first (up to 6 bytes)
	for l := min(len(s), 6); l >= 2; l-- {
		if name, ok := seqs[s[:l]]; ok {
			return l, name
		}
	}
	return 0, ""
}

// SendSpecialKey sends a named key (Enter, Escape, C-c, etc.) to a pane.
func (cc *ControlClient) SendSpecialKey(paneID, key string) error {
	return cc.writeCommand(fmt.Sprintf("send-keys -t %s %s", paneID, key), nil)
}

// RunCommand sends a command and waits for the block tmux answers it with.
// cmdMu only serializes callers; what binds the reply to this command is its
// position in cmdQ.
func (cc *ControlClient) RunCommand(command string) (string, error) {
	cc.cmdMu.Lock()
	defer cc.cmdMu.Unlock()

	// Captured before the write, so a nil here means the connection was
	// already dead and the command cannot have landed.
	cc.connMu.RLock()
	gone := cc.gone
	cc.connMu.RUnlock()
	if gone == nil {
		return "", errConnLost
	}

	// Buffered and sent exactly once, so readLoop never blocks on a waiter
	// that has already left via gone or done.
	reply := make(chan commandResponse, 1)
	if err := cc.writeCommand(command, reply); err != nil {
		return "", err
	}

	select {
	case resp := <-reply:
		return resp.output, resp.err
	case <-gone:
		return "", errConnLost
	case <-cc.done:
		return "", errClientClosed
	}
}

// SubscriberCount returns the total number of active subscribers.
func (cc *ControlClient) SubscriberCount() int {
	cc.mu.RLock()
	defer cc.mu.RUnlock()
	count := 0
	for _, subs := range cc.subs {
		count += len(subs)
	}
	return count
}

// Done returns a channel that closes when the control client exits.
func (cc *ControlClient) Done() <-chan struct{} {
	return cc.done
}

func (cc *ControlClient) Close() error {
	cc.closeMu.Lock()
	if cc.closed {
		cc.closeMu.Unlock()
		return nil
	}
	cc.closed = true
	cc.closeMu.Unlock()

	// closeMu must be released first: openGapLocked calls isClosed() (which
	// takes closeMu) while holding cc.mu, so sweeping under closeMu would
	// invert that order and deadlock.
	cc.clearGaps()

	cc.connMu.RLock()
	closeFn := cc.closeCur
	cc.connMu.RUnlock()

	if closeFn != nil {
		return closeFn()
	}
	return nil
}
