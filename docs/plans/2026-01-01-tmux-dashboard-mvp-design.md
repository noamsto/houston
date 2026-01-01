# tmux-dashboard MVP Design

**Date:** 2026-01-01
**Status:** Approved
**Goal:** Mobile-friendly dashboard for monitoring Claude Code agents in tmux sessions

## Problem Statement

When running Claude Code agents in tmux sessions, checking on them requires:
- Being at the computer
- Navigating to specific tmux windows
- Limited status visibility

Current workaround (hooks + tmux picker status) isn't phone-accessible and requires context switching.

## Solution: Alert-First Dashboard

A mobile-first web dashboard that shows **what needs attention** prominently, with the ability to view full output and send input.

### Primary Use Cases

1. Check if any agent is stuck/waiting for input
2. See what an agent did (output history)
3. Send occasional instructions or responses

## Architecture

### Overview

```
┌─────────────────────────────────────────────────────────┐
│                   Mobile Browser                         │
│  htmx + ansi_up + Tailwind CSS                          │
└─────────────────────────────────────────────────────────┘
                          │
            SSE (Streamable HTTP) + POST
                          │
                          ▼
┌─────────────────────────────────────────────────────────┐
│                     Go HTTP Server                       │
│                                                          │
│  ┌──────────────┐    ┌──────────────┐    ┌────────────┐ │
│  │ Status       │    │ Tmux         │    │ Output     │ │
│  │ Watcher      │    │ Client       │    │ Parser     │ │
│  │              │    │              │    │            │ │
│  │ Polls hook   │    │ list-sessions│    │ Detects:   │ │
│  │ status files │    │ capture-pane │    │ - choices  │ │
│  │              │    │ send-keys    │    │ - questions│ │
│  └──────────────┘    └──────────────┘    └────────────┘ │
└─────────────────────────────────────────────────────────┘
                          │
                          ▼
┌─────────────────────────────────────────────────────────┐
│  Hook Status Files          │  tmux CLI                  │
│  ~/.local/state/claude/     │  list-sessions, capture,   │
│                             │  send-keys                 │
└─────────────────────────────────────────────────────────┘
```

### Tech Stack

| Component | Choice | Rationale |
|-----------|--------|-----------|
| Backend | Go | Single binary, good process handling |
| Frontend | htmx + HTML templates | Simple, server-rendered |
| Styling | Tailwind CSS (CDN) | Mobile-first utilities |
| Output rendering | ansi_up | Lightweight ANSI→HTML (~10KB) |
| Live updates | SSE (Streamable HTTP) | Push-based, battery efficient |
| Auth | None (Tailscale/SSH) | Network-level security |

### API Design (Streamable HTTP)

Single endpoints, behavior varies by `Accept` header:

```
GET /sessions
  Accept: text/html         → Session cards HTML
  Accept: text/event-stream → Status change stream

GET /pane/:session/:window/:pane
  Accept: text/html         → Current output HTML
  Accept: text/event-stream → New lines stream

POST /pane/:session/:window/:pane/send
  Body: { "input": "..." }  → Send keys to pane
```

## UI Design

### Home Screen (Alert-First)

```
┌─────────────────────────────┐
│  tmux-dashboard        [⟳]  │
├─────────────────────────────┤
│  NEEDS ATTENTION (2)        │
│ ┌─────────────────────────┐ │
│ │ 🔴 claude-agent-1       │ │
│ │ Waiting for choice      │ │
│ │ "What approach should   │ │
│ │  we use?"               │ │
│ │                         │ │
│ │  [1] [2] [3] [4]        │ │
│ │                 2m ago  │ │
│ └─────────────────────────┘ │
│ ┌─────────────────────────┐ │
│ │ 🟠 nix-config           │ │
│ │ Error encountered       │ │
│ │ "Build failed: missing  │ │
│ │  derivation..."         │ │
│ │                 5m ago  │ │
│ └─────────────────────────┘ │
├─────────────────────────────┤
│  OTHER SESSIONS (3)     [▼] │
│  main • dev • scratch       │
└─────────────────────────────┘
```

**Key behaviors:**
- "Needs attention" cards expanded with context snippet
- Quick choice buttons for multiple-choice questions
- Tap card → full output view
- SSE pushes status changes instantly
- Badge count in browser tab

### Output View

```
┌─────────────────────────────┐
│  ← claude-agent-1      [⋮]  │
├─────────────────────────────┤
│                             │
│  $ claude --chat            │
│  > Using brainstorming...   │
│                             │
│  **What approach?**         │
│                             │
│  1. Option A                │
│  2. Option B                │
│  3. Option C                │
│                             │
│  ─────────────────────────  │
│  [1] [2] [3]     ← Quick    │
├─────────────────────────────┤
│ [________________] [🎤] [↵] │
└─────────────────────────────┘
```

**Key behaviors:**
- Auto-scroll to bottom on load
- Quick action buttons when choices detected
- Overflow menu: Ctrl+C, scroll top, refresh
- ansi_up renders ANSI colors
- SSE streams new output lines

## Status Detection

### Two Sources

1. **Hook status files** (primary alert trigger)
   - Directory: `~/.local/state/claude/` (configurable)
   - Format: one file per session with status flag
   - Polling interval: 2-3 seconds

2. **Output parsing** (rich context)
   - Multiple choice: `1.`, `2.` or `[1]`, `[2]` patterns
   - Questions: Lines ending with `?`
   - Errors: `error`, `failed`, `Error:` keywords
   - Approval: "proceed?", "continue?", "look right?"

### Status Priority

1. 🔴 Error encountered
2. 🔴 Waiting for input/choice
3. 🟠 Needs attention (hook flag, reason unclear)
4. 🟢 Actively working
5. ⚪ Idle

## Error Handling

### Connection

- SSE disconnect → Browser auto-reconnects (EventSource built-in)
- Server restart → Client reconnects, fetches full state
- Mobile background → Reconnects on foreground
- `Last-Event-ID` support for resuming streams

### tmux Edge Cases

- Session gone → Remove from UI, toast "Session ended"
- Pane closed → Redirect home, toast notification
- tmux not running → Show "No tmux server" message
- Permission denied → Show error, suggest fix

### Input Edge Cases

- Send to closed pane → Error toast, refresh list
- Long input → Truncate at 4KB with warning
- Special characters → Escape for `send-keys`

### Mobile

- Offline → Banner "Offline - reconnecting..."
- Slow connection → Loading states, non-blocking UI
- Screen rotation → Preserve scroll and selection

## MVP Scope (v0.1)

- [x] Alert-first home screen with session cards
- [x] Status detection from hook files
- [x] Output parsing for choices/questions/errors
- [x] SSE streaming for live updates
- [x] Output view with ansi_up rendering
- [x] Quick choice buttons
- [x] Text input to send commands
- [x] Mobile-responsive layout
- [ ] Voice input (moved to v0.2)

## Future Roadmap

### v0.2 - Enhanced Monitoring
- Voice-to-text input (Web Speech API)
- Session activity timeline
- Output search/filter
- Quick actions: Ctrl+C, scroll shortcuts

### v0.3 - Agent Control
- **Auth agent remotely** (OAuth flows, device codes)
- Switch Claude Code session context
- Spawn new agent sessions
- Kill/restart stuck agents
- Pause/resume agent

### v0.4 - Multi-Agent Management
- View agents across multiple tmux sessions
- Agent grouping (by project, task type)
- Bulk actions (stop all, check status)
- Agent templates

### v0.5 - Usage & Analytics
- Token usage tracking per agent
- Cost estimation
- Task duration history
- Success/failure rates
- Export reports

### v0.6 - Desktop Experience
- Responsive desktop layout (multi-column view)
- Side-by-side session list + pane output
- Desktop keyboard shortcuts
- Resizable panes
- Summary dashboard with all agents visible

### v0.7 - Advanced Features
- Multiple server support (SSH to hosts)
- Push notifications (service worker)
- Scheduled agent tasks
- Agent-to-agent handoff visualization
- Conversation history browser

### v1.0 - Polish
- PWA / native wrapper
- Themes and customization
- Performance optimizations

### v1.1 - Security Hardening
- Input validation and sanitization
- Rate limiting on SSE connections
- Optional authentication (basic auth, Tailscale headers)
- Security headers
- CSRF protection

## Project Structure

```
tmux-dashboard/
├── main.go              # Entry point, CLI flags
├── server.go            # HTTP server, SSE handlers
├── tmux.go              # tmux command wrappers
├── status.go            # Hook file watcher, status aggregation
├── parser.go            # Output pattern detection
├── handlers.go          # HTTP handlers
├── templates/
│   ├── layout.html      # Base template with htmx + ansi_up
│   ├── index.html       # Home page
│   ├── sessions.html    # Session cards partial
│   ├── pane.html        # Output view
│   └── input.html       # Input bar partial
├── static/
│   └── app.js           # Minimal JS (voice input later)
├── docs/
│   └── plans/
│       └── 2026-01-01-tmux-dashboard-mvp-design.md
├── flake.nix
├── .envrc
├── go.mod
└── CLAUDE.md
```

## Implementation Notes

### SSE in Go

```go
func (s *Server) handlePaneStream(w http.ResponseWriter, r *http.Request) {
    flusher, ok := w.(http.Flusher)
    if !ok {
        http.Error(w, "SSE not supported", http.StatusInternalServerError)
        return
    }

    w.Header().Set("Content-Type", "text/event-stream")
    w.Header().Set("Cache-Control", "no-cache")
    w.Header().Set("Connection", "keep-alive")

    // Stream pane output changes
    for {
        select {
        case <-r.Context().Done():
            return
        case output := <-s.paneUpdates:
            fmt.Fprintf(w, "data: %s\n\n", output)
            flusher.Flush()
        }
    }
}
```

### htmx SSE Integration

```html
<div hx-ext="sse" sse-connect="/pane/main:0.0" sse-swap="output">
  <pre id="output" class="font-mono bg-gray-900 text-gray-100">
    <!-- SSE updates append here -->
  </pre>
</div>
```

### Output Parsing Patterns

```go
var patterns = []struct {
    name    string
    regex   *regexp.Regexp
    priority int
}{
    {"choice", regexp.MustCompile(`(?m)^\s*[1-4][.)\]]\s+\S`), 1},
    {"question", regexp.MustCompile(`\?\s*$`), 2},
    {"error", regexp.MustCompile(`(?i)(error|failed|exception):`), 0},
    {"approval", regexp.MustCompile(`(?i)(proceed|continue|look right)\?`), 1},
}
```

## Security

- Bind to localhost only (`127.0.0.1:8080`)
- Access via Tailscale (recommended) or SSH tunnel
- No built-in auth for MVP
- Future: Tailscale auth headers, basic auth option
