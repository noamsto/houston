# Houston Architecture v2: tmux Control Mode + Structured Parsing

**Date:** 2026-01-03
**Status:** Design
**Replaces:** Terminal scraping approach

## Executive Summary

Pivot from ACP integration to a **three-layer monitoring architecture**:

1. **tmux control mode** - Structured terminal events (replaces `capture-pane` polling)
2. **Message-based parsing** - Leverage `>` and `•` markers for conversation structure
3. **OpenTelemetry** - Optional structured events from Claude Code

This approach provides 90% of ACP benefits (structured data, real-time events) without custom protocol work, and works with existing tmux sessions.

---

## Key Decisions

| Decision | Choice | Rationale |
|----------|--------|-----------|
| **Control mode connections** | One global connection for all sessions/panes | Simpler than per-session, lower overhead, event routing handled in server |
| **Tool vs Claude text detection** | Check known tool names + tree characters on next line | Both use `●` marker, must distinguish by content and structure |
| **Message deduplication** | Track seen line indices in parser | Prevents duplicate messages as buffer grows incrementally |
| **Event routing** | Global channel → event router → per-pane parsers | One connection, many parsers, router dispatches by pane ID |
| **Parser state** | Per-pane MessageParser with conversation history | Each pane maintains independent conversation state |
| **WebSocket updates** | Pub/sub pattern with pane-specific channels | Efficient, only notifies subscribers when state changes |
| **Initial state** | Send list commands on connect, parse %begin/%end blocks | Get initial sessions/windows/panes before subscribing to events |
| **Tree character location** | `⎿` appears ONE LINE BELOW tool name line | Tool call detection looks ahead one line |
| **ANSI color codes** | Strip before pattern matching | Claude output uses green `●`, must remove escape codes to match patterns |

---

## Architecture Overview

```
┌─────────────────────────────────────────────────┐
│               Mobile Browser                     │
│            (htmx + WebSocket)                    │
└──────────────────┬──────────────────────────────┘
                   │ WebSocket
┌──────────────────▼──────────────────────────────┐
│            Houston Go Server                     │
│                                                  │
│  ┌──────────────────────────────────────────┐  │
│  │ Layer 1: tmux Control Mode Client        │  │
│  │ ────────────────────────────────────────│  │
│  │ • Persistent connection to tmux          │  │
│  │ • Real-time %output events               │  │
│  │ • %window-add, %pane-mode-changed        │  │
│  │ • No polling, no visual parsing          │  │
│  └──────────────────────────────────────────┘  │
│                   ↓                              │
│  ┌──────────────────────────────────────────┐  │
│  │ Layer 2: Message Parser                  │  │
│  │ ────────────────────────────────────────│  │
│  │ • Detects message boundaries (>, •)      │  │
│  │ • Extracts conversation structure        │  │
│  │ • Identifies tool calls (●)              │  │
│  │ • State machine for agent activity       │  │
│  └──────────────────────────────────────────┘  │
│                   ↓                              │
│  ┌──────────────────────────────────────────┐  │
│  │ Layer 3: Enhanced Context (Optional)     │  │
│  │ ────────────────────────────────────────│  │
│  │ • Hook files (inotify watch)             │  │
│  │ • OpenTelemetry events (gRPC)            │  │
│  │ • Agent-specific metadata                │  │
│  └──────────────────────────────────────────┘  │
└──────────────────────────────────────────────────┘
```

---

## Layer 1: tmux Control Mode

### What It Replaces

**Current approach** (`tmux/client.go`):
```go
// Poll every 2 seconds
cmd := exec.Command("tmux", "capture-pane", "-t", pane, "-p", "-S", "-100")
output, _ := cmd.CombinedOutput()
// Parse visual output with regex
```

**New approach**:
```go
// Single persistent connection
conn := NewControlModeClient()
conn.Connect("-CC", "attach-session")

// Subscribe to events
for event := range conn.Events() {
    switch event.Type {
    case OutputEvent:
        // Real-time pane output
        handleOutput(event.PaneID, event.Content)
    case WindowAddEvent:
        // New window created
        refreshSessions()
    case PaneModeChangedEvent:
        // Pane state changed
        updatePaneStatus(event.PaneID)
    }
}
```

### Control Mode Protocol

tmux control mode uses a text-based protocol:

**Structure:**
```
%begin <timestamp> <message-number> <flags>
<command output>
%end <timestamp> <message-number> <flags>
```

**Event notifications:**
```
%output %<pane_id> <escaped_content>
%window-add @<window_id>
%pane-mode-changed %<pane_id>
%session-changed $<session_id>
```

**Key features:**
- **Structured responses**: Every command wrapped in `%begin`/`%end`
- **Asynchronous events**: Real-time notifications prefixed with `%`
- **Flow control**: Built-in buffering prevents overflow
- **Escaping**: Characters <32 and backslashes as octal (`\nnn`)

### Implementation: `tmux/control_mode.go`

**Connection Strategy:**
- **One global control mode connection** - Monitors ALL sessions/windows/panes
- Event stream includes pane IDs - we route to appropriate parsers
- Simpler than per-session connections, less overhead

**Initial State:**
- On connect, send `list-sessions`, `list-windows`, `list-panes` commands
- Parse `%begin`/`%end` blocks for initial state
- Then subscribe to real-time `%output` events

```go
package tmux

import (
    "bufio"
    "fmt"
    "io"
    "os/exec"
    "regexp"
    "strconv"
    "strings"
)

type EventType int

const (
    OutputEvent EventType = iota
    WindowAddEvent
    WindowCloseEvent
    PaneModeChangedEvent
    SessionChangedEvent
)

type Event struct {
    Type    EventType
    PaneID  string
    Content string
    Data    map[string]string
}

type ControlModeClient struct {
    cmd    *exec.Cmd
    stdout *bufio.Scanner
    stdin  io.WriteCloser
    events chan Event
}

func NewControlModeClient() *ControlModeClient {
    return &ControlModeClient{
        events: make(chan Event, 100),
    }
}

func (c *ControlModeClient) Connect(args ...string) error {
    fullArgs := append([]string{"-CC"}, args...)
    c.cmd = exec.Command("tmux", fullArgs...)

    stdout, err := c.cmd.StdoutPipe()
    if err != nil {
        return err
    }

    stdin, err := c.cmd.StdinPipe()
    if err != nil {
        return err
    }

    c.stdout = bufio.NewScanner(stdout)
    c.stdin = stdin

    if err := c.cmd.Start(); err != nil {
        return err
    }

    go c.readLoop()
    return nil
}

func (c *ControlModeClient) readLoop() {
    outputPattern := regexp.MustCompile(`^%output %(\d+) (.*)$`)
    windowAddPattern := regexp.MustCompile(`^%window-add @(\d+)$`)
    paneModePattern := regexp.MustCompile(`^%pane-mode-changed %(\d+)$`)

    for c.stdout.Scan() {
        line := c.stdout.Text()

        // Parse event notifications
        if matches := outputPattern.FindStringSubmatch(line); matches != nil {
            content := unescapeOctal(matches[2])
            c.events <- Event{
                Type:    OutputEvent,
                PaneID:  matches[1],
                Content: content,
            }
        } else if matches := windowAddPattern.FindStringSubmatch(line); matches != nil {
            c.events <- Event{
                Type: WindowAddEvent,
                Data: map[string]string{"window_id": matches[1]},
            }
        } else if matches := paneModePattern.FindStringSubmatch(line); matches != nil {
            c.events <- Event{
                Type:   PaneModeChangedEvent,
                PaneID: matches[1],
            }
        }
        // Handle %begin/%end blocks for command responses
        // ... (omitted for brevity)
    }
}

func (c *ControlModeClient) Events() <-chan Event {
    return c.events
}

func (c *ControlModeClient) SendKeys(target, keys string) error {
    cmd := fmt.Sprintf("send-keys -t %s %s\n", target, keys)
    _, err := c.stdin.Write([]byte(cmd))
    return err
}

func unescapeOctal(s string) string {
    // Unescape \nnn octal sequences
    re := regexp.MustCompile(`\\(\d{3})`)
    return re.ReplaceAllStringFunc(s, func(match string) string {
        code, _ := strconv.ParseInt(match[1:], 8, 32)
        return string(rune(code))
    })
}
```

---

## Layer 2: Message-Based Parsing

### The Key Observation

Claude Code output has **consistent message markers**:

```
> User input line
  Continuation lines are indented

● Claude response text
  Multiple lines with consistent indentation

● ToolName(parameters)          ← Same ● character!
  Tool description
  ⎿ Tool output line 1           ← Tree characters distinguish tool output
  ├ Tool output line 2
  └ Tool output line 3

✻ Thinking... (spinner with activity)
```

**Message markers:**
- `>` (U+003E) - User input
- `●` (U+25CF BLACK CIRCLE) - Both Claude text AND tool calls (same character!)
  - **Color:** GREEN (ANSI escape codes present in output)
  - **Distinguish by:** Tool calls followed by tool name (Read, Write, Bash, etc.)
  - **Distinguish by:** Tool output has `⎿` on the NEXT line (one line below tool name)
  - **Distinguish by:** Claude text is conversational, no tool patterns, NO tree characters
- `⎿├└│` - Tool output tree characters (ONLY used for tool output, NOT Claude responses)
- `✻` (U+273B TEARDROP-SPOKED ASTERISK) - Activity spinner (likely animates/blinks)

**ANSI Color Handling:**
- Output contains ANSI escape codes (e.g., `\033[32m●\033[0m` for green)
- **Keep colors in raw output** for frontend display (using ansi_up.js)
- **Strip colors temporarily** only during pattern matching in parser
- Store both raw (colored) and clean (stripped) versions
- tmux control mode may escape these further as octal sequences

**Examples:**

Tool call:
```
● Read(file.go)       ← Tool call line with tool name
⎿ 1→package main      ← Output starts next line with ⎿
├ 2→import "fmt"
└ 3→func main() {}
```

Claude response:
```
● I'll help you with that.    ← No tool name, no tree chars
  Let me explain the code...   ← Continuation lines indented
```

### Message Structure

```go
type MessageType int

const (
    UserMessage MessageType = iota
    ClaudeMessage
    ToolCall
    ToolOutput
    Activity
)

type Message struct {
    Type      MessageType
    Content   string            // Clean content (colors stripped for matching)
    RawContent string           // Original with ANSI colors (for display)
    Timestamp time.Time
    Metadata  map[string]string // tool name, activity, etc.
}

type ConversationState struct {
    Messages      []Message
    CurrentState  StateType // Idle, Thinking, WaitingForInput, etc.
    LastActivity  string
    PendingInput  string
}
```

### Parser Implementation: `parser/message_parser.go`

```go
package parser

import (
    "regexp"
    "strings"
    "time"
)

var (
    userPrefix    = ">"
    claudePrefix  = "●"  // Same for Claude text and tool calls (green in terminal)
    toolPrefix    = "●"
    toolOutputPrefix = []string{"⎿", "├", "└", "│"}
    spinnerChar   = '✻'  // Single character, may blink/animate

    // Known tool names to distinguish tool calls from Claude text
    knownTools = []string{"Read", "Write", "Edit", "Bash", "Grep", "Glob", "Task",
                          "NotebookEdit", "WebFetch", "WebSearch", "AskUserQuestion"}

    // ANSI color code regex for stripping
    ansiColorRegex = regexp.MustCompile(`\x1b\[[0-9;]*m`)
)

// stripColors removes ANSI escape codes from a line
func stripColors(s string) string {
    return ansiColorRegex.ReplaceAllString(s, "")
}

type MessageParser struct {
    buffer       []string  // Raw output with ANSI colors (for display)
    state        ConversationState
    seenMessages map[int]bool  // Track which lines we've processed to avoid duplicates
}

func NewMessageParser() *MessageParser {
    return &MessageParser{
        state:        ConversationState{},
        seenMessages: make(map[int]bool),
    }
}

// ProcessLine processes a single line from tmux %output event
func (p *MessageParser) ProcessLine(line string) {
    // Store raw line with colors for display
    p.buffer = append(p.buffer, line)

    // Keep last 100 lines in buffer
    if len(p.buffer) > 100 {
        p.buffer = p.buffer[1:]
        // Clear old seen messages (they're out of buffer)
        p.seenMessages = make(map[int]bool)
    }

    // Detect message boundaries and extract messages
    p.detectMessages()
}

func (p *MessageParser) detectMessages() {
    // Scan forward through buffer (oldest to newest)
    for i := 0; i < len(p.buffer); i++ {
        // Skip if we've already processed this line
        if p.seenMessages[i] {
            continue
        }

        // Strip colors for pattern matching, but keep raw in buffer
        line := strings.TrimSpace(stripColors(p.buffer[i]))

        // User message
        if strings.HasPrefix(line, userPrefix) {
            msg := p.extractMessage(i, userPrefix)
            if msg != nil {
                p.state.Messages = append(p.state.Messages, *msg)
                p.state.CurrentState = StateWaitingForClaude
                p.seenMessages[i] = true
            }
        }

        // ● prefix - could be Claude text OR tool call
        if strings.HasPrefix(line, claudePrefix) {
            // Check if it's a tool call
            if p.isToolCall(i) {
                msg := p.extractToolCall(i)
                if msg != nil {
                    p.state.Messages = append(p.state.Messages, *msg)
                    p.state.CurrentState = StateRunningTool
                    p.seenMessages[i] = true
                }
            } else {
                // It's Claude text
                msg := p.extractMessage(i, claudePrefix)
                if msg != nil {
                    p.state.Messages = append(p.state.Messages, *msg)
                    p.state.CurrentState = StateResponding
                    p.seenMessages[i] = true
                }
            }
        }

        // Activity (spinner)
        if activity := p.detectActivity(line); activity != "" {
            p.state.LastActivity = activity
            p.state.CurrentState = StateThinking
        }
    }
}

// isToolCall checks if a ● line is a tool call (vs Claude text)
func (p *MessageParser) isToolCall(lineIdx int) bool {
    line := p.buffer[lineIdx]
    content := strings.TrimPrefix(strings.TrimSpace(line), toolPrefix)
    content = strings.TrimSpace(content)

    // Check if starts with known tool name
    for _, tool := range knownTools {
        if strings.HasPrefix(content, tool) {
            // Verify it's followed by ( or : or space
            if len(content) > len(tool) {
                nextChar := content[len(tool)]
                if nextChar == '(' || nextChar == ':' || nextChar == ' ' {
                    return true
                }
            }
        }
    }

    // Check if next lines have tool output markers
    if lineIdx+1 < len(p.buffer) {
        nextLine := strings.TrimSpace(p.buffer[lineIdx+1])
        for _, prefix := range toolOutputPrefix {
            if strings.HasPrefix(nextLine, prefix) {
                return true
            }
        }
    }

    return false
}

// extractMessage extracts a multi-line message starting at index i
func (p *MessageParser) extractMessage(startIdx int, prefix string) *Message {
    var cleanLines []string
    var rawLines []string

    // First line (with prefix) - strip for clean, keep for raw
    cleanLine := stripColors(p.buffer[startIdx])
    firstClean := strings.TrimPrefix(cleanLine, prefix)
    cleanLines = append(cleanLines, strings.TrimSpace(firstClean))

    firstRaw := strings.TrimPrefix(p.buffer[startIdx], prefix)
    rawLines = append(rawLines, strings.TrimSpace(firstRaw))

    // Continuation lines (indented)
    for i := startIdx + 1; i < len(p.buffer); i++ {
        rawLine := p.buffer[i]
        cleanLine := stripColors(rawLine)

        // Stop at next message boundary (check clean version)
        if strings.HasPrefix(cleanLine, userPrefix) ||
           strings.HasPrefix(cleanLine, claudePrefix) ||
           strings.HasPrefix(cleanLine, toolPrefix) {
            break
        }

        // Empty line might be end of message
        if strings.TrimSpace(cleanLine) == "" {
            break
        }

        // Check if indented (continuation line)
        if len(cleanLine) > 0 && (cleanLine[0] == ' ' || cleanLine[0] == '\t') {
            cleanLines = append(cleanLines, strings.TrimSpace(cleanLine))
            rawLines = append(rawLines, strings.TrimSpace(rawLine))
        } else {
            break
        }
    }

    if len(cleanLines) == 0 {
        return nil
    }

    msgType := UserMessage
    if prefix == claudePrefix {
        msgType = ClaudeMessage
    }

    return &Message{
        Type:       msgType,
        Content:    strings.Join(cleanLines, " "),
        RawContent: strings.Join(rawLines, " "),
        Timestamp:  time.Now(),
    }
}

// extractToolCall extracts tool name and output
func (p *MessageParser) extractToolCall(startIdx int) *Message {
    rawLine := p.buffer[startIdx]
    cleanLine := stripColors(rawLine)

    // Extract tool name: "● Read" or "● Bash(command)"
    toolLine := strings.TrimPrefix(cleanLine, toolPrefix)
    toolName := strings.TrimSpace(strings.Split(toolLine, "(")[0])

    // Collect tool output lines (both raw and clean)
    var cleanOutputLines []string
    var rawOutputLines []string

    for i := startIdx + 1; i < len(p.buffer); i++ {
        rawLine := p.buffer[i]
        cleanLine := stripColors(rawLine)

        // Check if tool output line
        isToolOutput := false
        for _, prefix := range toolOutputPrefix {
            if strings.HasPrefix(strings.TrimSpace(cleanLine), prefix) {
                isToolOutput = true
                break
            }
        }

        if !isToolOutput {
            break
        }

        // Remove tree characters and trim (for clean version)
        cleaned := strings.TrimSpace(cleanLine)
        for _, prefix := range toolOutputPrefix {
            cleaned = strings.TrimPrefix(cleaned, prefix)
        }
        cleanOutputLines = append(cleanOutputLines, strings.TrimSpace(cleaned))

        // Keep raw with tree characters
        rawOutputLines = append(rawOutputLines, strings.TrimSpace(rawLine))
    }

    return &Message{
        Type:       ToolCall,
        Content:    strings.Join(cleanOutputLines, "\n"),
        RawContent: strings.Join(rawOutputLines, "\n"),
        Metadata: map[string]string{
            "tool": toolName,
        },
        Timestamp: time.Now(),
    }
}

func (p *MessageParser) detectActivity(line string) string {
    // Check for spinner + activity
    if strings.ContainsRune(line, spinnerChar) {
        // Extract activity text after spinner: "✻ Thinking..."
        parts := strings.SplitN(line, string(spinnerChar), 2)
        if len(parts) == 2 {
            activity := strings.TrimSpace(parts[1])
            // Remove ellipsis
            activity = strings.TrimSuffix(activity, "...")
            activity = strings.TrimSuffix(activity, "…")
            return activity
        }
    }
    return ""
}

func (p *MessageParser) GetState() ConversationState {
    return p.state
}
```

### State Machine

```go
type StateType int

const (
    StateIdle StateType = iota
    StateWaitingForClaude
    StateThinking
    StateResponding
    StateRunningTool
    StateWaitingForInput
    StateError
)

// State transitions:
// Idle → (user message) → WaitingForClaude
// WaitingForClaude → (spinner) → Thinking
// Thinking → (claude message) → Responding
// Responding → (tool call) → RunningTool
// RunningTool → (tool output done) → Responding
// Responding → (question?) → WaitingForInput
// * → (error) → Error
```

---

## Layer 3: Enhanced Context (Optional)

### Hook Files + inotify

**Current:** Polling status directory every N seconds
**New:** Watch with `fsnotify`

```go
// status/watcher.go - Enhanced
import (
    "log"
    "github.com/fsnotify/fsnotify"
)

func (w *Watcher) Start() error {
    watcher, err := fsnotify.NewWatcher()
    if err != nil {
        return err
    }

    if err := watcher.Add(w.statusDir); err != nil {
        return err
    }

    go func() {
        for {
            select {
            case event := <-watcher.Events:
                if event.Op&fsnotify.Write == fsnotify.Write {
                    // File changed, reload immediately
                    w.loadStatus(event.Name)
                    w.notifyListeners()
                }
            case err := <-watcher.Errors:
                log.Printf("watcher error: %v", err)
            }
        }
    }()

    return nil
}
```

### OpenTelemetry Integration

**Phase 1: Collector Mode**
```bash
# Run OTel collector
docker run -p 4317:4317 otel/opentelemetry-collector

# Configure Claude Code
export CLAUDE_CODE_ENABLE_TELEMETRY=1
export OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4317
```

**Phase 2: Houston queries collector**
```go
// telemetry/client.go
type OTelClient struct {
    collectorURL string
}

func (c *OTelClient) GetRecentEvents(sessionID string) ([]Event, error) {
    // Query Prometheus/Loki for events
    // Filter by session_id label
}
```

---

## Integration: How Layers Work Together

### Event Routing Architecture

**Problem:** Control mode has ONE global event channel, but we need per-pane parsers.

**Solution:** Event router in server

```go
// server/server.go
type Server struct {
    tmux           *tmux.ControlModeClient
    parsers        map[string]*parser.MessageParser  // paneID -> parser
    parsersMutex   sync.RWMutex
    statusWatcher  *status.Watcher
    otelClient     *telemetry.OTelClient
}

func (s *Server) Start() error {
    // Connect to tmux control mode
    s.tmux = tmux.NewControlModeClient()
    if err := s.tmux.Connect("attach-session"); err != nil {
        return err
    }

    // Start event router
    go s.routeEvents()

    return nil
}

func (s *Server) routeEvents() {
    for event := range s.tmux.Events() {
        switch event.Type {
        case tmux.OutputEvent:
            // Route to pane-specific parser
            parser := s.getOrCreateParser(event.PaneID)
            parser.ProcessLine(event.Content)

        case tmux.WindowAddEvent, tmux.WindowCloseEvent:
            // Refresh session list
            s.refreshSessions()

        case tmux.PaneModeChangedEvent:
            // Update pane status
            s.updatePaneStatus(event.PaneID)
        }
    }
}

func (s *Server) getOrCreateParser(paneID string) *parser.MessageParser {
    s.parsersMutex.Lock()
    defer s.parsersMutex.Unlock()

    if p, exists := s.parsers[paneID]; exists {
        return p
    }

    // Create new parser for this pane
    p := parser.NewMessageParser()
    s.parsers[paneID] = p
    return p
}
```

### Building Dashboard Data

```go
// server/server.go - Updated buildSessionsData()

func (s *Server) buildSessionsData() views.SessionsData {
    sessions := s.tmux.ListSessions() // Still works, but now from control mode

    for _, session := range sessions {
        for _, window := range session.Windows {
            // Get parser for this pane (maintains state)
            s.parsersMutex.RLock()
            parser := s.parsers[window.Pane.ID]
            s.parsersMutex.RUnlock()

            var state parser.ConversationState
            if parser != nil {
                state = parser.GetState()
            }

            // Optional: Enhance with hook file data
            hookStatus := s.statusWatcher.GetStatus(window.Pane.ID)

            // Optional: Enhance with OTel events
            otelEvents := s.otelClient.GetRecentEvents(session.Name)

            // Build UI data
            windowData := views.WindowData{
                State:          state.CurrentState.String(),
                Activity:       state.LastActivity,
                RecentMessages: state.Messages[len(state.Messages)-5:],
                HookStatus:     hookStatus,
                // ...
            }
        }
    }
}
```

### Real-time WebSocket Updates

```go
// WebSocket handler - sends updates when pane state changes
func (s *Server) handleWebSocket(w http.ResponseWriter, r *http.Request) {
    paneID := r.URL.Query().Get("pane")
    conn, _ := upgrader.Upgrade(w, r, nil)

    // Create subscription channel for this pane
    updates := make(chan parser.ConversationState, 10)

    // Register subscriber (notify when this pane's state changes)
    s.subscribeToPane(paneID, updates)
    defer s.unsubscribeFromPane(paneID, updates)

    // Send updates to browser
    for state := range updates {
        if err := conn.WriteJSON(state); err != nil {
            break
        }
    }
}

// Called by routeEvents when a pane's parser processes new data
func (s *Server) notifyPaneSubscribers(paneID string, state parser.ConversationState) {
    s.subscribersMutex.RLock()
    defer s.subscribersMutex.RUnlock()

    for _, ch := range s.paneSubscribers[paneID] {
        select {
        case ch <- state:
        default:
            // Skip if channel is full (slow client)
        }
    }
}
```

---

## Migration Plan

### Phase 1: tmux Control Mode (Week 1)
- [ ] Implement `ControlModeClient` in `tmux/control_mode.go`
- [ ] Parse `%output`, `%window-add`, `%pane-mode-changed` events
- [ ] Replace polling in `server.go` with event subscription
- [ ] Test with existing UI

### Phase 2: Message Parser (Week 2)
- [ ] Implement `MessageParser` in `parser/message_parser.go`
- [ ] Extract user/Claude/tool messages using `>`, `•`, `●` markers
- [ ] Build state machine for agent activity
- [ ] Update UI to show conversation structure

### Phase 3: Enhanced Context (Week 3)
- [ ] Add `fsnotify` to status watcher
- [ ] Test with Claude Code hooks
- [ ] Document hook file format for other agents
- [ ] Optional: Prototype OTel integration

### Phase 4: UI Improvements (Week 4)
- [ ] Show message history (user/Claude conversation)
- [ ] Display current activity with better granularity
- [ ] Tool execution timeline
- [ ] Real-time updates without page refresh

---

## Benefits Over ACP Approach

| Feature | ACP | Control Mode + Parsing |
|---------|-----|------------------------|
| **Works with existing sessions** | ❌ Must spawn agent | ✅ Attach to any tmux session |
| **Multi-client viewing** | ❌ 1:1 stdio | ✅ Multiple browsers to same session |
| **Structured data** | ✅ JSON-RPC | ✅ Control mode events |
| **Real-time updates** | ✅ Immediate | ✅ Immediate (`%output` events) |
| **Implementation complexity** | ⚠️ Custom transport | ✅ Use existing tmux protocol |
| **Agent compatibility** | ❌ Claude only (via bridge) | ✅ Any CLI tool in tmux |
| **Protocol coupling** | ⚠️ Tied to ACP spec | ✅ Terminal-agnostic |
| **Permission model** | ⚠️ Complex (who approves?) | ✅ Simple (send-keys simulates user) |

---

## Testing Strategy

### Unit Tests
- `ControlModeClient`: Parse event notifications
- `MessageParser`: Extract messages from buffer
- State machine transitions

### Integration Tests
- Spawn tmux session with Claude Code
- Connect control mode client
- Verify events received
- Send keys, verify state changes

### Manual Testing
- Run houston with existing Claude sessions
- Verify real-time updates on mobile
- Test voice input → send-keys → state update

---

## Future Enhancements

### Near-term
- Message search/filter in UI
- Export conversation as markdown
- Tool execution statistics
- Agent performance metrics (OTel)

### Long-term
- A2A protocol support (when agents adopt it)
- Multi-agent orchestration
- Conversation replay/debugging
- Agent comparison (Claude vs Aider vs Cursor)

---

## Conclusion

This architecture provides:
1. **Immediate value**: Works today with existing tmux sessions
2. **Clean abstraction**: Layers can evolve independently
3. **Future-proof**: Can add ACP/A2A later without rewrite
4. **Practical**: Solves the mobile monitoring use case directly

The `>` and `●` message markers are the key insight - they provide reliable conversation structure without brittle regex parsing.

---

## Open Questions (To Resolve During Implementation)

1. **Error handling**: How to handle:
   - tmux not running when server starts?
   - Control mode connection drops mid-session?
   - Parser buffer overflow with very long outputs?

2. **Performance**: At what point does per-pane parser memory become an issue?
   - Need cleanup strategy for closed panes
   - Consider LRU eviction for inactive parsers

3. **Known tools list**: Need to maintain comprehensive list
   - Should this be configurable?
   - Auto-detect from Claude Code --help output?

**Resolution strategy:** Prototype Phase 1, test with real sessions, document findings, adjust parser logic as needed.

**Plan status:** ✅ Ready for implementation with open questions to resolve during Phase 1 prototyping.
