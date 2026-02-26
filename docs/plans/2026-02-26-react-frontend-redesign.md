# React Frontend Redesign

## Problem

Houston's current frontend is ~2,000 lines of vanilla JS doing imperative DOM manipulation for a server-rendered two-page app (dashboard + pane view). The UX has fundamental friction:

- **Dashboard shows too little to act on.** Card previews are small, and acting on a session navigates to a separate page.
- **Pane view loses context.** Once you navigate in, you can't see other sessions. Getting back means page navigation.
- **No multi-session view.** Can't watch two agents side-by-side.
- **State management is manual.** Expanded sessions, dismissed cards, scroll positions — all tracked imperatively and reapplied after every SSE morph.

The desired UX is fundamentally different: a single-page app with a persistent sidebar, split terminal panes, and instant session switching. This requires a frontend rewrite, not incremental fixes.

## Architecture

```
┌─────────────────────────────────────────────────────┐
│                    Clients                           │
│                                                      │
│   React SPA          QuickShell Widget      Future   │
│   (browser)          (desktop)              clients  │
└──────┬───────────────────┬──────────────────────┬───┘
       │                   │                      │
       └───────────┬───────┘──────────────────────┘
                   │
            JSON + SSE + WebSocket API
                   │
       ┌───────────┴──────────────┐
       │     Go HTTP Server       │
       │                          │
       │  /api/sessions      JSON │
       │  /api/sessions?stream SSE│
       │  /api/pane/:target/ws WS │
       │  /api/pane/:target/* REST│
       │  /                  SPA  │
       └──────────┬───────────────┘
                  │
          ┌───────┴────────┐
          │  tmux + agents │
          │  OpenCode API  │
          └────────────────┘
```

Key decisions:
- Go backend gets `/api/*` routes returning JSON + SSE + WebSocket
- Old templ views/routes are removed after React is complete
- React SPA served from `/` via `go:embed`
- API is frontend-agnostic — designed for any client (QuickShell, CLI, etc.)

## Tech Stack

| Component | Choice | Rationale |
|-----------|--------|-----------|
| Framework | React + TypeScript | Component state, lifecycle management for split terminals |
| Build | Vite | Fast dev server, clean production builds |
| Terminal | xterm.js | Real terminal emulator — handles ANSI, cursor, reflow |
| Split panes | allotment | Lightweight React split pane component (~8KB) |
| Pane data | WebSocket | Bidirectional: output downstream, keystrokes upstream |
| Session list | SSE | One-way JSON stream, low frequency |
| State | React context + useReducer | Small state surface, no need for Redux |
| Styling | CSS custom properties | Dark-first, theme switching via class |
| Deployment | go:embed | Single binary, same as today |

## Layout

### Desktop (>=1024px)

```
┌──────────┬──────────────────────────────────────┐
│ Sidebar  │  Terminal Area                        │
│ (240px)  │                                       │
│          │  ┌─────────────┬─────────────────┐    │
│ Sessions │  │ pane A      │ pane B          │    │
│ (tree)   │  │ xterm.js    │ xterm.js        │    │
│          │  │             │                 │    │
│ ● = attn │  │             │                 │    │
│          │  └─────────────┴─────────────────┘    │
│          │                                       │
└──────────┴──────────────────────────────────────┘
```

- Sidebar: session tree grouped by status (attention > active > idle)
- Terminal area: one or more xterm.js panes in resizable splits
- Click session in sidebar → opens in focused pane
- Modifier+click → splits and opens alongside
- Direct keyboard input into focused terminal

### Mobile (<1024px)

```
┌─────────────────────────────┐
│ [☰] houston          [●2]  │
├─────────────────────────────┤
│                             │
│  xterm.js (read-only)       │
│                             │
├─────────────────────────────┤
│ [1:Yes] [2:No] [3:Skip]    │
├─────────────────────────────┤
│ [___input____] [🎤] [Send]  │
└─────────────────────────────┘
```

- Collapsible sidebar slides over from left
- Single terminal, no splits
- xterm.js mounted with `disableStdin: true`
- Choice buttons + input bar below terminal
- Attention badge in header shows count

## Backend API Contract

### Session Endpoints

```
GET /api/sessions
→ {
    "needsAttention": [SessionWithWindows...],
    "active": [SessionWithWindows...],
    "idle": [SessionWithWindows...]
  }

GET /api/sessions?stream=1
→ SSE: same JSON payload every ~3 seconds
  data: {"needsAttention":[...],"active":[...],"idle":[...]}
```

### Pane WebSocket

```
WS /api/pane/:target/ws
→ Bidirectional WebSocket

Server → Client:
  { "type": "output", "data": "<terminal bytes>" }
  { "type": "meta", "data": {
      "agent": "claude-code",
      "mode": "insert",
      "choices": ["Yes", "No"],
      "status": "Working: Reading file",
      "suggestion": "run the tests"
  }}

Client → Server:
  { "type": "input", "data": "<keystrokes>" }
  { "type": "resize", "cols": 120, "rows": 40 }
```

### Pane Actions (REST)

```
POST /api/pane/:target/kill
POST /api/pane/:target/respawn
POST /api/pane/:target/kill-window
POST /api/pane/:target/zoom
POST /api/pane/:target/send-with-images
  → { "text": "...", "images": [{ "name": "...", "data": "base64" }] }
```

### OpenCode Endpoints

```
GET  /api/opencode/sessions
GET  /api/opencode/sessions?stream=1              → SSE
GET  /api/opencode/session/:server/:id             → JSON
POST /api/opencode/session/:server/:id/send
POST /api/opencode/session/:server/:id/abort
```

## Data Flow

### State Shape

```typescript
interface AppState {
  sessions: SessionsData          // from SSE stream
  activePanes: PaneInstance[]     // currently open terminals
  splitLayout: SplitLayout        // binary tree of pane arrangement
  focusedPaneId: string | null    // keyboard focus target
  sidebarOpen: boolean            // mobile sidebar visibility
}

interface PaneInstance {
  id: string                      // unique instance id
  target: string                  // "session:window.pane"
  agentState: AgentState          // parsed from WebSocket meta frames
}

type SplitLayout =
  | { type: "single", paneId: string }
  | { type: "split", direction: "horizontal" | "vertical",
      ratio: number, first: SplitLayout, second: SplitLayout }
```

### Flow

1. App mounts → SSE to `/api/sessions?stream=1` → updates sidebar
2. User clicks window in sidebar → creates PaneInstance → mounts xterm.js → WebSocket to `/api/pane/:target/ws`
3. WebSocket output frames → `term.write(data)`
4. User types in xterm.js → `term.onData` → WebSocket input frame → tmux send-keys
5. WebSocket meta frames → update PaneHeader (agent type, status, choices)
6. User activates second session → layout splits, second xterm.js mounts independently
7. ResizeObserver → `fitAddon.fit()` → WebSocket resize frame → `tmux resize-pane`

### Persistence

`splitLayout` + active pane targets saved to `localStorage`. On reload, reconnects to same sessions in same layout.

## xterm.js Integration

Per terminal pane lifecycle:

```
Mount:
  → new Terminal({ cursorBlink: true, fontSize: 14, theme })
  → load FitAddon, WebLinksAddon
  → term.open(containerRef)
  → fitAddon.fit()
  → connect WebSocket

Running:
  WebSocket "output" → term.write(data)
  WebSocket "meta"   → update PaneHeader state
  term.onData(data)  → WebSocket { type: "input", data }
  ResizeObserver      → fitAddon.fit() → WebSocket { type: "resize", cols, rows }

Unmount:
  → WebSocket.close()
  → term.dispose()
```

Mobile: xterm.js mounted with `disableStdin: true`. Input goes through MobileInputBar, which sends full lines via POST.

## Sidebar

```
┌────────────────────────┐
│ houston           [◐]  │  theme toggle
├────────────────────────┤
│ 🔍 filter...           │
├────────────────────────┤
│ ATTENTION (2)          │
│ ● houston              │
│   ├─ main          [•] │  dot = open in terminal
│   └─ feature-x         │
│ ● claude-agent         │
│   └─ main          [•] │
├────────────────────────┤
│ ACTIVE (1)             │
│   nix-config           │
│   └─ dev-server         │
├────────────────────────┤
│ IDLE (3)               │
│   dotfiles             │
│   misc                 │
│   scratch              │
└────────────────────────┘
```

- Sessions grouped by status, attention always on top
- Click session → expand/collapse windows
- Click window → open in focused terminal pane
- Modifier+click → split and open alongside
- Active windows get visible indicator
- Filter input for quick search
- Resizable on desktop, slides over on mobile

## Agent-Specific UI

Agent metadata flows through WebSocket meta frames. Displayed in PaneHeader above each terminal:

```
Desktop (compact, 24px):
┌──────────────────────────────────────────────────┐
│ ◉ claude-code │ Working: Reading file │ INS   [×]│
├──────────────────────────────────────────────────┤
│ [1:Yes] [2:No] [3:Explain]                       │  ← only when choices
├──────────────────────────────────────────────────┤
│  xterm.js ...                                    │

Mobile (below terminal):
│  xterm.js (read-only) ...                        │
├──────────────────────────────────────────────────┤
│ ◉ claude-code │ Working... │ INS                 │
├──────────────────────────────────────────────────┤
│ [1:Yes] [2:No] [3:Explain]                       │
├──────────────────────────────────────────────────┤
│ [____________input____________] [Send]           │
```

Desktop: choice buttons are convenience — can type directly into xterm.js.
Mobile: choice buttons + input bar are the primary interaction method.

## Visual Design

**Overall feel:** Dark, clean, terminal-native. The terminal is the star — UI gets out of the way.

### Color System

```
Dark theme:
  Base:      #0a0a0f    (near-black, slight blue)
  Sidebar:   #0f1017    (slightly lifted)
  Terminal:  #000000    (pure black)
  Header:    #12131a    (subtle separation)
  Surface:   #1a1b26    (dropdowns, overlays)

Light theme:
  Base:      #f5f5f0    (warm gray, not stark white)
  Terminal:  #fafafa
  Sidebar:   #eeeee9
  Headers:   #e8e8e3

Status accents:
  Attention: #f59e0b    (amber — not red, red = error)
  Working:   #3b82f6    (calm blue)
  Done:      #22c55e    (green, fades to idle)
  Idle:      #4b5563    (muted gray)
  Error:     #ef4444    (red)
```

### Key Visual Details

- **Attention glow**: Sidebar items with attention get a soft amber pulse. Peripheral-vision noticeable.
- **Split animations**: Divider slides in, terminals resize smoothly, new pane fades in. ~200ms ease-out.
- **Terminal typography**: System Nerd Font or JetBrains Mono. Ligatures. Proper line height.
- **Frosted sidebar (mobile)**: `backdrop-filter: blur(12px)` when sliding over terminal.
- **Status transitions**: Done→idle fades over ~2s. State changes feel alive, not binary.
- **Thin chrome**: Pane headers are 24px, semi-transparent. Terminal dominates.
- **No borders**: Panels separated by 1px gaps, not borders. Split dividers invisible until hovered.
- **Choice buttons**: Slide up with spring animation. Subtle depth/shadow. Press state.

## Component Tree

```
App
├── Sidebar
│   ├── SidebarHeader (logo, theme toggle)
│   ├── FilterInput
│   ├── SessionTree
│   │   └── SessionGroup (attention / active / idle)
│   │       └── SessionItem (expandable)
│   │           └── WindowItem (click to open)
│   └── SidebarFooter
├── TerminalArea
│   ├── SplitContainer (allotment, recursive splits)
│   │   └── TerminalPane (per activated window)
│   │       ├── PaneHeader (agent, status, choices, close)
│   │       ├── XTermView (xterm.js instance)
│   │       └── MobileInputBar (mobile only)
│   └── EmptyState
└── NotificationBadge (mobile header)
```

## Project Structure

```
houston/
├── main.go
├── server.go              # add /api/* routes
├── api.go                 # NEW: JSON API handlers
├── websocket.go           # REWRITE: pane WebSocket handler
├── embed.go               # NEW: go:embed ui/dist/*
├── tmux/                  # UNCHANGED
├── parser/                # UNCHANGED
├── agents/                # UNCHANGED
├── opencode/              # UNCHANGED
├── ui/                    # NEW: React app
│   ├── package.json
│   ├── tsconfig.json
│   ├── vite.config.ts
│   ├── index.html
│   └── src/
│       ├── main.tsx
│       ├── App.tsx
│       ├── api/
│       │   ├── types.ts
│       │   ├── sessions.ts
│       │   └── pane.ts
│       ├── components/
│       │   ├── Sidebar.tsx
│       │   ├── SessionTree.tsx
│       │   ├── TerminalArea.tsx
│       │   ├── TerminalPane.tsx
│       │   ├── PaneHeader.tsx
│       │   ├── MobileInputBar.tsx
│       │   ├── SplitContainer.tsx
│       │   └── EmptyState.tsx
│       ├── hooks/
│       │   ├── useSessionsStream.ts
│       │   ├── usePaneSocket.ts
│       │   ├── useLayout.ts
│       │   └── useMediaQuery.ts
│       ├── theme/
│       │   ├── dark.ts
│       │   ├── light.ts
│       │   └── tokens.css
│       └── lib/
│           └── xterm.ts
├── views/                 # DELETE after React complete
├── handlers.go            # DELETE after React complete
├── static/                # KEEP: favicon
└── flake.nix              # UPDATE: add npm build step
```

## Build Pipeline

### Development

```bash
# Terminal 1: React dev server with hot reload
cd ui && npm run dev    # Vite on :5173, proxies /api/* to :9090

# Terminal 2: Go backend
go run . -addr localhost:9090
```

### Production

```bash
cd ui && npm run build          # → ui/dist/
go build -o houston .           # embeds ui/dist/ via go:embed
```

### Nix

Update `flake.nix` to run `npm ci && npm run build` before `go build`. Single `nix build` produces the embedded binary.

## Migration Strategy

Clean switchover in four phases:

**Phase 1 — API layer (backend)**
Add `/api/sessions` JSON endpoint and `/api/pane/:target/ws` WebSocket handler. Reuse existing `buildSessionsData()` and tmux client. Old routes keep running.

**Phase 2 — Core React app (frontend)**
Scaffold `ui/` with Vite + React + TypeScript. Build sidebar + single terminal pane. Connect to API, validate end-to-end data flow.

**Phase 3 — Split panes + polish**
Add split layout with allotment. Mobile responsive layout. Agent-specific pane headers. Visual polish, animations, theming.

**Phase 4 — Delete old frontend**
Remove `views/`, `handlers.go`, `static/app.js`. Remove templ dependency. Old routes become the API routes.

App stays functional throughout — old and new can run side-by-side during development.
