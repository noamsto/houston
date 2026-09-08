# houston overhaul — state of play

Written at the end of the session that built #1, #3, #4, #5, #7 and #8, so the next
session can pick up without re-deriving anything. Read this, then
`docs/superpowers/specs/2026-09-07-houston-overhaul-design.md`, which is the binding
design authority.

## Where things stand

All of the backend and the first UI surface are on `main`.

| PR | What landed |
|---|---|
| #1 | `justfile` builds the UI before `go build` |
| #3 | tmux control-mode transport survives dropped chunks, `%pause`, and connection loss |
| #4 | API token, origin allowlist, `Host` pinning |
| #5 | Security docs corrected |
| #7 | `Run` model, layered source registry, `/api/runs` + `/api/runs/stream` |
| #8 | Catppuccin Mocha tokens, `useRuns()` store, mobile 4-tab shell with Fleet live at `#/fleet` |

Open issues: **#6** (no CI at all — review is the only gate) and **#2** (the umbrella).

## What is not done

`#/fleet` is one route among three. `#/agents` and `#/panes` still exist, still work, and
still serve the live UI through the **old** API. Nothing has been deleted.

The UI's remaining old-API consumers are exactly three:

- `ui/src/hooks/useAgentsStream.ts` → `/api/agents/stream`
- `ui/src/hooks/useSessionsStream.ts` → `/api/sessions?stream=1`
- `ui/src/components/MobileInputBar.tsx` → `POST /api/pane/<target>/send`

Crews, Workspace and Dispatch are placeholder copy. There is no run detail view, so
there is no way to reach a terminal from the new shell.

## ⚠️ #8 has never been looked at

No browser was available while it was built — no `$DISPLAY`, no `xvfb-run`, browser
automation unavailable. Types, lint, 12 tests and the production build all pass, and the
data-level behaviour was confirmed by curl. **The rendered result is unverified.**

Worse, the Mocha stylesheet was *never imported* until that branch's final fix wave, so
the design has never actually rendered. Before building anything on top of the shell,
open `#/fleet` on a phone and check:

1. does it render in Mocha at all
2. the attention dot's position on the Fleet tab (`translate(14px,-10px)` is a guess)
3. `--text-faint` legibility outdoors — it carries every tab label
4. whether tapping feels alive
5. the tab bar on a notched phone (`env(safe-area-inset-bottom)`, 11px labels)
6. whether a stale blocked run is distinguishable from a fresh one

Quick oracle: the badge should show only *fresh* blocked runs while the Needs-you filter
shows *all* of them.

## Decisions already made — do not relitigate

- **The agent run is the primary object.** A terminal is one view of a run, not the app.
- **Correlation key is the tmux pane id.** Crew records key on `branch/<name>` because
  they know their branch, not their pane — so a dispatcher worker appears twice today,
  once as its pane and once as its branch. Joining them belongs with the Crews tab.
- **Sources publish sparse layers; the registry composes by fixed precedence**
  (`tmux → crew → hooks`, lowest first). A zero field means "no opinion".
- **A run exists only if some source claims an `Agent`.** The tmux layer still publishes
  enrichment for every pane, because enrichment is harmless alone.
- **houston never recomputes what lazytmux already computes** — branch, worktree, issue,
  PR state and crew name come from tmux user-options. No GitHub polling, no `gh`.
- **houston never resizes a pane.** `refresh-client -f ignore-size`, re-asserted on every
  reconnect. A human is attached to that pane in kitty.
- **`blocked` is the only state meaning "a human is required".** The freshness rule is
  asymmetric on purpose: nothing blocked is ever hidden, but only fresh ones count toward
  the badge. `FRESH_MS` in `ui/src/fleet/staleness.ts` is the one number that decides it.
- **Mobile and desktop get separate shells**, not one responsive layout.
- **Federation is designed but not built.** `Run.Host` exists; `""` means local.

## Known defects and follow-ups

Ordered by how much they matter.

1. **`Caps` can go stale true — the UI will offer an attach button for a dead pane.**
   `mergeInto` ORs capabilities and never clears them, and the hooks layer keeps
   asserting `Terminal: true` from a `TmuxPane` that outlives the pane in the state file.
   **Not fixable in change-detection** — `Caps.Terminal` never transitions true→false, so
   adding it to `runSignature` would broadcast nothing. Needs `Caps` derived at
   composition time from what is currently true. **Fix this before the terminal ships**,
   or the run detail view will offer terminals that cannot open.
2. **Nothing evicts finished runs.** `CrewSource` never emits `Gone`, so branches from an
   append-only log accumulate forever; the hub keeps ended sessions for 24h. Measured
   live: 21 runs of which ~10 were history.
3. **`%pause` re-seeds at the start of the gap, not the end**, so spec defect 2 is only
   partially closed. Dormant — nothing sets tmux's `pause-after`, whose default is 0, and
   `refresh-client -A %N:pause` sets a per-pane flag rather than that option. Closing it
   properly needs an expected-continue counter plus a resume path that cannot live in
   `readLoop`.
4. **No `server/` test exercises `pane_ws` at all.** That gap let an ack-ordering defect
   through five clean reviews in #3, and it still applies to the WebSocket origin path.
   Needs a WebSocket + tmux harness.
5. **`server/agents_test.go:90` has a real data race** under `-race`, pre-existing and
   `-race`-only. Tracked in #6. May be moot — the milestone that deletes `/api/agents`
   removes the test with it.
6. `Server.Start(ctx)/Close()` lifecycle — sources and the hub run under
   `context.Background()` and outlive the server. Every `New()` in a test leaks three
   goroutines plus the delta drain.
7. `runSignature` omits `Tokens`, so token counts refresh only when something else changes.
8. `crewDir` caches negative results for the process lifetime.
9. `useRuns()` has no lifecycle test (does unmount close the `EventSource`?) — needs a DOM
   test utility.
10. Multi-host grouping in the Fleet view is unexercised; every run is currently host-less.
11. `gofmt -l` is non-empty on `main` (`server/server.go`, `tmux/client.go`,
    `tmux/control.go`, `tmux/escape.go`) — all pre-existing, one `gofmt -w` clears it.

## Next plans, in order

Each should be its own plan document, worktree, and PR, following
`docs/superpowers/plans/` precedent.

### A. Run detail and the mobile terminal
The largest remaining piece, and what makes the shell useful rather than informational.

- Tapping a run opens a detail view with `Activity / Terminal` tabs (`Diff` later).
- The terminal reuses the existing xterm.js component and pane WebSocket — that transport
  is already hardened by #3.
- **The mobile terminal work from the spec lands here**: zoom selects a *font size*
  (9–24px floor), not a CSS scale; pan is primary with magnetism to column 0; a column
  scrubber; follow-the-cursor auto-pan; double-tap for readable ⇄ fit. Today's zoom is a
  CSS transform over a 13px terminal, so no zoom level is both readable and crisp except
  exactly 1.0.
- Fix `ui/src/hooks/useTouchGestures.ts:43` — the hardcoded `13 * 1.2` line height breaks
  the moment font size becomes variable.
- **Prerequisite: fix `Caps` staleness (defect 1)**, or the view will offer dead terminals.

### B. Crews, Workspace and Dispatch tabs
Fills the three placeholders.

- Crews: group runs by `Crew`, show tier and PR badges, and — the point of it — answer a
  blocked worker's question via `crew reply`.
- Workspace: the tmux tree across hosts, including panes that are not agents.
- Dispatch: repo, task, tier, engine → shell out to `dispatch`.
- Joining crew runs to their panes belongs here (a `branch → session:window` map is free
  from `ListWindowOptions`; only `session:window → pane_id` is missing).

### C. Desktop console shell
Rail (hosts, crews, filters) / fleet list / detail pane, sharing the same store and tokens.

### D. Retire the old surface
Only once A–C make the new shell sufficient: delete `#/agents` and `#/panes`, their
components, `agents.css`, the `--ag-*` tokens, `/api/sessions`, `/api/agents`,
`/api/pane/*`, and demote screen-scraping to amp/generic.

### Independent of all of the above
**#6 — CI.** No workflows exist; review has been the only gate for six merged PRs. This
is the one piece of remaining work that parallelises cleanly.

## Working practices that actually caught things

Across four plans, **every Critical defect originated in plan text, and none were caught
by a passing test suite.** What did catch them:

- **Prove a bug before fixing it.** Reproduce it, then fix, then confirm the reproduction
  fails. The reconnect hot-spin reported *99,015 dials in 300ms* against the buggy code;
  the DNS-rebinding hole was demonstrated with `curl -H "Host: evil.example"` returning
  the real token.
- **Prove a regression test bites.** Temporarily break the code and watch the new test
  fail. Several tests that "passed first try" turned out to pass against the bug too.
- **Verify claims rather than accept them.** "Tests pass" hid a red commit; "verified"
  hid a test whose assertion could not fail; a 300× test slowdown was found only by
  comparing wall-clock against a baseline.
- **For CSS, a passing build proves nothing.** The entire Mocha token file was unimported
  through a full plan — types, lint, tests, build and an Opus review all green. Grep the
  built bundle for actual declarations.
- **A comment stating the wrong reason is worse than none.** Three times a correct
  behaviour carried a false rationale ("browsers always send Origin", "the sole WebSocket
  writer", "older git answers relatively"), each an invitation to a confident wrong change.
- **Ask reviewers falsifiable questions.** "Is there a residual path to a state change
  without a valid token?" surfaced a critical hole that "looks fine" would have missed.
