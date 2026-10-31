# houston

Mission control for AI coding agents. A mobile-friendly web dashboard for monitoring and controlling tmux sessions and OpenCode instances remotely. Built for checking on AI agents and sending them instructions from your phone.

This file is an orientation index. Subsystem reference lives in `docs/reference/`; open the doc named in "Reference docs" before changing that area.

## Supported Agents

- **Claude Code**, **Amp** - via tmux session monitoring
- **OpenCode** - via native API integration
- **pi, Codex, Cursor** - via hookyard's normalized hook envelope (`docs/reference/hooks-and-state.md`); Fleet shows the engine as the run's `agent`

### OpenCode Setup

OpenCode's TUI picks a **random port by default**, which houston can't discover. Start it with a fixed port (`opencode --port 4096`, or `opencode serve --port 4096` plus `opencode attach`), pass `houston --opencode-url http://localhost:PORT`, or (recommended) install the houston plugin (`contrib/opencode-plugin/houston.ts` into `~/.config/opencode/plugins/`), which writes server info to `~/.local/state/houston/opencode-servers/` for houston to read; restart OpenCode after installing. Houston scans ports 4096-4100 by default; `--no-opencode` disables it. All options: `docs/reference/dev-and-qa.md`.

## Modes

houston runs in one of two modes, chosen by `-mode auto|dispatcher|tmux` (default `auto`) and resolved once at startup (`mode.Resolve`), never re-detected.

- **`auto`**: dispatcher mode when both `dispatch` and `crew` resolve on the tmux server's global PATH joined ahead of houston's own; otherwise tmux mode.
- **Dispatcher mode**: full feature set (Dispatch, reply, crew feed, restart, repo registry, crew bus).
- **Tmux mode**: plain tmux plus hooks monitoring. The command-execution routes (marked D below, plus `/api/hosts/{host}/dispatch*|repos*`) are **not registered (404)**, there is no crew bus, and the tmux layer ignores `@crew_name`/`@crew_color`. Adding a dispatcher-only tab or route means touching both shells / gating on mode.
- Peers and `GET /api/hosts` work in both modes.

Details (resolution probe, UI gating, what each mode registers): `docs/reference/modes-and-architecture.md`.

## Routes

All `/api/` routes are behind the token gate. (D) = dispatcher mode only, 404 in tmux mode. A run id `peer-NAME.<id>` on a run-addressed route is forwarded to that peer (`docs/reference/federation.md`).

| Route | Purpose |
|---|---|
| `GET /api/mode`, `GET /api/hosts` | Mode; local host + peers (both modes) |
| `GET /api/runs*` | Run list + SSE stream (`/api/runs/stream`; `?scope=local`) |
| `WS /api/runs/:id/terminal` | Run terminal I/O |
| `POST /api/runs/:id/input` | Send text / key / image |
| `POST /api/runs/:id/answer`, `GET /api/runs/:id/prompt` | Answer a question/permission dialog; read the dialog |
| `GET /api/runs/:id/chat*` | Chat page, SSE stream, tool detail |
| `POST /api/runs/:id/reply` (D) | Reply to a crew worker's question |
| `POST /api/runs/:id/restart` (D) | Restart a dispatcher |
| `GET /api/runs/:id/crew/feed*` (D) | Crew activity feed, SSE |
| `GET /api/dispatch/options`, `POST /api/dispatch`, `POST /api/dispatch/dispatcher` (D) | Dispatch form, start a worker, new dispatcher |
| `GET\|POST\|DELETE /api/repos`, `GET /api/repos/candidates` (D) | Repo registry and picker |
| `/api/hosts/:host/dispatch*\|repos*` (D) | Forwarded to that peer |
| `GET /*` | React SPA, embedded via `go:embed` |

Behind the server: tmux CLI (`list-sessions`, `capture-pane`, `send-keys`, ...), OpenCode API (`GET /sessions`, `WS /events`), hook state files (`houston hook`) and the Claude mod's Unix socket. Diagram: `docs/reference/modes-and-architecture.md`.

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
├── main.go, embed.go    # Entry point, CLI flags (-mode, -peer, ...), go:embed of ui/dist
├── server/              # HTTP server, mux, route wiring, WS pane I/O, dispatch, peer forwarding
├── tmux/                # tmux CLI wrapper + control client (list/capture/send)
├── opencode/            # OpenCode HTTP/WS client, discovery, lifecycle
├── hub/                 # Session discovery, transcript tracking, chat ring; mod.go overlays the Claude mod
├── runs/                # Run registry: tmux/hook/crew/peer sources, composition, attention, caps
├── hook/                # `houston hook` ingest, install/doctor, state files
├── claudemod/           # Claude mod: Unix-socket record ingest + per-session fold
├── peer/                # Federation: -peer parsing, one runs.Source per peer, forwarding transport
├── chat/                # ACP transcript readers (claude, pi); stdlib only
├── crewfeed/            # Crew-bus activity feed fold + store; stdlib only
├── answer/              # Question/permission dialog parsing + key plans (pane text in, keys out)
├── agents/              # Agent type detection and parsing (claude-code, amp)
├── mode/                # Dispatcher vs tmux mode resolution; stdlib only
├── parser/              # Terminal output parsing
├── terminal/            # Host terminal font-size control (kitty)
├── cmd/                 # loadfixture, sessionlog (tools build tag)
├── internal/            # ansi, loadfixture
├── contrib/             # claude-mod (hooks module), opencode-plugin
├── scripts/             # claude-hook.sh, perf/ (load measurement), qa/ (end-to-end harness)
├── docs/                # reference/ (subsystem docs), superpowers/ (specs, plans), design/
├── ui/                  # React frontend (Vite): src/{api,components,fleet,hooks,lib,theme}
├── justfile, .air.toml, flake.nix, go.mod
```

UI entry points: `ui/src/App.tsx`, `fleet/` (shells, Fleet list, RunDetail, ChatTab, CrewTab, DispatchView, `nav.ts`, `routes.ts`), `components/TerminalPane.tsx`, `hooks/usePaneSocket.ts`.

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


### CI commands

CI (`.github/workflows/ci.yml`) runs `nix build --no-link -L .#default` (the only step that catches stale `npmDepsHash`/`vendorHash` in `flake.nix` after a `go.mod` or `package-lock.json` change), then, each as `nix develop -c …`: `just build`, `go vet ./...`, `go test ./...` (inside a keepalive tmux session), gofmt check, `golangci-lint run`, `nilaway -include-pkgs=github.com/noamsto/houston ./...`, UI `npx eslint .`, `npx tsc -b`, `npm test` (in `ui/`), `go test -race ./...`, and `just mod-check` (Claude mod). Reproduce locally with the same commands; `just test|lint|nilaway|race|mod-check` wrap most of them. Load measurement and the QA harness: `docs/reference/dev-and-qa.md`.

## Security essentials

Full model: `docs/reference/security.md`.

- **Token**: random token in `<status-dir>/token` (0600); every `/api/` request needs it as the `houston_token` cookie or `Authorization: Bearer`; `?token=` is accepted only on a WebSocket upgrade.
- **Origin allowlist**: same-origin (plus the Vite dev server under `-debug`) for HTTP and WS handshakes.
- **Host allowlist**: the `Host` header is pinned to names derived from the machine itself (hostname, Tailscale address and its reverse name, loopback); anything else is 421 and never issued a token. Add names with `-hostname` (repeatable). Houston never resolves a client-supplied name.
- **`-no-auth`** disables the token only; Origin and Host checks stay, and forwarding to peers is turned off (403 `peer actions need auth`).
- Default bind `127.0.0.1:9090`; reach it via Tailscale or an SSH tunnel.

## Cross-cutting invariants

Each of these causes bugs when missed; the owning reference doc has the full rule.

- **`chat/` and `crewfeed/` import only the standard library** (`boundary_test.go` fails otherwise); `mode/` is stdlib-only by convention.
- **Never commit transcript content.** Fixtures under `chat/testdata/` are hand-written; real-transcript checks log counts only.
- **needs-you ⟺ `blocked`**: `blocked` is the only state meaning a human must act now; a turn end alone is never `blocked` (`docs/reference/fleet-runs.md`).
- **A `peer-` run id never reaches a local handler**: `findRun` and `buildWorkspace` see only `Host == ""` runs, and every run-addressed route is `peerOr(forward, local)` (`docs/reference/federation.md`).
- **No user text in tmux argv**: tmux splits a command at an element ending in `;`, format-expands `-c`, and runs a single-argument command via `sh -c`; the dispatcher window command is always `houston launch-dispatcher <file>` (`docs/reference/dispatch.md`).
- **Every send to a pane re-verifies the pane's tmux server**: `runPane` compares the recorded server pid at point of use (409 on mismatch). `/input` does not check which prompt is on screen (it types whatever it is given); answers go through `/answer` (`docs/reference/fleet-runs.md` "Point-of-use server check", `terminal-and-mobile.md`, `chat.md`).
- **The Claude mod is read-only**: hooks pass `e` to `next` and return its result; only four `$` calls are allowed, enforced by `claudemod/modguard_test.go` and `just mod-check` (`docs/reference/hooks-and-state.md`).
- **Never log secrets or task text**: tokens, task bodies, peer tokens, labels and frames stay out of logs.
- **Fail closed on drift**: answer/permission checks that no longer match Claude Code's dialogs answer 409 and type nothing; re-record fixtures and update parsers together.
- **Both shells**: dispatcher-only tabs and routes need changes in `MobileShell` and `ConsoleShell`, and tmux mode must 404 them.

## Reference docs

Read the doc for the area you are changing before editing it.

| Doc | Read this when… |
|---|---|
| [modes-and-architecture.md](docs/reference/modes-and-architecture.md) | resolving mode (`auto`/dispatcher/tmux), gating a tab or route by mode, or needing the architecture diagram |
| [hooks-and-state.md](docs/reference/hooks-and-state.md) | touching `houston hook` ingestion, hookyard envelopes, permission/dismissed-dialog healing, the Claude mod, background-task chips, or the context meter/pi spend |
| [fleet-runs.md](docs/reference/fleet-runs.md) | changing run composition, project/role, needs-you or attention, foreign-pane guards, crew-layer pane join, dispatcher ↔ crew join, or Fleet nesting and grouping |
| [crew-feed.md](docs/reference/crew-feed.md) | changing the crew activity feed (`crewfeed/`, feed routes, `CrewFeedList`) |
| [terminal-and-mobile.md](docs/reference/terminal-and-mobile.md) | changing the terminal WebSocket protocol, `/input`, mobile gestures, the composer, quick keys or SSE/WS reopen on resume |
| [navigation.md](docs/reference/navigation.md) | changing hash routes, the history stack, Back, popToRoot or edge swipe |
| [chat.md](docs/reference/chat.md) | changing the Chat tab, `chat/` readers, chat routes, or answering questions and permission prompts from Chat |
| [dispatch.md](docs/reference/dispatch.md) | changing `/api/dispatch*`, the new-dispatcher launcher, dispatcher restart, or the repo registry |
| [federation.md](docs/reference/federation.md) | changing peers, peer forwarding, `/api/hosts`, or dispatching onto a peer |
| [security.md](docs/reference/security.md) | changing auth, Origin/Host checks or peer token handling |
| [dev-and-qa.md](docs/reference/dev-and-qa.md) | running load fixtures, the QA harness, the Vite proxy, or full OpenCode setup |

Reference docs cite sections by their old CLAUDE.md heading (e.g. see "Claude mod"); headings are unchanged, so find the file by heading:

- [modes-and-architecture.md](docs/reference/modes-and-architecture.md): Modes, Architecture
- [hooks-and-state.md](docs/reference/hooks-and-state.md): Hook ingestion (incl. Dismissed dialogs), Claude mod, Background tasks, Context meter and pi spend
- [fleet-runs.md](docs/reference/fleet-runs.md): Project and role (Fleet) (incl. Ghost hook runs, Foreign panes, Point-of-use server check, Needs-you, Attention, Crew-layer pane join, Dispatcher ↔ crew join, Fleet nesting)
- [crew-feed.md](docs/reference/crew-feed.md): Crew activity feed
- [terminal-and-mobile.md](docs/reference/terminal-and-mobile.md): Mobile Features, WebSocket Protocol, Stream liveness
- [navigation.md](docs/reference/navigation.md): Navigation
- [chat.md](docs/reference/chat.md): Chat, Answering from Chat
- [dispatch.md](docs/reference/dispatch.md): Dispatch, New dispatcher, Restart dispatcher, Repo registry
- [federation.md](docs/reference/federation.md): Federation (peers) (incl. Dispatch onto a peer)
- [security.md](docs/reference/security.md): Security
- [dev-and-qa.md](docs/reference/dev-and-qa.md): OpenCode Setup (all options), Load measurement, QA harness, Vite Proxy
- Kept in this file: Supported Agents, Tech Stack, Project Structure, Development Setup (Workflow, Pre-commit Hooks), Dependencies

Design notes and plans: `docs/superpowers/` and `docs/design/`.

## Dependencies

**Go:** `github.com/gorilla/websocket` and `github.com/fsnotify/fsnotify` (hub state-dir watcher) — the only external dependencies. Everything else is stdlib.

**React:** `@xterm/xterm`, `@xterm/addon-fit`, `@xterm/addon-web-links`, `react`, `react-dom`, `react-markdown`, `remark-gfm`, `remark-breaks`
