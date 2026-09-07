package tmux

import (
	"bufio"
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

	// Synchronous command support: one command at a time.
	// tmux assigns command numbers server-side, so we serialize
	// and route the next %begin/%end to the pending caller.
	cmdMu   sync.Mutex
	pending chan commandResponse

	done chan struct{}
}

type commandResponse struct {
	output string
	err    error
}

const backoffMax = 10 * time.Second

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
	// Every send path reads cc.stdin under stdinMu, so the assignment must be
	// guarded by the same lock, not connMu.
	cc.stdinMu.Lock()
	cc.stdin = w
	// Exclude this CC client from window size calculations so it never
	// overrides kitty's dimensions (window-size=latest).
	_, err := io.WriteString(w, "refresh-client -f ignore-size\n")
	cc.stdinMu.Unlock()
	if err != nil {
		slog.Warn("failed to set ignore-size on CC client", "error", err)
	}

	cc.connMu.Lock()
	cc.closeCur = closeFn
	cc.connOK = true
	cc.gone = make(chan struct{})
	cc.connMu.Unlock()
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
			// tmux discards output while paused, so resuming without a
			// re-seed would paint on top of a hole. Only %pause marks:
			// houston's own seed handshake pauses before it subscribes, so
			// its deliberate pause usually reaches nobody — but tmux does
			// not guarantee the %pause notification precedes that command's
			// %end, so on the rare reorder this connect does one spurious,
			// harmless re-seed. Marking on %continue instead would re-seed
			// straight after every seed.
			cc.markPaneDirty(event.PaneID)

		case EventContinue:
			slog.Debug("control mode continue", "session", cc.session, "paneID", event.PaneID)

		case EventBegin:
			inBlock = true
			cmdBuf.Reset()

		case EventEnd:
			inBlock = false
			select {
			case cc.pending <- commandResponse{output: cmdBuf.String()}:
			default:
			}

		case EventError:
			inBlock = false
			select {
			case cc.pending <- commandResponse{err: fmt.Errorf("tmux error: %s", cmdBuf.String())}:
			default:
			}

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
			// Drained. Three goroutines write subscriber channels (readLoop,
			// supervise, and the WebSocket write loop), but all are
			// serialized by cc.mu and the reader only ever removes, so this
			// send still cannot block.
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
	cmd := fmt.Sprintf("send-keys -t %s -l '%s'\n", paneID, escaped)
	cc.stdinMu.Lock()
	_, err := io.WriteString(cc.stdin, cmd)
	cc.stdinMu.Unlock()
	return err
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
			cmd := fmt.Sprintf("send-keys -t %s -H %02x\n", paneID, b)
			cc.stdinMu.Lock()
			_, err := io.WriteString(cc.stdin, cmd)
			cc.stdinMu.Unlock()
			return err
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
	cmd := fmt.Sprintf("send-keys -t %s %s\n", paneID, key)
	cc.stdinMu.Lock()
	_, err := io.WriteString(cc.stdin, cmd)
	cc.stdinMu.Unlock()
	return err
}

// RunCommand sends a command and waits for its response.
// Only one command runs at a time (serialized via cmdMu).
func (cc *ControlClient) RunCommand(command string) (string, error) {
	cc.cmdMu.Lock()
	defer cc.cmdMu.Unlock()

	// Drain any stale response from a previous unsolicited %begin/%end
	select {
	case <-cc.pending:
	default:
	}

	// Capture the current connection's cancellation channel BEFORE writing, so
	// a nil here means the connection was already dead and the command cannot
	// have landed. Reading it after the write would let a reattach that swaps
	// cc.stdin before publishing its gone channel report "lost" for a command
	// that was in fact delivered — which strands the caller's state (a pane
	// left paused with nothing to resume it).
	cc.connMu.RLock()
	gone := cc.gone
	cc.connMu.RUnlock()
	if gone == nil {
		return "", fmt.Errorf("control connection lost")
	}

	cc.stdinMu.Lock()
	_, err := io.WriteString(cc.stdin, command+"\n")
	cc.stdinMu.Unlock()
	if err != nil {
		return "", err
	}

	select {
	case resp := <-cc.pending:
		return resp.output, resp.err
	case <-gone:
		return "", fmt.Errorf("control connection lost")
	case <-cc.done:
		return "", fmt.Errorf("control client closed")
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

	cc.connMu.RLock()
	closeFn := cc.closeCur
	cc.connMu.RUnlock()

	if closeFn != nil {
		return closeFn()
	}
	return nil
}
