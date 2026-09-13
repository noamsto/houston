# Desktop console shell, and the new UI as the default view

Closes #42. Plan C of `docs/superpowers/2026-09-08-state-of-play.md`.

## Problem

The Mocha/Fleet UI (`ui/src/fleet/`) is reachable only at `#/fleet`; an empty hash
opens the classic `agents` view (`ui/src/App.tsx` `initialView`). On a desktop the
fleet shell is the phone layout stretched — one card column and a bottom tab bar. The
design (#2, state-of-play "Decisions") says desktop gets **its own shell**, a console of
rail / list / detail, sharing the same `useRuns()` store and Mocha tokens — not a
breakpoint of the mobile shell.

## Requirements

### R1 — default view
- `viewForHash(hash)` (exported from `App.tsx`, pure) maps: `#/agents` → `agents`,
  `#/panes` → `panes`, anything else — including `''`, `'#'`, `'#/'`, `#/fleet…` and
  unknown hashes — → `fleet`.
- Classic views are kept intact. The fleet "Classic" link still sets `#/agents`; the
  classic `ViewSwitch` still offers Fleet.
- An unknown hash landing on fleet (rather than agents) is deliberate: the new shell is
  the product, classic is the explicit opt-out.

### R2 — shell selection
- `Shell` (the `#/fleet` entry point) owns the one `useRuns()` + `useNow()` and picks a
  layout with `useIsDesktop()` (`(min-width: 1024px)`, the breakpoint the classic Panes
  view already uses):
  - `<1024px` → `MobileShell`: today's `Shell` body verbatim, now receiving
    `runs/connected/hasSnapshot/now` as props instead of calling the hooks. No markup or
    CSS change, so it is visually unchanged.
  - `≥1024px` → `ConsoleShell`.
- Hoisting the store above the switch means crossing the breakpoint (window resize)
  swaps layouts without closing and reopening the EventSource — still exactly one data
  path.
- **Breakpoint justification:** 1024px fits a 220px rail + 380px list + ≥420px detail,
  the narrowest a terminal detail pane is useful at; it also matches `useIsDesktop`, so
  the classic and new UIs agree on what "desktop" is.

### R3 — console shell layout
A three-column CSS grid filling the viewport: **rail | list | detail**. Mocha tokens
only (`.mocha` scope, semantic tokens, no `--ctp-*` in components).

**Rail** (top to bottom):
1. Title "houston" and the attention badge — count of `needsYou` runs (fresh blocked),
   or "all quiet" / "offline", identical rules to `FleetView`'s badge. Clicking it
   selects section Fleet + filter Needs you.
2. **Fleet** filters — Active / Needs you / All. Each count is how many runs that filter
   lists over **all** runs (so Needs you = every blocked run, fresh or stale; the badge
   alone counts fresh ones). Selecting one sets section = Fleet and the filter;
   selecting Needs you also clears the crew filter.
3. **Hosts** — read-only: one entry per distinct `run.host || 'local'` (only `local`
   exists today) with its run count. No host filter — with one host it would filter
   nothing; the fleet list already groups by host.
4. **Crews** — one entry per distinct non-empty `run.crew.name`, short-id label as in
   `CrewsView`, member count and fresh-blocked count. Clicking toggles a crew filter on
   the fleet list (section = Fleet, current state filter kept); clicking the active crew
   clears it. The crew filter narrows whichever state filter is on.
5. **Views** — Crews, Workspace, Dispatch: switch the list column to that surface.
6. Footer: "Classic" link → `#/agents`.

**List** column, by section:
- Fleet → the fleet run list: exactly `FleetView`'s filtering, sorting and host grouping
  (extracted into a shared pure function so the two shells cannot drift), further
  narrowed by the rail's crew filter when set, rendered with `RunCard`. The selected
  run's card is marked (`aria-current="true"`, `.selected`). Header shows the active
  filter name; when a crew filter is on it shows a chip with a clear button and
  "N of M" (M = the count without the crew filter), so narrowing is never silent.
- A selected run that is not in the current list (filtered out, or opened from
  Workspace/Crews) still shows in the detail column; no card is marked and filters are
  not widened.
- Crews → `CrewsView` (keeps `ReplyComposer` for crew-blocked runs).
- Workspace → `WorkspaceView`.
- Dispatch → the same placeholder copy as mobile.
- Fleet and Crews stay mounted when hidden (same reason as mobile: the filter and a
  half-typed reply survive a section switch; scroll position is not promised).

Each grid cell is `position: relative; overflow: hidden`, because the reused views
(`.fleet`, `.crews`, `.workspace`, `.shell-placeholder`, `.run-detail`) are all
`position: absolute; inset: 0` and would otherwise cover the whole console.

**Detail** column: `RunDetail` for the selected run (activity / terminal tabs,
terminal); an empty state "Select a run" when nothing is selected. `RunDetail` is reused
as-is; its "‹ Fleet" back button clears the selection, which on desktop reads as "close".

**Invariant honoured — nothing blocked is ever hidden:**
- Rail counts (badge, Active / Needs you / All) are **global** — computed over all runs,
  never narrowed by the crew filter. They answer "what is out there", like mobile.
- Clicking the badge **clears the crew filter**, sets section Fleet and filter Needs you,
  so the list then shows every blocked run, fresh and stale. Selecting the Needs-you
  filter entry in the rail does the same.
- The only other narrowing (a crew filter, on any state filter) is always visible as a
  clearable chip with "N of M".
- The badge counts only fresh blocked runs (`needsYou`); the Needs-you filter shows all
  blocked runs — unchanged from mobile.

### R4 — selection in the hash
- Canonical selection route stays the existing one: `#/fleet/<id>/<activity|terminal>`,
  shared by both shells so a link copied on desktop opens the same run on a phone.
- `#/fleet/<id>` (no tab) is also accepted and means `activity`. An unknown tab means
  `activity` (as today). `#/fleet`, `#/fleet/` and an empty id return `null`.
- Parsing lives in one exported pure function `parseDetailRoute(hash)` used by both
  shells. Opening a run from any list writes `#/fleet/<id>/activity`; reload restores
  the selection; `hashchange` (back/forward) updates it.
- Section and filters are not in the hash (mobile's tab isn't either).
- Run ids are URL-safe by construction (`runs/registry.go` `idFor`: `pane-N`,
  `sess-<uuid>`, `crew-`/`key-` + base64url — never `/` or `%`), so they go in the hash
  unencoded and the route splits on `/`.

### Accepted consequences
- Intended mobile behaviour changes (routing only, no markup/CSS): an empty or unknown
  hash opens fleet instead of agents, and a tab-less `#/fleet/<id>` now opens detail.
- Crossing 1024px unmounts one shell and mounts the other: the EventSource survives, but
  per-shell UI state (mobile tab, filters, rail section, half-typed replies) resets.
  Resizing across the breakpoint is rare and this is cheaper than sharing UI state.

## Non-goals
State inference / hooks; federation; retiring classic views; any backend change;
changing mobile markup or CSS; keyboard navigation beyond native button focus.

## Acceptance
- Empty hash on any viewport opens the new shell; `#/agents`, `#/panes` still work.
- ≥1024px: three-region console; <1024px: the existing mobile shell, unchanged.
- Vitest (happy-dom; `window.matchMedia` is stubbed in the test, with a `change`
  listener, since no shared mock exists): `viewForHash`; `Shell` picks console vs mobile
  and swaps on a `change` event without constructing a second `EventSource`; `parseDetailRoute` incl. tab-less form; console
  selection — hash preselects a run's detail, clicking a card writes the hash and the
  detail follows, `hashchange` updates it; crew narrowing with "N of M"; with a crew
  filter on, clicking the badge lists every blocked run (fresh and stale). Existing
  tests pass; `tsc -b`, eslint clean.
- Screenshots against a built server, headless Chromium via CDP: desktop 1400×900 on the
  branch; phone 400×860 of `#/fleet` both **before** (main's build) and after, so
  "visually unchanged" is checkable.
