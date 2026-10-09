# houston

Mission control for AI coding agents. A mobile-friendly web dashboard for monitoring and controlling tmux sessions and OpenCode instances remotely. Built for checking on AI agents and sending them instructions from your phone.

## Supported Agents

- **Claude Code** - via tmux session monitoring
- **Amp** - via tmux session monitoring
- **OpenCode** - via native API integration
- **pi, Codex, Cursor** - via hookyard's normalized hook envelope (see "Hook ingestion" below); Fleet shows the engine as the run's `agent`

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

## Modes

houston runs in one of two modes, chosen by `-mode auto|dispatcher|tmux` (default `auto`) and resolved once at startup, never re-detected. The result is logged as `houston mode` with `mode`, `requested` and `reason` (never the PATH value).

- **`auto`** (`mode.Resolve`, production probe `server.ModeProbe`) picks dispatcher mode when both `dispatch` and `crew` resolve (`mode.OnPath`: an executable regular file; an empty or relative PATH element never matches) on the tmux server's global PATH joined ahead of houston's own — the PATH `dispatch` runs with (`dispatchEnv`). If the tmux server PATH can't be read (server unreachable, no global PATH), only houston's PATH is searched. Otherwise tmux mode. `exec` resolves `dispatch` on houston's own PATH, so a host with `dispatch` only on the tmux server's PATH still gets dispatcher mode, and `POST /api/dispatch` then answers 502 `could not run dispatch: …`.
- `server.Config.Mode` must be `mode.Dispatcher` or `mode.Tmux`; `server.New` errors otherwise.
- **Dispatcher mode** is everything described below.
- **Tmux mode** is plain tmux plus hooks monitoring:
  - `POST /api/runs/:id/reply`, `POST /api/dispatch`, `GET /api/dispatch/options`, `POST /api/dispatch/dispatcher`, `GET|POST|DELETE /api/repos` and `GET /api/repos/candidates` are not registered (404): the command-execution surface is gone, not hidden.
  - No `CrewSource` (`runSources`); the registry order `runs.Order` is tmux, hooks, so nothing reads `<git-common-dir>/crew`.
  - The tmux layer ignores `@crew_name`/`@crew_color` (`tmux.WithoutCrew`): no `role`, no codename or colour, no dispatcher worker-count line, and the Workspace response carries no `crew_codename`. Role-grid panes (`@crew_role` other than `lead`) are still skipped in both modes.
- `GET /api/mode` → `{"mode":"dispatcher"|"tmux"}` in both modes, behind the auth gate. The UI (`useMode`) fetches it once and retries every 5 s on failure; until it is known only Fleet and Workspace show, so in dispatcher mode Crews and Dispatch appear a moment after load. In tmux mode those two tabs are absent because of the `mode === 'dispatcher'` gates in `MobileShell` / `ConsoleShell`, and `#/crews` / `#/dispatch` (with any query) are rewritten in place to `#/fleet` by `tabsFor` via `useShellTab(mode)`; while the mode is unknown such a hash is left alone. Adding a dispatcher-only tab means touching both.

## Architecture

```
┌──────────────────────────────────────────────────────┐
│                   Mobile Browser                      │
│  React SPA (xterm.js + WebSocket + SSE)              │
└──────────────────────────────────────────────────────┘
                          │
            SSE (run/session list) + WebSocket (terminal)
                          │
                          ▼
┌──────────────────────────────────────────────────────┐
│                  Go HTTP Server                       │
│                                                       │
│  JSON API:                                            │
│  GET  /api/mode               - Mode (both modes)     │
│  GET  /api/runs*              - Run list + SSE stream │
│  POST /api/runs/:id/reply     - Reply to a run (D)    │
│  WS   /api/runs/:id/terminal  - Run terminal I/O      │
│  POST /api/runs/:id/input     - Send text/key/image   │
│  GET  /api/runs/:id/chat*     - Chat page, SSE, tool  │
│  GET  /api/dispatch/options   - Dispatch form (D)     │
│  POST /api/dispatch           - Start a worker (D)    │
│  POST /api/dispatch/dispatcher - New dispatcher (D)   │
│  GET  /api/repos              - Repo registry (D)     │
│  POST|DELETE /api/repos       - Add/forget a repo (D) │
│  GET  /api/repos/candidates   - Repo picker (D)       │
│  GET  /*                      - Serve React SPA       │
│  (D) = dispatcher mode only; 404 in tmux mode         │
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
│   ├── server.go        # HTTP server, mux, route wiring
│   ├── api.go           # OpenCode session handlers
│   ├── pane_ws.go       # Shared WebSocket pane I/O (terminal routes)
│   └── runs_terminal.go # Run-addressed terminal WS + input routes
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
├── chat/                # Standalone ACP transcript readers (stdlib only)
├── hub/                 # Session discovery + transcript tracking + chat ring
├── mode/                # Dispatcher vs tmux mode resolution (stdlib only)
├── runs/                # Run registry (tmux/hook/crew sources)
├── hook/                # Claude hook install/doctor/state
├── contrib/             # OpenCode plugin
├── scripts/             # claude-hook.sh
├── docs/                # Design notes and plans
├── parser/              # Terminal output parsing
├── internal/            # Internal utilities
├── ui/                  # React frontend (Vite)
│   ├── src/
│   │   ├── App.tsx              # Root: renders the Fleet shell
│   │   ├── main.tsx             # React entry point
│   │   ├── api/
│   │   │   ├── types.ts         # Shared TypeScript types (WSMeta, etc.)
│   │   │   └── terminal.ts      # TerminalAddress (run) + send helpers
│   │   ├── components/
│   │   │   ├── TerminalPane.tsx # xterm.js terminal with mobile zoom/pan
│   │   │   ├── PaneHeader.tsx   # Agent icon, status, mode badge
│   │   │   └── MobileInputBar.tsx # Quick actions, text input, voice
│   │   ├── fleet/
│   │   │   └── RunDetail.tsx    # Run detail view; Terminal tab uses run address
│   │   ├── hooks/
│   │   │   ├── usePaneSocket.ts     # WebSocket hook for pane I/O
│   │   │   ├── useLayout.ts         # Persisted font-size/grouping preferences
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
- **Free one-finger pan**: a single finger drags in both axes at once (after an 8px slop) — horizontal movement pans, vertical movement scrolls terminal history; there is no axis lock
- **Pinch-to-zoom**: Two-finger pinch with focal-point tracking
- **Detached mode**: horizontal finger movement (≥8px that actually pans, drag, pinch or the column scrubber) or ending a drag scrolled up detaches the view from the live cursor — output no longer auto-pans it, a reconnect reseed with unchanged dims keeps the pan and distance from the bottom, and a "↓ Live" pill appears bottom-centre (a keyboard/container resize still re-snaps vertically). Re-attach = tapping the pill, or a drag that starts scrolled up and ends at the bottom edge; either snaps to the cursor and follows it immediately
- **Composer** (`MobileInputBar.tsx`, docked under the terminal in the run-detail Terminal tab): multi-line field where Enter inserts a newline and Send (or Ctrl/Cmd+Enter) sends the text followed by Enter; an empty Send presses Enter. Also a voice button (Web Speech API) and file attach: picking a file only stages it as a thumbnail chip (× removes it), and Send then posts image plus text as one `{type:'image'}` input.
  Both composers (this one and the Chat tab's) keep their draft per run in localStorage under separate keys (`houston-draft:run:<id>` for this Terminal composer, `houston-draft:chat:<id>` for Chat; `useComposerDraft`) and clear it only after a confirmed successful send, even one that lands after the composer remounted; a failed or timed-out send shows the error and keeps the text and any staged image. A draft expires after 24h; expired or malformed entries are swept on first use per page load. For a pane run both composers key by `run.draft_key` instead of the run id (`draftKeyOf`, `runs/draftkey.go`: a one-way hash of tmux server pid + pane id + session id, `omitempty` on the wire, so the raw session id never leaves the server), because pane ids restart at `%0` after a tmux server restart so a draft does not carry over to the next agent that gets the pane id; a run with no pane (no `draft_key`) keeps its run id. When houston knows the run's session id (hooks layer, or crew `engine_session`), a new session in the same pane (`/clear`) starts with an empty composer; a tmux-only run with no session id, or an unknown server pid, is keyed by what is known and leans on the 24h expiry. The staged image is held in memory only, so it is lost when the composer unmounts or the page reloads.
- **Quick keys**: one horizontally scrollable row — Esc, ^C (asks Confirm/Cancel inline before sending), Enter, Tab, Shift+Tab, ↑/↓, 1–5, Y/N, Alt+P, ^O, ^Z, `/copy`. All but `/copy` are keystrokes: `POST /api/runs/:id/input` `{type:'key', key:...}` (400 if `key` isn't in the `terminalKeys` allowlist). No implicit Enter, so a digit answers a numbered prompt without a stray Enter.
- **Quick commands** (`QuickCommands.tsx`, claude runs only — `run.agent` `claude`/`claude-code`; shown above the quick keys in the Terminal-tab composer and above the Chat composer's row): a `/` pill reveals `/compact`, `/compact…` (prefills the composer with `/compact `), `/clear` (inline Confirm/Cancel first, since it wipes the context) and `/context`, sent as text + Enter. All are disabled unless the run's `state` is `idle` (`quickCommands.ts`; a hook `waiting` is already demoted to `idle`), with the reason in the pill title. A `↳ suggested` pill appears when the very last chat item is a non-commentary assistant message with a fenced block whose body is a single line starting with a slash command (`/name …`; multi-line blocks and absolute paths like `/home/u/x.sh` are ignored; derived client-side from the chat updates, no server support); it opens an editable sheet whose Send posts the edited text, and the pill stays hidden after a successful send until the suggestion changes (and shows again if the same command is suggested later). `/compact…` prefills only an empty composer (a non-empty draft or a staged image is never overwritten or turned into an image prompt; a note asks to clear the draft and attachment first) and then goes through the normal composer Send, which is not idle-guarded. Feedback line: `Sent … — waiting`, then `… accepted` once the run leaves `idle`, or after 10 s `No reaction seen yet` (for `/compact`) / `Sent — this command may not change the run state; check the terminal.` (other commands, which can finish without a state change).
- **Choices**: when the pane `meta.choices` is present, each choice renders as a tappable `n. label` button above the row and answers with its ordinal key.
- **Keyboard**: `useKeyboardInset` tracks the on-screen keyboard via `visualViewport`; `MobileShell` shortens itself to sit above it and hides the tab bar so the terminal and composer keep the space.

On desktop the Terminal tab takes keystrokes directly (xterm `onData` → WS `input`); clicking it focuses it.

There is no WIDE/FIT toggle — that affordance was removed. A terminal rework
(font-size zoom, possibly more) is in progress; revisit this
section once it ships rather than trusting the bullets above for anything not
already verified in `TerminalPane.tsx` / `useTouchGestures.ts`.

## Navigation

Every shell tab is a hash route — `#/fleet`, `#/crews`, `#/workspace`, `#/dispatch` (`ui/src/fleet/routes.ts`, `useShellTab`) — so Back/Forward move between tabs and reload restores the tab. A run detail (`#/fleet/<id>/<tab>`) keeps the tab it was opened from, and its back button returns there. On mobile a tab tap always writes the hash (closing the detail overlay); on desktop the detail is a persistent pane, so a rail switch while a run is selected is state-only (no history entry, not restored on reload). `#/crews` and `#/dispatch` exist only in dispatcher mode; tmux mode rewrites them to `#/fleet`.

## Hook ingestion

`houston hook` (`hook.Dispatch`) accepts two shapes on stdin, auto-detected by
a non-empty top-level `engine` field (Claude's native payload never has one):
Claude Code's native hook payload, or hookyard's normalized envelope
(`{engine, canonical_event, native_event, session_id, cwd, tool_name,
tool_input, native}`, `native` carrying the engine's own payload). In envelope
mode the envelope's own event is authoritative and a CLI `<event>` arg is
ignored (that arg exists so wrapper scripts can name the event for a native
Claude payload; `houston hooks install` registers a bare `<binary> hook` and
relies on `hook_event_name`).

`engine: claude-code` decodes `native` as a Claude `Event`; the event is
`native.hook_event_name`, else the envelope's `native_event` (neither →
no-op), and the file records `agent: claude`. From there it runs the same
`apply` as the native path. Other engines map canonical events onto houston's
internal (Claude) event vocabulary; engine-scoped events (empty
`canonical_event`, `engine` set) are matched by engine and native name first:

| envelope | houston event | notes |
|---|---|---|
| `session_start` | SessionStart | |
| `prompt_submit` | UserPromptSubmit | Turn++ |
| `pre_tool` | PreToolUse | tool + hint from the envelope's normalized `tool_name`/`tool_input` |
| `post_tool` | PostToolUse | |
| `pre_compact` | PreCompact | |
| `turn_end` | Stop (pi: `TurnEnd`) | pi's `turn_end` is intermediate — see below |
| `""` + pi `agent_settled` | Stop | pi's run settled — the final signal, hook state `waiting` (Fleet `idle`) |
| `""` + pi `session_shutdown` | SessionEnd | `reason: reload` is a no-op instead — pi re-emits `session_start` for the same session |
| `""` + codex `SessionEnd` | SessionEnd | |
| `""` + cursor `sessionEnd` | SessionEnd | |
| anything else | — | no-op: no state-file write |

`TranscriptPath` comes from the native payload's `transcript_path`, else pi's
`session_file`.

**Notification and SubagentStop** (`hook.apply`, Claude): `permission_prompt`
and `elicitation_dialog` set `permission` (a dialog that blocks the turn).
`idle_prompt` always sets `waiting` (with its message), even when a permission
is pending: it is the only signal a denied or Esc-dismissed dialog ever gets (an
interrupt fires no `PostToolUse` or `Stop`), so it heals the card. A dialog
genuinely left open past `idle_prompt` therefore reads `idle`, as in base. Any
other or untyped type (e.g. `auth_success`) keeps the state and replaces the
message, unless a permission is pending, whose message it does not displace.
`SubagentStop` is
a no-op — the main agent is still inside the `Task` tool — so state, tool and
message all stand. `waiting` is only the hook state value: Fleet maps it to
`idle` (see "Needs-you").

**pi turns:** pi fires `turn_end` after every LLM response, not just the
final one, so it maps to houston's internal `TurnEnd` event and always leaves
the card `thinking`. pi's final signal is the engine-scoped `agent_settled`
(mapped to `Stop` → `waiting`, which Fleet shows as `idle` unless the turn
ended on a question), which fires once per run after retries,
compaction and queued follow-ups, and also on an aborted (Esc) run — so an
aborted run settles instead of hanging `thinking`. pi has no
Notification, so a pi run never shows a permission prompt.

The state file records `agent` (`claude`/`codex`/`cursor`/`pi`); an empty
value (legacy files, native Claude hooks, transcript-discovered sessions)
reads as `claude` — the default lives in `hub.mergeStateIntoView`, not in the
hook package.

`Dispatch` serializes its read-modify-write with a `flock` on
`<state-dir>/claude/.lock`, because hookyard fires most events (`post_tool`,
`turn_end`, `prompt_submit`, `session_start`) as detached fire-and-forget
processes that would otherwise race a Read→apply→Write and silently drop an
update. The tmux `display-message` exec stays outside the lock.

The lock serializes writes but doesn't order them — a detached event can
still land after a later one, so a late `post_tool` landing after a `turn_end`
leaves a card `thinking` until the next event corrects it. The same race
applies to pi's final `turn_end` and `agent_settled`: `apply`'s `EventTurnEnd`
case guards against this by skipping the `thinking` transition when the state
is already `waiting` — a `turn_end` after the run settled can only be a late
one.

houston only sees events hookyard's manifest subscribes it to. The
engine-scoped `pi:agent_settled`, `pi:session_shutdown`, `codex:SessionEnd`
and `cursor:sessionEnd`, and the canonical `pre_compact`, must all be named
in the manifest's `events` (the nix-config houston manifest) — otherwise a pi
run never leaves `thinking` when it settles, a pi run that exits while its
pane stays open (e.g. back to a shell) is never ended, a codex/cursor session
that ends stays `waiting` (Fleet `idle`, never ended), and `compacting` never shows.

lazytmux does not set `@claude_status` on pi (or codex/cursor) panes, so the
tmux layer never lists them on its own — they reach Fleet through the hooks
layer only, keyed by the same pane id, so the tmux layer still supplies
branch/crew enrichment and terminal caps. `runs/crewjoin.go`'s `resolvePane`
accepts agent-detect's `@agent_screen` panes too, so a pi crew worker's bus
record joins its pane (see "Crew-layer pane join").

The hub's transcript parser is Claude's: a pi session file yields preview text
but no trail/tokens.

`SessionState.PID` is the hook's parent process — under hookyard that is the
router, not the agent — and isn't read anywhere in houston.

## Background tasks

A Claude Code run's outstanding background shells and monitors ride on `Run.Background` (`[{id, kind: "shell"|"monitor", hint, since}]`), shown as a `N bg` chip on the Fleet card and a list in the Chat status strip (`RunStatusStrip`). They are informational: they never change `State`. A turn that ended with a shell still running keeps its hook/tmux verdict (`idle`/`blocked`), because `State` describes the agent's turn, `blocked` is reserved for a human being required, and `running` would claim the agent is working.

- **Source is the transcript**, folded by `hub/background.go`'s `bgTracker` inside the hub's existing 2 s tail. It starts at byte 0 on every houston start, so it is restart-safe with no persisted state, and needs no tmux capture. Hooks were rejected (no event fires on completion or on a UI/timeout kill, and the hookyard manifest would need new subscriptions); the pane's `N shell still running` text was rejected (a capture per poll, no per-task detail, nothing for monitors).
- **Start**: a `Bash` tool_use with `run_in_background: true`, or a `Monitor` tool_use, whose non-error tool_result names the task id (`with ID: <id>` / `(task <id>,`). A failed launch is never tracked.
- **End**: a `<task-notification>` carrying a `<status>` (completed/failed/killed/stopped), matched by every `<task-id>` and `<tool-use-id>` in it (a resume's orphan summary lists many ids), read from either the `user` record or the `queue-operation` enqueue copy; or a non-error `TaskStop` result. A notification with no `<status>` is a monitor's per-event line and ends nothing.
- A non-persistent `Monitor` times out (`timeout_ms`) with no transcript marker, so the tracker expires it itself (`list(now)`; the hub re-checks on every 2 s tick even when the file did not grow). Shells have no such lifetime.
- `ReadTranscriptFrom` stops at the last complete line: consuming a half-written start/end record would lose it for good.
- Claude only (`agent == claude`); dropped when the run is `done`/ended. Not covered: background `Agent`/Task subagents, and anything started inside a subagent's own transcript. A session whose Claude was killed without a notification or `SessionEnd` (while its pane stays open) can show a stale chip until the run ends; shells that outlive a `/clear` belong to the new session id and go uncounted.

## Context meter and pi spend

`Run.Context {used, limit?}` and `Run.SpendUSD` (pi only) feed the Chat status strip (`RunStatusStrip`: `142k / 200k ctx`, a percentage bar when the limit is known, `$x.xx` for pi). The Fleet card does not show them: it is already dense and the meter belongs with the session detail.

- **Source is the transcript**, in the hub's existing tail (`hub/transcript.go` `parseLine` → `applyTranscriptEvent`), no extra file pass. `used` is the latest assistant message's uncached + cached input: Claude `input_tokens + cache_read_input_tokens + cache_creation_input_tokens`, pi `usage.input + cacheRead + cacheWrite`. The latest wins (it shrinks after a compaction), never the maximum. A pi aborted message has usage but no content blocks; it still counts.
- **Limit** (`hub/context.go`): a small model-id prefix table (Claude families 200k) plus `[1m]` ids at 1M. An unknown model, or a window smaller than the session's peak use (a 1M run reported under its base id), has no limit; houston never guesses one. The limit is checked against the session's peak use, not the latest, so it does not return after a compaction. Residual: Claude Code writes the API model id with no `[1m]`, so a 1M session reads against 200k (an over-full bar) until it first passes 200k, then shows no limit.
- **Spend**: pi only, the running sum of each message's recorded `usage.cost.total`. Claude records no cost and houston keeps no price table, so Claude runs have no `spend_usd`. The tail starts at byte 0 on every houston start, so the sum is restart-safe.

## Project and role (Fleet)

Every run can carry `project` and `role` (`runs/project.go`, `runs/tmuxsource.go`, `runs/crewsource.go`).

- **`project`** is the main repo's name: `git rev-parse --git-common-dir` of the window's `@git_root`, so a linked worktree resolves to its main repo rather than its own directory name. Crew-bus runs derive it from the bus directory (`<common>/crew`) with no extra git call. Hook-only runs (no tmux window) get no `project`; the UI falls back to `repo`, which `repoAndBranch` already strips of the worktree leaf.
- **`role`** is `dispatcher` when the window's `@crew_name` is the literal `dispatcher` (the dispatcher launcher, `adapters/core/dispatcher.sh`, sets it), `worker` for any other non-empty `@crew_name` and for every crew-bus run, and absent for a solo session. A dispatcher started any other way reads as solo. `worker` never overwrites `dispatcher` when layers merge. Tmux mode sets no `role`: the tmux layer ignores `@crew_name` and there is no crew bus.
- Hook-only runs (`runs/hooksource.go`) resolve `project` from the state file's `cwd` via the git common dir, and only when git actually names it — the first non-empty `Project` across layers wins, so a hook `cd` never overrides the tmux layer's.
- **Ghost hook runs:** a hook state file whose pane is missing from a *successful* `ListPaneOptions` is published as `done` (kept in history, `caps.terminal` false); a failed listing never ends anything. Hook activity newer than that verdict marks the session alive elsewhere (another tmux server) and it is never ended again.
- `hub` prunes an ended hook state file `hub.DefaultPruneTTL` (24h) after its last update; a file whose last-written state is not `ended` — including a ghost session's — is never pruned by this pass.
- **Foreign panes:** tmux pane ids are unique only within one server incarnation and restart at `%0` after a server restart, so a hook state file can name a pane that now belongs to someone else. Every hook event re-reads `$TMUX_PANE` and the server pid from `$TMUX` (no exec) and recomputes the coordinates when they disagree with the file or on `SessionStart`, recording the server pid as `tmux_server`. `runs/hooksource.go` then distrusts a pane whose recorded server differs from the one houston is listing, or whose state predates that server's start time (which covers files written before `tmux_server` existed): the coordinates are dropped before the run is keyed, so it keys off its session id, carries no `Tmux` ref or terminal/reply/kill caps, and is ended into history under the same revive rule as a vanished pane. The server identity rides the same `list-panes -a -F` listing that produces the pane ids (`#{pid}`/`#{start_time}` in `paneOptionsFormat`), so the two can never disagree about which server minted a pane; a failed listing publishes nothing and leaves earlier verdicts standing, and a tmux that cannot expand those fields logs once and falls back to trusting the recorded pane. The same normalize step disowns a session whose hook state is `ended` even when its pane is live and same-server: tmux hands that id to the next occupant the moment the pane is reused, and a session that announced its own end can never prove it is still there, so it too keys off its session id with no `Tmux` ref or terminal/reply/kill caps (#120). `last_message` is cleared by every hook event except `Notification` and `SubagentStop`, and the hub only surfaces it while the state is waiting or permission (`runFromSessionView` publishes it as `Activity.Message` only for a `blocked` run).
- **Point-of-use server check:** the foreign-pane guard above is sound only for the listing it ran against — `HookSource` polls every 5s, so a tmux restart landing right after a successful listing leaves a window where a cached `Tmux` ref still matches the pre-restart server while the new server has already reused the same pane id. `runs.TmuxRef` and `tmux.Pane` both carry the tmux server pid (`Server`, internal only — `json:"-"`, never reaches the runs JSON/SSE API) alongside the pane id, and `server/runs_terminal.go`'s `runPane` re-resolves and compares it on every send/terminal-attach, refusing with 409 when both the run's recorded server and the freshly resolved pane's server are known and disagree. Either side being unknown never refuses on its own — same "unknown ⇒ false" posture as the foreign-pane guard. This closes the race for the run-addressed routes below. An already-open run-route WebSocket re-verifies too: it re-resolves the pane at open and again after every control-client reconnect, gated on the client's generation counter (`server/pane_ws.go`, `tmux.ControlClient.Generation`), and WS input is dropped until that check passes. A gone pane or a different server closes the socket with 4409, which the UI shows as ended without retrying; an unverifiable resolve closes with 1011 instead. Only an unknown server on the resolved pane falls back to trust (unknown ⇒ allow, as in `runPane`). `metaPollLoop`'s `meta` frames stay ungated.
- **Needs-you (`runs/state.go`):** `blocked` is the only state meaning a human must act now — it drives the badge, the sort order, and `NeedsAttention()`. A turn end alone is never `blocked`: a hook `waiting` (`Stop`, `idle_prompt`, pi's `agent_settled`, codex/cursor turn ends) maps to `idle` (`FromHookState`), and lazytmux's `@claude_status` `done` (written on `Stop`) is `idle` too, while `denied` (the auto-mode classifier's refusal; the turn continues) is `running`. A role-grid `@crew_role` pane is skipped by every layer (tmux/crew since #99, the hooks layer since #143, which gates its `@crew_role` lookup on the same not-foreign test `normalize` uses), so a critic pane never becomes its own card (#111). A grid lead's `@crew_role=lead` marks the worker itself and is not skipped (#179). The only sources of `blocked`:
  - `hook.StatePermission` — a `permission_prompt` or `elicitation_dialog` Notification (the dialog's message is the `Question`).
  - A Claude `PreToolUse` of `AskUserQuestion` (blocks until answered; `PostToolUse` resumes), with a fixed `Question`.
  - A turn end whose last assistant text ends in `?`: the hub tracks `SessionView.Asks` (the final line of the last assistant text block, markdown `*`/`_`/`` ` `` trimmed; reset by any later tool_use, tool_result or user text) and `runFromSessionView` turns a hook `waiting` with `Asks` set into `blocked` with `Question{Via: "pane"}`. Applies to engines with a hub transcript reader (Claude, pi). Two edges: a question followed by a closing line ("Let me know.") is missed, and a rhetorical closing question ("Hope that helps — anything else?") is flagged. The transcript tail lags `Stop` by up to 2 s, so the card can read `idle` briefly first. `Activity.Message` is published only for a `blocked` run.
  - tmux `@claude_status waiting` (a permission/elicitation dialog), and a crew watchdog `prompt:` status (an interactive prompt a human clears at the pane, via `pane`).
  - A crew worker's own `crew status … blocked "<question>"` (non-watchdog) is addressed to its dispatcher, so `runs/crewsource.go` (`routeCrewQuestion`) keeps it `running` with no `Question` (the text stays in `Crew.Detail`) while the dispatcher is alive and the blocked episode is younger than `crewDispatcherSilence` (15 min); otherwise it is `blocked` with a `crew` `Question`. Dispatcher alive ⟺ `<bus>/crews/<crew>/pane` names a pane in the current tmux listing that carries an engine status (`@claude_status` or `@agent_screen`) in a window whose `@crew_name` is `dispatcher` (the stamp guards against a pane id reused after a tmux restart); a dispatcher started without the stamp, or a crew with no `pane` file (houston-minted), reads as gone. The episode starts at the first `blocked` status after the last non-blocked status, watchdog status, or dispatcher reply.

  **Attention.** `Run.Attention` (`needs-you|stuck|done|""`, plus a houston-worded `attention_note`) is computed once in `composeLocked` (`attentionOf`, `runs/registry.go`) from the composed state plus the tmux and crew layers, and rides `runSignature`. Only the crew layer publishes an input `Attention`/`AttentionNote` (its watchdog verdict); `mergeInto` never copies them, so an `Attention` set by any other layer is ignored. `needs-you` ⟺ `State == blocked`. `stuck`: the composed state, the tmux layer (`@claude_status error`, even when the hooks layer wins `State`) or the crew layer is `failed`, or the crew layer carries a stuck watchdog prefix. The note is the crew layer's own when the crew layer is failed or stuck (empty for a worker's own `failed`, whose reason is in `crew.detail`); otherwise, for a composed or tmux failure, "Its last turn failed — check its terminal.". `done`: the composed state is `review`, or the crew layer is `done`/`pr_open`, taken from the crew layer even when a higher layer's state (hooks `idle` after the worker's final Stop) wins `State`. A crew watchdog `blocked` is liveness bookkeeping; its prefix (`runs/crewsource.go`, `watchdogPrefixes`) decides, and the watchdog's own detail is dispatcher-facing and never shown raw:

  | watchdog prefix | state | attention |
  |---|---|---|
  | `prompt:` | `blocked` + `pane` `Question` | needs-you |
  | `quota:`, `turn-stall:`, `quiet:`, `stalled:`, `runaway:`, `unread:`, `budget:` | `running` | stuck, with a per-prefix note |
  | `load:`, unknown | `running` | none (host-level / unknown: no claim) |
  | `failed` (`dead:`) | `failed` | stuck, note "Its engine stopped — the watchdog marked it failed." |

  **UI.** The header badge counts fresh (≤ 1 h) `blocked` runs (`needsYou` in `ui/src/fleet/staleness.ts`): `NeedsAttention()` plus a freshness gate — the same set as `attention === 'needs-you'`, because needs-you ⟺ blocked. Filters: Active · Needs you · Stuck · Done · All; Active/All sort needs-you > stuck > done > rest, each by recency, and a stale one ranks as rest. Stuck and done cards get a calmer border and chip, never the needs-you red; a stale stuck or done card keeps a muted border, chip and note. An **ended** run (`state === 'done'` with no attention: `SessionEnd`, ghost, foreign) leaves Active immediately and is reachable under All; a `done` run that also carries `stuck` attention is not ended, and a crew-`done` worker (attention `done`) stays.

  **Residuals.** After a permission dialog is approved no hook fires until the tool's `PostToolUse`, so a long approved tool reads `blocked` while it runs (lazytmux has the same gap). A Claude too old to send `notification_type` loses its permission flag. A dialog left open past `idle_prompt` reads `idle` (the notification that heals a dismissed dialog cannot tell it from one still on screen). A crew worker whose own turn ends on a `?` reads needs-you even while its earlier bus question is routed to a live dispatcher — intended: a stopped turn can only be resumed at the pane. A crew branch's `done`/`failed` layer is evicted `crewEvictionGrace` (10 min) after its last bus status, after which a joined pane's card shows its hooks/tmux state and loses `stuck`/`done` attention.
- **Crew-layer pane join:** `runs/crewjoin.go`'s `resolvePane` joins a crew-bus branch to its one candidate pane. A candidate is any pane carrying `@claude_status` (a Claude run) **or** `@agent_screen` (agent-detect's stamp for pi, codex and cursor), excluding role-grid panes (any `@crew_role` other than `lead`) and plain shells. When the branch's latest bus status is terminal (`done`/`failed`) it only joins if the pane looks like that same finished session: its own activity epoch — `@claude_status`'s 2nd field if present, else `@agent_screen`'s — is within `terminalJoinGrace` (5 min) of the terminal record, and its state word says the session finished (`done`/`idle`/`error` — the `@claude_status` vocabulary; `error` maps to the internal failed state — for a Claude pane, `idle` for `@agent_screen`). The grace exists because the worker posts `done` and *then* ends its final turn, so the Stop hook stamps the pane a few seconds later (observed +4s/+13s); an actively working pane (processing, or waiting on a prompt) is a new occupant even inside the window, and an unknown epoch (0) refuses the join. A third condition guards the pane's identity, not just its epoch: the pane's foreground engine — the tty's foreground process-group leader, reached from `#{pane_pid}` (procfs on Linux, `/bin/ps` elsewhere, since houston's service PATH has no `ps`) — must have started within `[s<epoch> − 30s, record ts]`, where `s<epoch>` comes from the latest status record's worker id `worker:<branch>#s<epoch>-<pid>`; an unknown start time or a bare worker id (no `#s<epoch>`) refuses the join. A fourth condition is engine-session identity: when the branch's bus record carries `engine_session` (`Run.CrewSession`, on dispatcher#652 dispatch/resume rows) and the pane's current hook session id is known, the two must match. The pane→session map comes from the hooks layer's own view (`paneHookSessions` over `hub.Snapshot()`, filtered by the same `paneForeign`/ended/role-pane tests), so a `/clear` or `/resume` in the same engine process starts a new session id the process identity can't see and the stale terminal record no longer joins. Either side unknown — an older dispatcher row, codex/cursor's null `engine_session`, or no hook state for the pane — keeps the process-start rule exactly. Residual: the bus row keeps its pre-`/clear` `engine_session` until a `resume` row replaces it, so a worker that cleared mid-run and then finished splits into a fallback bus card plus the hooks card instead of joining.
- **Crews tab roster (#231):** each worker card mirrors `crew roster`: `crew.title`, a `tier · engine · model` line (`crew.tier`, `run.agent`, `crew.model`), `crew.detail` (the live phase, clamped, with the run's age), the PR chip, and `N sessions` when `crew.sessions` > 1. `crew.sessions` counts the branch's bus `dispatch` + `resume` rows (`runs/crewsource.go`). The card border, chip and the header counts line (`working · needs you · stuck · done`, then `idle` and `ended` when non-zero) bucket on `run.attention` (`crewAttention` in `ui/src/fleet/crewsModel.ts`), flags gated on freshness like the Fleet badge (a stale flag counts as `ended`). A run with no attention counts by state: working, `idle`, or `ended` (state `done`), never `done`. An unflagged idle card carries an `idle` chip, and live tool activity outranks `crew.detail` for the phase line only while the run is working.
- **Crew-named chat session (#184):** a `dispatch`/`resume` bus row carries `engine_session`, the engine's own session id (a uuid for claude/pi, `null` for codex/cursor, absent on an older dispatcher's rows — all three read as today). The newest such row per branch lands on `Run.CrewSession` (internal) and, when no hooks layer names a `Session`, on the composed `Session`. `deriveCaps` grants `caps.chat` from it when the *bus record's* engine has a chat reader, with no pane identity involved, so the #120 ended-session disown is untouched: a worker whose Claude exited back to the shell keeps its Chat on the worker card. The registry then hides the session-keyed `sess-<id>` history card (`shadowedLocked`) while any other key's crew layer names that session, and shows it again once that layer is gone.
- A dispatcher card's `N workers · M blocked` line counts the workers with the same project and host; it is hidden when more than one live dispatcher shares that project and host, because the bus's crew id is not on the dispatcher's own run.
- The Fleet list's **Group by project** toggle (`houston-fleet-group-by-project` in localStorage) is off by default: the flat list keeps needs-you-first across all repos, and the project chip is always visible.

## WebSocket Protocol

The primary terminal route is `GET /api/runs/:id/terminal`
(`server/runs_terminal.go`), which resolves the run to its live tmux pane and
upgrades to the same bidirectional WebSocket envelope described below. Pane
resolution happens before the upgrade, so a bad address never reaches
`Upgrade()` — it comes back as a plain HTTP error: 503 if the run registry
isn't started, 404 for an unknown run, 409 if the run has no terminal
capability/pane, 409 again if tmux confirms the pane id no longer exists (it
exited or the session is gone), 409 again if the run's recorded tmux server
and the freshly resolved pane's server are both known and disagree (see
"Point-of-use server check" above), or 503 `tmux unavailable` if tmux itself
couldn't be reached (missing binary, timeout, wrong socket permissions, or a
client/server protocol mismatch after an upgrade left a stale tmux server
running).

Past the upgrade, a run-route socket can still close with 4409 "tmux server
changed" (the pane is gone or has moved to a different server) or 1011
"could not verify tmux server" (the check itself failed) — see "Point-of-use
server check" above.

The run route carries a JSON envelope, `{"type":"...","data":{...}}`
(`server/pane_ws.go`, `ui/src/hooks/usePaneSocket.ts`,
`ui/src/api/terminal.ts` for address/URL selection):

**Server → Client:**
- `dims` — Pane dimensions (cols/rows) to resize xterm.js to match
- `seed` — Full snapshot of pane content; also used for a mid-stream re-seed (e.g. after a resize) — there is no separate `reseed` type on the wire
- `output` — Incremental terminal data
- `meta` — Pane metadata (agent type, status, mode, activity, choices)

**Client → Server:**
- `input` — `{data: <text>}`, sent verbatim to the pane (no implicit Enter)
- `resize` — `{cols, rows}`, request terminal resize

There is no `resize-done` acknowledgment, and no `special:<key>` message type
on either WebSocket — the WS `input` message always carries raw keystrokes.

Non-typing input (quick keys, images) instead goes through a POST route, and
the allowlist that bounds it lives there, not on the socket:

- `POST /api/runs/:id/input` (run address) — same resolution ladder as the WS
  route (503/404/409/409/409/503) before the body is even read. Body is one of:
  `{"type":"text","text":"..."}` (literal text, then Enter — `tmux.Client.SendKeys` sends it as
  `send-keys -l -- <chunk>` in ≤ 8 KiB chunks, backslash-escaping a chunk's trailing `;` because tmux
  drops it as a command separator, and waits `enterSettle` before Enter so a TUI agent doesn't read
  text+Enter as one paste and turn the Enter into a newline),
  `{"type":"key","key":"<terminalKeys>"}` (one key, no Enter — 400 if `key`
  isn't in the `terminalKeys` allowlist in `server/runs_terminal.go`: Escape,
  C-c, Enter, Tab, BTab, Up, Down, M-p, C-o, C-z, y, n, 1–9), or
  `{"type":"image","text":"...","images":[...]}` (temp-file paths + text,
  then Enter). Text and image `text` longer than 256 KiB is 413 `text too long`
  before any `send-keys`. `SendKeys` uses the request context, so a client
  disconnect stops later chunks and never sends Enter after cancel. Max body 50 MiB; an
  oversized body is 413. A send that already delivered a chunk and then fails
  or is cancelled is 502 with body `{"partial":true}`. Success is 204 with no body. Because the allowlist
  tops out at `9`, a choice past the 9th ordinal has no key to send.
  `MobileInputBar.tsx` sends through `ui/src/api/terminal.ts`'s run-address
  helpers.

## Chat

A run's Chat tab renders the agent's own session transcript as a
conversation. Design and measured per-engine mapping:
`docs/superpowers/specs/2026-09-27-mobile-chat-and-dispatcher-home.md`
(slice 1); what was built: `docs/superpowers/plans/2026-09-27-chat-tab-slice-1.md`.

- **`chat/` is standalone**: it imports only the standard library
  (`chat/boundary_test.go` fails otherwise) so it can move to its own module.
  It speaks ACP `session/update` shapes (`chat.Update`: `user_message_chunk`,
  `agent_message_chunk`, `tool_call`, `tool_call_update`) plus `id`/`seq`/`ts`.
  `chat.For(agent)` picks the reader; claude-code (`"claude"`,
  `"claude-code"`) and `"pi"` have one, so codex and cursor have no Chat tab yet.
  `chat.ToolTitle` mirrors `hook.ToolHint`; `hub/chat_title_test.go` pins them.
- **pi mapping** (`chat/pi.go`): `type:"message"` entries count, with
  roles user/assistant/toolResult; `system` and other roles, and other
  non-message entries (`session`, `session_info`, `model_change`,
  `thinking_level_change`), are dropped. `branch_summary` and `compaction`
  entries become a `user_message_chunk` with `_meta.origin` `pi-branch` /
  `pi-compaction` and the summary as content (a `/tree` switch without a
  summary writes no entry, so no divider). Assistant text →
  `agent_message_chunk` with `_meta.messageId` = the entry id, thinking
  blocks dropped, `toolCall` → `tool_call`; a `toolResult`
  pairs by `toolCallId` (`isError` → failed). `Tool()` builds an edit diff from
  the arguments' `edits[]`; `write` gets none. An `aborted`/`error` assistant
  message's tool calls are emitted `failed` (pi never runs them) and an
  `error` message's `errorMessage` becomes an `Error: …` agent chunk.
- **Readers must be chunking-independent**: reading a file in one call or in
  any number of incremental calls yields the same updates. They consume only
  complete lines, never replace an emitted update, and keep no per-file state
  (safe for concurrent use). That is what makes `seq` = the update's ordinal
  from byte 0: the hub ring (`hub/chat.go`, 500 per session) and a scroll-back
  re-read of the file land on the same seqs, across houston restarts too.
- **Epoch**: every cursor is `<epoch>.<seq>`, epoch = a hash of (session id,
  transcript path, hash of the file's first line, ring generation); the raw
  session id never reaches the wire. The generation bumps when the reader
  reports a reset (the file shrank, or the bytes before the cursor changed —
  `chat.Cursor.Pending` fingerprints them) or the path changes; it restarts at
  0 with houston, which is why the file identity is in the hash. A brand-new
  session whose file has no complete line yet resets once when its first line
  lands. A new session in the same pane is a new run `Session`, hence a new
  epoch.
- **`_meta` keys**: `messageId` (on text chunks and tool calls), `tool` (tool
  name — `title` is the human hint), `origin` (`task-notification`,
  `pi-branch`, `pi-compaction`; rendered as a divider), `subagent` (on the
  Agent/Task call's update). The claude reader
  never sets `phase`: the UI treats a text chunk as commentary when
  `_meta.phase == "commentary"` or a later `tool_call` shares its `messageId`.
  Tool input and output never ride the stream; the tool route serves them.
- **Run → session**: `runs.Run.Session` (`json:"-"`) is set by the hooks layer.
  `caps.chat` is true when the key's hooks layer names a Session whose agent
  has a reader, or its crew layer names one (`engine_session`, see "Project and role") for an engine with a reader (`deriveCaps`); the UI offers the tab from it (no probe), and it
  is the default tab when the route names none (`#/fleet/<id>`).
- **Routes** (`server/runs_chat.go`, behind the `/api/` auth gate; none touches
  tmux): `GET /api/runs/{id}/chat?before=<seq>&limit=<≤100>` →
  `{epoch, updates, more}`; `GET /api/runs/{id}/chat/stream?after=<epoch>.<seq>`
  → SSE `updates` batches with `id: <epoch>.<last seq>` (`Last-Event-ID` wins
  over `after`, so a native EventSource reconnect resumes with no gap), `reset`
  then EOF on an epoch mismatch, a cursor the ring can't serve, the run's
  Session changing (checked every 2 s), or the session going away;
  `GET /api/runs/{id}/chat/tool/{callId}` → name/input/output/diff, each of
  output/oldText/newText capped at 16 KiB (`truncated`), an input over 16 KiB
  omitted rather than cut (`inputOmitted`, since a cut would no longer be
  JSON); a
  failed call carries its error output and no diff. Ladder: 503 no registry ·
  404 `no such run` · 404 `no chat`.
- **Real-transcript check**: `HOUSTON_SAMPLES=<dir> go test -tags samples ./chat/`
  replays `<dir>/claude-code/*.jsonl` and `<dir>/pi/*.jsonl` and logs counts
  only; it skips when unset. Never commit transcript content — fixtures under `chat/testdata/` are
  hand-written. `go run -tags tools ./cmd/sessionlog <file>` prints a file as
  ACP JSONL for eyeballing.

## Dispatch

The Dispatch tab and every route in this section exist only in dispatcher mode (see "Modes").

The Dispatch tab starts a worker by running the host's `dispatch` CLI
(`server/dispatch.go`, `server/dispatch_exec.go`), or a new dispatcher through
the launcher described under "New dispatcher". This is remote command
execution from an HTTP request, so the handler is closed by construction:

- `GET /api/dispatch/options` — the repos houston knows (tmux ∪ the repo
  registry, deduplicated by resolved path; a tmux `@git_root` is resolved to its
  main repo), each with `registered` (a registry member), that repo's crews from
  `<git-common-dir>/crew/crews/` and `home` — the subset of `crews` whose
  dispatcher pane lives in that repo. Also `repo_roots` (the configured roots),
  tiers, efforts, plans, the per-engine model allowlist (`engines`, displayed in
  `engine_order`), `tier_models` (the default model per engine+tier), and
  `dispatcher_engines`: `dispatch --engines` intersected with
  `dispatchEngineOrder`, or `[]` plus `dispatcher_engines_error` when that
  command fails, so the worker form still loads.
- `POST /api/dispatch` — `{repo, title, spec?, tier, engine, model, effort,
  plan?, crew, issue?}`. Every argv value is an enum, an allowlisted model, or
  an anchored-regex match; `repo` must be in the options set and `crew` must be
  `"new"` or exist in that repo. The title is the only free text — one argv
  element that may not start with `-`, equal `resume`, or look like an issue id
  (dispatch has no `--`). The task body goes to a 0600 temp file passed as
  `DISPATCH_SPEC` and removed afterwards; it is never logged. The response
  carries `crew` only when that crew exists after the response: the supplied
  or minted id on success (200) and on timeout (504), the minted id on the
  rare exit-0-without-`worker_id` 502, and the supplied (never a minted) id on
  a dispatch-side failure (422).
- `crew: "new"` mints a crew itself rather than requiring an existing one:
  `<unix>-<pid>` (houston's own pid), created under `<git-common-dir>/crew/crews/`
  inside the dispatch slot so only one request can mint per second; a
  same-second collision is 409 (retry). The literal `"new"` never reaches
  argv — `--crew-id` always carries the minted or supplied id. The minted
  directory is removed only when dispatch couldn't be started or exited
  non-zero; it's kept on timeout, on success, and on the rare exit-0-without-
  `worker_id` case. A houston-minted crew has no dispatcher process (no
  `pid`/`pane` files) — its workers report into the bus and Fleet only;
  `crew adopt <id>` attaches a dispatcher to it later.
- One dispatch at a time (429 otherwise), 120 s timeout. The run is detached
  from the request, so a dropped phone connection doesn't abort a half-built
  worktree; on timeout the process group gets SIGTERM (dispatch's trap clears
  its branch lock), then SIGKILL.
- A crew id other than `"new"` is refused by houston itself before dispatch
  runs: well-formed (`<unix>-<pid>`) but not found under the repo is 404,
  malformed is 400. dispatch's own refusals (tier↔model map, budget) come back
  as 422 with its stderr verbatim. The new run then appears in Fleet through
  the crew source — nothing else to wire.
- The model allowlist (`dispatchModels`) and per-tier defaults
  (`dispatchTierModels`) are Go constants; keep both in step with dispatch's
  tier map when models change.
- houston's own `PATH` must reach `tmux` and `dispatch` (exec resolves
  `dispatch` there; a 502 means it could not be started), and a tmux server
  must be running. dispatch and `dispatch --engines` run with
  `PATH=<tmux server's global PATH>:<houston's PATH>` (`dispatchEnv`), so
  `crew`, `git`, an authenticated `gh`, `wt`, `direnv` and the agent CLIs may
  live on either. When the server's `PATH` can't be read, the worker and
  options endpoints `slog.Warn` once per request and keep houston's `PATH`
  alone; the new-dispatcher endpoint fails closed instead (see "Server
  PATH"). Any other missing tool fails inside dispatch and comes back as a 422
  with its stderr.
- `-no-auth` leaves this endpoint enabled: that mode already exposes
  `/api/runs/:id/input` (identity-guarded, but not authenticated), which is
  equivalent command execution.
- `#/dispatch?repo=<path>&crew=<id|new>` prefills the form (repo and crew
  only) from a link; after applying it the UI replaces the hash with
  `#/dispatch`.

### New dispatcher

`POST /api/dispatch/dispatcher` (`server/dispatcher_launch.go`) opens a tmux
window in a repo running the host's `dispatcher` launcher, seeded with the
tasks as its first prompt. Body `{repo, tasks[], engine, model?, effort?}`;
unknown fields 400, body over 256 KiB 413.

- **Order** (`handleDispatcherLaunch`): tmux server `PATH` 503 → engines
  502 → fields 400 (`validateDispatcher`) → repo 404 → unsafe repo path 422 →
  houston exe/launch dir not passable to tmux 500 → slot 429 → crew mint 409 →
  tmux 503/502. A repo-list failure is 502, a launch-file write 500.
- **Fields:** `repo` must be in the options set; `engine` must be in
  `dispatch --engines` ∩ `dispatchEngineOrder`, run fresh with a 10 s timeout;
  `model` is empty or in `dispatchModels[engine]`; `effort` is empty or in
  `dispatchEfforts`. At most 20 entries in `tasks` (blank ones count toward the
  cap), each ≤ 2000 runes after normalization: every run of `unicode.IsSpace`
  becomes one space, then trimmed, so a multi-line row or paste is accepted;
  empty ones are dropped. A normalized task may not contain a `Cc` rune or start
  with `-` (the launcher parses `--agent`/`--model`/`--effort` anywhere in
  argv). `Cf` is allowed (ZWJ emoji, the bidi marks a Hebrew keyboard inserts):
  task text reaches only `execve` argv, never a shell or tmux. A leading `@` or
  `/` is the operator's own prompt. The UI's `normalizeTask` mirrors this; the
  vectors are duplicated in `server/dispatcher_launch_test.go` and
  `ui/src/fleet/dispatcherForm.test.ts` — keep them in step.
- **Prompt:** 0 tasks → none (the launcher's own name); 1 → the task verbatim;
  N → `N tasks: (1) a (2) b` on one line (claude/pi use the prompt as the
  session `--name`, so no newlines). ≤ 8 KiB.
- **Crew:** houston mints `<unix>-<pid>` with an exclusive `os.Mkdir` under
  `<git-common-dir>/crew/crews/` (same mint as the worker's `crew: "new"`; a
  same-second clash is 409) and forces it through the wrapper's `CREW_ID`, so the
  id is known before launch and `crews` lists it at once.
- **No user text in tmux argv.** tmux 3.x splits a command at any argv element
  ending in `;`, format-expands `-c` (any `#`, so `#()` runs a command), and runs
  a single-argument command through `sh -c`. So the window's command is always
  `houston launch-dispatcher <file>` (three elements, never `sh -c`); the task
  text and launcher args ride in a launch file (`{args, crew_id}`), and
  `tmuxSafeArg` refuses (422) a repo path containing `#`, a control rune or a
  trailing `;` before exec. The file is `<status-dir>/launch/<random>.json`, 0600
  in a 0700 dir; the wrapper removes it on read, houston on a failed start, and
  `New` and every launch (under the slot, so files kept on a 502 don't wait
  for a restart) sweep any older than 10 minutes (a wrapper that never ran
  must not leave task text behind).
- **Wrapper** (`houston launch-dispatcher <file>`, `ExecDispatcherLaunch`): sets
  `remain-on-exit failed` on its own pane (best effort, so a failed launch keeps
  its error text for the startup check and the user; a clean exit closes the
  window), reads and removes the file, resolves `dispatcher` with
  `exec.LookPath`, stamps its window by pane id (`@crew_name dispatcher`,
  `@crew_color colour99`), then
  `syscall.Exec`s the launcher — exec, not a child, so `crew register $$` sees
  an ordinary hand-launched dispatcher. Every tmux call is targeted at
  `$TMUX_PANE` with a 2 s timeout, best effort. It runs in the **tmux
  server's** environment, not houston's: `PATH` is the tmux server's global
  one (see "Server PATH") and must reach `dispatcher` and `claude`/`pi` (a
  missing launcher fails in the pane, so it comes back as a 422 with that
  output, never a 502); `HOUSTON_*` and
  service env don't leak; `CREW_ID`, `CREW_WORKER_ID`, `CREW_ROLE_ID` and
  `DISPATCH_SPEC` are stripped (`launchEnv`) and `CREW_ID` set to the minted id,
  so a tmux-global `CREW_ID` can't win.
- **Session:** the session with the most windows whose `@git_root` resolves to the
  repo (tie → name order); else an existing session named after the sanitized
  repo basename (`[^A-Za-z0-9_-]` → `_`); else `new-session`. `has-session`
  failing with anything but `can't find session` (no server, timeout) is 503
  `tmux unavailable` with nothing created — houston never starts a tmux server.
  `new-session` is `-d` (houston has no terminal to attach) and `-E`: without
  it tmux copies the creating client's — houston's service — values of every
  `update-environment` name (`SSH_AUTH_SOCK`, `DISPLAY`, …) into the session and
  marks the absent ones removed, shadowing the server's globals.
  `new-window` is `-d`: the window never becomes the session's current one, so
  a client viewing that session is not switched to the new dispatcher window.
  The launcher stamps its own window by pane id (`-t "$TMUX_PANE"`, dispatcher
  #655 — houston and #655 ship in the same nix-config bump, because a launcher
  predating it stamps untargeted and would relabel the client's current
  window). The wrapper also pre-stamps `@crew_name`/`@crew_color` by pane id
  (belt-and-braces, so the new window carries its tags even if the launcher's
  own stamp is missed), and the new window carries them without being current.
- **Server PATH** (`tmuxServerPath`): tmux hands a pane created by a
  session-less client — houston — that client's `PATH`, over the global
  environment and even over `-e PATH`. So houston first reads
  `show-environment -g PATH` and runs the `new-window`/`new-session` client,
  and `dispatch --engines`, with it (houston.service's own `PATH` has no
  `dispatcher`, `claude` or `pi`). No global `PATH` (absent, `-PATH`, empty) →
  503 `the tmux server has no global PATH`; tmux unreachable → 503
  `tmux unavailable: …`; nothing created, never a fallback to houston's
  `PATH`. The value is never logged.
- **Startup check** (≤ 3 s, 200 ms poll of `display-message #{pane_dead}`):
  success as soon as `<crew>/pid` exists (the launcher passed its own gates and
  registered). A dead pane → 422 `dispatcher exited with status N` with the
  captured `output`, the window (or the session, when this request created it)
  killed. A gone pane — a tmux error matching `tmux.IsGoneMessage` (`can't
  find`, no socket, `no server running`), or blank fields from a live server
  that no longer knows it — → 422 `dispatcher exited immediately`. Either 422
  removes the launch file and the crew dir when it is empty, otherwise returns
  `crew`. Any other check error → 502 `could not check the dispatcher pane: …`
  with `crew, session, window, pane`, keeping the crew dir and the launch file.
  Neither within 3 s → 200, still starting. Other 502s: engines lookup, tmux
  failing to start the window (crew dir and launch file removed), or tmux
  printing no pane (crew and launch file kept, `crew` returned).
- **Slot:** `launchSlot`, separate from the worker's `dispatchSlot` so a 120 s
  worker dispatch doesn't block a launch; busy → 429.
- **Response** 200: `{crew, session, window, pane, run_id}`; `run_id` is
  `runs.PaneRunID(pane)`, so the UI never derives it. Errors use the same
  `dispatcherResponse` shape (`error`, `output`). Task text is never logged.
- Fleet's `project` for the new window comes from `@git_root`, which the tmux-og
  `after-new-window` hook stamps — houston doesn't set it. Without that hook the
  run still gets role `dispatcher` (`@crew_name`) but no project chip. The run
  enters Fleet only once its agent sets `@claude_status` or `@agent_screen`.
- houston's own `PATH` needs `tmux` and `dispatch` (for `--engines`);
  `dispatcher` and `claude`/`pi` come from the tmux server's global `PATH`.

### Repo registry

The set of repos beyond tmux's view (`server/repo_registry.go`,
`server/repos_api.go`).

- **Roots:** `-repo-root DIR` (repeatable) bounds what can be registered or
  picked. Default `$HOME/git` (none, with a warning, when the home dir is
  unknown) — houston runs as a service and cannot infer more; repos elsewhere
  stay reachable through the tmux set whenever a window is open there, and become registrable by adding a `-repo-root` for their parent. Each
  root is made absolute and symlink-resolved at startup; one that doesn't resolve
  is dropped with a warning. No usable root → the picker is empty and adds are
  422.
- **File:** `<status-dir>/repos.json` `{"repos":[…]}`, 0600, temp file + rename,
  capped at 500 (409). A corrupt file is logged and read as empty, and is not
  overwritten until the next successful add/remove.
- **Invariant:** every entry, after symlink resolution, lies inside a root and is
  a git main checkout (`<real>/.git` is a directory and the git common dir is
  `<real>/.git`). The stored value is the resolved path, so a symlink retargeted
  outside the roots fails the next listing. Add runs the full check including
  `git rev-parse` (400 relative or containing a `Cc` or `Cf` rune —
  `dispatchHasControlRune`, checked before any filesystem access — 422 when the
  symlink-resolved path contains one, lies outside the roots, or is not a main
  checkout); listing runs the cheap one (`EvalSymlinks` inside a
  root plus `Lstat(.git)` is a directory), so an options GET never spawns up to
  500 gits. An entry failing it is dropped from `options.repos` but kept in the
  file — a transient unmount must not delete it — and `GET /api/repos` shows it
  `valid:false` so the picker can still remove it.
- **Routes:** `GET /api/repos` → `{roots, repos:[{path,name,valid}]}`;
  `POST /api/repos` `{path}` → 200 `{path,name}` (idempotent); `DELETE /api/repos`
  `{path}` → 204, 404 when absent, never touching the repo itself.
- **Picker:** `GET /api/repos/candidates?q=` → `{roots, candidates:[{path,name,
  registered}], truncated}`. Depth ≤ 3 below each root, hidden directories
  skipped, symlinks not followed, a directory with a `.git` directory is reported
  and not descended into; 10 000 directories visited or 200 results →
  `truncated`. `q` is a case-insensitive substring of the root-relative path,
  ≤ 200 runes (400 on a `Cc` or `Cf` rune, `dispatchHasControlRune`). Registration re-validates fully.
- **Remember-on-use:** a 200 from either dispatch endpoint adds the repo through
  the same path; one outside the roots is silently not remembered, which keeps
  one invariant (every entry is inside a root) instead of a second "tmux once
  listed it" class.
- **No auto-registration at startup:** a service's cwd is `/` or `$HOME`, so
  "houston's own repo" is meaningless, and tmux plus remember-on-use already
  cover every repo the user opens or dispatches into.

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

**Go:** `github.com/gorilla/websocket` and `github.com/fsnotify/fsnotify` (hub state-dir watcher) — the only external dependencies. Everything else is stdlib.

**React:** `@xterm/xterm`, `@xterm/addon-fit`, `@xterm/addon-web-links`, `react`, `react-dom`, `react-markdown`, `remark-gfm`, `remark-breaks`
