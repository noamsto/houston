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
├── views/               # Legacy templ templates (to be removed)
├── static/              # Legacy static assets (to be removed)
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

- **Wide terminal mode** (default): 960px container (~120 columns) scaled to fit viewport, good for diffs
- **Fit mode**: Terminal matches viewport width, larger text
- **Touch scrolling**: Single-finger vertical scroll through terminal history
- **Pinch-to-zoom**: Two-finger pinch with focal-point tracking
- **Pan**: Single-finger horizontal drag or two-finger drag when zoomed
- **Quick actions**: Number keys (1-5), Y/N, with expandable section for ^C, Enter, arrows, Esc, Tab, Shift+Tab, Alt+P, Ctrl+O, Ctrl+Z
- **Voice input**: Web Speech API microphone button
- **WIDE/FIT toggle**: In pane header, switches between wide and fit modes

## WebSocket Protocol

The pane WebSocket (`/api/pane/:target/ws`) is bidirectional:

**Server → Client:**
- `output:<data>` — Terminal capture-pane content (sent on change, deduped)
- `meta:<json>` — Pane metadata (agent type, status, mode, activity, choices)
- `resize-done` — Acknowledgment of resize

**Client → Server:**
- `input:<text>` — Send text to pane (appends Enter)
- `special:<key>` — Send special key (C-c, Enter, Up, Down, Escape, Tab, BTab, M-p, C-o, C-z)
- `resize:<cols>:<rows>` — Request terminal resize

## Security

**No built-in auth** — rely on network-level security:
1. Default bind: `127.0.0.1:9090` (localhost only)
2. Access via Tailscale (recommended)
3. Or SSH tunnel: `ssh -L 9090:localhost:9090 host`

## Dependencies

**Go:** `github.com/gorilla/websocket` — only external dependency. Everything else is stdlib.

**React:** `@xterm/xterm`, `@xterm/addon-fit`, `@xterm/addon-web-links`, `allotment`, `react`, `react-dom`
