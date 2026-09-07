# Houston overhaul — agent-run spine, Mocha shell, federation seam

**Issue:** #2
**Status:** accepted
**Milestone:** M1 (local-complete). M2 federation and M3 pi/notifications have
seams defined here but no code.

## Problem

Houston is two applications sharing one binary.

`#/agents` renders hook-driven Claude Code cards from `hub/` over one SSE stream.
`#/panes` renders a tmux tree and xterm panes from `server/` over a different SSE
stream. They share no types, no status vocabulary and no visual language, and are
joined by a floating toggle drawn with inline styles in `ui/src/App.tsx`. Adding
a fifth agent, a second host, or a dispatcher crew to that structure means adding
it twice.

Three forces make the split untenable rather than merely untidy:

1. **Not every agent is a tmux pane.** OpenCode is an HTTP API. `pi` on halo is a
   local-model agent with its own JSONL sessions. A dispatcher crew is a set of
   workers that happen to live in panes. The pane cannot be the spine.
2. **Not every agent is on this machine.** halo, tp-g6 and mbp all run agents and
   are all on the tailnet.
3. **Enrichment already exists and houston ignores it.** lazytmux computes PR
   state, issue identity, crew membership, worktree and branch for every window,
   keeps them fresh, and parks them in tmux user-options. Houston screen-scrapes
   `capture-pane` once a second instead.

## Decisions

| Decision | Choice | Rationale |
|---|---|---|
| Spine | The **agent run** | The only model all of tmux, OpenCode, pi and crew workers fit |
| Terminal | A **view of a run**, tab 2 | Keeps it one tap away without making it the app |
| Remote | **Federated houstons** over Tailscale | Each peer reads its own filesystem; no cross-host state problem |
| Dispatcher | **Full control**, including dispatching | The composer needs a real screen, which decided the shell |
| Visual | **Instrument Mocha** | Card structure with Catppuccin; coherent with lazytmux |
| Mobile shell | 4 tabs — Fleet · Crews · Workspace · Dispatch | Dispatch earns a destination |
| Desktop shell | Its own console layout | A sibling shell, not a breakpoint |
| Enrichment | Read lazytmux's tmux user-options | Do not rebuild what is already computed and fresh |

### Why federation rather than an SSH control-mode bridge

lazytmux dials `ssh -CC attach-session` because tmux is the only thing it may
assume on the remote. Houston has no such constraint: it can install itself, and
every host is already on the tailnet. A peer houston reads its own hook state
files, its own tmux options and its own pi sessions — none of which survive an
SSH control-mode stream, which carries terminal bytes and structural
notifications and nothing else. lazytmux works around this by stamping status
into tmux pane options; houston does not need the workaround.

What transfers from the bridge is not its transport but its **failure
semantics** — see "Control-mode transport" below.

## The `Run` model

One struct replaces `hub.SessionView`, `server.SessionWithWindows`,
`server.AgentStripItem` and `server.OpenCodeSession`.

```go
// Run is one agent run, anywhere. It is the only object the UI lists.
type Run struct {
    ID    string // stable: host/agent/native-session-id
    Host  string // "" means local; stamped by the hub for peers
    Agent string // claude | codex | cursor | amp | opencode | pi
    State State

    Repo, Branch, Worktree string

    Tmux  *TmuxRef  // nil for agents with no pane
    Issue *IssueRef // nil when the window has no @issue_id
    PR    *PRRef    // nil when the window has no @pr_number
    Crew  *CrewRef  // nil when not dispatcher-managed

    Activity Activity  // tool, hint, trail, preview
    Question *Question // the thing you answer; non-nil implies State == Blocked
    Tokens   Tokens

    Since, UpdatedAt int64
    Stale bool // source went quiet; last-known values retained
    Caps  Caps // what this run supports right now
}

type Caps struct {
    Terminal bool // a live pane feed is available
    Reply    bool // text can be delivered (pane keys or `crew reply`)
    Kill     bool
}
```

`Caps` is load-bearing. An OpenCode run has no pane; a peer that is offline has no
terminal; a run outside a crew cannot take a `crew reply`. The UI renders
affordances from capabilities rather than branching on agent type, so adding an
agent does not mean touching the UI.

`Stale` is a flag, never a state. A stale run keeps its last-known `State` and
says it is stale. This is the lazytmux reconnect lesson (`#482`): a transport
drop must not destroy state that is trivially re-derivable.

### One state vocabulary

Three vocabularies exist today and they disagree. This is the single mapping.

| `Run.State` | Claude hooks (`hook.State`) | crew bus | `@claude_status` |
|---|---|---|---|
| `thinking` | `thinking`, `starting` | — | `processing` |
| `running` | `tool-running` | `working` | `processing` |
| `blocked` | `waiting`, `waiting:permission` | `blocked` | `waiting`, `denied` |
| `compacting` | `compacting` | — | `compacting` |
| `review` | — | `pr_open` | — |
| `done` | `ended` | `done` | `done` |
| `failed` | — | `failed`, `exited` | `error` |
| `idle` | — | — | `idle`, `interrupted` |

`blocked` is the only state meaning *a human is required*. It drives the badge,
the sort order, and in M3 the push notification. Nothing else may claim it.

## Sources, correlation and merge

```go
type Source interface {
    Name() string
    Run(ctx context.Context, out chan<- Delta) error
}
```

A source emits partial `Delta`s; a registry merges them into `Run`s and
broadcasts changes. Sources never see each other.

| Source | Reads | Authoritative for |
|---|---|---|
| `hooksource` | `<state>/claude/*.json` (today's `hub/`) | `State`, `Activity`, `Tokens` |
| `tmuxsource` | `list-windows -a -F`, `show-options -p` | `Repo`, `Branch`, `Worktree`, `Issue`, `PR`, `Crew` |
| `crewsource` | `.git/crew/*.jsonl` in known repos | `Question`, crew messages |
| `opencodesource` | OpenCode HTTP/WS (today's client) | everything, for its own runs |

`tmuxsource` harvests, per window: `@branch @git_root @worktree @issue_id
@issue_title @issue_url @issue_provider @pr_number @pr_state @pr_check_state
@pr_mergeable @pr_draft @pr_url @crew_name @crew_color @window_task`, and per
pane `@claude_status`. All are written and kept fresh by lazytmux. Houston
neither computes nor refreshes any of them.

### Correlation

**The join key is the tmux pane id.** Claude Code hooks already capture
`TmuxPane` (`hook.SessionState.TmuxPane`, e.g. `%307`), and lazytmux writes
`@claude_status` onto that same pane. Crew bus records key by branch, which
resolves to a window through `@branch`/`@crew_name`. A run with no pane
(OpenCode, and pi in M3) keys on its native session id and carries `Tmux == nil`.

### Merge precedence

Per field, highest first: **hooks → crew bus → tmux options → screen-scrape.**

Hooks win because they fire synchronously with the event they describe. Screen
scraping is last because it infers state from rendered text. Concretely this
demotes `parser/` and `agents/*/detect.go` to the fallback for agents with no
better source — **amp and generic only** — and retires
`server/pane_ws.go:metaPollLoop`, which currently runs a `capture-pane` every
second per open pane in parallel with the control stream.

To be explicit, because "demote screen-scraping" is easy to over-read:
identifying *which* pane runs Claude Code does not need detection at all. The
hook state file carries `TmuxPane`, so a Claude pane identifies itself. What is
retired is inferring Claude's *state* from rendered text —
`agents/claude/detect.go` and `agents/claude/state.go` lose their role in the
status path. The Workspace view labels plain panes from
`#{pane_current_command}`, not from the parser.

## Control-mode transport

Houston already has a working control-mode client. Three of the four transport
lessons from lazytmux's bridge are already implemented; the design keeps them and
fixes the rest.

**Already correct, keep:**

- `tmux/control_client.go:81` — `refresh-client -f ignore-size`
- `server/pane_ws.go:95-160` — a genuine seed handshake:
  `refresh-client -A %pane:pause` → subscribe → `capture-pane` seed → SIGWINCH
  redraw → `continue`. This solves the seed race with tmux's own per-pane pause
  rather than a stream mark.
- `tmux/control_client.go:144-154` — non-blocking fanout; a slow subscriber
  cannot stall the control stream.

**Fix:**

1. **`%pause`/`%continue` are parsed and then ignored**
   (`tmux/control_client.go:110`). That is safe only while houston drives the
   pause itself. A tmux-initiated pause discards output, so resuming without a
   re-seed leaves a corrupted screen. Mark the pane dirty on `%pause`; force a
   `capture-pane` re-seed on `%continue`.
2. **A dropped chunk is silent** (`tmux/control_client.go:151`). Dropping mid
   escape sequence corrupts everything rendered after it, and the pane stays
   wrong until the socket is reopened. Every drop must mark the pane dirty and
   trigger the same re-seed. The subscriber most likely to be slow is a phone on
   bad LTE, which is houston's premise.
3. **No reconnect** (`tmux/control_manager.go:47`): the client is deleted on
   `Done()` and never re-dialled, so a tmux server restart kills every attached
   socket. Re-dial with backoff; mark that session's runs `Stale` meanwhile;
   reconcile with one `capture-pane` per attached pane on success.

**Deliberately not adopted from the bridge:**

- **Size authority.** lazytmux takes it (`local → remote`) because it mirrors
  into real local panes that must match. Houston renounces it, because a human is
  attached to the same pane in kitty and must not be resized, and because N
  browser viewers at different widths cannot all own one size. `ignore-size`
  stays; the mismatch is absorbed client-side.
- **Structural mirroring.** `%window-add` → local `new-window` has no meaning
  here; houston has no local tmux to mirror into and renders structure as a list.

**Cleanup:** `ControlClient.SetClientSize` (`tmux/control_client.go:302`) has no
callers — delete it. The comment at `server/pane_ws.go:317` claims the client
size is "fixed at 400x200"; it is `ignore-size`. Correct it.

## Mobile terminal

Because houston renounces size authority, the pane is whatever width kitty made
it — commonly ~190 columns — and the phone absorbs the entire mismatch.

Today zoom is a CSS `transform: scale()` over a terminal pinned to 13px
(`ui/src/components/TerminalPane.tsx:206`, `:460`). At scale 1.0 text is crisp
but roughly 45 of 190 columns are visible; zooming out to see more makes text
both smaller and blurry, because rasterised glyphs are being resampled. No zoom
level is simultaneously readable and crisp except exactly 1.0.

**Zoom selects a font size, not a scale.** During a pinch, keep the CSS transform
— it tracks fingers at 60fps. On gesture end, snap to the nearest real font size,
set `term.options.fontSize`, and reset the transform to 1.0. At rest the
transform is always 1.0, so glyphs are never resampled, and the zoom range is a
bounded **9–24px** rather than an unbounded scale factor. The 9px floor is what
makes "text can't be too small" enforceable.

Navigation:

- **Pan is primary.** Default is a readable font, panned to column 0, with
  magnetism toward column 0 so a flick returns home.
- **Column scrubber** below the viewport showing which slice of the width is
  visible, draggable for fast travel across a wide diff or table.
- **Follow the cursor.** When output arrives and no touch is active, ease the
  horizontal pan to bring the cursor column into view. Suppressed while touching.
- **Double-tap toggles readable ⇄ fit-width**, restoring the removed WIDE/FIT
  affordance as a gesture. Fit-width is a glance mode and is honest about being
  unreadable.
- **Font size is a persisted per-device preference** in `useLayout`, not derived
  from the viewport.

**Bug this surfaces:** `ui/src/hooks/useTouchGestures.ts:43` hardcodes
`const lineHeight = 13 * 1.2`. Correct only while the font is pinned at 13px;
scrollback maths drifts the moment font size varies. It must read the terminal's
actual cell metrics.

## API surface

```
GET  /api/runs                 snapshot
GET  /api/runs/stream          SSE; one event per delta
GET  /api/runs/{id}            detail incl. transcript tail
POST /api/runs/{id}/reply      text → pane keys, or `crew reply` when crew-backed
POST /api/runs/{id}/key        special key
POST /api/runs/{id}/kill
WS   /api/runs/{id}/terminal   control-mode pipe, re-addressed
GET  /api/workspace            tmux tree — agent runs and plain panes alike
GET  /api/crews                crews with members and open questions
POST /api/dispatch             shells out to `dispatch`
GET  /api/hosts                peers and health (returns only local in M1)
```

`/api/sessions`, `/api/agents` and `/api/pane/*` are **deleted, not aliased**.
Nothing but this UI consumes them, and keeping two vocabularies alive is the
problem being fixed.

`POST /api/dispatch` takes `{repo, task, tier, engine}` and execs the `dispatch`
CLI in that repo. It does not reimplement any dispatcher logic.

## Auth

Currently `server/api.go:233` sets `Access-Control-Allow-Origin: *` on all of
`/api/` unconditionally, and `server/pane_ws.go:19` returns `true` from
`CheckOrigin`. Any page in any browser that can route to houston can read every
session and POST arbitrary keystrokes into any tmux pane — command execution
triggered by visiting a page, gated only by reaching the port. On a
tailnet-bound houston that includes every tailnet device and any page open on the
local machine hitting `localhost:9090`.

M1 fixes it:

- `Access-Control-Allow-Origin` becomes an explicit allowlist; the Vite dev
  origin is permitted only when the server is started with `-debug`.
- `CheckOrigin` validates against that same allowlist.
- A bearer token is generated on first run into the state dir (0600), delivered
  to the SPA as an httpOnly, SameSite=Strict cookie, and required by every `/api/`
  route. The same token becomes the peer credential in M2.

This is not a hardening nicety. Dispatch-from-phone must not be built on top of
an unauthenticated keystroke endpoint.

## UI architecture

One `useRuns()` store subscribing to `/api/runs/stream`, consumed by two shells.

- **Mobile** — 4 tabs. Fleet (grouped by host, or crew via a toggle), Crews,
  Workspace (the tmux tree, including panes that are not agents), Dispatch. A run
  opens a detail view with `Activity / Terminal / Diff` tabs; `Diff` is M3 and is
  hidden until then.
- **Desktop** — console: rail (hosts, crews, saved filters), fleet list, detail
  pane. Keeps `allotment`.

`ui/src/theme/tokens.css` is rewritten as the Mocha system: raw `--ctp-*` values
plus semantic aliases (`--state-blocked`, `--state-running`, `--surface-card`).
The `--ag-*` block scoped inside `ui/src/components/agents/agents.css` is
removed — it exists only to avoid leaking into the other view, and there is no
other view.

## What is deleted

- `ui/src/App.tsx`'s hash router and inline-styled `ViewSwitch`
- `ui/src/components/Sidebar.tsx`, `SessionTree.tsx`, `TerminalArea.tsx`,
  `PaneHeader.tsx` — replaced by the shells
- `server/types.go`: `SessionsData`, `SessionWithWindows`, `WindowWithStatus`,
  `AgentStripItem`, `PaneData`, `OpenCodeData`, `OpenCodeSession`
- `hub.SessionView` (becomes `Run`)
- `ControlClient.SetClientSize`
- `server/pane_ws.go:metaPollLoop`
- `CLAUDE.md`'s "Legacy templ templates" and "Legacy static assets" entries —
  `views/` and `static/` are already gone; the doc is stale

## Testing

- **Table test for the state vocabulary** — every row of the mapping table, all
  three input vocabularies. This is where correctness lives.
- **Table test for merge precedence** — a `Run` assembled from conflicting
  deltas across all four sources resolves per the documented order.
- **`tmuxsource` against recorded fixtures** — real `list-windows -F` and
  `show-options -p` output captured from halo, including a window with a PR, a
  window with an issue and no PR, a crew window, and a bare window with none.
- **`crewsource` against a fixture JSONL** — a `dispatch` record, a `blocked`
  status with a question, and a terminal `done`.
- **Control-mode re-seed** — a drop and a `%pause`/`%continue` cycle each produce
  exactly one re-seed; extends `tmux/control_test.go`.
- **Reconnect** — killing the control client marks runs stale, re-dial clears it.

Existing `hook/`, `tmux/control_*_test.go` and `agents/` tests stay.

## Seams for later milestones

- **M2 federation.** `Run.Host` exists from day one, `""` meaning local. M2 adds
  a `peersource` dialling `https://<peer>/api/runs/stream`, stamping `Host`, and
  proxying terminal WebSockets. A peer disconnect sets `Stale` on that host's
  runs and **keeps them listed**. `GET /api/hosts` already exists and returns
  only the local host in M1.
- **M3 pi.** A `pisource` fed by a pi extension writing houston state files,
  mirroring `contrib/opencode-plugin/houston.ts`, with session-JSONL tailing at
  `~/.pi/agent/sessions/<slug>/*.jsonl` as the fallback. Needs no model change:
  `Agent: "pi"`, `Tmux: nil` when headless.
- **M3 notifications.** `State == Blocked` is already the single trigger.

## Risks

- **`tmuxsource` couples houston to lazytmux's option names.** Accepted: they are
  stable, documented in lazytmux's `CLAUDE.md`, and the fields degrade to absent
  rather than wrong when missing. A houston on a host without lazytmux loses PR
  and issue chips and nothing else.
- **Deleting the old routes is irreversible for any out-of-tree consumer.** No
  such consumer is known; the only client is this SPA.
- **Font-size zoom re-renders the whole terminal on gesture end.** Measured cost
  is one xterm re-layout per pinch, not per frame, because the transform carries
  the live gesture. If it proves visible on an old phone, the fallback is to
  debounce the commit rather than to return to scale-zoom.
