# Control Mode I/O Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Replace capture-pane polling + send-keys with tmux control mode for real-time pane I/O.

**Architecture:** One `tmux -CC attach -t session` subprocess per session, managed by a ControlManager. `%output %N data` events are octal-unescaped and routed to per-pane subscriber channels. WebSocket handlers subscribe on connect, seed with a one-shot capture-pane, then forward incremental output. Agent detection keeps using capture-pane at ~1s via the existing tmux.Client. Frontend switches from clear-and-rewrite snapshots to incremental `term.write()`.

**Tech Stack:** Go stdlib (`os/exec`, `bufio`, `sync`), existing `tmux.Client` for metadata, xterm.js for incremental rendering.

---

## Key Reference

- tmux control mode docs: https://github.com/tmux/tmux/wiki/Control-Mode
- `%output %N data` — pane output, octal-escaped (chars <32 and `\` → `\NNN`)
- `%begin T N F` / `%end T N F` — command response blocks
- `refresh-client -C WxH` — set control client size (makes it a real tmux client)
- Pane IDs are global (`%0`, `%1`, `%20`), NOT `session:window.index`
- Houston's `tmux.Pane` uses `Session:Window.Index` format — need pane ID lookup

---

### Task 1: Octal Unescaping

**Files:**
- Create: `tmux/escape.go`
- Create: `tmux/escape_test.go`

**Step 1: Write the failing tests**

```go
// tmux/escape_test.go
package tmux

import "testing"

func TestUnescapeOctal(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"plain text", "hello world", "hello world"},
		{"newline CR LF", `hello\015\012world`, "hello\r\nworld"},
		{"backslash", `path\134to\134file`, `path\to\file`},
		{"tab", `col1\011col2`, "col1\tcol2"},
		{"escape char", `\033[32mgreen\033[0m`, "\033[32mgreen\033[0m"},
		{"mixed", `ls /\015\015\012bin/ dev/`, "ls /\r\r\nbin/ dev/"},
		{"empty", "", ""},
		{"no escapes", "just plain text 123", "just plain text 123"},
		{"backslash at end", `trailing\134`, `trailing\`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := UnescapeOctal(tt.input)
			if got != tt.want {
				t.Errorf("UnescapeOctal(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
```

**Step 2: Run tests to verify they fail**

Run: `(cd tmux && go test -run TestUnescapeOctal -v)`
Expected: FAIL — `UnescapeOctal` not defined

**Step 3: Write implementation**

```go
// tmux/escape.go
package tmux

// UnescapeOctal decodes tmux control mode octal escaping.
// Characters with ASCII value <32 and backslash are encoded as \NNN (3-digit octal).
func UnescapeOctal(s string) string {
	// Fast path: no backslash means no escapes
	hasBackslash := false
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' {
			hasBackslash = true
			break
		}
	}
	if !hasBackslash {
		return s
	}

	buf := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			d1, d2, d3 := s[i+1], s[i+2], s[i+3]
			if d1 >= '0' && d1 <= '3' && d2 >= '0' && d2 <= '7' && d3 >= '0' && d3 <= '7' {
				val := (d1-'0')*64 + (d2-'0')*8 + (d3-'0')
				buf = append(buf, val)
				i += 3
				continue
			}
		}
		buf = append(buf, s[i])
	}
	return string(buf)
}
```

**Step 4: Run tests to verify they pass**

Run: `(cd tmux && go test -run TestUnescapeOctal -v)`
Expected: PASS

**Step 5: Commit**

```
git add tmux/escape.go tmux/escape_test.go
git commit -m "feat: add tmux control mode octal unescaping"
```

---

### Task 2: Control Mode Line Parser

**Files:**
- Create: `tmux/control.go`
- Create: `tmux/control_test.go`

This task builds the event parsing layer only — no subprocess, no I/O. Pure line-in, event-out.

**Step 1: Write the failing tests**

```go
// tmux/control_test.go
package tmux

import "testing"

func TestParseControlLine(t *testing.T) {
	tests := []struct {
		name  string
		line  string
		want  ControlEvent
	}{
		{
			"pane output",
			`%output %42 hello\015\012`,
			ControlEvent{Type: EventOutput, PaneID: "%42", Data: "hello\r\n"},
		},
		{
			"begin block",
			"%begin 1578920019 258 0",
			ControlEvent{Type: EventBegin, CmdNumber: 258},
		},
		{
			"end block",
			"%end 1578920019 258 0",
			ControlEvent{Type: EventEnd, CmdNumber: 258},
		},
		{
			"error block",
			"%error 1578920019 258 0",
			ControlEvent{Type: EventError, CmdNumber: 258},
		},
		{
			"window renamed",
			"%window-renamed @1 vim",
			ControlEvent{Type: EventWindowRenamed, WindowID: "@1", Data: "vim"},
		},
		{
			"sessions changed",
			"%sessions-changed",
			ControlEvent{Type: EventSessionsChanged},
		},
		{
			"session changed",
			"%session-changed $1 mysession",
			ControlEvent{Type: EventSessionChanged, Data: "mysession"},
		},
		{
			"window add",
			"%window-add @5",
			ControlEvent{Type: EventWindowAdd, WindowID: "@5"},
		},
		{
			"window close",
			"%window-close @5",
			ControlEvent{Type: EventWindowClose, WindowID: "@5"},
		},
		{
			"pane mode changed",
			"%pane-mode-changed %3",
			ControlEvent{Type: EventPaneModeChanged, PaneID: "%3"},
		},
		{
			"unknown line",
			"some random output",
			ControlEvent{Type: EventData, Data: "some random output"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseControlLine(tt.line)
			if got.Type != tt.want.Type {
				t.Errorf("Type = %v, want %v", got.Type, tt.want.Type)
			}
			if got.PaneID != tt.want.PaneID {
				t.Errorf("PaneID = %q, want %q", got.PaneID, tt.want.PaneID)
			}
			if got.WindowID != tt.want.WindowID {
				t.Errorf("WindowID = %q, want %q", got.WindowID, tt.want.WindowID)
			}
			if got.Data != tt.want.Data {
				t.Errorf("Data = %q, want %q", got.Data, tt.want.Data)
			}
			if got.CmdNumber != tt.want.CmdNumber {
				t.Errorf("CmdNumber = %d, want %d", got.CmdNumber, tt.want.CmdNumber)
			}
		})
	}
}
```

**Step 2: Run tests to verify they fail**

Run: `(cd tmux && go test -run TestParseControlLine -v)`
Expected: FAIL — types not defined

**Step 3: Write implementation**

Add to `tmux/control.go`:

```go
package tmux

import (
	"strconv"
	"strings"
)

// ControlEventType represents a parsed control mode line type.
type ControlEventType int

const (
	EventOutput          ControlEventType = iota
	EventBegin
	EventEnd
	EventError
	EventWindowAdd
	EventWindowClose
	EventWindowRenamed
	EventSessionChanged
	EventSessionsChanged
	EventPaneModeChanged
	EventData // non-notification line (command response body, etc.)
)

// ControlEvent is a parsed control mode line.
type ControlEvent struct {
	Type      ControlEventType
	PaneID    string // e.g. "%42"
	WindowID  string // e.g. "@1"
	CmdNumber int    // from %begin/%end/%error
	Data      string // unescaped output, window name, etc.
}

// ParseControlLine parses a single line from tmux control mode stdout.
func ParseControlLine(line string) ControlEvent {
	switch {
	case strings.HasPrefix(line, "%output "):
		return parseOutput(line)
	case strings.HasPrefix(line, "%begin "):
		return parseBlock(line, EventBegin)
	case strings.HasPrefix(line, "%end "):
		return parseBlock(line, EventEnd)
	case strings.HasPrefix(line, "%error "):
		return parseBlock(line, EventError)
	case strings.HasPrefix(line, "%window-renamed "):
		return parseWindowRenamed(line)
	case strings.HasPrefix(line, "%window-add "):
		return parseWindowEvent(line, EventWindowAdd)
	case strings.HasPrefix(line, "%window-close "):
		return parseWindowEvent(line, EventWindowClose)
	case strings.HasPrefix(line, "%sessions-changed"):
		return ControlEvent{Type: EventSessionsChanged}
	case strings.HasPrefix(line, "%session-changed "):
		return parseSessionChanged(line)
	case strings.HasPrefix(line, "%pane-mode-changed "):
		return parsePaneModeChanged(line)
	default:
		return ControlEvent{Type: EventData, Data: line}
	}
}

func parseOutput(line string) ControlEvent {
	// "%output %42 hello\015\012"
	// Find pane ID (starts after "%output ")
	rest := line[len("%output "):]
	spaceIdx := strings.IndexByte(rest, ' ')
	if spaceIdx < 0 {
		return ControlEvent{Type: EventOutput, PaneID: rest}
	}
	paneID := rest[:spaceIdx]
	data := UnescapeOctal(rest[spaceIdx+1:])
	return ControlEvent{Type: EventOutput, PaneID: paneID, Data: data}
}

func parseBlock(line string, eventType ControlEventType) ControlEvent {
	// "%begin 1578920019 258 0"
	fields := strings.Fields(line)
	var cmdNum int
	if len(fields) >= 3 {
		cmdNum, _ = strconv.Atoi(fields[2])
	}
	return ControlEvent{Type: eventType, CmdNumber: cmdNum}
}

func parseWindowRenamed(line string) ControlEvent {
	// "%window-renamed @1 vim"
	rest := line[len("%window-renamed "):]
	spaceIdx := strings.IndexByte(rest, ' ')
	if spaceIdx < 0 {
		return ControlEvent{Type: EventWindowRenamed, WindowID: rest}
	}
	return ControlEvent{Type: EventWindowRenamed, WindowID: rest[:spaceIdx], Data: rest[spaceIdx+1:]}
}

func parseWindowEvent(line string, eventType ControlEventType) ControlEvent {
	// "%window-add @5" or "%window-close @5"
	fields := strings.Fields(line)
	var windowID string
	if len(fields) >= 2 {
		windowID = fields[1]
	}
	return ControlEvent{Type: eventType, WindowID: windowID}
}

func parseSessionChanged(line string) ControlEvent {
	// "%session-changed $1 mysession"
	fields := strings.Fields(line)
	var name string
	if len(fields) >= 3 {
		name = strings.Join(fields[2:], " ")
	}
	return ControlEvent{Type: EventSessionChanged, Data: name}
}

func parsePaneModeChanged(line string) ControlEvent {
	// "%pane-mode-changed %3"
	fields := strings.Fields(line)
	var paneID string
	if len(fields) >= 2 {
		paneID = fields[1]
	}
	return ControlEvent{Type: EventPaneModeChanged, PaneID: paneID}
}
```

**Step 4: Run tests to verify they pass**

Run: `(cd tmux && go test -run TestParseControlLine -v)`
Expected: PASS

**Step 5: Commit**

```
git add tmux/control.go tmux/control_test.go
git commit -m "feat: add tmux control mode line parser"
```

---

### Task 3: ControlClient — Subprocess + Event Loop

**Files:**
- Modify: `tmux/control.go` (append)
- Create: `tmux/control_integration_test.go`

This adds the real subprocess management: spawning `tmux -CC attach`, reading stdout, writing stdin, and routing `%output` to per-pane subscriber channels.

**Step 1: Write integration test**

```go
// tmux/control_integration_test.go
package tmux

import (
	"testing"
	"time"
)

func TestControlClientIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	// Create a temp tmux session for testing
	client := NewClient("")
	session := "houston-test-ctrl-" + strconv.Itoa(os.Getpid())
	err := client.run("new-session", "-d", "-s", session, "-x", "80", "-y", "24")
	if err != nil {
		t.Fatalf("failed to create test session: %v", err)
	}
	defer client.run("kill-session", "-t", session)

	// Get the pane ID
	paneID, err := client.runOutput("display-message", "-t", session, "-p", "#{pane_id}")
	if err != nil {
		t.Fatalf("failed to get pane ID: %v", err)
	}
	paneID = strings.TrimSpace(paneID)

	// Connect control client
	cc := NewControlClient(session)
	if err := cc.Start(); err != nil {
		t.Fatalf("failed to start control client: %v", err)
	}
	defer cc.Close()

	// Subscribe to pane output
	ch := cc.Subscribe(paneID)
	defer cc.Unsubscribe(paneID, ch)

	// Send keys via control client
	if err := cc.SendKeys(paneID, "echo hello-control-mode"); err != nil {
		t.Fatalf("SendKeys failed: %v", err)
	}

	// Wait for output containing our text
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	var received string
	for {
		select {
		case data := <-ch:
			received += string(data)
			if strings.Contains(received, "hello-control-mode") {
				return // success
			}
		case <-timer.C:
			t.Fatalf("timeout waiting for output, received so far: %q", received)
		}
	}
}
```

**Step 2: Run test to verify it fails**

Run: `(cd tmux && go test -run TestControlClientIntegration -v -count=1)`
Expected: FAIL — `NewControlClient` not defined

**Step 3: Write implementation**

Append to `tmux/control.go`:

```go
import (
	"bufio"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"sync"
)

// ControlClient manages a tmux -CC connection to a session.
type ControlClient struct {
	session string
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	stdinMu sync.Mutex // serialize writes to stdin

	mu   sync.RWMutex
	subs map[string][]chan<- []byte // paneID → output subscribers

	// Synchronous command support
	cmdMu      sync.Mutex
	cmdCounter int
	pending    map[int]chan commandResponse

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
		pending: make(map[int]chan commandResponse),
		done:    make(chan struct{}),
	}
}

func (cc *ControlClient) Start() error {
	cc.cmd = exec.Command("tmux", "-CC", "attach-session", "-t", cc.session)
	stdout, err := cc.cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout pipe: %w", err)
	}
	stdin, err := cc.cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("stdin pipe: %w", err)
	}
	cc.stdin = stdin

	if err := cc.cmd.Start(); err != nil {
		return fmt.Errorf("start tmux -CC: %w", err)
	}

	go cc.readLoop(bufio.NewReader(stdout))
	return nil
}

func (cc *ControlClient) readLoop(r *bufio.Reader) {
	defer close(cc.done)

	var cmdBuf strings.Builder
	var cmdNumber int
	inBlock := false

	for {
		line, err := r.ReadString('\n')
		if err != nil {
			slog.Debug("control mode read loop ended", "session", cc.session, "error", err)
			return
		}
		line = strings.TrimRight(line, "\n")

		event := ParseControlLine(line)

		switch event.Type {
		case EventOutput:
			cc.dispatch(event.PaneID, []byte(event.Data))

		case EventBegin:
			inBlock = true
			cmdNumber = event.CmdNumber
			cmdBuf.Reset()

		case EventEnd:
			inBlock = false
			cc.completeCommand(cmdNumber, cmdBuf.String(), nil)

		case EventError:
			inBlock = false
			cc.completeCommand(cmdNumber, "", fmt.Errorf("tmux error: %s", cmdBuf.String()))

		case EventData:
			if inBlock {
				if cmdBuf.Len() > 0 {
					cmdBuf.WriteByte('\n')
				}
				cmdBuf.WriteString(event.Data)
			}

		default:
			// Notifications (window-add, session-changed, etc.) — log for now
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
			// Drop if subscriber is slow — they'll get the next one
		}
	}
}

func (cc *ControlClient) completeCommand(cmdNumber int, output string, err error) {
	cc.cmdMu.Lock()
	ch, ok := cc.pending[cmdNumber]
	if ok {
		delete(cc.pending, cmdNumber)
	}
	cc.cmdMu.Unlock()
	if ok {
		ch <- commandResponse{output: output, err: err}
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
		// Compare by channel identity via pointer
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
	// Escape single quotes in the text for tmux
	escaped := strings.ReplaceAll(text, "'", "'\\''")
	cmd := fmt.Sprintf("send-keys -t %s -l '%s'\n", paneID, escaped)
	cc.stdinMu.Lock()
	_, err := io.WriteString(cc.stdin, cmd)
	cc.stdinMu.Unlock()
	return err
}

// SetClientSize sets the control client dimensions, making houston
// participate in tmux's window-size latest policy.
func (cc *ControlClient) SetClientSize(cols, rows int) error {
	cmd := fmt.Sprintf("refresh-client -C %d,%d\n", cols, rows)
	cc.stdinMu.Lock()
	_, err := io.WriteString(cc.stdin, cmd)
	cc.stdinMu.Unlock()
	return err
}

// RunCommand sends a command and waits for its response.
func (cc *ControlClient) RunCommand(command string) (string, error) {
	cc.cmdMu.Lock()
	cc.cmdCounter++
	num := cc.cmdCounter
	ch := make(chan commandResponse, 1)
	cc.pending[num] = ch
	cc.cmdMu.Unlock()

	// Note: tmux assigns its own command numbers in %begin/%end.
	// We can't control them. Instead, we serialize commands (one at a time)
	// and the next %begin/%end pair corresponds to our command.
	// For simplicity, just use a mutex to ensure one command at a time.
	cc.stdinMu.Lock()
	_, err := io.WriteString(cc.stdin, command+"\n")
	cc.stdinMu.Unlock()
	if err != nil {
		return "", err
	}

	select {
	case resp := <-ch:
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
	// Empty line detaches control mode client
	_, _ = io.WriteString(cc.stdin, "\n")
	_ = cc.stdin.Close()
	cc.stdinMu.Unlock()
	return cc.cmd.Wait()
}
```

**Important note on RunCommand:** The tmux server assigns command numbers in `%begin`/`%end`, not the client. We need to track them differently. The simplest approach: since we serialize commands via `stdinMu`, the next `%begin`/`%end` after our write corresponds to our command. Adjust `readLoop` to route the response to the most recent pending command regardless of number.

Actually, let me simplify: use a single `pendingCmd chan commandResponse` instead of the map. One command at a time via the mutex.

**Step 4: Run integration test**

Run: `(cd tmux && go test -run TestControlClientIntegration -v -count=1)`
Expected: PASS (sends `echo hello-control-mode` to a temp session, receives output via subscriber)

**Step 5: Commit**

```
git add tmux/control.go tmux/control_integration_test.go
git commit -m "feat: add tmux control mode client with subscriber routing"
```

---

### Task 4: ControlManager — Multi-Session Lifecycle

**Files:**
- Create: `tmux/control_manager.go`
- Create: `tmux/control_manager_test.go`

The manager creates/reuses ControlClients per session. Ref-counted: when the last subscriber for a session disconnects, the control client is closed.

**Step 1: Write the failing test**

```go
// tmux/control_manager_test.go
package tmux

import (
	"testing"
)

func TestControlManagerGetClient(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	client := NewClient("")
	session := "houston-test-mgr-" + strconv.Itoa(os.Getpid())
	err := client.run("new-session", "-d", "-s", session, "-x", "80", "-y", "24")
	if err != nil {
		t.Fatalf("failed to create test session: %v", err)
	}
	defer client.run("kill-session", "-t", session)

	mgr := NewControlManager()
	defer mgr.Close()

	// First get creates a new client
	cc1, err := mgr.GetClient(session)
	if err != nil {
		t.Fatalf("GetClient failed: %v", err)
	}

	// Second get returns the same client
	cc2, err := mgr.GetClient(session)
	if err != nil {
		t.Fatalf("second GetClient failed: %v", err)
	}
	if cc1 != cc2 {
		t.Error("expected same client instance")
	}

	// Release twice (ref count drops to 0) — client should be closed
	mgr.ReleaseClient(session)
	mgr.ReleaseClient(session)

	// Next get creates a new client
	cc3, err := mgr.GetClient(session)
	if err != nil {
		t.Fatalf("third GetClient failed: %v", err)
	}
	if cc3 == cc1 {
		t.Error("expected new client after release")
	}
	mgr.ReleaseClient(session)
}
```

**Step 2: Run test to verify it fails**

Run: `(cd tmux && go test -run TestControlManagerGetClient -v -count=1)`
Expected: FAIL — `NewControlManager` not defined

**Step 3: Write implementation**

```go
// tmux/control_manager.go
package tmux

import (
	"log/slog"
	"sync"
)

type managedClient struct {
	client   *ControlClient
	refCount int
}

// ControlManager manages ControlClient instances across sessions.
// Creates on demand, ref-counted, cleans up when last reference released.
type ControlManager struct {
	mu      sync.Mutex
	clients map[string]*managedClient
}

func NewControlManager() *ControlManager {
	return &ControlManager{
		clients: make(map[string]*managedClient),
	}
}

// GetClient returns a ControlClient for the session, creating one if needed.
// Each call increments the ref count; call ReleaseClient when done.
func (m *ControlManager) GetClient(session string) (*ControlClient, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if mc, ok := m.clients[session]; ok {
		mc.refCount++
		return mc.client, nil
	}

	cc := NewControlClient(session)
	if err := cc.Start(); err != nil {
		return nil, err
	}

	m.clients[session] = &managedClient{client: cc, refCount: 1}
	slog.Info("started control client", "session", session)

	// Monitor for unexpected exit
	go func() {
		<-cc.Done()
		m.mu.Lock()
		delete(m.clients, session)
		m.mu.Unlock()
		slog.Info("control client exited", "session", session)
	}()

	return cc, nil
}

// ReleaseClient decrements the ref count and closes the client when it reaches 0.
func (m *ControlManager) ReleaseClient(session string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	mc, ok := m.clients[session]
	if !ok {
		return
	}

	mc.refCount--
	if mc.refCount <= 0 {
		delete(m.clients, session)
		go mc.client.Close()
		slog.Info("closed control client (no more subscribers)", "session", session)
	}
}

// Close shuts down all control clients.
func (m *ControlManager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for session, mc := range m.clients {
		_ = mc.client.Close()
		delete(m.clients, session)
		slog.Info("closed control client (shutdown)", "session", session)
	}
}
```

**Step 4: Run test**

Run: `(cd tmux && go test -run TestControlManagerGetClient -v -count=1)`
Expected: PASS

**Step 5: Commit**

```
git add tmux/control_manager.go tmux/control_manager_test.go
git commit -m "feat: add ControlManager for multi-session lifecycle"
```

---

### Task 5: Pane ID Lookup

**Files:**
- Modify: `tmux/client.go` (add `GetPaneID` method)

Control mode uses pane IDs (`%42`) but houston uses `session:window.index` targets. We need a lookup.

**Step 1: Add method to tmux.Client**

```go
// GetPaneID returns the tmux pane ID (e.g. "%42") for a given pane.
func (c *Client) GetPaneID(p Pane) (string, error) {
	output, err := c.runOutput("display-message", "-t", p.Target(), "-p", "#{pane_id}")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(output), nil
}
```

**Step 2: Test manually**

Run: `tmux display-message -t houston:1.1 -p '#{pane_id}'`
Expected: `%3` (or similar)

**Step 3: Commit**

```
git add tmux/client.go
git commit -m "feat: add GetPaneID for control mode pane ID lookup"
```

---

### Task 6: Rewrite pane_ws.go — Server-Side Integration

**Files:**
- Modify: `server/server.go` (add `controlMgr` field)
- Modify: `server/pane_ws.go` (rewrite `handlePaneWS`, `paneWSWriteLoop`, `paneWSReadLoop`)

This is the big one. The WS handler switches from capture-pane polling to control mode streaming.

**Step 1: Add ControlManager to Server**

In `server/server.go`, add field to `Server` struct:

```go
type Server struct {
	tmux         *tmux.Client
	controlMgr   *tmux.ControlManager
	// ... rest unchanged
}
```

Initialize in constructor:

```go
s.controlMgr = tmux.NewControlManager()
```

Add cleanup in server shutdown (if there's a Close method, add `s.controlMgr.Close()`).

**Step 2: Rewrite handlePaneWS**

Replace the entire function in `server/pane_ws.go`:

```go
func (s *Server) handlePaneWS(w http.ResponseWriter, r *http.Request, pane tmux.Pane) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Error("websocket upgrade failed", "error", err)
		return
	}

	// Look up tmux pane ID (%N format) for control mode routing
	paneID, err := s.tmux.GetPaneID(pane)
	if err != nil {
		slog.Error("failed to get pane ID", "target", pane.Target(), "error", err)
		_ = conn.Close()
		return
	}

	// Get or create control client for this session
	cc, err := s.controlMgr.GetClient(pane.Session)
	if err != nil {
		slog.Error("failed to get control client", "session", pane.Session, "error", err)
		_ = conn.Close()
		return
	}

	slog.Info("pane websocket connected (control mode)", "target", pane.Target(), "paneID", paneID)

	defer func() {
		_ = conn.Close()
		s.controlMgr.ReleaseClient(pane.Session)
	}()

	// Subscribe to pane output FIRST (buffers events during seed)
	outputCh := cc.Subscribe(paneID)
	defer cc.Unsubscribe(paneID, outputCh)

	// Keepalive
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(45 * time.Second))
	})
	_ = conn.SetReadDeadline(time.Now().Add(45 * time.Second))

	// Seed: capture current pane state and send as initial snapshot
	seedOutput, err := s.tmux.CapturePaneWithMode(pane, 500)
	if err == nil && seedOutput.Output != "" {
		outputJSON, _ := json.Marshal(WSOutput{Data: seedOutput.Output})
		msg, _ := json.Marshal(WSMessage{Type: "seed", Data: outputJSON})
		if err := conn.WriteMessage(websocket.TextMessage, msg); err != nil {
			return
		}
	}

	// Drain any buffered output that arrived during seed
	drainLoop:
	for {
		select {
		case data := <-outputCh:
			outputJSON, _ := json.Marshal(WSOutput{Data: string(data)})
			msg, _ := json.Marshal(WSMessage{Type: "output", Data: outputJSON})
			if err := conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				return
			}
		default:
			break drainLoop
		}
	}

	go s.paneWSReadLoop(conn, cc, pane, paneID)
	s.paneWSWriteLoop(conn, cc, pane, paneID, outputCh)
}
```

**Step 3: Rewrite paneWSWriteLoop**

```go
func (s *Server) paneWSWriteLoop(conn *websocket.Conn, cc *tmux.ControlClient, pane tmux.Pane, paneID string, outputCh <-chan []byte) {
	metaTicker := time.NewTicker(1 * time.Second)
	defer metaTicker.Stop()

	pingTicker := time.NewTicker(30 * time.Second)
	defer pingTicker.Stop()

	var lastMeta WSMeta

	// Get initial pane info for agent detection
	panes, _ := s.tmux.ListPanes(pane.Session, pane.Window)
	var panePath, paneCommand string
	for _, p := range panes {
		if p.Index == pane.Index {
			panePath = p.Path
			paneCommand = p.Command
			break
		}
	}
	var windowName string
	if windows, err := s.tmux.ListWindows(pane.Session); err == nil {
		for _, w := range windows {
			if w.Index == pane.Window {
				windowName = w.Name
				break
			}
		}
	}

	for {
		select {
		case data, ok := <-outputCh:
			if !ok {
				return
			}
			outputJSON, _ := json.Marshal(WSOutput{Data: string(data)})
			msg, _ := json.Marshal(WSMessage{Type: "output", Data: outputJSON})
			if err := conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				return
			}

		case <-metaTicker.C:
			// Agent detection via capture-pane (side channel)
			capture, err := s.tmux.CapturePaneWithMode(pane, 500)
			if err != nil {
				continue
			}
			agent := s.registry.Detect(pane.Target(), paneCommand, capture.Output)
			parseResult := getAgentState(agent, panePath, capture.Output)

			meta := WSMeta{
				Agent:      agent.Type(),
				Mode:       modeToString(parseResult.Mode),
				Activity:   parseResult.Activity,
				WindowName: windowName,
			}
			if len(parseResult.Choices) > 0 {
				meta.Choices = parseResult.Choices
			}
			statusLine := agent.ExtractStatusLine(capture.Output)
			if statusLine != "" {
				meta.StatusLine = statusLine
			}
			if agent.Type() == agents.AgentClaudeCode {
				meta.Suggestion = claude.ExtractSuggestion(capture.Output)
			}
			meta.Status = resultTypeToString(parseResult.Type)

			if !metaEqual(meta, lastMeta) {
				lastMeta = meta
				metaJSON, _ := json.Marshal(meta)
				msg, _ := json.Marshal(WSMessage{Type: "meta", Data: metaJSON})
				if err := conn.WriteMessage(websocket.TextMessage, msg); err != nil {
					return
				}
			}

		case <-pingTicker.C:
			if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}

		case <-cc.Done():
			return
		}
	}
}
```

**Step 4: Rewrite paneWSReadLoop**

```go
func (s *Server) paneWSReadLoop(conn *websocket.Conn, cc *tmux.ControlClient, pane tmux.Pane, paneID string) {
	defer func() { _ = conn.Close() }()

	for {
		_, msgBytes, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				slog.Debug("websocket read error", "error", err)
			}
			return
		}

		var msg WSMessage
		if err := json.Unmarshal(msgBytes, &msg); err != nil {
			slog.Debug("websocket unmarshal error", "error", err)
			continue
		}

		switch msg.Type {
		case "input":
			var input WSInput
			if err := json.Unmarshal(msg.Data, &input); err != nil {
				continue
			}
			if err := cc.SendKeys(paneID, input.Data); err != nil {
				slog.Error("send keys failed", "error", err)
			}

		case "resize":
			var resize WSResize
			if err := json.Unmarshal(msg.Data, &resize); err != nil {
				continue
			}
			if resize.Cols > 0 && resize.Rows > 0 {
				if err := cc.SetClientSize(resize.Cols, resize.Rows); err != nil {
					slog.Debug("set client size failed", "error", err)
				}
			}
		}
	}
}
```

**Step 5: Build and verify**

Run: `go build ./...`
Expected: No errors

**Step 6: Commit**

```
git add server/server.go server/pane_ws.go
git commit -m "feat: rewrite pane WS to use control mode for I/O"
```

---

### Task 7: Frontend — Incremental Output Mode

**Files:**
- Modify: `ui/src/components/TerminalPane.tsx`

Switch from clear-and-rewrite snapshots to incremental `term.write()`.

**Step 1: Update onOutput callback and writeSnapshot logic**

The key change: the first message (type `"seed"`) uses `writeSnapshot()`. All subsequent `"output"` messages use incremental `term.write()`.

In `usePaneSocket.ts`, add support for the `"seed"` message type:

```tsx
// In ws.onmessage handler, add:
case 'seed': {
  const output = msg.data as WSOutput
  callbacksRef.current.onSeed(output.data)
  break
}
```

Update `PaneSocketCallbacks`:
```tsx
interface PaneSocketCallbacks {
  onSeed: (data: string) => void    // full snapshot for initial state
  onOutput: (data: string) => void  // incremental streaming
  onMeta: (meta: WSMeta) => void
}
```

In `TerminalPane.tsx`, simplify the output handling:

```tsx
const { connected, sendInput, sendResize } = usePaneSocket(pane.target, {
  onSeed: (data) => {
    const term = termRef.current
    if (!term) return
    writeSnapshot(term, data)
  },
  onOutput: (data) => {
    const term = termRef.current
    if (!term) return
    term.write(data)
  },
  onMeta: (m) => setMeta(m),
})
```

Remove:
- `pendingOutputRef`, `rafRef`, `deferredOutputRef`, `lastOutputRef`, `writingRef`
- `scheduleFlush()` function
- RAF coalescing logic
- Deferred output when scrolled up
- Viewport scroll listener for deferred output

Keep:
- `writeSnapshot()` function (used for seed)
- Desktop focus logic
- Mobile wide mode logic
- Theme sync
- Resize observer

**Step 2: Build and verify**

Run: `(cd ui && npx tsc -b && npx vite build)`
Expected: No errors

**Step 3: Commit**

```
git add ui/src/components/TerminalPane.tsx ui/src/hooks/usePaneSocket.ts
git commit -m "feat: switch frontend to incremental output from control mode"
```

---

### Task 8: Manual Testing + Cleanup

**Files:**
- Possibly modify: `tmux/control.go` (bug fixes)
- Possibly modify: `server/pane_ws.go` (bug fixes)

**Step 1: Start houston and test**

Run: `just dev` (or `go run . --dev`)

Test checklist:
- [ ] Open a pane in browser — see terminal output
- [ ] Type in terminal — characters appear in tmux
- [ ] Agent detection shows correct icon and status
- [ ] Resize button works (desktop)
- [ ] Mobile wide mode works
- [ ] Multiple browser tabs viewing same pane
- [ ] Switch between panes
- [ ] Close pane — no crashes

**Step 2: Remove dead code**

Remove from `server/pane_ws.go`:
- `origW, origH, sizeErr` — window size save/restore (control mode handles this)
- `didResize` atomic — no longer needed
- `nudge` channel — input goes directly via control mode

Remove from `tmux/client.go` (only if nothing else uses them):
- Check if `SendKeys` is still used elsewhere (yes — `handlePaneSend` in api.go uses it)
- Keep `SendKeys` in client.go for the REST API endpoint

**Step 3: Commit cleanup**

```
git add -A
git commit -m "chore: remove dead code from pre-control-mode architecture"
```

---

## Execution Notes

- Tasks 1-2 are pure, testable units with no side effects
- Task 3 needs a running tmux server (integration test)
- Task 4 builds on 3
- Task 5 is a small utility addition
- Task 6 is the core integration — depends on 1-5
- Task 7 is the frontend counterpart to 6
- Task 8 is manual verification and cleanup

The biggest risk is in Task 6 (server integration) and the send-keys escaping in Task 3. If control mode's `send-keys -l` doesn't handle certain characters well, we may need to adjust the escaping.
