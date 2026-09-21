# houston

Mission control for AI coding agents. A mobile-friendly web dashboard for monitoring and controlling tmux sessions and OpenCode instances remotely. Built for checking on AI agents and sending them instructions from your phone.

## Supported Agents

- **Claude Code** - via tmux session monitoring
- **Amp** - via tmux session monitoring
- **OpenCode** - via native API integration

### OpenCode Setup

OpenCode TUI uses a **random port by default**, which houston can't discover. To enable houston integration:

**Option 1: Start OpenCode with a fixed port**
```bash
opencode --port 4096
```

**Option 2: Use opencode serve + attach**
```bash
# Terminal 1: Start headless server
opencode serve --port 4096

# Terminal 2: Attach TUI for interaction
opencode attach http://localhost:4096
```

**Option 3: Tell houston the port**
```bash
houston --opencode-url http://localhost:YOUR_PORT
```

**Option 4: Use the houston plugin (recommended)**

Copy the plugin to your OpenCode config:
```bash
cp ~/.config/opencode/plugins/houston.ts ~/.config/opencode/plugins/
```

The plugin automatically writes server info to `~/.local/state/houston/opencode-servers/` when OpenCode starts. Houston reads these files to discover running instances.

After installing the plugin, restart your OpenCode instances.

Houston scans ports 4096-4100 by default. Use `--no-opencode` to disable.

## Architecture

```
┌──────────────────────────────────────────────────────┐
│                   Mobile Browser                      │
│  React SPA (xterm.js + WebSocket + SSE)              │
└──────────────────────────────────────────────────────┘
                          │
           SSE (session list) + WebSocket (pane I/O)
                          │
                          ▼
┌──────────────────────────────────────────────────────┐
│                  Go HTTP Server                       │
│                                                       │
│  JSON API:                                            │
│  GET  /api/sessions?stream=1  - SSE session stream    │
│  WS   /api/pane/:target/ws   - Pane I/O (bidi)       │
│  POST /api/pane/:target/send - Send text/special keys │
│  GET  /api/font/bigger       - Increase terminal font │
│  GET  /api/font/smaller      - Decrease terminal font │
│  GET  /api/dispatch/options  - Dispatch form choices  │
│  POST /api/dispatch          - Start a worker         │
│  GET  /*                     - Serve React SPA        │
│                                                       │
│  React SPA embedded via go:embed at compile time      │
└──────────────────────────────────────────────────────┘
                          │
                ┌─────────┴─────────┐
                ▼                   ▼
┌────────────────────┐  ┌────────────────────┐
│     tmux CLI        │  │   OpenCode API      │
│                     │  │                     │
│  list-sessions      │  │  GET /sessions      │
│  list-windows       │  │  WS  /events        │
│  capture-pane       │  │                     │
│  send-keys          │  │                     │
└────────────────────┘  └────────────────────┘
```

## Tech Stack

| Component | Choice | Rationale |
|-----------|--------|-----------|
| Backend | Go | Single binary, fast, good tmux process handling |
| Frontend | React + TypeScript | Rich terminal rendering with xterm.js |
| Terminal | xterm.js v6 | Full terminal emulation in browser |
| Layout | allotment | Resizable split panes (desktop) |
| Styling | CSS custom properties | Dark theme, no framework dependency |
| Build | Vite | Fast dev server with HMR, production bundling |
| Live updates | SSE + WebSocket | SSE for session list, WS for pane I/O |
| Voice input | Web Speech API | Built into browsers, no backend needed |
| Auth | None | Rely on Tailscale/SSH tunnel for security |

## Project Structure

```
houston/
├── main.go              # Entry point, CLI flags, embed FS setup
├── embed.go             # go:embed directive for ui/dist
├── server/
│   ├── server.go        # HTTP server, mux, SSE session stream
│   ├── api.go           # JSON API handlers (sessions, panes, font)
│   └── pane_ws.go       # WebSocket handler for pane I/O
├── tmux/
│   ├── client.go        # tmux CLI wrapper (list/capture/send)
│   └── client_test.go
├── opencode/
│   ├── client.go        # OpenCode HTTP/WS client
│   ├── discovery.go     # Port scanning + file-based discovery
│   ├── manager.go       # Lifecycle management
│   ├── types.go         # OpenCode data types
│   └── client_test.go
├── terminal/
│   └── font.go          # Terminal font size control (kitty)
├── agents/              # Agent type detection (claude-code, amp)
├── hub/                 # Session discovery + transcript tracking
├── runs/                # Run registry (tmux/hook/crew sources)
├── hook/                # Claude hook install/doctor/state
├── contrib/             # OpenCode plugin
├── scripts/             # claude-hook.sh
├── docs/                # Design notes and plans
├── parser/              # Terminal output parsing
├── status/              # Status file management
├── internal/            # Internal utilities
├── ui/                  # React frontend (Vite)
│   ├── src/
│   │   ├── App.tsx              # Root layout, sidebar toggle, pane management
│   │   ├── main.tsx             # React entry point
│   │   ├── api/
│   │   │   └── types.ts         # Shared TypeScript types (Session, WSMeta, etc.)
│   │   ├── components/
│   │   │   ├── Sidebar.tsx      # Slide-out session list with filter
│   │   │   ├── SessionTree.tsx  # Collapsible session/window tree
│   │   │   ├── TerminalArea.tsx # Container managing open panes
│   │   │   ├── TerminalPane.tsx # xterm.js terminal with mobile zoom/pan
│   │   │   ├── SplitContainer.tsx # Desktop split pane layout (allotment)
│   │   │   ├── PaneHeader.tsx   # Agent icon, status, mode badge, wide toggle
│   │   │   └── MobileInputBar.tsx # Quick actions, text input, voice
│   │   ├── hooks/
│   │   │   ├── useSessionsStream.ts # SSE hook for live session list
│   │   │   ├── usePaneSocket.ts     # WebSocket hook for pane I/O
│   │   │   ├── useLayout.ts         # Persist layout state to localStorage
│   │   │   └── useMediaQuery.ts     # Responsive breakpoint hook
│   │   ├── lib/
│   │   │   └── xterm.ts        # xterm.js theme and initialization
│   │   └── theme/
│   │       └── tokens.css      # CSS custom properties (colors, fonts)
│   ├── vite.config.ts          # Vite config with API proxy
│   ├── tsconfig.json
│   └── package.json
├── justfile             # Task runner
├── .air.toml            # Hot reload config
├── flake.nix            # Nix flake
├── go.mod
└── go.sum
```

## Development Setup

```bash
# Enter dev shell
nix develop

# Run Go backend with hot reload (auto-finds port 7474-7479)
just dev

# Run React dev server with HMR (port 5173, proxies /api to Go backend)
just ui-dev

# Build React frontend for production
just ui-build

# Build Go binary (embeds ui/dist at compile time)
go build -o houston .
```

### Development Workflow

**For frontend changes:** Use `just ui-dev` for instant HMR. The Vite dev server proxies `/api` requests to the Go backend.

**For production testing:** Run `just ui-build` then `just dev`. The Go binary embeds `ui/dist` via `go:embed`, so you must rebuild both the React app and the Go binary to see frontend changes in production mode.

**Important:** `air` watches `.go` files but not `ui/dist`. After `just ui-build`, touch `embed.go` to trigger a Go rebuild, or restart `just dev`.

### Pre-commit Hooks

Managed by `git-hooks.nix`. Auto-installed on `nix develop` / `direnv reload`.

- **golangci-lint** — runs on staged `.go` files
- **eslint** — runs `npx eslint .` in `ui/` when `.ts`/`.tsx` files change
- **tsc** — runs `tsc -b` in `ui/` when `.ts`/`.tsx` files change

Run all hooks manually: `pre-commit run -a`
Skip hooks: `git commit --no-verify`

### Vite Proxy

The Vite dev server (`ui/vite.config.ts`) proxies `/api` to `http://localhost:9090`. Change the target port if your Go backend runs on a different port.

## Mobile Features

- **Wide terminal**: fixed-width container (~120 columns), CSS-scaled to fit the viewport
- **Touch scrolling**: Single-finger vertical scroll through terminal history
- **Pinch-to-zoom**: Two-finger pinch with focal-point tracking
- **Pan**: Single-finger horizontal drag or two-finger drag when zoomed
- **Composer** (`MobileInputBar.tsx`, docked under the terminal in the run-detail Terminal tab): multi-line field where Enter inserts a newline and Send (or Ctrl/Cmd+Enter) sends the text followed by Enter; an empty Send presses Enter. Also a voice button (Web Speech API) and file attach.
- **Quick keys**: one horizontally scrollable row — Esc, ^C, Enter, Tab, Shift+Tab, ↑/↓, 1–5, Y/N, Alt+P, ^O, ^Z, `/copy`. All but `/copy` are keystrokes sent through `POST /api/pane/:target/send` with `special=true` (no implicit Enter, so a digit answers a numbered prompt without a stray Enter).
- **Choices**: when the pane `meta.choices` is present, each choice renders as a tappable `n. label` button above the row and answers with its ordinal key.
- **Keyboard**: `useKeyboardInset` tracks the on-screen keyboard via `visualViewport`; `MobileShell` shortens itself to sit above it and hides the tab bar so the terminal and composer keep the space.

On desktop the Terminal tab takes keystrokes directly (xterm `onData` → WS `input`); clicking it focuses it.

There is no WIDE/FIT toggle — that affordance was removed. A terminal rework
(font-size zoom, pan behavior, possibly more) is in progress; revisit this
section once it ships rather than trusting the bullets above for anything not
already verified in `TerminalPane.tsx` / `useTouchGestures.ts`.

## WebSocket Protocol

The pane WebSocket (`/api/pane/:target/ws`) is bidirectional and carries a
JSON envelope, `{"type":"...","data":{...}}` (`server/pane_ws.go`,
`ui/src/hooks/usePaneSocket.ts`):

**Server → Client:**
- `dims` — Pane dimensions (cols/rows) to resize xterm.js to match
- `seed` — Full snapshot of pane content; also used for a mid-stream re-seed (e.g. after a resize) — there is no separate `reseed` type on the wire
- `output` — Incremental terminal data
- `meta` — Pane metadata (agent type, status, mode, activity, choices)

**Client → Server:**
- `input` — `{data: <text>}`, sent verbatim to the pane (no implicit Enter)
- `resize` — `{cols, rows}`, request terminal resize

There is no `resize-done` acknowledgment, and no `special:<key>` message type
on this WebSocket. `special` does exist, but as a form parameter on the
legacy REST route `POST /api/pane/:target/send` (`server/server.go`): when
`special=true`, `input` is sent as a key name (C-c, Up, Down, Escape, Tab,
BTab, M-p, C-o, C-z) rather than literal text. `MobileInputBar.tsx` uses this
route for quick actions instead of the WebSocket.

## Dispatch

The Dispatch tab starts a worker by running the host's `dispatch` CLI
(`server/dispatch.go`, `server/dispatch_exec.go`). This is remote command
execution from an HTTP request, so the handler is closed by construction:

- `GET /api/dispatch/options` — the repos houston knows (each distinct tmux
  `@git_root` resolved to its main repo, with that repo's crews from
  `<git-common-dir>/crew/crews/`), tiers, efforts, plans, and the per-engine
  model allowlist (`engines`, displayed in `engine_order`).
- `POST /api/dispatch` — `{repo, title, spec?, tier, engine, model, effort,
  plan?, crew, issue?}`. Every argv value is an enum, an allowlisted model, or
  an anchored-regex match; `repo` must be in the options set and `crew` must
  exist in that repo. The title is the only free text — one argv element that
  may not start with `-`, equal `resume`, or look like an issue id (dispatch has
  no `--`). The task body goes to a 0600 temp file passed as `DISPATCH_SPEC` and
  removed afterwards; it is never logged.
- One dispatch at a time (429 otherwise), 120 s timeout. The run is detached
  from the request, so a dropped phone connection doesn't abort a half-built
  worktree; on timeout the process group gets SIGTERM (dispatch's trap clears
  its branch lock), then SIGKILL.
- An unknown crew is refused by houston itself (404) before dispatch runs;
  dispatch's own refusals (tier↔model map, budget) come back as 422 with its
  stderr verbatim. The new run then appears in Fleet through the
  crew source — nothing else to wire.
- The model allowlist is a Go constant (`dispatchModels`); keep it in step with
  dispatch's tier map when models change.
- houston's environment must provide what dispatch needs: `dispatch`, `crew`,
  `git`, an authenticated `gh`, `wt`, `direnv`, and a running tmux server on
  `PATH`. A 502 means `dispatch` itself could not be started; any other
  missing tool fails inside dispatch and comes back as a 422 with its stderr.
- `-no-auth` leaves this endpoint enabled: that mode already exposes
  `/api/pane/:target/send`, which is equivalent command execution.

## Security

**Token-gated API.** On first run houston generates a random token into
`<status-dir>/token` (`0600`). Every `/api/` request must present it as the
`houston_token` cookie, an `Authorization: Bearer` header, or a `?token=` query
parameter. The query parameter is accepted **only on a WebSocket upgrade**,
which is the one request a browser can't attach a header to; everywhere else it
would just leak the token into history and proxy logs. Loading the SPA issues
the cookie as an httpOnly `SameSite=Strict` response cookie, so a browser that
has opened the UI once authenticates transparently on every subsequent call.

**Origin allowlist.** Requests are checked against same-origin plus, under
`-debug`, the Vite dev server — covering both the HTTP API and the WebSocket
handshake, so a page from another origin is refused even with a valid token.

**Host allowlist.** Origin checking alone cannot stop DNS rebinding: a rebound
attacker controls both `Host` and `Origin`, so they always agree. houston
therefore pins the `Host` header, derived from what the machine knows about
itself — `os.Hostname()`, its Tailscale address (CGNAT `100.64.0.0/10`), the
reverse-DNS name of that address, and loopback literals. It never resolves a
client-supplied name, because under rebinding that resolves to us and would
validate the attacker's own claim. An unrecognised `Host` gets **421** and, more
importantly, is never issued a token. If you reach houston by a name it can't
derive — a reverse proxy, custom DNS — add it with `-hostname` (repeatable).

`-no-auth` disables the **token** only. Origin and Host checking still apply:
turning off authentication shouldn't make the server cross-origin drivable.

This model is the primary defense; the following are additional layers:
1. Default bind: `127.0.0.1:9090` (localhost only)
2. Access via Tailscale (recommended)
3. Or SSH tunnel: `ssh -L 9090:localhost:9090 host`

## Dependencies

**Go:** `github.com/gorilla/websocket` — only external dependency. Everything else is stdlib.

**React:** `@xterm/xterm`, `@xterm/addon-fit`, `@xterm/addon-web-links`, `allotment`, `react`, `react-dom`
