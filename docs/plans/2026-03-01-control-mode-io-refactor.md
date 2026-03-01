# Control Mode I/O Refactor

**Date:** 2026-03-01
**Status:** Design
**Scope:** Replace capture-pane polling + send-keys with tmux control mode for pane I/O

## Problem

Houston uses `capture-pane` every 200ms and `send-keys` for input. This means:
1. Not a real tmux client (doesn't participate in `window-size latest`)
2. 200ms output latency
3. N+1 tmux CLI forks per second per pane
4. Resize requires saving/restoring window size to avoid fighting other clients

## Solution

Use tmux control mode (`tmux -CC attach`) for real-time pane I/O. Keep existing agent detection via lower-frequency capture-pane as a metadata side-channel.

## Architecture

```
Browser xterm.js
    ↕ WebSocket (incremental terminal data + meta JSON)
Go Server
    ├─ ControlClient per session (tmux -CC attach -t $session)
    │   ├─ stdout: parse %output %N data → route to pane subscribers
    │   ├─ stdin:  send-keys -t %N, refresh-client -C, capture-pane
    │   └─ lifecycle: reconnect on crash, clean shutdown
    └─ MetadataLoop per pane (capture-pane every ~1s)
        └─ agent detection, status parsing, mode, choices
```

## Control Mode Protocol

tmux control mode is line-oriented, text-only:

```
%output %42 hello world\015\012     # pane output (octal-escaped)
%begin 123456 1 1                    # command response start
output lines...
%end 123456 1 1                      # command response end
%window-renamed @1 vim               # async notification
```

Key commands we'll use:
- `send-keys -t %N -l 'text'` — send literal input to pane
- `refresh-client -C 120x40` — set client size (makes us a real client)
- `capture-pane -e -p -t %N -S -500` — one-shot capture for seeding + metadata

## Design Decisions

| Decision | Choice | Rationale |
|----------|--------|-----------|
| Granularity | One control client per session | Matches tmux model; %output includes pane ID for routing |
| Agent detection | Keep capture-pane at ~1s | Existing parser works on full snapshots; streaming parse is complex and fragile |
| Initial WS state | capture-pane seed + buffer replay | New clients need current screen state before streaming starts |
| Frontend output | Incremental `term.write(data)` | xterm.js is a terminal emulator — streaming is its native mode |
| Resize | `refresh-client -C WxH` | Makes houston a real tmux client; window-size latest handles the rest |
| creack/pty | Not needed | Control mode is text-based; exec.Command with pipes suffices |

## Component Design

### ControlClient (`tmux/control.go`)

Manages one `tmux -CC attach -t session` subprocess.

```
type ControlClient struct {
    session  string
    cmd      *exec.Cmd
    stdin    io.Writer        // send commands
    stdout   *bufio.Reader    // parse events
    subs     map[string][]chan<- []byte  // paneID → output subscribers
    cmdResp  chan commandResponse        // for synchronous commands
}

Methods:
  Subscribe(paneID string) <-chan []byte
  Unsubscribe(paneID string, ch <-chan []byte)
  SendKeys(paneID, text string) error
  SetClientSize(cols, rows int) error
  RunCommand(cmd string) (string, error)  // synchronous: capture-pane, list-panes, etc.
  Close() error
```

**Output parsing**: Line scanner on stdout. Lines starting with `%output` get octal-unescaped and routed to subscribers. `%begin`/`%end` blocks get captured for synchronous command responses.

**Octal unescaping**: Characters <32 and `\` are escaped as `\NNN`. Decode with single-pass byte scanner.

### ControlManager (`tmux/control_manager.go`)

Manages control clients across sessions. Creates on demand, cleans up when last subscriber leaves.

```
type ControlManager struct {
    clients map[string]*ControlClient  // session → client
}

Methods:
  GetClient(session string) (*ControlClient, error)
  ReleaseClient(session string)
```

### Updated pane_ws.go

```
handlePaneWS:
  1. Parse pane target → session + pane ID
  2. Get/create ControlClient for session
  3. Subscribe to pane output
  4. Seed: RunCommand("capture-pane -e -p -t %N") → writeSnapshot to WS
  5. Start forwarding: output sub → WS (as {type:"output", data:{data:...}})
  6. Start metadata loop: capture-pane every ~1s → agent detect → WS meta
  7. Read loop: WS input → SendKeys; WS resize → SetClientSize
  8. Cleanup: unsubscribe, release client
```

### Frontend Changes

**TerminalPane.tsx:**
- First message: `writeSnapshot()` (same as today — clears and writes full screen)
- Subsequent messages: `term.write(data)` — incremental, no clear
- Track whether we've received the seed snapshot (boolean flag)
- Remove RAF coalescing + deferred output (xterm.js handles scrollback natively)
- Remove `lastOutputRef` replay logic (control mode is persistent, not deduped)

**usePaneSocket.ts:**
- No protocol changes needed — same `{type, data}` JSON messages
- Output callback receives incremental data instead of snapshots

## Seeding Race Window

When a new WS connects, `%output` events are already flowing. We need to avoid losing output between the capture-pane seed and starting to forward.

Solution:
1. Subscribe to %output FIRST (buffer events in channel)
2. Run capture-pane via control client (synchronous command)
3. Send capture result as seed snapshot to WS client
4. Drain buffered %output events that arrived during capture
5. Start normal forwarding

The buffered events may contain data already in the capture. xterm.js handles this gracefully — duplicate writes just overwrite the same cells.

## Error Handling

- **tmux not running**: ControlClient.Connect fails → WS returns error, frontend shows disconnected
- **Control mode crash**: Detect EOF on stdout → reconnect with backoff, re-subscribe panes
- **Pane closed**: `%output` stops arriving; metadata loop detects pane gone → notify WS → close
- **Multiple browser clients**: Each gets its own subscriber channel; resize from any client affects session via window-size

## What Changes vs What Stays

**Changes:**
- `server/pane_ws.go` — rewrite write loop (streaming), keep read loop structure
- New `tmux/control.go` — control mode client
- New `tmux/control_manager.go` — multi-session management
- `ui/src/components/TerminalPane.tsx` — incremental output mode
- Remove: window size save/restore (control mode handles this)
- Remove: capture failure counting (metadata loop can fail gracefully)
- Remove: nudge channel (input goes directly via control mode stdin)

**Stays:**
- `agents/` — all agent detection code unchanged
- `parser/` — all parsing code unchanged
- `server/server.go` — SSE session stream unchanged
- `tmux/client.go` — kept for metadata capture-pane + session listing
- `ui/src/hooks/usePaneSocket.ts` — WS protocol unchanged
- `ui/src/components/MobileInputBar.tsx` — unchanged
- `ui/src/components/PaneHeader.tsx` — unchanged

## Testing

- Unit test: octal unescaping
- Unit test: %output line parsing and routing
- Unit test: %begin/%end command response parsing
- Integration: spawn tmux session, connect control client, verify output arrives
- Manual: verify real-time output, resize, input, agent detection still works
