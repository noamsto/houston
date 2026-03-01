package tmux

import (
	"bufio"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
)

// ControlClient manages a tmux -CC connection to a session.
type ControlClient struct {
	session string
	cmd     *exec.Cmd
	stdin   io.Writer
	stdinMu sync.Mutex // serialize writes to stdin

	// PTY master/slave for tmux -CC (requires a terminal)
	ptyMaster *os.File
	ptySlave  *os.File

	mu   sync.RWMutex
	subs map[string][]chan<- []byte // paneID → output subscribers

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

func NewControlClient(session string) *ControlClient {
	return &ControlClient{
		session: session,
		subs:    make(map[string][]chan<- []byte),
		pending: make(chan commandResponse, 1),
		done:    make(chan struct{}),
	}
}

func (cc *ControlClient) Start() error {
	// tmux -CC requires a terminal for tcgetattr — use a PTY
	master, slave, err := openPTY()
	if err != nil {
		return fmt.Errorf("open pty: %w", err)
	}
	cc.ptyMaster = master
	cc.ptySlave = slave

	cc.cmd = exec.Command("tmux", "-CC", "attach-session", "-t", cc.session)
	cc.cmd.Stdin = slave
	cc.cmd.Stdout = slave
	cc.cmd.Stderr = slave
	cc.stdin = master // write commands to PTY master

	if err := cc.cmd.Start(); err != nil {
		master.Close()
		slave.Close()
		return fmt.Errorf("start tmux -CC: %w", err)
	}

	// Close slave in parent — child inherited it
	slave.Close()
	cc.ptySlave = nil

	go cc.readLoop(bufio.NewReader(master))
	return nil
}

func (cc *ControlClient) readLoop(r *bufio.Reader) {
	defer close(cc.done)

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
		case EventOutput:
			cc.dispatch(event.PaneID, []byte(event.Data))

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

func (cc *ControlClient) dispatch(paneID string, data []byte) {
	cc.mu.RLock()
	defer cc.mu.RUnlock()
	for _, ch := range cc.subs[paneID] {
		select {
		case ch <- data:
		default:
			// Subscriber too slow — drop this chunk
		}
	}
}

// Subscribe returns a channel that receives raw output for the given pane.
func (cc *ControlClient) Subscribe(paneID string) <-chan []byte {
	ch := make(chan []byte, 64)
	cc.mu.Lock()
	cc.subs[paneID] = append(cc.subs[paneID], ch)
	cc.mu.Unlock()
	return ch
}

// Unsubscribe removes a subscriber channel for a pane.
func (cc *ControlClient) Unsubscribe(paneID string, ch <-chan []byte) {
	cc.mu.Lock()
	defer cc.mu.Unlock()
	subs := cc.subs[paneID]
	for i, s := range subs {
		if fmt.Sprintf("%p", s) == fmt.Sprintf("%p", ch) {
			cc.subs[paneID] = append(subs[:i], subs[i+1:]...)
			break
		}
	}
	if len(cc.subs[paneID]) == 0 {
		delete(cc.subs, paneID)
	}
}

// SendKeys sends literal text to a pane via the control mode connection.
func (cc *ControlClient) SendKeys(paneID, text string) error {
	escaped := strings.ReplaceAll(text, "'", "'\\''")
	cmd := fmt.Sprintf("send-keys -t %s -l '%s'\n", paneID, escaped)
	cc.stdinMu.Lock()
	_, err := io.WriteString(cc.stdin, cmd)
	cc.stdinMu.Unlock()
	return err
}

// SendSpecialKey sends a named key (Enter, Escape, C-c, etc.) to a pane.
func (cc *ControlClient) SendSpecialKey(paneID, key string) error {
	cmd := fmt.Sprintf("send-keys -t %s %s\n", paneID, key)
	cc.stdinMu.Lock()
	_, err := io.WriteString(cc.stdin, cmd)
	cc.stdinMu.Unlock()
	return err
}

// SetClientSize sets the control client dimensions.
func (cc *ControlClient) SetClientSize(cols, rows int) error {
	cmd := fmt.Sprintf("refresh-client -C %d,%d\n", cols, rows)
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

	cc.stdinMu.Lock()
	_, err := io.WriteString(cc.stdin, command+"\n")
	cc.stdinMu.Unlock()
	if err != nil {
		return "", err
	}

	select {
	case resp := <-cc.pending:
		return resp.output, resp.err
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
	cc.stdinMu.Lock()
	_, _ = io.WriteString(cc.stdin, "\n")
	cc.stdinMu.Unlock()

	if cc.ptyMaster != nil {
		cc.ptyMaster.Close()
	}
	return cc.cmd.Wait()
}
