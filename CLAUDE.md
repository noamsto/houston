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
  - `POST /api/runs/:id/reply`, `POST /api/runs/:id/restart`, `GET /api/runs/:id/crew/feed`, `GET /api/runs/:id/crew/feed/stream`, `POST /api/dispatch`, `GET /api/dispatch/options`, `POST /api/dispatch/dispatcher`, `GET|POST|DELETE /api/repos` and `GET /api/repos/candidates` are not registered (404): the command-execution surface is gone, not hidden.
  - No `CrewSource` (`runSources`) and no `crewfeed.Store`; the registry order `runs.Order` is tmux, hooks, so nothing reads `<git-common-dir>/crew`.
  - The tmux layer ignores `@crew_name`/`@crew_color` (`tmux.WithoutCrew`): no `role`, no codename or colour, no dispatcher ↔ crew join (so no nested workers or crew line), and the Workspace response carries no `crew_codename`. Role-grid panes (`@crew_role` other than `lead`) are still skipped in both modes.
- `GET /api/mode` → `{"mode":"dispatcher"|"tmux"}` in both modes, behind the auth gate. The UI (`useMode`) fetches it once and retries every 5 s on failure; until it is known only Fleet and Workspace show, so in dispatcher mode Dispatch appears a moment after load. In tmux mode that tab is absent because of the `mode === 'dispatcher'` gates in `MobileShell` / `ConsoleShell`, and `#/dispatch` (with any query) is rewritten in place to `#/fleet` by `tabsFor` via `useShellTab(mode)`; while the mode is unknown such a hash is left alone. There is no Crews tab in any mode: `#/crews` (with any query) is rewritten in place to `#/fleet` at once, mode known or not (the crew roster lives in the dispatcher's detail, see "Crew activity feed"). Adding a dispatcher-only tab means touching both shells.

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
│  POST /api/runs/:id/restart   - Restart dispatcher (D)│
│  GET  /api/runs/:id/crew/feed* - Crew feed, SSE (D)   │
│  WS   /api/runs/:id/terminal  - Run terminal I/O      │
│  POST /api/runs/:id/input     - Send text/key/image   │
│  POST /api/runs/:id/answer    - Answer a prompt       │
│  GET  /api/runs/:id/prompt    - Permission dialog     │
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

Not drawn: hook state files (`houston hook`) and the Claude mod's records (Unix socket `<status-dir>/claude-mod.sock`, see "Claude mod") feed the hub alongside tmux; the mod's fold overrides hook state for a live Claude session.

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
├── answer/              # Claude question/permission dialog parsing + key plans (pane-capture text in, keys out)
├── chat/                # Standalone ACP transcript readers (stdlib only)
├── hub/                 # Session discovery + transcript tracking + chat ring; mod.go overlays the Claude mod's fold
├── mode/                # Dispatcher vs tmux mode resolution (stdlib only)
├── runs/                # Run registry (tmux/hook/crew sources)
├── crewfeed/            # Crew-bus activity feed: events.jsonl fold + incremental store (stdlib only)
├── claudemod/           # Claude Code mod: Unix-socket record ingest + per-session fold; the hub overlays it on hook state (flag -claude-mod, default on)
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

### Load measurement

Synthetic load, never real session content:

- `go run -tags tools ./cmd/loadfixture -dir D` writes a state dir
  (`D/state`) and Claude projects dir (`D/home/.claude/projects`) of synthetic
  sessions and transcripts (`-sessions`, `-scale`, `-seed`), and a
  `D/state/.loadfixture` marker naming the projects dir. Run houston against
  it with `HOME=D/home houston -status-dir D/state`.
- `scripts/perf/startup.sh BIN D PORT [seconds] [mode]` runs an isolated
  instance on a copy of the fixture state and reports startup CPU/RSS, when all
  sessions are listed, list and chat readiness, the `/api/runs` snapshot size
  and latency, and a reconnect storm (concurrent snapshots: wall time, server
  CPU). With `PPROF=<file>` it starts houston with `-debug` and saves a CPU
  profile of the storm there.
- `NODE_PATH=<playwright-core install>/node_modules CHROMIUM=<chromium> node scripts/perf/client.cjs PORT STATE_DIR RUN_ID CHAT_RUN_ID`
  drives headless mobile Chromium (`playwright-core`, 4x CPU slowdown) through
  a local proxy that can stall (no FIN/RST) or reset live connections, and
  reports time-to-interactive for first load, reload and Chat, plus how long a
  sleep and resume takes to deliver live runs and chat updates. It **writes**
  into `STATE_DIR` (hook state files under `claude/`) and appends lines to the
  transcript of `CHAT_RUN_ID`'s state file, so `STATE_DIR` must be a
  loadfixture `D/state`: the script refuses a dir without the `.loadfixture`
  marker and any transcript outside the marker's projects dir. The target is a
  houston started by hand on that dir (`HOME=D/home houston -status-dir D/state
  -addr 127.0.0.1:PORT -mode tmux -no-opencode`); `startup.sh`'s private state
  dir is not reusable. Its header comment has the invocation.
- **Dispatcher-mode fixture:** `-crews N -workers M` on `cmd/loadfixture` also writes `D/repo` (a git main checkout) whose bus (`.git/crew/events.jsonl`: dispatch, status, msg, pr-watch and reap rows, plus a few finished windowless branches per crew) holds N crews of M windowed workers, and `D/crews.tsv` naming the dispatchers and workers (`internal/loadfixture/crews.go`). It leaves `crews/<id>/pane|pid` to the tmux helper, because the join rejects a pane file older than the tmux server.
  - `scripts/perf/crewtmux.sh start D WORKDIR` starts a **private tmux server** (`env -u TMUX -u TMUX_PANE TMUX_TMPDIR=WORKDIR/tmux tmux -f /dev/null`; with `$TMUX` set tmux ignores `TMUX_TMPDIR` and would create the windows on the live server, so the script also refuses WORKDIR/tmux when it is a default socket dir; tmux 3.2+ likewise falls back to `/tmp` when `TMUX_TMPDIR` is empty or not an existing directory, so every call refuses unless WORKDIR/tmux exists) with a dispatcher window and the worker windows stamped (`@git_root`, `@crew_name`, `@crew_color`, `@branch`, `@claude_status`), writes each crew's `pane` and `pid` (the window's `pane_pid`), and prints `TMUX_TMPDIR=WORKDIR/tmux`. `start` sets the server option `@crewtmux 1`; `stop WORKDIR` (and `start`'s failure cleanup) kills the server only when it carries that marker, and creates nothing. Keep WORKDIR short: the socket path must fit a unix socket (`sun_path`, about 108 bytes).
  - `startup.sh` cannot target this server: it always runs on its own fresh, empty `$work/tmux`, so dispatcher mode there sees no dispatcher or worker windows. Start houston by hand on the fixture state: `env -u TMUX -u TMUX_PANE TMUX_TMPDIR=WORKDIR/tmux HOME=D/home houston -status-dir D/state -addr 127.0.0.1:PORT -mode dispatcher -no-opencode`, then run `client.cjs`'s `fleet` scenario against it.
  - `SCENARIOS=fleet node scripts/perf/client.cjs PORT` (same `NODE_PATH`/`CHROMIUM`; it writes nothing) times the Fleet list at 390x844 (`fleet`) and 1280x800 (`fleet-desktop`), both with the 4x CPU slowdown: `tti`, `nodes` (elements under the list container), `cards` (`.run-card`) and `rows` (`.worker-row`). `FLEET_EXPECT` is the count of run cards plus worker rows to wait for (unset: any count that has stopped changing for 500 ms); `SCREENSHOT_DIR` saves `fleet-mobile.png` and `fleet-desktop.png`. Run it against a main-built binary and this branch on the same fixture to compare.

### QA harness

`scripts/qa/` is an end-to-end pass over the ten UI areas (Fleet, navigation, Chat, answering, Terminal, Crew, Workspace, Dispatch, modes, liveness) against real claude and pi agents, run before releases. **Local only**: it needs real claude/pi credentials and quota, so it is not CI. It runs next to the live houston (`127.0.0.1:9090`) and the live tmux server, which must receive nothing; every rule below keeps the two apart.

- `scripts/qa/setup.sh [--no-agents] [S]`: builds the UI and houston, writes a `loadfixture` (12 sessions, 2 crews of 7 workers), starts the private tmux server, scratch repos `$S/repos/qa-repo` and `qa-wt`, shell windows (`wt`, `less`), a claude window (`--model haiku`) and a pi window (`QA_PI_MODEL`), then houston in dispatcher mode, and installs `playwright-core` into `$S/pw` with `npm ci` from the committed `scripts/qa/pw/package-lock.json` (1.57.0 exactly, integrity-checked). `--no-agents` skips claude and pi (enough for Fleet, nav, crew and fixture-only checks). `S` is `mktemp -d /tmp/hqa.XXXXXX` by default and must match `/tmp/hqa.<random>` (a marked `.houstonqa` dir); an explicit `S` must be a real directory (not a symlink) you own with mode 700, a new one is made with `mkdir -m 700`, and every script (`qa_check_root`) refuses otherwise. It prints `PORT=` and `S=` and writes `$S/qa.env` (`PORT`, `TMUX_TMPDIR`, `NODE_PATH`, `CHROMIUM`, pane ids, `CREW1`/`CREW2`). Crew 2 has its `pane` file removed, so it is the orphan crew.
- `S=$S [SCENARIOS=fleet,nav] [SHOTS=dir] node scripts/qa/qa.cjs`: default is all of `fleet nav chat answer terminal crew workspace dispatch modes liveness`, each from `scripts/qa/scenarios/<name>.cjs`. The built-in `guard` self-test (area 0) runs first and proves a foreign HTTP and WebSocket origin is refused. Stdout is one JSON line per check, `{"area","check","result":"pass|fail|skip","note","shot"}`, then `{"summary":{pass,fail,skip},"guard_violations":[…]}`. Exit 1 on any fail or guard violation, 2 on a setup error. Screenshots go to `$S/shots`.
- `scripts/qa/houston.sh S dispatcher|tmux|restart|stop` swaps the houston under test (the `modes` and `liveness` scenarios use it and restore dispatcher mode). `scripts/qa/isolation.sh S [--self-test]` reads content, never mtimes, to prove the live hook state, live tmux panes and `~/.claude/projects` hold nothing of `$S`; run it before and after a pass. `scripts/qa/teardown.sh [--purge] S` stops houston, kills the private server (only with its `@crewtmux` marker), fails if any process still names `$S` or carries `HOUSTON_STATE_DIR=$S/state`, and `--purge` removes `$S`.

Isolation rules:

- **Private tmux server** (`crewtmux.sh start`, `@crewtmux 1`): every tmux call goes through `tp` (`lib.sh`) or `ctx.tmux`, which run `env -u TMUX -u TMUX_PANE TMUX_TMPDIR=$S/tmux tmux -f /dev/null`. With `$TMUX` set tmux ignores `TMUX_TMPDIR` and acts on the caller's server; tmux 3.2+ falls back to `/tmp` when the dir is missing, so both refuse unless `$S/tmux` exists and is not a default socket dir. The one plain `tmux` call is `isolation.sh`'s read-only listing of the live server; with no live server to list, check (c) cannot run and `isolation.sh` exits 2 (`ISOLATION_NO_LIVE_TMUX=1` accepts that and leaves (c) out of the summary).
- **Env scrub** (`qa_scrub_env`, plus `set-environment -gu` on the server): `CREW_*`, `DISPATCH_*`, `DISPATCHER_*`, `CLAUDE_CODE_*`, `CLAUDECODE`, `CLAUDE_PID`, `CLAUDE_EFFORT`, `TMUX` and `TMUX_PANE` are dropped from the script's environment, and the server's global environment loses `CREW_WORKER_ID`, `CREW_ID`, `CREW_ROLE_ID` and `DISPATCH_SPEC` (`set-environment -gu`), because an agent started with `CREW_WORKER_ID` has hookyard post "exited" for the caller's worker on the real crew bus.
- **Hook state**: the server's global env sets `HOUSTON_STATE_DIR` and `HOUSTON_STATUS_DIR` to `$S/state` before any agent starts, so the deployed `houston hook` writes there; `setup.sh` fails unless a claude state file for the pane appears under `$S/state`.
- **Transcripts**: claude and the houston under test run under `bwrap --dev-bind / / --bind $S/home/.claude/projects ~/.claude/projects` (`HOME` unchanged, so credentials, plugins and OAuth refresh stay the single real files). The live houston rescans `~/.claude/projects` every 30 s and would list QA sessions; a hook's `transcript_path` resolves to the scratch dir only inside the bind. pi writes to `$S/pi-sessions`.
- **`--permission-mode default`** for the QA claude: the user's settings default to auto, which would never show the permission dialogs the `answer` scenario needs.
- **Browser guard** (`lib.cjs`): `route('**/*')` and `routeWebSocket` let only `127.0.0.1:$PORT` through (anything else is aborted and recorded in `violations`); service workers are blocked; `lib.cjs` takes `S`, `PORT` and every other `qa.env` key from the file and refuses a differing process-environment value (`NODE_PATH` and `TMUX_TMPDIR` excepted: the file wins), requires `S` to be a marked `/tmp/hqa.<random>` dir, and refuses any `PORT` outside 17470-17499 (so 9090 too; `lib.sh` enforces the same range). `POST /api/dispatch*` is never sent: `page.route` fulfils it in the browser, `ctx.api` refuses non-GET there, and a context-level route aborts and records any such POST that no `page.route` fixture caught (page routes win over it).
- **Token and scratch dir**: the bearer token for `houston.sh`'s readiness probe sits in `$S/auth.hdr` (0600, `curl -H @file`), never in argv, where other local users could read it. `qa_pid_names_root` matches `$S` only as a whole path, so `/tmp/hqa.ab` never matches `/tmp/hqa.abc`; `teardown.sh` still kills the private tmux server and scans when houston will not stop, then exits 1.
- **`crew` CLI against the fixture bus** runs from `$S/repo` with `CREW_WORKER_ID` and `CREW_ROLE_ID` unset and the private tmux env (`TMUX`/`TMUX_PANE` unset, `TMUX_TMPDIR=$S/tmux`): it can nudge and set options on the pane id in `crews/<id>/pane`, which on the caller's server would address a live pane.

Gotchas:

- Claude's workspace-trust dialog starts on "No, exit"; `setup.sh` moves to "Yes" before it sends the first prompt.
- `gtrash` cannot trash under `/tmp` (no per-uid trash dir there), so `--purge` falls back to `rm -rf` of the validated `/tmp/hqa.*` dir.
- The crew `question-live` and `question-composer` checks append to `$S`'s bus and skip on a second run; re-run `setup.sh` for a fresh `$S`. The `workspace` scenario ends by removing `$S/repos/qa-wt`.
- Real-agent checks spend Claude quota: haiku, one-sentence prompts. Keep new prompts short.
- Pass only `S=` to the scripts; never export `qa.env` or setup's `TMUX_TMPDIR=` line into your shell. `tp` and `ctx.tmux` compare `$S/tmux` against the caller's `TMUX_TMPDIR` to refuse a default socket dir, so with `TMUX_TMPDIR=$S/tmux` exported every tmux call is refused, teardown cannot stop the private server, and `isolation.sh` exits 2.

Not covered: WebKit and iOS Safari (no WebKit browser launches on this host), so anything Safari-specific stays manual.

### Vite Proxy

The Vite dev server (`ui/vite.config.ts`) proxies `/api` to `http://localhost:9090`. Change the target port if your Go backend runs on a different port.

## Mobile Features

- **Wide terminal**: fixed-width container (~120 columns), CSS-scaled to fit the viewport
- **Free one-finger pan**: a single finger drags in both axes at once (after an 8px slop) — horizontal movement pans, vertical movement scrolls terminal history; there is no axis lock
- **Pinch-to-zoom**: Two-finger pinch with focal-point tracking
- **Alternate-screen apps** (pi, `less`, vim): the app has no tmux scrollback, so xterm has nothing to scroll. The WS `meta` frame carries `alternate_on` and `mouse_on` (`#{alternate_on}`; `mouse_on` = `#{mouse_any_flag}` and `#{mouse_sgr_flag}`), read by the 1 s meta poll and re-sent when they change. While `alternate_on`, a vertical one-finger drag (mobile) or a wheel turn (desktop) goes to the app over the WS `scroll` message instead of `scrollLines` (`lib/altScroll.ts`; `usePaneSocket.sendScroll(mode, data)`, `mode` `wheel` or `page`): with `mouse_on`, SGR wheel reports `\x1b[<64;X;YM` (up) / `\x1b[<65;X;YM` (down) at the 1-based cell under the finger or cursor, one per 24 px of travel; otherwise PageUp/PageDown, one per 160 px. The `meta` the UI acts on is up to a second old, so the server re-reads the pane (`tmux.Client.PaneScrollState`, one `display-message`) just before sending and drops the scroll unless the pane is still on the alternate screen and not in a mode (copy mode), and, for `wheel`, still tracking the mouse in SGR; an unreadable state or an unknown `mode` drops it too. Typed `input` is not gated. `ControlClient.SendKeys` writes a mouse report with `send-keys -H` so it reaches the app as one sequence. Horizontal pan, pinch and double-tap are unchanged; the normal screen (Claude with real history) keeps xterm scrolling, and the Detached / "↓ Live" logic does not apply to the app's own scroll.
- **Run tabs in the bottom bar** (`MobileShell`, `RunDetail tabsInBar`): while a run detail is open the `.shell-tabs` bar holds a leading Back button (`.run-tabs-back`: `‹` plus the destination, see "Navigation") followed by a `role="tablist"` of the run's tabs (`role="tab"`, `aria-selected`, ≥44 px, same height as the shell tabs), and the top tab row is not rendered; desktop `ConsoleShell` keeps the top row. A run with no tabs (or still loading) shows just Back. Holding Back for 500 ms pops to the shell root (`popToRoot`; the title says so). The bar hides with the on-screen keyboard (`keyboard-open`) like the shell tabs. The old landscape-terminal collapse of the header/bar was removed, since it would have hidden both the tabs and Back. The bar highlights from the route and `caps` alone, so it may differ from the body only for a Terminal route whose capability was lost after going live.
- **Edge swipe back** (`hooks/useEdgeSwipeBack.ts`, mobile run-detail overlay only; `RunDetail edgeSwipeBack`, off in `ConsoleShell`): a touch starting within 24 px of the overlay's left edge that moves rightward and horizontal-dominant (decided after 10 px of travel) drags the overlay with the finger (`translateX`, list showing through) and commits past 38% of the width, or a flick of ≥ 0.5 px/ms over ≥ 40 px, by calling the same `back()` as the header and bottom-bar Back buttons (see "Navigation"); otherwise it springs back. A vertical-dominant or leftward start cancels. The listeners are capture-phase on the overlay and stop an edge touch from reaching descendants, so the terminal's one-finger pan, chat scroll and horizontally scrollable rows never see it; any other start behaves as before (the terminal's pan therefore does not start in the leftmost 24 px). The on-screen keyboard does not disable it (the zone is never a field). `prefers-reduced-motion`: no follow-the-finger, just commit or cancel. Safari's own edge swipe: a `touchcancel` never commits, and a commit is dropped if `location.hash` changed since the touch began, so the two cannot both navigate. If `onBack` leaves the hash unchanged (a swallowed pop), the overlay springs back instead of being stranded off-screen. `.run-detail` sets `overscroll-behavior-x: none`.
- **Detached mode**: horizontal finger movement (≥8px that actually pans, drag, pinch or the column scrubber) or ending a drag scrolled up detaches the view from the live cursor — output no longer auto-pans it, a reconnect reseed with unchanged dims keeps the pan and distance from the bottom, and a "↓ Live" pill appears bottom-centre (a keyboard/container resize still re-snaps vertically). Re-attach = tapping the pill, or a drag that starts scrolled up and ends at the bottom edge; either snaps to the cursor and follows it immediately
- **Composer** (`MobileInputBar.tsx`, docked under the terminal in the run-detail Terminal tab): multi-line field where Enter inserts a newline and Send (or Ctrl/Cmd+Enter) sends the text followed by Enter; an empty Send presses Enter. Both composers share one layout (`ComposerControls.tsx`, `composer.css`): a full-width auto-growing field (about five lines), then one action row with attach, voice (Web Speech API), the `/` toggle and a primary Send on the right (Chat adds Esc). File attach: picking a file only stages it as a thumbnail chip (× removes it), and Send then posts image plus text as one `{type:'image'}` input.
  Both composers (this one and the Chat tab's) keep their draft per run in localStorage under separate keys (`houston-draft:run:<id>` for this Terminal composer, `houston-draft:chat:<id>` for Chat; `useComposerDraft`) and clear it only after a confirmed successful send, even one that lands after the composer remounted; a failed or timed-out send shows the error and keeps the text and any staged image. A draft expires after 24h; expired or malformed entries are swept on first use per page load. For a pane run both composers key by `run.draft_key` instead of the run id (`draftKeyOf`, `runs/draftkey.go`: a one-way hash of tmux server pid + pane id + session id, `omitempty` on the wire, so the raw session id never leaves the server), because pane ids restart at `%0` after a tmux server restart so a draft does not carry over to the next agent that gets the pane id; a run with no pane (no `draft_key`) keeps its run id. When houston knows the run's session id (hooks layer, or crew `engine_session`), a new session in the same pane (`/clear`) starts with an empty composer; a tmux-only run with no session id, or an unknown server pid, is keyed by what is known and leans on the 24h expiry. The staged image is held in memory only, so it is lost when the composer unmounts or the page reloads.
- **Quick keys**: one horizontally scrollable row — Esc, ^C (asks Confirm/Cancel inline before sending), Enter, Tab, Shift+Tab, ↑/↓, 1–5, Y/N, Alt+P, ^O, ^Z, `/copy`. All but `/copy` are keystrokes: `POST /api/runs/:id/input` `{type:'key', key:...}` (400 if `key` isn't in the `terminalKeys` allowlist). No implicit Enter, so a digit answers a numbered prompt without a stray Enter.
- **Quick commands** (`QuickCommands.tsx`, claude runs only — `run.agent` `claude`/`claude-code`; the `/` toggle sits in the action row of both composers, and its pills open above the field, below the quick keys in the Terminal tab): the toggle reveals `/compact`, `/compact…` (prefills the composer with `/compact `), `/clear` (inline Confirm/Cancel first, since it wipes the context) and `/context`, sent as text + Enter. All are disabled unless the run's `state` is `idle` (`quickCommands.ts`; a hook `waiting` is already demoted to `idle`), with the reason in the pill title. A `↳ suggested` pill appears when the very last chat item is a non-commentary assistant message with a fenced block whose body is a single line starting with a slash command (`/name …`; multi-line blocks and absolute paths like `/home/u/x.sh` are ignored; derived client-side from the chat updates, no server support); it opens an editable sheet whose Send posts the edited text, and the pill stays hidden after a successful send until the suggestion changes (and shows again if the same command is suggested later). `/compact…` prefills only an empty composer (a non-empty draft or a staged image is never overwritten or turned into an image prompt; a note asks to clear the draft and attachment first) and then goes through the normal composer Send, which is not idle-guarded. Feedback line: `Sent … — waiting`, then `… accepted` once the run leaves `idle`, or after 10 s `No reaction seen yet` (for `/compact`) / `Sent — this command may not change the run state; check the terminal.` (other commands, which can finish without a state change).
- **Choices**: when the pane `meta.choices` is present, each choice renders as a tappable `n. label` button above the row and answers with its ordinal key.
- **Answering a prompt** (Chat tab, Claude runs; see "Answering from Chat"): a pending `AskUserQuestion` renders as a staged picker with ≥44 px option rows (`--tap`) and one "Send answer" button; the Other text field opens on tap, takes focus and scrolls itself into view (again on every `visualViewport` resize while focused), so it works with the on-screen keyboard open. A permission dialog is pinned above the timeline as `PermissionBar`: the question, a tap-to-expand detail (the command, clamped to 8 lines until expanded), and one ≥44 px button per choice.
- **Keyboard**: `useKeyboardInset` tracks the on-screen keyboard via `visualViewport`; `MobileShell` shortens itself to sit above it and hides the tab bar so the terminal and composer keep the space.

On desktop the Terminal tab takes keystrokes directly (xterm `onData` → WS `input`); clicking it focuses it.

There is no WIDE/FIT toggle — that affordance was removed. A terminal rework
(font-size zoom, possibly more) is in progress; revisit this
section once it ships rather than trusting the bullets above for anything not
already verified in `TerminalPane.tsx` / `useTouchGestures.ts`.

## Navigation

Every shell tab is a hash route — `#/fleet`, `#/workspace`, `#/dispatch` (`ui/src/fleet/routes.ts`, `useShellTab`) — so Back/Forward move between tabs and reload restores the tab. A run detail is `#/fleet/<id>/<tab>`, tab `chat`, `terminal` or `crew`. `#/fleet/<id>/crew` is the dispatcher's Crew tab: offered only on a dispatcher run that carries `crew.name` in dispatcher mode, and that run's default tab when the route names none; on any other run a `crew` route degrades like an unknown tab. `#/dispatch` exists only in dispatcher mode; tmux mode rewrites it to `#/fleet`. `#/crews` (any query) is a retired alias rewritten to `#/fleet` in every mode, including while the mode is unknown (#248).

Navigation is a stack kept in `history.state` (`ui/src/fleet/nav.ts`), so houston's Back, the edge swipe and the browser's native back all land on the same entry:

- **Levels:** a shell tab is the root; a run detail pushes on top of it; a run opened from inside another run's detail (a worker from its dispatcher's Crew roster or activity feed) pushes again. Each pushed entry carries `{houston: {depth, from, root}}`: `depth` counts houston's run entries above the first one it did not push, `from` is the hash it was opened from, `root` the shell-tab hash at the bottom (null when the stack starts on a deep-linked run).
- `openRun(id, tab?)` pushes, except that re-opening the run already shown replaces in place. `selectRun(id)` (desktop list and Workspace picks) replaces laterally when a detail is shown, since the detail is a persistent pane beside the list; choosing the run the current entry's `from` names (the run on screen's parent) collapses the stack with `back()` instead, so `from` never names the run now shown. Crew roster/feed links keep `openRun`. `switchRunTab(id, tab)` replaces: switching a run's Chat/Terminal/Crew tabs (bottom bar, top row, "Reply in Terminal", the question card's Terminal links) adds no entry, on desktop too.
- `back(rootHash)`: `history.back()` when `depth ≥ 1` (a real pop); otherwise (deep link, reload with nothing of houston's underneath) `replaceState` to `from`, else the shell root. It never pushes, so a native back afterwards cannot walk forward into the run just left.
- `popToRoot(rootHash)`: `history.go(-depth)` when the stack sits on a shell root, else `replaceState` to the root. Mobile reaches it by holding the bottom-bar Back, or the header's root button (shown at depth ≥ 2).
- `back` and `popToRoot` are async, so a second call while a pop is in flight is ignored until the guard clears: `popstate` (the usual case), else `pageshow`/`hashchange`, or a ~1 s fallback timer — a traversal that never lands must not wedge every later Back (a double tap would otherwise skip the dispatcher or leave the app).
- Back labels name the destination (`backLabel`): the parent run's codename (else branch), or the shell tab.
- Shell tabs: a tab tap pushes the tab hash (on mobile also closing the detail overlay); re-tapping the tab already shown adds no entry in either shell, and on mobile also scrolls that list to the top. On desktop the detail is a persistent pane, so a rail switch while a run is selected is state-only (no history entry, not restored on reload).
- `pushState`/`replaceState` fire no `hashchange`, so `navigateHash` dispatches one itself; every route hook (`useDetailRoute`, `useShellTab`, `useDispatchRoute`'s `seq`) keeps listening to `hashchange`, which a real pop also fires. In-app links keep their `href` (long-press, copy, new tab) and route a plain click through `openRun`/`switchRunTab` (`onAppLink`).

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

For Claude Code, the mod's fold replaces the hook state's `State`, tool, message and background list while the mod is live for the session (see "Claude mod"); the hook state is the fallback and stays the only source for pi, codex and cursor, and the transcript heal below runs only on that fallback path.

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
is pending: it is the only *hook* signal a denied or Esc-dismissed dialog ever
gets (an interrupt fires no `PostToolUse` or `Stop`), so it heals the card when
it comes. A dialog genuinely left open past `idle_prompt` therefore reads
`idle`, as in base. Any other or untyped type (e.g. `auth_success`) keeps the
state and replaces the message, unless a permission is pending, whose message it
does not displace. `SubagentStop` is a no-op — the main agent is still inside
the `Task` tool — so state, tool and message all stand. `waiting` is only the
hook state value: Fleet maps it to `idle` (see "Needs-you").

**Dismissed dialogs (transcript heal).** A denied or dismissed dialog usually
gets no `idle_prompt` either: Claude Code delays the `permission_prompt` ~6 s
after `PreToolUse` and fires nothing when the user answers "No" (before or after
that Notification). The hub therefore heals the hook state to `waiting` from the
transcript (`Session.applyHook`, recomputed from the raw hook state on every
state load and transcript sync, never a one-way overwrite) when the tool the
state names (`Tool`) has a `tool_use` in the transcript since the last reset and
no `tool_use` is unresolved (the dismissed tool gets an error `tool_result`):
- `waiting:permission` heals on that alone.
- `tool-running` (the Notification has not fired, or later hook events were lost
  so the file is stale) heals only on a `tool_result` newer than the state's
  `Since`: any result for `AskUserQuestion`, a result Claude Code wrote for a declined dialog (`The user doesn't want to…`) for any other tool; an ordinary failed call is not healed, since the file stays `tool-running` after it and the agent is still working.
  A result with no timestamp, or older than the state, never heals, so a call
  that just started is not mistaken for the finished earlier call of the same
  tool; a normal successful tool never reads `idle`.

A dialog with no tool behind it, or a tool still running, stays `permission`.
The tool bookkeeping resets on the hook `Turn` increment and on a transcript
path change. A first read (after a houston restart or transcript switch) folds
the whole tail in one go with no `Turn` signal, so it also drops unresolved
`tool_use`s at each real user prompt (`TranscriptEvent.Prompt`: user text that is
not `isMeta`, an interrupt marker or an injected `<…>` record), keeping resolved
outcomes; an earlier turn's interrupted tool no longer holds a dismissed dialog
`blocked`.

The heal is the fallback: while the Claude mod is live for the session it is off,
because a dismissal or interrupt arrives from the mod as `turn.complete` (see
"Claude mod").

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

The hub's transcript parser is Claude's: a pi session file yields token,
context and cost counts (see "Context meter and pi spend") but no trail.

`SessionState.PID` is the hook's parent process — under hookyard that is the
router, not the agent — and isn't read anywhere in houston.

## Claude mod

`contrib/claude-mod/` is a read-only Claude Code hooks module (loaded with `claude --plugin-dir contrib/claude-mod`; nix-config does not ship it yet, that change is pending). It posts session, turn, tool, permission and background-task records to houston over a Unix socket, and houston folds them (`claudemod/`) into the Claude run's state and background tasks while the mod is live for a session. Hooks plus the transcript stay the fallback and the only source for pi, codex and cursor. Design: `docs/superpowers/specs/2026-10-18-claude-mod-primary-source.md`.

- **Default on.** `-claude-mod=false` disables it. A listen failure (a live socket on that path because another houston serves the same status dir, a non-socket path) logs a warning and houston runs hooks-only, so a dev instance beside the service still starts. With no mod connected there are no records, so no session is ever live and nothing changes.
- **Socket** `<status-dir>/claude-mod.sock`: bound in a private 0700 temp dir beside it, chmod 0600, then `os.Link`ed into place, so it is never reachable at wider perms and an `EEXIST` (another houston won the race) refuses without touching the other's socket; the temp name and dir are then removed. A live socket or a non-socket path is refused, a dead socket is replaced. The mod resolves the same dir at run time (`$HOUSTON_STATE_DIR`, else `$HOUSTON_STATUS_DIR`, else `$HOME/.local/state/houston`), so `-status-dir` must match.
- **Records**: `POST /v1/record`, one JSON object `{v:1, sid, boot, seq, ts, ev, …}` of at most 16 KiB; `seq ≥ 1`, `ts ≥ 1` (ms), `boot` minted per module load (at most 64 bytes; `sid` at most 128: `Decode` rejects longer). Answers 204 (stale records included), 400, 413, 405 or 404, never with a body. Event names and fields: `contrib/claude-mod/README.md`.
- **Mod side**: a hook only builds its record and queues it (at most 256, oldest dropped); a `$.clock.every(200)` timer drains it in order with unawaited `$.http.fetch` to `http://houston.invalid/v1/record` over the socket (the `.invalid` host is never resolved), so a `tool.end` posted from a dispatch the engine abandoned (an interrupt) still goes out. `post` starts the timer with the posting hook's `$` when none runs, and restarts it from the next post once it has been silent for 2 s (a refused period ends an interval). Each record carries the session generation it was built in (bumped at `session.start` and after `session.end`), so a record queued before `/clear` is sent under the old sid. A record leaves the queue only once its sid lookup completes: a failed `$.session.id()` leaves it queued for the next tick, and one whose generation's sid is not known is dropped unsent. A drain stuck for more than 5 s is replaced (a drain run counter stops the old one sending, so nothing goes out twice). `session.end` posts its record and then flushes the queue itself, awaiting a drain within the engine's exit bound, since the timer may not outlive an exit. After 15 s (75 ticks) with nothing queued the timer posts a `heartbeat` (no fields), so an idle session still proves the mod is alive. `session.compact` posts `compact`, and `compact.end` once `next` resolves or throws. Size caps: a Notification `message` is cut to 500 and `stop` tasks to 32; a body over 15000 bytes sheds `ask`, `tasks`, `last`, `input`, `message` in that order until it fits (houston answers 413 above 16 KiB). `task.end` is parsed only at top-level `<task-notification>` boundaries, from the header (before `<summary>`/`<event>`), and only for terminal statuses.
- **Store** (`claudemod.Store`): per session a seq-ordered window (64) of unfolded records, so a late record folds where its seq puts it (`reordered`); a duplicate, a seq at or below what the window already folded, or a record of a retired boot is `stale` (a session remembers at most 16 retired boots; a late record of an older one falls to the `bootDelaySlack` rule). A new boot continues the previous boot's fold (a hot reload keeps the session) and retires it. An unseen boot whose first `ts` predates the current boot's is a delayed record of an older boot only while the current boot was first seen less than `bootDelaySlack` (5 s, houston's clock) ago; later it is a reload whose clock stepped back and replaces the current boot. Only applied records refresh the eviction clock. A session is evicted 24 h after its last applied record (ended or idle alike; the sweep runs at most once a minute inside `Apply` and from the hub's 2 s tick, `Store.Sweep`), and the store holds at most 1000 sessions (the least recently applied goes). An applied record or an eviction marks its sid dirty and signals `Store.Notify()`; the hub's `Run` loop takes `TakeDirty()`, reads each `Store.Session`, and recomputes and broadcasts that session at once, so Fleet sees a permission within milliseconds rather than on a tick.
- **Fold** (`claudemod/fold.go`) separates the main loop's phase from a pending permission. A subagent's records (`agent` set) never move the main phase: their `tool.start`/`tool.end`/`tool.check` track their own calls, and their `permission` record or `permission_prompt`/`elicitation_dialog` Notification raises the dialog.
  - Phase: `session.start` → `starting`; `turn.start` → `thinking` (turns++); `tool.start` → `tool-running` (in-flight id added); a `tool.end` of an in-flight id removes it and returns the phase to `thinking` when no main call is left; an id not in flight changes no phase (it can still clear a matching dialog and start or stop a background task), so a late `tool.end` after `turn.complete` never reopens the turn; `turn.complete` (any reason: answer, abort, refusal, error) → `waiting`; `compact` → `compacting`, remembering the phase it interrupted; `compact.end` restores that phase (`thinking`, `tool-running` or `waiting`, else `waiting`), and a late one after the phase moved changes nothing; `session.end` → `ended`, and while the phase is still `ended` the store folds the whole window into the base, so a later record at or below the highest folded seq is stale while a higher seq still applies. `heartbeat` folds nothing but counts as applied (`Updated`, the eviction clock, dirty), but not as activity (`Active` and `MainUpdated` stay). Two clocks: `Updated` (ms, the newest applied record of any kind) is liveness only; `Active` (ms, the newest applied non-heartbeat record, any agent) is what the view's age comes from, so an idle card ages instead of reading seconds old forever.
  - Permission: a `tool.check` with decision `ask` remembers `(agent, tool, id)` as the candidate (`PermissionRequest` carries no tool id); a `permission` record sets the dialog with its tool, hint and, when tool and agent match, the candidate's id; a `permission_prompt`/`elicitation_dialog` Notification sets it only when none is pending, and supplies the message only to a pending dialog of the same agent (the mod's Notification record carries no `agent`, so its text lands only on a main-loop dialog). A new dialog keeps the previous one's message only when it is the same dialog (agent, tool, id). While one is pending the view state is `permission`, else the phase. It clears on `tool.end` of its id (with no id: same agent and tool; with neither, e.g. an MCP elicitation with no `ask` candidate: a `tool.end` of the same agent that leaves that agent no call in flight, so a parallel call ending first does not clear it), `turn.start`, `turn.complete`, `compact`, `session.start`, `session.end`. `idle_prompt` and every other Notification change nothing: a dismissal arrives as `turn.complete`, so an `idle_prompt` while a dialog is still up must not hide it.
  - `stop` (main): `last` (final 300 characters of the assistant text) is kept and `stopSeen` set until the next `turn.start`.
  - Background: a main-loop `tool.start` of a `bg` Bash or a Monitor records its hint and `ts`; a non-error `tool.end` carrying `task` adds the task (`shell` for Bash, `monitor` for Monitor, whose `expires` is the end `ts` + `timeout_ms`, none when persistent); a non-error `TaskStop`/`KillShell` `tool.end` carrying `stop_task` drops it; `task.end` drops each listed task (the mod lists only notification parts with a terminal `<status>`, so a monitor's per-event line ends nothing). A main-loop `stop` carrying `tasks` is a membership snapshot: a known entry stays whole (the snapshot calls a monitor a shell) if listed `running`/`pending`, else drops; an unknown listed `shell`/`monitor` is added with no expiry; other types are ignored. This catches a UI kill at the next turn end. `tasks` absent leaves the list.
  - `Known`: a main-loop `session.start`, `turn.start`, `tool.start`, `tool.end` of an in-flight id, `permission`, `turn.complete`, `compact`, a `compact.end` that ended a compaction, or `session.end` folded since houston started. `BGKnown`: a main-loop `stop` with a `tasks` array folded since houston started (not `session.start` alone: where an organization manages Claude Code no `stop` arrives, and a UI-killed shell would never clear).
- **Liveness** (`hub.Session.modLive(now)`): live ⟺ `Known` and the mod's newest record (any kind, a `heartbeat` included; `mod.Updated`) is under `modLiveWindow` (45 s) old. The mod posts a `heartbeat` from its timer after 15 s with nothing queued, so an idle session still answers and three missed heartbeats mean it stopped (unloaded, broken by an engine update, a failed reload). The hub's 2 s tick (`tickMod`) re-evaluates every session with mod data, so a dead mod falls back to the hook path within about 47 s with no traffic. Liveness is the mod's own recency and never the hook file's: a late or fresh hook write does not decide it (hook events are detached, so a `post_tool` can land after `Stop` or a prompt before the mod's queued record). An earlier design (hook state ahead of the mod for a 10 s grace) retired a healthy mod on a late hook write and let a stream of hook writes keep a dead one alive. The one hook-side exception: a hook state `ended` written at or after the newest mod record's second ends the mod's say for that session, so a lost `session.end` cannot keep a pane-owning card alive. `session.start` alone is not the test: `/clear` begins the next session with `turn.start`, and a restart sees no `session.start`; a houston restart mid-session therefore reads the hook path until the next state-defining record. Under managed Claude Code the `permission_prompt` Notification has no mod record, so the hook-permission override below is what shows the dialog.
- **Overlay** (`hub/mod.go`; `Session.refreshView` is the one place every view recomputation goes through, so the sources switch together and are never both counted): the mod feeds the same `hub.Session` the hooks layer reads, so `runs/hooksource.go`, the foreign-pane guard, the ended-session disown and the one-key-per-session rule apply unchanged.

  | field | mod live | otherwise |
  |---|---|---|
  | `State`, `Tool`, `ToolInputHint`, `LastMessage` | mod fold (`LastMessage`: the Notification message, else `Claude needs your permission to use <tool>`, only while a dialog is pending) | hook state |
  | `Since`, `UpdatedAt` | mod's last state change / max(hook, mod `Active`) | hook |
  | `Asks` | `questionTail` of the Stop record's `last` once a main-loop `stop` was seen this turn, so it shows before the transcript tail | transcript |
  | `Background` | mod list once `BGKnown` | transcript `bgTracker` |
  | dismissed-dialog heal | off (`turn.complete` covers dismissals and interrupts) | on |

  Trail, tokens, context, spend and chat stay transcript-derived for every session (the `turn.complete` usage sums the turn's requests, so it is not the context size). A session with no hook state (mod only) gets cwd, transcript path, pane and `tmux_server` from the mod's records (`session.start`, `turn.start` and every classic record carry `pane`/`tmux_server`, read once from `$TMUX_PANE`/`$TMUX`), `UpdatedAt` from the newest non-heartbeat record's `ts` (`Active`), and `Turn` from its main `turn.start` count (which also resets the trail); the hub creates a `claude` session for it and tails its transcript, so `paneForeign`/`paneGone` work on it as on a hook session. Eviction drops the overlay, and deletes a session known only from mod records (as a removed state file is), so `HookSource` emits `Gone`.
- **Hook permission wins** while the mod reads `tool-running` and the hook state is `waiting:permission` written at or after the newest main-loop mod record's second (no `agent`, not a `heartbeat`, which advances only the liveness time; a subagent's records must not hide the main loop's dialog): where an organization manages Claude Code, a user-installed plugin's classic hooks do not run, the mod sees no `permission`/`Notification`/`Stop`, and only the `permission_prompt` Notification names the dialog. It applies only while the mod is live. Once the mod moves on (`tool.end`, `turn.complete`) it is newer and wins again. A hook permission while the mod reads `waiting` or `thinking` is ignored.
- **Residuals**: after a houston restart the first known-state record folds without the in-flight ids of earlier calls, so a `tool.end` of such a call is ignored and the phase can read `thinking` while a parallel earlier call still runs, until the next main-loop record. A mod that dies between heartbeats can be trusted for up to about 47 s after its last record. A reload of the mod continues the previous boot's fold. Where Claude Code is managed without classic hooks there is no `Stop` record, so `Asks` stays on the transcript and `BGKnown` never turns true (background stays on the transcript fold). After a permission is approved no record fires until the call's `tool.end`, so a long approved tool reads `blocked` while it runs.
- **Privacy**: beyond ids, names, states and counts, the records hold the last 300 characters of the final assistant message, tool-input hints (≤ 200 characters per field), the cwd and transcript path, the tmux pane id and server pid, AskUserQuestion questions, option labels and whether an option has a preview, background task descriptions (≤ 200) and the Notification message. No prompt text and no tool output. Records go only to the local socket.
- **Read-only guard**: every hook is `($, e, next)`, passes its own `e` to `next` and returns `next`'s result; the only calls on `$` are `$.http.fetch` (never awaited, errors swallowed, houston's answer never read), `$.env.get`, `$.session.id` and `$.clock.every`. `claudemod/modguard_test.go` scans `register.ts` and fails CI on: a forbidden API or name; a `$.noun.method` call outside those four (every allowed call must also be found, so a scan that goes blind fails); bracket access to `$` or `$` aliased or destructured; a bare `$` other than a hook-head parameter, a `$: EngineInterface` parameter, or an argument to a function the file declares with `$: EngineInterface` at that position (so `$` cannot reach an untyped helper); an `EngineInterface` parameter not named `$`; a hook not shaped `on('name'[, matcher], ($, e, next) => …)`; a `next(` argument other than `e`; a spread or property write of `e` or of a variable bound from `next(e)` (`const`/`let`/`var`, or a plain assignment, parenthesized or not, awaited or not; one level of alias through `await <that name>` is followed); a `.then`/`.catch`/`.finally` on `next(e)` or on a bound name; a comma right after `next(e)` (a sequence expression discards the result); `Object.assign`/`defineProperty`/`defineProperties` or `Reflect.set`/`defineProperty` on `e` or a `next(e)` result inside a hook; a hook returning an object literal (parenthesized or not); a hook that never calls `next(e)`. Comments are stripped and `?.`/`!.` normalised first; strings are not exempt. It is a tripwire, not a proof: it cannot see an indirect rewrite through a helper or an alias of `e`, so review keeps the contract, and the call list `claude plugin validate contrib/claude-mod` prints is authoritative.
- **CI and local check**: the CI job `mod` runs `just mod-check`, the one place the version lives (`@anthropic-ai/claude-code@2.1.302` through `npx -y`, the version the mod is written against; neither step needs credentials). The recipe runs `claude plugin validate contrib/claude-mod` and fails when its `calls:` line names any call other than `$.clock.every`, `$.env.get`, `$.http.fetch`, `$.session.id`, then `claude plugin test contrib/claude-mod`.
- `GET /api/debug/claude-mod` (registered only while the listener is up, behind the auth gate) shows the store as `{"sessions": [...]}` (state, `known`, `bg_known`, in-flight ids, `applied`/`stale`/`reordered`).

## Background tasks

A Claude Code run's outstanding background shells and monitors ride on `Run.Background` (`[{id, kind: "shell"|"monitor", hint, since}]`), shown as a `N bg` chip on the Fleet card and a list in the Chat status strip (`RunStatusStrip`). They are informational: they never change `State`. A turn that ended with a shell still running keeps its hook/tmux verdict (`idle`/`blocked`), because `State` describes the agent's turn, `blocked` is reserved for a human being required, and `running` would claim the agent is working.

- **Mod source**: while the Claude mod is live for the session and its list is known (a main-loop `stop` with `tasks` folded since houston started; `claudemod/fold.go`, see "Claude mod"), the list comes from the mod and the transcript fold below is not consulted. The two are never unioned: the switch is per view computation, so after a houston restart the transcript's list stands until the next turn end, and a UI kill clears at that `stop`.
- **Otherwise the source is the transcript**, folded by `hub/background.go`'s `bgTracker` inside the hub's existing 2 s tail. It folds from byte 0 on every houston start, so it is restart-safe with no persisted state, and needs no tmux capture. The first read of a transcript (`hub/initialread.go`) does not parse every line: it parses only a tail (256 KiB, doubled until it holds enough to determine trail, asks and token counts; once the tail would pass half the file, the whole file is read instead; a pi file, which has no `tool_use` events, is always read whole) and folds background tasks, the context peak and pi spend over the bytes before it with a pre-pass that decodes only lines containing a substring one of them must contain (`run_in_background`, `Monitor`, `TaskStop`, `KillShell`, `task-notification`, the id of a still-pending start/stop, or `"usage"`), then applies the tail's events in file order; the result equals a full replay. Hooks were rejected (no event fires on completion or on a UI/timeout kill, and the hookyard manifest would need new subscriptions); the pane's `N shell still running` text was rejected (a capture per poll, no per-task detail, nothing for monitors).
- **Start**: a `Bash` tool_use with `run_in_background: true`, or a `Monitor` tool_use, whose non-error tool_result names the task id (`with ID: <id>` / `(task <id>,`). A failed launch is never tracked.
- **End**: a `<task-notification>` carrying a `<status>` (completed/failed/killed/stopped), matched by every `<task-id>` and `<tool-use-id>` in it (a resume's orphan summary lists many ids), read from either the `user` record or the `queue-operation` enqueue copy; or a non-error `TaskStop` result. A notification with no `<status>` is a monitor's per-event line and ends nothing.
- A non-persistent `Monitor` times out (`timeout_ms`) with no transcript marker, so the tracker expires it itself (`list(now)`; the hub re-checks on every 2 s tick even when the file did not grow). Shells have no such lifetime.
- `ReadTranscriptFrom` stops at the last complete line: consuming a half-written start/end record would lose it for good.
- Claude only (`agent == claude`); dropped when the run is `done`/ended. Not covered: background `Agent`/Task subagents, and anything started inside a subagent's own transcript. A session whose Claude was killed without a notification or `SessionEnd` (while its pane stays open) can show a stale chip until the run ends; shells that outlive a `/clear` belong to the new session id and go uncounted.

## Context meter and pi spend

`Run.Context {used, limit?}` and `Run.SpendUSD` (pi only) feed the Chat status strip (`RunStatusStrip`: `142k / 200k ctx`, a percentage bar when the limit is known, `$x.xx` for pi). The Fleet card does not show them: it is already dense and the meter belongs with the session detail.

- **Source is the transcript**, in the hub's existing tail (`hub/transcript.go` `parseLine` → `applyTranscriptEvent`), no extra file pass. `used` is the latest assistant message's uncached + cached input: Claude `input_tokens + cache_read_input_tokens + cache_creation_input_tokens`, pi `usage.input + cacheRead + cacheWrite`. The latest wins (it shrinks after a compaction), never the maximum. A pi aborted message has usage but no content blocks; it still counts.
- **Limit** (`hub/context.go`): a small model-id prefix table (Claude families 200k) plus `[1m]` ids at 1M. An unknown model, or a window smaller than the session's peak use (a 1M run reported under its base id), has no limit; houston never guesses one. The limit is checked against the session's peak use, not the latest, so it does not return after a compaction. Residual: Claude Code writes the API model id with no `[1m]`, so a 1M session reads against 200k (an over-full bar) until it first passes 200k, then shows no limit.
- **Spend**: pi only, the running sum of each message's recorded `usage.cost.total`. Claude records no cost and houston keeps no price table, so Claude runs have no `spend_usd`. A first read folds the sum and the peak over the bytes before its tail (see "Background tasks"), so both are restart-safe.

## Project and role (Fleet)

Every run can carry `project` and `role` (`runs/project.go`, `runs/tmuxsource.go`, `runs/crewsource.go`).

- **`project`** is the main repo's name: `git rev-parse --git-common-dir` of the window's `@git_root`, so a linked worktree resolves to its main repo rather than its own directory name. Crew-bus runs derive it from the bus directory (`<common>/crew`) with no extra git call. A window whose `@git_root` no longer exists (a reaped worktree whose window stayed open) gets no `project` from tmux (never the worktree's leaf name; a crew worker takes it from the bus, others fall back in the UI to `repo`, which for them is still the leaf). Hook-only runs (no tmux window) get no `project`; the UI falls back to `repo`, which `repoAndBranch` already strips of the worktree leaf.
- **Preview line** (`Activity.Task`, from lazytmux's `@claude_task`, else the window task; tmux layer, both modes) skips injected turns (`isInjectedTurn`: a leading `<tag …>` envelope such as `<agent-message>`, `<task-notification>`, `<system-reminder>`, or `[Request interrupted`). An injected `@claude_task` falls back to the window task, else the card shows its PR or state rather than markup. A human prompt that itself starts with an HTML-like tag is filtered too. pi has no equivalent feed.
- **`role`** is `dispatcher` when the window's `@crew_name` is the literal `dispatcher` (the dispatcher launcher, `adapters/core/dispatcher.sh`, sets it), `worker` for any other non-empty `@crew_name` and for every crew-bus run, and absent for a solo session. A dispatcher started any other way reads as solo. `worker` never overwrites `dispatcher` when layers merge. Tmux mode sets no `role`: the tmux layer ignores `@crew_name` and there is no crew bus.
- Hook-only runs (`runs/hooksource.go`) resolve `project` from the state file's `cwd` via the git common dir, and only when git actually names it — the first non-empty `Project` across layers wins, so a hook `cd` never overrides the tmux layer's.
- **Ghost hook runs:** a hook state file whose pane is missing from a *successful* `ListPaneOptions` is published as `done` (kept in history, `caps.terminal` false); a failed listing never ends anything. Hook activity newer than that verdict marks the session alive elsewhere (another tmux server) and it is never ended again.
- `hub` prunes an ended hook state file `hub.DefaultPruneTTL` (24h) after its last update; a file whose last-written state is not `ended` — including a ghost session's — is never pruned by this pass.
- **Foreign panes:** tmux pane ids are unique only within one server incarnation and restart at `%0` after a server restart, so a hook state file can name a pane that now belongs to someone else. Every hook event re-reads `$TMUX_PANE` and the server pid from `$TMUX` (no exec) and recomputes the coordinates when they disagree with the file or on `SessionStart`, recording the server pid as `tmux_server`. `runs/hooksource.go` then distrusts a pane whose recorded server differs from the one houston is listing, or whose state predates that server's start time (which covers files written before `tmux_server` existed): the coordinates are dropped before the run is keyed, so it keys off its session id, carries no `Tmux` ref or terminal/reply/kill caps, and is ended into history under the same revive rule as a vanished pane. The server identity rides the same `list-panes -a -F` listing that produces the pane ids (`#{pid}`/`#{start_time}` in `paneOptionsFormat`), so the two can never disagree about which server minted a pane; a failed listing publishes nothing and leaves earlier verdicts standing, and a tmux that cannot expand those fields logs once and falls back to trusting the recorded pane. The same normalize step disowns a session whose hook state is `ended` even when its pane is live and same-server: tmux hands that id to the next occupant the moment the pane is reused, and a session that announced its own end can never prove it is still there, so it too keys off its session id with no `Tmux` ref or terminal/reply/kill caps (#120). `last_message` is cleared by every hook event except `Notification` and `SubagentStop`, and the hub only surfaces it while the state is waiting or permission (`runFromSessionView` publishes it as `Activity.Message` only for a `blocked` run).
- **Point-of-use server check:** the foreign-pane guard above is sound only for the listing it ran against — `HookSource` polls every 5s, so a tmux restart landing right after a successful listing leaves a window where a cached `Tmux` ref still matches the pre-restart server while the new server has already reused the same pane id. `runs.TmuxRef` and `tmux.Pane` both carry the tmux server pid (`Server`, internal only — `json:"-"`, never reaches the runs JSON/SSE API) alongside the pane id, and `server/runs_terminal.go`'s `runPane` re-resolves and compares it on every send/terminal-attach, refusing with 409 when both the run's recorded server and the freshly resolved pane's server are known and disagree. Either side being unknown never refuses on its own — same "unknown ⇒ false" posture as the foreign-pane guard. This closes the race for the run-addressed routes below. An already-open run-route WebSocket re-verifies too: it re-resolves the pane at open and again after every control-client reconnect, gated on the client's generation counter (`server/pane_ws.go`, `tmux.ControlClient.Generation`), and WS input is dropped until that check passes. A gone pane or a different server closes the socket with 4409, which the UI shows as ended without retrying; an unverifiable resolve closes with 1011 instead. Only an unknown server on the resolved pane falls back to trust (unknown ⇒ allow, as in `runPane`). `metaPollLoop`'s `meta` frames stay ungated.
- **Needs-you (`runs/state.go`):** `blocked` is the only state meaning a human must act now — it drives the badge, the sort order, and `NeedsAttention()`. A turn end alone is never `blocked`: a hook `waiting` (`Stop`, `idle_prompt`, pi's `agent_settled`, codex/cursor turn ends) maps to `idle` (`FromHookState`), as does a hook `starting` (`SessionStart` — a fresh, `resume` or `clear` session before its first prompt runs nothing; `UserPromptSubmit` moves it to `thinking`; a `compact` SessionStart is recorded as `thinking`, since an automatic compaction fires it mid-turn), and lazytmux's `@claude_status` `done` (written on `Stop`) is `idle` too, while `denied` (the auto-mode classifier's refusal; the turn continues) is `running`. A role-grid `@crew_role` pane is skipped by every layer (tmux/crew since #99, the hooks layer since #143, which gates its `@crew_role` lookup on the same not-foreign test `normalize` uses), so a critic pane never becomes its own card (#111). A grid lead's `@crew_role=lead` marks the worker itself and is not skipped (#179). The only sources of `blocked`:
  - `hook.StatePermission` — a `permission_prompt` or `elicitation_dialog` Notification (the dialog's message is the `Question`), unless the hub heals it from the transcript (see "Dismissed dialogs"). While the Claude mod is live, its fold replaces this state (see "Claude mod"): the `permission` record (the engine's PermissionRequest) makes the run `blocked` about 6 s before the hook path's Notification, and a Notification record arrives with the hook's own Notification.
  - A Claude `PreToolUse` of `AskUserQuestion` (blocks until answered; `PostToolUse` resumes), with a fixed `Question`, also while its own `permission_prompt` Notification has put the hook state in `permission` (the `Tool` stays `AskUserQuestion`; `ask` is ordered before `dialog`), so from the hooks layer only a permission or elicitation dialog for another tool is `dialog`.
  - A turn end whose last assistant text ends in `?`: the hub tracks `SessionView.Asks` (the final line of the last assistant text block, markdown `*`/`_`/`` ` `` trimmed; reset by any later tool_use, tool_result or user text) and `runFromSessionView` turns a hook `waiting` with `Asks` set into `blocked` with `Question{Via: "pane"}`. Applies to engines with a hub transcript reader (Claude, pi). Two edges: a question followed by a closing line ("Let me know.") is missed, and a rhetorical closing question ("Hope that helps — anything else?") is flagged. The transcript tail lags `Stop` by up to 2 s, so the card can read `idle` briefly first; while the mod is live the `Asks` line comes from the tail of its `stop` record's `last`, so the card does not lag. `Activity.Message` is published only for a `blocked` run.
  - tmux `@claude_status waiting` (a permission/elicitation dialog), and a crew watchdog `prompt:` status (an interactive prompt a human clears at the pane, via `pane`).
  - A crew worker's own `crew status … blocked "<question>"` (non-watchdog) is addressed to its dispatcher, so `runs/crewsource.go` (`routeCrewQuestion`) keeps it `running` with no `Question` (the text stays in `Crew.Detail`) while the dispatcher is alive and the blocked episode is younger than `crewDispatcherSilence` (15 min); otherwise it is `blocked` with a `crew` `Question`. Dispatcher alive ⟺ the crew joins its dispatcher pane (`dispatcherLive` over `joinDispatchers`, see "Dispatcher ↔ crew join"); a houston-minted crew (no `pane` file), a pane file left over from an earlier tmux server, a dead or recycled dispatcher pid and a dispatcher started without the stamp all read as gone, so such a crew's worker questions surface as needs-you at once instead of being held for `crewDispatcherSilence`. The episode starts at the first `blocked` status after the last non-blocked status, watchdog status, or dispatcher reply.

    `Question.Kind` (`turn`|`dialog`|`ask`|`crew`, `omitempty`) says what raised the question: `turn` is the `?` turn end, `dialog` a permission/elicitation dialog or crew watchdog `prompt:`, `ask` an `AskUserQuestion`, `crew` a worker's bus question. Chat omits `RunQuestion` for `turn` (the transcript bubble and the `needs you` chip already show it and the composer answers it); the Terminal tab and the RunDetail header keep it, and Chat still shows the other kinds. Kind rides `runSignature` but changes no state, attention or text.

  **Attention.** `Run.Attention` (`needs-you|stuck|done|""`, plus a houston-worded `attention_note`) is computed once in `composeLocked` (`attentionOf`, `runs/registry.go`) from the composed state plus the tmux and crew layers, and rides `runSignature`. Only the crew layer publishes an input `Attention`/`AttentionNote` (its watchdog verdict); `mergeInto` never copies them, so an `Attention` set by any other layer is ignored. `needs-you` ⟺ `State == blocked`. `stuck`: the composed state, the tmux layer (`@claude_status error`, even when the hooks layer wins `State`) or the crew layer is `failed`, or the crew layer carries a stuck watchdog prefix. The note is the crew layer's own when the crew layer is failed or stuck (empty for a worker's own `failed`, whose reason is in `crew.detail`); otherwise, for a composed or tmux failure, "Its last turn failed — check its terminal.". `done`: the composed state is `review`, or the crew layer is `done`/`pr_open`, taken from the crew layer even when a higher layer's state (hooks `idle` after the worker's final Stop) wins `State`. A crew watchdog `blocked` is liveness bookkeeping; its prefix (`runs/crewsource.go`, `watchdogPrefixes`) decides, and the watchdog's own detail is dispatcher-facing and never shown raw:

  | watchdog prefix | state | attention |
  |---|---|---|
  | `prompt:` | `blocked` + `pane` `Question` | needs-you |
  | `quota:`, `turn-stall:`, `quiet:`, `stalled:`, `runaway:`, `unread:`, `budget:` | `running` | stuck, with a per-prefix note |
  | `load:`, unknown | `running` | none (host-level / unknown: no claim) |
  | `failed` (`dead:`) | `failed` | stuck, note "Its engine stopped — the watchdog marked it failed." |

  **UI.** The header badge counts fresh (≤ 1 h) `blocked` runs (`needsYou` in `ui/src/fleet/staleness.ts`): `NeedsAttention()` plus a freshness gate — the same set as `attention === 'needs-you'`, because needs-you ⟺ blocked. Filters: Active · Needs you · Stuck · Done · All; Active/All sort needs-you > stuck > done > rest, each by recency, and a stale one ranks as rest. Stuck and done cards get a calmer border and chip, never the needs-you red; a stale stuck or done card keeps a muted border, chip and note. An **ended** run (`state === 'done'` with no attention: `SessionEnd`, ghost, foreign) leaves Active immediately and is reachable under All; a `done` run that also carries `stuck` attention is not ended, and a crew-`done` worker (attention `done`) stays.

  **Residuals.** (With the Claude mod live, a denied or dismissed dialog and an interrupt arrive as `turn.complete`, so no transcript heal runs and the `idle_prompt` residual below does not apply: `idle_prompt` does not hide a dialog the mod still sees. The approved-tool residual below still holds.) After a permission dialog is approved no hook fires until the tool's `PostToolUse`, so a long approved tool reads `blocked` while it runs (lazytmux has the same gap). A Claude too old to send `notification_type` loses its permission flag. A dialog left open past `idle_prompt` reads `idle` (the notification that heals a dismissed dialog cannot tell it from one still on screen). The transcript heal rests on tool bookkeeping from the main transcript, so it can read `idle` for up to one 2 s tick while the hub's tail lags a freshly raised dialog, and a background `Agent`'s own dialog (its calls live in a sidechain transcript) can be healed when the main transcript holds a finished tool of the same name. A foreground subagent's dialog dismissed with "No" is not healed (its `Task` `tool_use` stays unresolved in the main transcript). A `tool-running` state whose later hook events were lost heals (for `AskUserQuestion` only; a lost `PostToolUse` of another tool reads `running` until the next event) to `idle` even if the agent is still mid-turn (the next hook event corrects it), and only once the transcript tail has read the result (up to 2 s). A crew worker whose own turn ends on a `?` reads needs-you even while its earlier bus question is routed to a live dispatcher — intended: a stopped turn can only be resumed at the pane. A crew branch's `done`/`failed` layer is evicted `crewEvictionGrace` (10 min) after its last bus status, after which a joined pane's card shows its hooks/tmux state and loses `stuck`/`done` attention. A pane-keyed (joined) run is not dropped but reduced to its identity (`identityOnly`: crew id, codename, title, tier/model, project, branch, PR; no state, attention, question, `crew.detail` or timestamps), so a finished worker stays nested under its dispatcher (and in its `N workers` line and Crew roster, bucketed by its hooks/tmux state) for as long as the terminal pane join holds (the pane still listed and untouched since it finished; a new turn in it fails the join and the run is evicted whole); a windowless (`crew/…`-keyed) run is evicted whole. The identity layer keeps `Agent`, so in the one tick after its pane vanishes the key can list briefly with no state (as any crew layer can). A branch whose latest status is `reaped`, or that has a `reap` row after its last revival, is skipped at once (no grace) until a later `dispatch`/`resume` row (which also clears its state, detail and PR) or any other status revives it; `FromCrewState` maps `reaped` to `done` only defensively, since `scan` never lists it. A windowless branch (no single joined pane) is also dropped once its newest dispatch/resume/status row is older than `crewStaleAfter` (6 h), unless its crew's dispatcher is joined on any bus (alive) or it is `blocked` after routing (`crewRunAbandoned`); a pane-joined branch is never dropped by age.
- **Crew-layer pane join:** `runs/crewjoin.go`'s `resolvePane` joins a crew-bus branch to its one candidate pane. A candidate is any pane carrying `@claude_status` (a Claude run) **or** `@agent_screen` (agent-detect's stamp for pi, codex and cursor), excluding role-grid panes (any `@crew_role` other than `lead`) and plain shells. When the branch's latest bus status is terminal (`done`/`failed`) it only joins if the pane looks like that same finished session: its own activity epoch — `@claude_status`'s 2nd field if present, else `@agent_screen`'s — is within `terminalJoinGrace` (5 min) of the terminal record, and its state word says the session finished (`done`/`idle`/`error` — the `@claude_status` vocabulary; `error` maps to the internal failed state — for a Claude pane, `idle` for `@agent_screen`). The grace exists because the worker posts `done` and *then* ends its final turn, so the Stop hook stamps the pane a few seconds later (observed +4s/+13s); an actively working pane (processing, or waiting on a prompt) is a new occupant even inside the window, and an unknown epoch (0) refuses the join. A third condition guards the pane's identity, not just its epoch: the pane's foreground engine — the tty's foreground process-group leader, reached from `#{pane_pid}` (procfs on Linux, `/bin/ps` elsewhere, since houston's service PATH has no `ps`) — must have started within `[s<epoch> − 30s, record ts]`, where `s<epoch>` comes from the latest status record's worker id `worker:<branch>#s<epoch>-<pid>`; an unknown start time or a bare worker id (no `#s<epoch>`) refuses the join. A fourth condition is engine-session identity: when the branch's bus record carries `engine_session` (`Run.CrewSession`, on dispatcher#652 dispatch/resume rows) and the pane's current hook session id is known, the two must match. The pane→session map comes from the hooks layer's own view (`paneHookSessions` over `hub.Snapshot()`, filtered by the same `paneForeign`/ended/role-pane tests), so a `/clear` or `/resume` in the same engine process starts a new session id the process identity can't see and the stale terminal record no longer joins. Either side unknown — an older dispatcher row, codex/cursor's null `engine_session`, or no hook state for the pane — keeps the process-start rule exactly. Residual: the bus row keeps its pre-`/clear` `engine_session` until a `resume` row replaces it, so a worker that cleared mid-run and then finished splits into a fallback bus card plus the hooks card instead of joining.
- **Dispatcher Crew tab (`CrewTab`, #231, #248):** the dispatcher run's detail tab replaces the retired Crews tab: a crew header (repo name, short crew id, counts line, `Dispatch here` when `useCrewRepos` resolves the repo), the roster, then the activity feed (see "Crew activity feed"). The roster is the crew's workers (same `crew.name` and host; fresh needs-you first, then live, then history, each by recency). Each member row mirrors `crew roster`: `crew.title`, a `tier · engine · model` line (`crew.tier`, `run.agent`, `crew.model`), `crew.detail` (the live phase, clamped, with the run's age), the PR chip, and `N sessions` when `crew.sessions` > 1; a `via: crew` question carries its `ReplyComposer`, a `pane` one a hint to answer in the terminal. `crew.sessions` counts the branch's bus `dispatch` + `resume` rows (`runs/crewsource.go`). The row border, chip and the counts line (`working · needs you · stuck · done`, then `idle` and `ended` when non-zero) bucket on `run.attention` (`bucket` in `ui/src/fleet/crewsModel.ts`), flags gated on freshness like the Fleet badge (a stale flag counts as `ended`). A run with no attention counts by state: working, `idle`, or `ended` (state `done`), never `done`. An unflagged idle row carries an `idle` chip, and live tool activity outranks `crew.detail` for the phase line only while the run is working.
- **Codename, colour and home repo from the bus:** the tmux layer's `@crew_name`/`@crew_color` give `crew.codename`/`crew.color` for a worker with a window and win when present (the field-wise merge keeps the first non-empty, tmux merging first). A worker with no window takes them from the crew bus instead: the branch's last well-formed `dispatch` row (`name` matches `^[a-z][a-z0-9-]*$`, `tmux` matches `^colour[0-9]+$`, the crew CLI's own rule), the colour via `tmuxColorToHex`; no CRC-pool fallback, and a branch with no well-formed row has neither. `crew.home` is the project of the one resolved bus holding `crews/<crew id>/` (`crewHomes`), set on every worker of that crew whichever bus it reports on; a crew dir in several buses, or in none, leaves it unset.
- **Crew-named chat session (#184):** a `dispatch`/`resume` bus row carries `engine_session`, the engine's own session id (a uuid for claude/pi, `null` for codex/cursor, absent on an older dispatcher's rows — all three read as today). The newest such row per branch lands on `Run.CrewSession` (internal) and, when no hooks layer names a `Session`, on the composed `Session`. `deriveCaps` grants `caps.chat` from it when the *bus record's* engine has a chat reader, with no pane identity involved, so the #120 ended-session disown is untouched: a worker whose Claude exited back to the shell keeps its Chat on the worker card. The registry then hides the session-keyed `sess-<id>` history card (`shadowedLocked`) while any other key's crew layer names that session, and shows it again once that layer is gone.
- **Dispatcher ↔ crew join (#248):** the dispatcher run does not know its crew id, so each crew-source tick `runs/crewdispatch.go`'s `joinDispatchers` maps every `crews/<id>` of every resolved bus (also one whose log names no branch yet) to its dispatcher pane. Crew C joins pane P when all hold:
  - `crews/C/pane` names P, and P is in the tick's listing with an engine status (`@claude_status` or `@agent_screen`), is not a role-grid pane, and its window's `@crew_name` is `dispatcher`.
  - Server identity: the pane file's mtime is not before the listing's tmux server start (an older file names a pane id an earlier server minted; an unknown start passes, as in `paneForeign`).
  - Process: when `crews/C/pid` exists and parses, that process is alive (signal 0; EPERM counts), did not start more than 2 s after the file's mtime (`pidRecycleSlack`, a recycled pid) and is P's `pane_pid` or a descendant (≤ `maxPPIDSteps` = 32 parent hops; procfs on Linux, `/bin/ps` elsewhere). An unknown start time, unknown pane pid or failed parent walk fails closed; a missing or unparseable pid file has no opinion.
  - Several crews on one pane (a dispatcher restarted in the same pane, the old crew's files left behind): the newest pane-file mtime wins, then the greater crew id.

  The join emits a crew-layer delta keyed by P carrying only `role: dispatcher`, `crew.name` and the internal `Run.CrewBus` (the bus dir, also set on worker layers; `json:"-"`, merged first-non-empty). It has no `Agent`, so it never lists a run alone: the tmux/hooks layers supply the card, and between a pane vanishing and the next tick the key is unlisted, not a stateless ghost. A worker's own join to P wins the key (debug log). Residuals: `crew adopt` after a tmux restart rewrites `pid` but not `pane`, so the stale pane file fails the server check and that crew shows as an orphan crew header until it re-registers (a missed join, never a wrong one); a host where the process probe fails (no procfs and no `/bin/ps`) joins no dispatcher that has a pid file (`warnProbeBroken` warns once).
- **Fleet nesting (dispatcher mode, `ui/src/fleet/fleetEntries.ts`):** the top level is dispatcher groups, orphan crew groups and other runs. A `DispatcherGroup` is the dispatcher's `RunCard` with its crew's workers (same `crew.name` and host) nested as compact memoized `WorkerRow`s: codename swatch, title (`crew.title`, issue title, branch), `tier · engine · model`, state dot or attention chip, age, PR chip. The card's crew line is exact, `N workers · M need you` (live workers whatever the filter, hidden at none), and a dispatcher with a fresh needs-you worker gets a `M need you` badge and ranks with the needs-you group; the global header badge is unchanged (workers counted once). A `CrewGroup` is the orphan form: workers whose `crew.name` matches no dispatcher run in the list (houston-minted or orphaned crews) under a `CrewGroupHeader` with repo, short crew id, counts line and `Dispatch here`; its project is the members' sole project, else the crew's `crew.home`, else none (so a mixed-repo crew is named and filed under its home repo, not `unknown`). Every other run (solo, or role `worker` with no `crew.name`) stays a plain card.
  - Filters apply to members: a group shows when its head or any member matches, only matching members are nested, and the head stays as the container.
  - Ordering: a group's rank is the max over its head and shown members (needs-you > stuck > done > rest, freshness-gated); ties by the head's stable order (dispatcher: `since`, then id; crew group: the crew id's leading unix seconds, newest first, then id). Members sort by the same rank, then stable order.
  - Past `COLLAPSE_OVER` = 6 shown members a group renders `MEMBER_CAP` = 5 plus a `Show N more` toggle; needs-you and stuck rows sort first, so they are hidden only when more than `MEMBER_CAP` of them are shown.
  - A dispatcher with no shown members and no needs-you worker renders as a plain `RunCard` (with its crew line), not a group container.
  - Tmux mode keeps the flat list: `fleetEntries` returns plain run entries unless the mode is `dispatcher`.
- The Fleet list's **Group by project** toggle (`houston-fleet-group-by-project` in localStorage) is off by default: the flat list keeps needs-you-first across all repos, and the project chip is always visible. Grouping by project or host works on top-level entries: a nested group sits under its head's project and host (a crew group under its members' sole project, else `unknown`), a project's dispatchers lead, and section counts are the runs on screen (head plus shown members). The console rail's Crews narrowing lists workers only (the dispatcher now carries `crew.name` too); `narrowToCrew` keeps the crew's dispatcher card, or crew group, as the container of the narrowed list.

## Crew activity feed

A dispatcher's detail Crew tab shows what its crew did, newest first (`ui/src/fleet/CrewFeedList.tsx`, `useCrewFeed`). Dispatcher mode only, one feed per joined dispatcher (see "Dispatcher ↔ crew join"); an orphan crew has no feed. **`crewfeed/` is standalone**: stdlib only (`crewfeed/boundary_test.go` fails otherwise).

- **Source**: `<bus>/events.jsonl`, tailed incrementally by `crewfeed.Store.Advance`, which the crew source calls each tick for every resolved bus (`CrewSource.feed`). It consumes only through the last `\n` (a torn trailing line waits), a line over 1 MiB is skipped without an entry, and a shrunk file or a changed first line starts over from byte 0 under a new generation, hence a new epoch. The crew layer's own whole-file fold is separate and unchanged. A missing file registers the bus with no entries.
- **Entries are a pure function of the file prefix** (chunking-independent, like the `chat/` readers): id = `<epoch>.<byte offset of its line>`, epoch = a short hash of the bus's epoch (bus path, first-line hash, generation) and the crew id, so it is per crew: ids survive a houston restart, the raw path never reaches the wire, and an id minted for one crew is refused (`reset` / 409) on another crew's dispatcher run, e.g. a dispatcher restarted in the same pane. Each crew keeps the newest `feedRing` = 500 entries; an older page re-scans the file prefix on demand (`rescan`, only on a scroll-back, never per poll) and lands on the same ids.
- **Attribution**: the record's `crew_id`, else (`reap`, `release`, which carry none) the crew the branch's latest `dispatch`/`resume` row names. A record attributable to no crew is dropped.
- **Rows** (`crewfeed/fold.go`, wire `Entry` = `{id, ts, kind, text, branch?, codename?, state?, pr?: {number, url}}`): `dispatch` (`Dispatched "<title>" · tier · engine · model`), `resume`, `status`, `question` (msg worker → dispatcher), `follow-ups` (same, body starting `follow-ups (untracked):`), `reply` (msg dispatcher → worker), `pr` (a `pr-watch:<n>` msg with a parseable body: `PR #165 checks PENDING → SUCCESS`, `PR #164 merged`, `PR #164 closed`; other changes are no row) and `reap` (`reap`, `reclaim`, `release`). A `status` row's `text` is the bus detail alone (the state word when there is none) and the `state` rides in `state`, shown as the row's kind label. A `status` row comes from `worker:<branch>#…` senders only and only on a change: the (state, source) pair differs from the branch's previous status of the *same source class* (worker and watchdog tracked apart, so neither masks the other), or a non-watchdog `blocked` whose detail differs from the previous one or that follows a dispatcher reply (a re-asked question). A watchdog row is houston-worded (`watchdog: <prefix>`; the script's detail is never shown). Excluded: `claim`, `claim-issue`, `role:` status, msgs to or from `role:`, `metrics:`, `review:`, `retro:` and anything else.
- **Sanitising**: bus text is cleaned (`clean`) before it is an entry: control runes (C0 and C1) dropped, every whitespace run collapsed to one space, at most 200 runes (`…` marks a cut); a record whose branch is over 255 bytes or contains a control rune yields no entry. A PR link exists only for a URL matching `^https://github\.com/[A-Za-z0-9-]+/[A-Za-z0-9._-]+/pull/[0-9]+$`; the client re-checks it. Run links are not on the wire: the entry's `branch` is resolved client-side against the run list (the worker with the same `crew.name` and `branch`), and `text` renders as a plain React text node (no markdown, no HTML, no autolinking).
- **Routes** (`server/runs_crewfeed.go`, dispatcher mode, behind the `/api/` auth gate; none touches tmux or runs `crew`):
  - `GET /api/runs/{id}/crew/feed?before=<epoch>.<offset>&limit=<≤100, default 50>` → `{epoch, entries, more}`, entries oldest to newest, the newest `limit` older than `before` (newest overall without it).
  - `GET /api/runs/{id}/crew/feed/stream` → SSE `entries` batches with `id: <epoch>.<last offset>`, `reset` then EOF, and a `ping` every 25 s (see "Stream liveness"). The cursor is `Last-Event-ID`, else `after=<epoch>.<offset>`, else `from=<epoch>` (from the start of that epoch), else the current tail. `reset` follows an epoch mismatch, a cursor the ring cannot serve, or the run's crew or bus changing (checked every 2 s).
  - Ladder: 503 no registry · 404 `no such run` · 404 `no crew` (not a joined dispatcher: no `crew.name` and `CrewBus`) · 400 malformed cursor or limit · 409 `{"reset":true}` on a `before` from another epoch (another crew's included).
- **UI**: `useCrewFeed(runId, enabled)` loads the newest page, follows the stream, refetches the first page on `reset`, and reopens the stream through `streamLiveness` like `useRunChat`. A row is time (`HH:MM`, full timestamp in `title`), kind label, text, and a branch/codename chip (a link when the worker is found) plus a PR chip; `Load older` pages by `before`.

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
- `dims` — Pane dimensions (cols/rows) to resize xterm.js to match. Sent at connect and again whenever the 1 s meta poll sees the pane's size change (another client attaching, a zoom toggled elsewhere), each time followed by a fresh `seed`: a TUI redraws relative to the screen height, so an xterm left at the old size scrolls Claude's prompt box and status bar instead of repainting them in place.
- `seed` — Full snapshot of pane content plus `cursor` (`{x, y}`, 0-based within the visible screen, read in the same tmux call as the capture); also used for a mid-stream re-seed (a dropped-output gap or a resize) — there is no separate `reseed` type on the wire. The UI clears scrollback in the write queue before every seed (the seed repeats the pane's history, so keeping the old scrollback shows it twice) and parks the cursor at `cursor` (the lines are written one after another, which leaves it at the end of the last line, and a shell echoes keystrokes without positioning them).
- `output` — Incremental terminal data
- `meta` — Pane metadata (agent type, status, mode, activity, choices)

**Client → Server:**
- `input` — `{data: <text>}`, sent verbatim to the pane (no implicit Enter)
- `resize` — `{cols, rows}`, request terminal resize
- `scroll` — `{mode: "wheel"|"page", data}`: scroll bytes for an app on the alternate screen (see "Mobile Features"). Sent like `input` only if the server's fresh check of the pane passes; otherwise dropped silently.

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
  `/input` is unchecked: it types whatever it is given. Answering a question or permission dialog goes through `POST /api/runs/:id/answer` instead, which verifies the pane first (see "Answering from Chat").

## Stream liveness

All SSE streams, `/api/runs/stream`, the chat stream and the crew feed stream, send a named
`ping` event (`event: ping`, empty data — not a comment, so `EventSource`
listeners see it) every 25 s. On the runs stream a ping tick owed a resync
(a dropped update for that subscriber) sends a full `snapshot` instead.

A phone that slept leaves EventSource and WebSocket connections half-open with
no error ever firing, so the UI reopens streams itself
(`ui/src/hooks/streamLiveness.ts`): `useRuns`, `useRunChat` and `useCrewFeed` reopen their
stream when the page was hidden ≥ 10 s, or when a visible page has heard
nothing (message or ping) for 60 s. `usePaneSocket` replaces its terminal
socket on return after ≥ 10 s hidden at once, without waiting for the old
socket's close, and ignores that socket's later events. After a shorter hidden
spell an open connection is left alone; the terminal socket still reconnects at
once if it is not open.

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
  The ring is built lazily: a session has none until its first chat page,
  stream, epoch or subscribe call, which reads the transcript into it (the
  tool route reads the file directly and builds nothing); from then on the
  hub's 2 s tick maintains it. A late-built ring is identical to
  one built at start, so seqs and epochs are unaffected.
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
  is the default tab when the route names none (`#/fleet/<id>`), except on a joined dispatcher in dispatcher mode, whose default is the Crew tab (see "Navigation").
- **Routes** (`server/runs_chat.go`, behind the `/api/` auth gate; none touches
  tmux): `GET /api/runs/{id}/chat?before=<seq>&limit=<≤100>` →
  `{epoch, updates, more}`; `GET /api/runs/{id}/chat/stream?after=<epoch>.<seq>`
  → SSE `updates` batches with `id: <epoch>.<last seq>` (`Last-Event-ID` wins
  over `after`, so a native EventSource reconnect resumes with no gap), `reset`
  then EOF on an epoch mismatch, a cursor the ring can't serve, the run's
  Session changing (checked every 2 s), or the session going away; the first pull after every connect always sends an `updates` event, `[]` when nothing is missed, so the UI can tell the catch-up batch from live ticks (only the newest update of the catch-up is marked live and animates, `useRunChat`); an idle
  stream gets a named `ping` event every 25 s (see "Stream liveness");
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


### Answering from Chat

Claude runs only (`run.agent` `claude`/`claude-code`; `isClaudeAgent` on both sides). On another engine's live run the card stays read-only with "answer in the Terminal tab". Code: `answer/` (pure text in, keys out), `server/runs_answer.go`, `ui/src/api/answer.ts`, `QuestionCard.tsx` + `questionStage.ts`, `PermissionBar.tsx`.

- **Question picker**: a pending `AskUserQuestion` card (run live with a terminal, Claude) shows every question as radio (single-select) or checkbox (multi-select) rows plus an Other row with a text field (not for a preview-layout question, see below). Taps only *stage* (`questionStage.ts`; single-select is exclusive and never unselects); nothing is sent until the one "Send answer" button, enabled when every question has an option or non-blank Other text and no question has Other switched on with blank text. Single-select rows are a radiogroup with a roving tabindex (one tab stop, arrow keys move and select; arrowing onto Other keeps focus on the radio, only a tap moves it into the text field). If the detail cannot be shown (fetch error, omitted or unreadable input) a pending question on a live run with a terminal shows a hint and an "Open Terminal" link instead, and the card calls `onLayout` when its detail loads or the picker appears so the sticky timeline stays pinned. A completed or failed call, or a run that is not live (no terminal, `done`, `failed`), renders read-only.
- **`POST /api/runs/{id}/answer`** (both modes). Body, unknown fields 400, max 64 KiB (413): `{"kind":"question","toolCallId","answers":[{"question":i,"options":[0-based…],"text"?}]}` (one entry per question, in order) or `{"kind":"choice","ordinal":1..9,"frame":"<64 hex>"}`. Same resolution ladder as `/input` (503/404/409/409/409/503, via `runPane`), then body (invalid UTF-8 → 400 before decoding, since `encoding/json` would turn it into U+FFFD), then 422 `only a Claude run can be answered`. Free text is trimmed, may hold no `Cc` rune, ≤ 500 runes; `Cf` (ZWJ, bidi marks) is allowed. Single-select takes exactly one option xor text; multi-select takes any options plus optional text, not neither. Statuses: 204 typed; 400 invalid body/answer/`tool call is not a question`; 404 `no chat` / `no such tool call`; 409 `already answered` (transcript says the call finished), `prompt changed` (first check failed, nothing typed), `pane is in copy mode` (nothing typed), or 409 `{"partial":true}` (a later check mismatched or timed out, the pane entered copy mode, or the request was cancelled, after keys went in); 413; 422 unreadable questions or a plan the answer cannot fit (≤ 8 options per question keeps the Other digit a single key); 429 another answer is in progress for that pane (per-pane `TryLock`, no queueing); 500 chat read or first-key send failed; 502 `{"partial":true}` a send failed after keys went in; 503 `tmux unavailable` (the first capture or mode probe failed, nothing typed) or 503 `{"partial":true}` (a capture or mode probe failed after keys went in). Logs carry pane, kind, step count and status, never text, labels or frames.
- **Executor** (`typeSteps`): the questions come from the transcript (`ChatTool` on `toolCallId`), never from the client; the client supplies only indexes and text, validated against them. `answer.Plan` returns steps of `{Expect, Keys}`. The pane is captured (`capture-pane -p -e`, 200 lines) and must pass `steps[0].Expect` before the first key; after every key batch the executor polls (100 ms, 3 s timeout) until the next step's `Expect` passes. Before the first key and before every later key batch it also probes `#{pane_in_mode}` (`PaneInMode`): `capture-pane` shows the live screen under copy mode, so every check would pass while copy mode swallowed the keys. Any failure — mismatch, timeout, capture or probe error, copy mode, cancel — stops typing; nothing after the last batch is checked. Key sequences (Claude Code 2.1.293 and 2.1.299, which render these dialogs alike):
  - Single-select, one question (plain dialog): the option's digit selects and submits.
  - Single-select Other: digit `n+1` (Other row), then the text literally, then Enter.
  - Preview layout: a single-select question where any option has a non-blank `preview` renders side by side in Claude Code 2.1.293 and 2.1.299 (`Question.PreviewLayout`): labels on the left, the focused option's preview box on the right, no description lines, no "Type something" Other row, an unnumbered `Chat about this` row, a `Notes: press n to add notes` hint, and `n to add notes` in the footer. Multi-select questions ignore previews; in a tabbed dialog each tab renders per its own question. Checks compare only the label column (each line cut at the preview box's left border); the preview itself is never compared (it can be clipped), and typed notes fail the check. The digit only moves the cursor here, so the plan is digit, a check that the cursor landed, then Enter (submits a plain dialog, advances a tabbed one). Other text on such a question is 422 (no Other row), and `QuestionCard` hides the Other row for it (same predicate).
  - Multiple questions or any multi-select makes a *tabbed* dialog: a digit on a single-select advances to the next tab; on a multi-select a digit toggles its box without moving the cursor; Down moves to Other (type text, then Down) and on to the Next/Submit row; Enter confirms. After the last question the review tab is checked against every question and answer as Claude summarises it, then `1` submits.
- **Dialog region rule**: checks run only inside the active dialog at the bottom of the pane — the lines after the last `─` rule that is directly followed by a tab bar or `☐`/`☒` header, where only an unindented (column 0) rule line counts, so a `────` line inside an indented command or description cannot move the anchor; the rule must span the pane's full width (`#{pane_width}`, read in the same tmux call as the capture, `tmux.Client.CapturePaneWidth`), so a short column-0 `──` spilled into a command never anchors. A capture with no full-width rule (a dialog taller than the 200 captured lines) holds no dialog: `/prompt` is 404, an answer whose first check sees none is 409 `prompt changed` with nothing typed, and a later step that loses it times out as 409 `{"partial":true}`; an unreadable `#{pane_width}` is a capture failure (503 `tmux unavailable`). This applies to question and permission dialogs alike. A named session (`claude --name`, as the dispatcher launcher starts every dispatcher) keeps its input box's top rule, carrying the name, as the pane's last line under a question dialog (blank rows between): `─…─ <name> ─`, or ` <name, cut with …> ─` when the pane is narrow. Exactly that one line is dropped before the question and review checks, only when it is the capture's last non-blank line and it starts in column 0 (a `─` or one space) and ends in ` ─`; a name it cannot recognise (e.g. one starting with `─`) is not dropped, so the dialog fails closed as 409. Permission dialogs draw no such line. Checks never run against scrollback, which repeats the question in the user's prompt and in earlier answer summaries. Text is compared normalized (`answer.Normalize`: leading `│` dropped, all whitespace deleted; ANSI and OSC 8 are stripped earlier, in `captureLines`) so Claude's hard wrapping cannot matter. A tab must show exactly this question, its options with the expected cursor row — each option row must equal the label followed by its description exactly (no prefix match; descriptions come from the transcript's tool input) — ticked boxes and Other text, the `Chat about this` row and an `Esc to cancel` footer; the tab bar's per-question boxes must be ticked before this tab and clear after it, so look-alike questions in another position do not match. A permission dialog never parses as a question dialog and vice versa.
- **`GET /api/runs/{id}/prompt`** (both modes): the permission-style dialog at the bottom of a Claude run's pane (`answer.PermissionPrompt`: after the last unindented (column 0) full-width `─` rule, a line ending in `?`, consecutive choices `1..m` (2 ≤ m ≤ 9) with exactly one `❯` cursor, then an `Esc to cancel` footer). 200 `{question, choices[], detail (≤ 2 KiB, ending in `… (truncated — open the Terminal tab)` when cut), frame}`; 404 `no prompt`; ladder as above, 422 non-Claude. `frame` is a sha256 of the whole dialog, command included: leading `│` dropped, non-blank lines joined by line breaks, words within a line joined by single spaces (so `rm -rf /tmp/x` and `rm -rf / tmp/x` differ, and a line break that moves changes the frame), and `─`/`╌` runs collapsed to one. A rewrap after a resize therefore also changes the frame, and the UI refetches after the 409. `{"kind":"choice"}` re-captures, requires the same `frame` (else 409 `prompt changed`, nothing typed), 400 `no such choice` past the last choice, probes for copy mode (409 `pane is in copy mode`), and presses the ordinal as one key. The hash covers the command's words and line breaks, so an approval cannot land on a command that differs from the one the user saw. A dialog with `Type something` / `Chat about this` rows is a question dialog, not a permission prompt.
- **`PermissionBar`** (`ui/src/fleet/PermissionBar.tsx`): polls the prompt route every 2 s while the run is Claude, has a terminal and its `state` is `blocked` *or `idle`* — `idle_prompt` demotes a still-open dialog to `idle`, and the route stays the authority on whether one exists. A failed poll keeps the last value; polls never overlap and each times out after 10 s. One tap sends one ordinal with the frame it displayed; success hides that frame until a poll shows none or another, or shows it again after 3 polls that still return it. A 409 shows the notice "The session moved on — refresh" (a ≥44 px dismiss button) and refetches; it lives outside the dialog, so it survives the dialog unmounting, and stays until dismissed, a later answer is sent, or the bar deactivates (the run leaves `blocked`/`idle`). Only polls started after a successful answer count toward the 3-poll re-show; an in-flight poll from before the tap is ignored.
- **`answeredHere`** (`RunQuestion`, `RunStatusStrip.tsx`): `ChatTab.tsx` sets it when the Chat tab already offers the answer (a permission bar is shown, or an answerable `AskUserQuestion` is pending); it hides the Chat header's "Reply in Terminal" button so the two affordances do not compete.
- **UI result handling** (`ui/src/api/answer.ts`): 409 → `moved` (`partial` from the JSON body), except `pane is in copy mode`, which is an error telling the user to exit copy mode and retry ("The terminal is scrolled back (copy mode) — exit it, then retry"). Only a 409 `{"partial":true}` locks the question picker and links to the Terminal tab ("Part of the answer went in"); the moved (non-partial 409) and error states link to the Terminal tab too; a 502/503 `{"partial":true}` shows "may have been partly sent — check the agent before resending" and leaves the picker unlocked; a 15 s client timeout reports the answer may be partly sent. Sent state is tied to the call status, so the card unlocks when the status changes.
- **Drift fails closed.** Fixtures are hand-written captures, recorded against Claude Code 2.1.293 (`answer/testdata/`) and 2.1.299 (`answer/testdata/2.1.299/`, and `answer/testdata/2.1.299-named/` for a named session); a Claude Code UI change that breaks a check answers 409 and types nothing (or stops at the first key batch whose result no longer matches), never guesses. When Claude Code's dialogs change, re-record the fixtures and update the parsers together.

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
- The model allowlist and per-tier defaults come from `dispatch --models --json` (`dispatchModelSet`, `server/dispatch_models.go`), run like `dispatch --engines` (`dispatchEnv`, tmux server PATH, 10 s timeout) once per options GET, `POST /api/dispatch` and `POST /api/dispatch/dispatcher`, never cached. Only literal ids are kept: any entry outside `dispatchLiteralModelRe` (globs `*?[`, a leading `-`, spaces) and every `regex` list is dropped, so POST validation stays a string-equality allowlist. `tier_models` is the tier's `default`, else the first literal model in that tier. If the command fails (older dispatch without `--models`, non-zero exit, timeout, unparseable or empty output), `builtinDispatchModels` (the literal entries of dispatch's `modelMap`) is used for options and validation, and the options response carries `dispatch_models_error`. Update the built-ins when the tier map changes; they are only a fallback.
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
  success as soon as `<crew>/pid` holds non-empty text (`readPIDText`: a regular
  file of at most 64 bytes; the launcher passed its own gates and registered; a
  restart also needs the text to differ from what it held before the launch, see
  "Restart dispatcher"). A dead pane → 422 `dispatcher exited with status N` with the
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

### Restart dispatcher

`POST /api/runs/{id}/restart` (`server/dispatcher_restart.go`, dispatcher mode, behind the auth gate) ends a dispatcher run's engine session and starts a new dispatcher on the same crew in the same repo. Workers are untouched.

- **Server state only:** the crew id (`Run.Crew.Name`, which must match `dispatchCrewRe`) and the bus (`Run.CrewBus`) come from the dispatcher ↔ crew join; the repo is the dispatch repo whose common dir is the bus's parent (symlink-resolved before matching, so a repo reached through a symlink still matches), and `crews/<id>` must be a directory there. The crew reaches the launcher through the launch file's `crew_id` (`launchEnv` sets `CREW_ID`); it is never minted and houston never removes it, whatever fails.
- **Body** `{engine?, model?, effort?, force?}`, unknown fields 400. An absent field inherits; `""` for model or effort is the launcher default. Inheritance reads the old launcher's argv (`/proc/<pid>/cmdline` of the crew's recorded `pid`, Linux only; `parseLauncherArgv` mirrors the launcher's flag loop). Without it the engine is the run's `agent` and model/effort the launcher defaults. A changed engine does not inherit the model (models are per engine). The resolved values pass `validateDispatcher` exactly like the launch route; an inherited value that no longer validates is a 400, never a silent change. A restart carries no task prompt. `--autocompact` and a `/model` toggled inside the session are not carried.
- **Busy** (`restartBusy`): `thinking`/`running`/`compacting`, or `blocked` unless the question kind is `turn`; for a Claude run also a permission dialog on screen (`answer.PermissionPrompt` over a capture, since a dialog left open past `idle_prompt` reads `idle`). Busy without `force` → 409 `{busy: true}`, nothing touched; the state check (not the capture) repeats just before the kill. Residual: an `AskUserQuestion` dialog left open past `idle_prompt` reads `idle` and is not detected.
- **Order** (`handleRunRestart`; only the window, kill and startup steps are `openDispatcherWindow`, shared with the launch route, as is `launchSlot`): registry 503 → run, dispatcher and crew 404 → body 400/413 → tmux server `PATH` 503 → engines 502 → settings 400 → repo list 502 → repo and crew 404 → crew records no dispatcher pid 409 (`the crew records no dispatcher pid — restart it from a terminal`; fail closed: without the pid the wrapper could not wait for the old dispatcher, and an abort would leave the crew registered to the killed new pane) → unsafe repo path 422 → houston exe/launch dir 500 → old pane re-resolved (`resolveRunPane`) 409/503 → busy 409, or 503 when the Claude pane capture for the dialog check fails → launcher on the tmux server's `PATH` 502 (checked before anything is touched) → slot 429 → launch file 500. Then the new window opens first (the old pane's session while it exists, else the launch route's session rule), the old pane is re-resolved (point-of-use server check) and ended with `kill-pane` by pane id, then the startup check runs. A pane already gone at the kill is fine.
  - The launch file carries `wait_pid` (the old recorded pid): the wrapper waits for that pid to exit before exec, because `crew register` refuses while the recorded pid is alive and the old launcher's `crew deregister` would otherwise remove the new registration. `launchWaitLimit` (60 s) is only a backstop that matters if houston itself dies mid-request (the wrapper then stops waiting and registers): on every houston-side outcome, including the post-await check below, houston has already answered or killed the new window, which ends the waiting wrapper.
  - Killing first was rejected: a failed new window would leave the crew without a dispatcher, and killing a session's last window can take the tmux server down with it. A failure between opening and the kill (busy again, different server, `kill-pane` failed) kills the new window and leaves the old one running.
  - `kill-pane`, not `kill-window`: a dispatcher window has one pane, so the window closes; a user's extra panes in it survive.
- **Startup check:** `awaitDispatcher` counts the launcher as registered when the crew's `pid` file text is non-empty and differs from the text before the launch (a restart's pid file already exists); houston's own await, after the kill, is `launchCheck + restartSlack` (`Server.restartSlack`, 5 s in production, covering a slow exit of the old process).
  - **Post-await check** (restart only, `windowLaunch.onStarting`, runs only when the startup check is still "starting"): if the old recorded pid is still alive (`Server.pidAlive`, default `processAlive`), the wrapper is still waiting on it and cannot register, so the new window is killed and the restart answers 502 `the old dispatcher process is still running after its pane was ended — the new launcher could not register; stop pid N and relaunch with CREW_ID=<crew> dispatcher`, with `old_ended`. Old pid dead → the 200 "still starting" is genuine.
- **`old_ended`** (`dispatcherResponse.OldEnded`, `omitempty`, restart only; the launch route never sets it): true exactly when the restart's `afterOpen` killed the old pane or found it already gone. So every post-kill status carries it (200, 422 dead/gone, 502 unchecked, the 502 above) and no pre-kill refusal does (those leave the old dispatcher running).
- **Statuses:**

  | status | meaning |
  |---|---|
  | 200 | `{crew, session, window, pane, run_id, old_ended}` |
  | 400 | malformed body or a setting outside the allowlists |
  | 404 | `no such run`, `run is not a dispatcher`, `dispatcher has no crew`, `repo is not a known repo`, `crew is not a crew of this repo` |
  | 409 | busy (`busy: true`), crew records no dispatcher pid, run has no terminal, pane gone, pane on a different tmux server (at the first check or just before the kill) |
  | 413 | body too large |
  | 422 | repo path not tmux-safe; the new launcher exited (`output` carries its pane text; `old_ended`) |
  | 429 | another launch or restart is running (`launchSlot`) |
  | 500 | houston exe/launch dir not passable to tmux, launch file not written |
  | 502 | engines or repo list failed; launcher not on the tmux server's `PATH`; tmux could not open the window; could not end the old session (new window killed, old left running); could not check the new pane (`old_ended`); the old dispatcher process still running after its pane was ended (new window killed, `old_ended`) |
  | 503 | no run registry; tmux server `PATH` missing; tmux unavailable (including a failed pane capture in the busy check) |

- **Residuals:** once the old pane is killed, a launcher failure leaves the crew without a dispatcher; the 422 carries the pane output and the crew, and `CREW_ID=<id> dispatcher` in the repo recovers. An old engine's `crew stream` child can outlive the pane; the protocol's `--status`/`--force` reclaims it.
  - A pid recycled between validation and the post-await check reads alive: a spurious 502 and a killed new window although the old dispatcher is dead (a manual relaunch, never a wrong claim).
  - houston dying between the kill and its reply leaves no record; the UI's pending mark expires to "no longer available".
  - A dropped request the server completes never tells the phone the new run id; the new dispatcher simply appears in Fleet.
- **UI:** a `Restart` control in the Crew tab header (`RestartDispatcher`, so only on a joined dispatcher in dispatcher mode). An inline confirm says the session ends and workers keep running; force is decided when the confirm opens (`isBusy` at that moment, mirroring `restartBusy` minus the pane check) or by a 409 `busy`, never by a state change after opening; either turns it into `Force restart`. An optional Settings disclosure offers engine/model/effort (`same as now` = omitted, `launcher default` = `""`). While the request is in flight the old run id is marked pending (`pendingRuns.ts`, 60 s). A failure is recorded per old run id (`recordRestartFailure`, 10 min) only when the response carries `old_ended`; pre-kill refusals leave the old dispatcher running and record nothing. Cancel clears the record. A network error (status 0) shows "The connection dropped — the restart may still be running on the server…", records nothing and keeps the old id pending. When the old run has left the list, `RunDetail` shows "The restart did not come up" with the error and output instead of "no longer available", and the `CREW_ID=<crew> dispatcher` relaunch line only for 422 or the old-process 502 (the launcher is definitely not running), else "Check Fleet for the new dispatcher before relaunching.". On success the new id is marked pending (`RunDetail` shows "Starting the restarted dispatcher…" instead of "no longer available") and `switchRunTab(newId, 'crew')` replaces the history entry, only while the route still names the old run, so Back never returns to the killed run.

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

`/api/debug/pprof/` (`net/http/pprof`) is registered only under `-debug`, and
sits behind the same token and Host gates as the rest of `/api/`.

This model is the primary defense; the following are additional layers:
1. Default bind: `127.0.0.1:9090` (localhost only)
2. Access via Tailscale (recommended)
3. Or SSH tunnel: `ssh -L 9090:localhost:9090 host`

## Dependencies

**Go:** `github.com/gorilla/websocket` and `github.com/fsnotify/fsnotify` (hub state-dir watcher) — the only external dependencies. Everything else is stdlib.

**React:** `@xterm/xterm`, `@xterm/addon-fit`, `@xterm/addon-web-links`, `react`, `react-dom`, `react-markdown`, `remark-gfm`, `remark-breaks`
