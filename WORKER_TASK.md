tier: deep
kind: implement
draft: false
engine: claude
model: sonnet
effort: high
plan: required
title: Run detail view and the mobile terminal
Closes #26
dispatcher_pane: %2
crew_dir: /home/noams/git/houston/.git/crew
crew_id: 1788896428-153426
agent_name: slate
worker_id: worker:feat/26-run-detail-view-and-the-mobile-terminal#s1788937269-614699

## Task

Run detail view and the mobile terminal — plan A of the houston overhaul.

This is the largest remaining piece, and the one that makes the new shell useful
rather than informational. Today there is no way to reach a terminal from `#/fleet`.

## Read first

- `docs/superpowers/specs/2026-09-07-houston-overhaul-design.md` — **binding design
  authority.**
- `docs/superpowers/2026-09-08-state-of-play.md` — "Next plans, in order", section A.
  Note this doc is now **partly stale**: it lists prerequisites and defects that have
  since landed (see "What has changed" below). Trust the code over the doc where they
  disagree, and say so if you find a contradiction.

Do not relitigate anything under "Decisions already made". In particular: **the agent
run is the primary object — a terminal is one view of a run, not the app**, and
**houston never resizes a pane** (`refresh-client -f ignore-size`, re-asserted on
every reconnect, because a human is attached in kitty).

## What has changed since that doc was written

All merged to `main` this session — build on them, don't rebuild them:

- **#20** — `Caps` is now derived at composition time, so `Caps.Terminal` correctly
  goes false for a dead pane and the transition is broadcast. This was the stated
  hard prerequisite for this work: **you can now trust `Caps.Terminal` to decide
  whether to offer an attach affordance.** Do trust it; do not re-derive it in the UI.
- **#25** — `server/pane_ws.go` now has a real test harness (`server/pane_ws_harness_test.go`)
  plus auth, protocol, ordering and teardown coverage. It also fixed a live
  `metaPollLoop` leak. If you touch anything server-side on that path, extend those
  tests rather than starting a new pattern.
- **#24** — the frontend now has a DOM test utility (happy-dom + `@testing-library/react`)
  and a **reusable fake `EventSource`** at `ui/src/testing/fakeEventSource.ts`, built
  deliberately for the hook this work adds. Use it.
- **#22** — the fleet shell is notch-safe (`viewport-fit=cover` now actually applies,
  so `env(safe-area-inset-*)` is live) and its text tokens clear WCAG AA. Any new
  surface must hold both properties.
- **#19** — CI now gates build, vet, test, race, gofmt, golangci-lint, eslint and tsc
  on every PR. Frontend vitest is **not** yet gated (tracked in #23) — run
  `npx vitest run` yourself.

## ⚠️ The protocol documentation is wrong

`CLAUDE.md`'s "WebSocket Protocol" section documents an `output:` / `meta:` /
`resize-done` string-prefix protocol. **The code has not spoken that since the
control-mode rewrite.** The real protocol is a JSON envelope,
`{"type":"...","data":{...}}` — `dims` / `seed` / `output` / `meta` server→client,
`input` / `resize` client→server. There is no `special` message type and no
`resize-done` ack anywhere in `pane_ws.go`.

The authoritative reference is the code plus `server/pane_ws_protocol_test.go`.
Fixing `CLAUDE.md` is in scope for this PR — it is a two-paragraph correction and
leaving it wrong invites a confident wrong change later.

## What to build

### 1. Run detail

Tapping a run in `#/fleet` opens a detail view with **`Activity` / `Terminal`** tabs.
(`Diff` is explicitly later — do not build it.) Design the route, the back
affordance and the state model yourself; it must survive a reload and a direct deep
link, and it must not lose the fleet list's scroll position on back.

Offer the Terminal tab **only when `Caps.Terminal` is true**, and handle it going
false while the view is open — the pane can die under you.

### 2. The mobile terminal

This is the substance, and the spec is specific because the current behaviour is
known-wrong. Today's zoom is a CSS transform over a 13px terminal, so **no zoom
level is both readable and crisp except exactly 1.0.**

- **Zoom selects a font size** (floor 9px, ceiling 24px), *not* a CSS scale. Text
  must be crisp at every level.
- **Pan is the primary gesture**, with magnetism to column 0 — the left edge is where
  the content is, and drifting off it is the common frustration.
- **A column scrubber** for coarse horizontal position.
- **Follow-the-cursor auto-pan** so output stays visible as the agent writes.
- **Double-tap toggles readable ⇄ fit.**
- **Fix `ui/src/hooks/useTouchGestures.ts:43`** — the hardcoded `13 * 1.2` line height
  breaks the moment font size becomes variable. This is not optional cleanup; it is
  load-bearing for everything above.

Reuse the existing xterm.js component and the pane WebSocket. That transport is
hardened (#3) and now tested (#25) — do not write a second one.

## Verification — a green build proves nothing here

This repo has twice shipped UI that built cleanly and did not work: a Mocha
stylesheet that was never imported, and a `viewport-fit=cover` that was missing so
every safe-area inset silently resolved to `0px`.

You have browser automation (playwright / firefox-devtools MCP). **Use it.** Run the
app, open a real run's terminal at a mobile viewport, and drive the gestures. Attach
screenshots to the PR for every visual claim. Grep the **built bundle** for any CSS
you add, not just the source.

Per the state-of-play's working practices, which are binding: prove a bug before
fixing it, and prove each regression test bites by breaking the code and watching it
fail before you rely on it.

## Scope boundary

You own `ui/src/fleet/`, `ui/src/hooks/`, `ui/src/components/` (terminal-related),
and the `CLAUDE.md` protocol correction. No other worker is running — the roster is
otherwise empty — so you are not racing anyone, but stay out of `#/agents` / `#/panes`
and the old API: **retiring those is plan D and explicitly gated on A–C.**

If you find something real but out of scope, open a GitHub issue and reference it in
the PR body rather than expanding.

## Acceptance

- A run detail view reachable from `#/fleet`, with Activity and Terminal tabs,
  surviving reload and deep link.
- Terminal offered only when `Caps.Terminal` holds, and degrading correctly when it
  drops.
- Font-size-based zoom (9–24px), pan with column-0 magnetism, column scrubber,
  follow-the-cursor, double-tap toggle — each demonstrated in a screenshot or a test.
- `useTouchGestures.ts` no longer assumes a fixed line height.
- `CLAUDE.md`'s WebSocket Protocol section matches the code.
- `npx vitest run`, `npx tsc -b`, `npx eslint .`, `go test ./...` and the production
  build all pass; CI green on the PR.
