# Modes and architecture

Mode resolution (auto/dispatcher/tmux) and the process architecture diagram. The route table lives in `CLAUDE.md`.

Moved verbatim from the root `CLAUDE.md`; section headings are unchanged.

## Modes

houston runs in one of two modes, chosen by `-mode auto|dispatcher|tmux` (default `auto`) and resolved once at startup, never re-detected. The result is logged as `houston mode` with `mode`, `requested` and `reason` (never the PATH value).

- **`auto`** (`mode.Resolve`, production probe `server.ModeProbe`) picks dispatcher mode when both `dispatch` and `crew` resolve (`mode.OnPath`: an executable regular file; an empty or relative PATH element never matches) on the tmux server's global PATH joined ahead of houston's own — the PATH `dispatch` runs with (`dispatchEnv`). If the tmux server PATH can't be read (server unreachable, no global PATH), only houston's PATH is searched. Otherwise tmux mode. `exec` resolves `dispatch` on houston's own PATH, so a host with `dispatch` only on the tmux server's PATH still gets dispatcher mode, and `POST /api/dispatch` then answers 502 `could not run dispatch: …`.
- `server.Config.Mode` must be `mode.Dispatcher` or `mode.Tmux`; `server.New` errors otherwise.
- **Dispatcher mode** is everything described below.
- **Tmux mode** is plain tmux plus hooks monitoring:
  - `POST /api/runs/:id/reply`, `POST /api/runs/:id/restart`, `GET /api/runs/:id/crew/feed`, `GET /api/runs/:id/crew/feed/stream`, `POST /api/dispatch`, `GET /api/dispatch/options`, `POST /api/dispatch/dispatcher`, `GET|POST|DELETE /api/repos`, `GET /api/repos/candidates` and the host-addressed `/api/hosts/{host}/dispatch*|repos*` forwards (see "Dispatch onto a peer") are not registered (404): the command-execution surface is gone, not hidden.
  - No `CrewSource` (`runSources`) and no `crewfeed.Store`; the registry order `runs.Order` is tmux, hooks, so nothing reads `<git-common-dir>/crew`.
  - The tmux layer ignores `@crew_name`/`@crew_color` (`tmux.WithoutCrew`): no `role`, no codename or colour, no dispatcher ↔ crew join (so no nested workers or crew line), and the Workspace response carries no `crew_codename`. Role-grid panes (`@crew_role` other than `lead`) are still skipped in both modes.
- `GET /api/mode` → `{"mode":"dispatcher"|"tmux"}` in both modes, behind the auth gate. The UI (`useMode`) fetches it once and retries every 5 s on failure; until it is known only Fleet and Workspace show, so in dispatcher mode Dispatch appears a moment after load. In tmux mode that tab is absent because of the `mode === 'dispatcher'` gates in `MobileShell` / `ConsoleShell`, and `#/dispatch` (with any query) is rewritten in place to `#/fleet` by `tabsFor` via `useShellTab(mode)`; while the mode is unknown such a hash is left alone. There is no Crews tab in any mode: `#/crews` (with any query) is rewritten in place to `#/fleet` at once, mode known or not (the crew roster lives in the dispatcher's detail, see "Crew activity feed"). Adding a dispatcher-only tab means touching both shells.
- Peers (`-peer`, see "Federation (peers)") and `GET /api/hosts` work in both modes: a peer source reads another houston's runs stream and forwards run-addressed requests to it, but the peer executes them; this houston runs nothing for a peer run, so the source is not part of the command-execution set above. Reply, crew-feed and restart forwarding, and dispatch onto a peer (`/api/hosts/{host}/…`), exist only in dispatcher mode, where those routes are registered. A tmux-mode houston can mirror a dispatcher-mode peer's runs, and the other way round.


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
│  (routes: see the route table in CLAUDE.md)           │
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


