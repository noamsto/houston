# Run detail view and the mobile terminal

Closes #26. Scope: `ui/src/fleet/`, `ui/src/hooks/`, `ui/src/components/` (terminal-related), `ui/src/App.tsx`'s hash matcher, `CLAUDE.md`.


Binding authority: `docs/superpowers/specs/2026-09-07-houston-overhaul-design.md`
("Mobile terminal" section, "Decisions", `Caps`). Context:
`docs/superpowers/2026-09-08-state-of-play.md` §A (partially stale — trust code
over doc; `Caps` staleness (defect 1) is already fixed by #20, treat
`Caps.Terminal` as trustworthy).

## Problem

`#/fleet` lists runs but tapping one does nothing — there is no way to reach a
terminal from the new shell. The old `#/panes` terminal (`TerminalPane.tsx`)
still exists and is the terminal implementation to reuse, but its "zoom" is a
CSS `transform: scale()` over a font pinned at 13px, which the design doc
diagnoses as unfixable: no scale level is both readable and crisp except 1.0.

## Scope

In scope: `ui/src/fleet/` (new run-detail route + tabs), `ui/src/hooks/`
(touch gestures, a shared mobile-terminal hook), `ui/src/components/`
terminal-related pieces reused by the detail view, `ui/src/App.tsx`'s hash
matcher (needs a prefix match — see "Current state"), and `CLAUDE.md`'s
WebSocket Protocol section.

Out of scope (explicitly deferred, do not touch): `#/agents`, `#/panes`, the
old `/api/sessions` `/api/agents` `/api/pane/*` routes (beyond addressing the
existing `/api/pane/:target/ws` route from the client — see "Current state"),
the `Diff` tab, Crews/Workspace/Dispatch tabs, and typing input into the
terminal (no `input` WS messages from this view — see "Non-goals"). No Go
changes are anticipated; the addressing scheme in "Current state" below
requires none. If execution finds that scheme doesn't hold up under the live
verification this spec requires, stop and escalate (block→await, or open a
GitHub issue and note it in the PR) rather than silently adding a new server
endpoint — the M1 API surface in the design doc already plans
`/api/runs/{id}/terminal`, and a bespoke pane-scoped route added here would
cut against that direction.

## Current state (verified against code, not doc)

- `Run.ID` (`runs/registry.go:idFor`) is already URL-path-safe with no
  slashes: `pane-307`, `sess-<sid>`, `branch-<base64url>`, or
  `key-<base64url>`. It is a single path segment as-is — no encoding scheme
  needed for the route itself.
- `Run.tmux` (`ui/src/api/runs.ts`) carries `{session, window, pane_id}`.
  `pane_id` is a tmux global pane id like `"%307"`.
- The pane WebSocket route is `/api/pane/:target/ws`
  (`server/server.go:parsePaneTarget`, reached via
  `server/api.go:handleAPIPane`). `target` is normally `session:window.pane`,
  but when the path has no `:`, the whole segment becomes `Session` with
  `Window=Index=0`, and `Pane.Target()` returns that segment verbatim when
  `Window==0 && Index==0` (`tmux/client.go:56-63`). tmux itself accepts a bare
  `%pane_id` as a valid `-t` target, so addressing a pane by its bare
  `pane_id` (no window/index needed) is the right idea — **but naively
  putting `%307` in the URL does not work.** `handleAPIPane` trims `/api` off
  `r.URL.Path`, which Go's `net/http` has *already* percent-decoded once; then
  `parsePaneTarget` calls `url.PathUnescape` on it *again*
  (`server/server.go:745`) — two decode passes total between the wire and the
  target string. A single `encodeURIComponent(run.tmux.pane_id)` only
  survives one decode pass and arrives mangled (`%307` → wire `%25307` →
  first decode `%307` → second decode `07`, losing the `%3`). To have
  `parsePaneTarget` see the literal `"%307"` after both decodes, the client
  must encode it **twice**:
  `encodeURIComponent(encodeURIComponent(run.tmux.pane_id))`.
  **This exact derivation must be verified against a live pane (both a
  Go-side unit test on `parsePaneTarget` with the once-already-decoded
  intermediate string, and a real browser connecting to a real pane, against
  the production build — `just ui-build && go build` — not the Vite dev
  proxy, which sits between browser and Go and may not preserve the raw
  `%2525…` bytes) before being relied on** — see Verification. If it doesn't
  hold up, stop and escalate per "Scope" above rather than improvising a new
  route. **The encoding is applied by whoever builds the WS URL** —
  `usePaneSocket.ts` interpolates its `target` argument into the URL with no
  encoding of its own — so the call site that constructs `target` from
  `run.tmux.pane_id` is where both `encodeURIComponent` passes belong, not
  inside the hook.
  - **This addressing scheme degrades the `meta` message and forks the
    control client — both accepted, neither fixed here (fixing either needs
    a Go change, which is out of scope).** With `Session="%307",
    Window=0, Index=0`: `metaPollLoop`'s `ListPanes(pane.Session, pane.Window)`
    (`server/pane_ws.go:316`) runs `list-panes -t "%307:0"`, not a valid tmux
    target, so `paneCommand`/`panePath` come back empty and agent/mode
    detection in `meta` runs degraded; `ListWindows` + matching on
    `pane.Window` (`:326-332`) can pick the wrong window, making
    `WindowName` unreliable. Separately, `ControlManager.GetClient` keys its
    ref-counted client map on the literal session string
    (`tmux/control_manager.go:28-43`), so a bare `%307` target opens a
    *second* `tmux -CC attach-session` for a real session that `#/panes` may
    already be attached to under its real name — wasteful, not broken.
    **Consequence for design:** the detail view must not trust the `meta`
    message for anything — agent type, state, and activity are already known
    from the `Run` object the detail view is rendering (that's the point of
    the `Run` spine per the design doc: sources compose it, the UI doesn't
    re-derive status from pane output). If reusing `TerminalPane.tsx`
    verbatim, its `onMeta`-driven `PaneHeader` must not be rendered in the
    detail context — see requirement changes below.
  - **The one call on this path that takes a session (not pane) target is
    unexamined risk, not accepted degradation, and must be the *first* thing
    verified — ahead of everything else in this scheme.**
    `ControlManager.GetClient(pane.Session)` (`server/pane_ws.go:92`) runs
    `tmux -CC attach-session -t <session>` (`tmux/control_client.go:74`).
    Every other call on this path (`GetPaneID`, `CapturePane`, `GetPaneSize`,
    `WindowPaneCount`, `IsZoomed`) targets a *pane*, which tmux resolves a
    bare `%307` against directly — but `attach-session -t` resolves a
    *session* target, and whether tmux accepts a pane id there at all is not
    established by anything above. If it doesn't, the whole scheme fails
    hard (the socket closes immediately at `pane_ws.go:93-97`), not merely
    degrades. **Before any of the double-encoding UI work, run
    `tmux -CC attach-session -t %<real-pane-id>` by hand (or in a scratch
    script) against a live pane and confirm it attaches.** If it fails, the
    addressing scheme in this spec does not work, and the fallback is
    `session:window.0` (using `Run.tmux.session`/`window` directly, no
    encoding tricks needed) — correct for any agent pane that is the sole
    pane in its window, which is the common case, but imprecise if a window
    has multiple panes. Escalate rather than silently switching schemes if
    this happens.
- The real WS protocol (verified against `server/pane_ws.go` and
  `ui/src/hooks/usePaneSocket.ts`, not `CLAUDE.md`) is a JSON envelope with
  exactly four server→client message types — `dims`, `seed`, `output`,
  `meta` — and two client→server — `input`, `resize`
  (`{"type":"...","data":{...}}`). **There is no `reseed` message type on the
  wire**: a mid-stream re-seed (e.g. after a resize) is sent as an ordinary
  `seed` (`server/pane_ws.go:writeSeed`, called from both the initial seed
  path and the re-seed path). `usePaneSocket`'s `onReseed` callback is
  therefore dead code — never invoked by the real server. There is no
  `special:` message type on this WebSocket, but the string still has a
  meaning elsewhere: it's a form parameter on the legacy, out-of-scope
  `POST /api/pane/:target/send` (used by `MobileInputBar.tsx`, not touched by
  this feature). There is no `resize-done` ack anywhere. `CLAUDE.md` documents
  a stale `output:`/`meta:`/`resize-done` string-prefix protocol from before
  the control-mode rewrite, and a WIDE/FIT toggle that no longer exists in
  code (see below) — correct both as part of this change, and relocate rather
  than delete the `special` mention (it still describes the legacy send
  route).
  - **Consequence for behavior, not just docs:** because a re-seed arrives as
    an ordinary `seed`, and `TerminalPane`'s `onSeed` handler calls
    `writeSnapshot` (which clears scrollback), a mid-session re-seed resets
    scrollback. The mobile terminal rework must decide what pan/zoom does
    when a `seed` arrives after the initial one — see requirement 8 below.
- `usePaneSocket(target, callbacks)` is transport-only and reusable as-is,
  modulo the dead `onReseed` callback (leave it or remove it — execution's
  call, it has no behavioral effect either way since the server never sends
  it).
- `useTouchGestures.ts:43` hardcodes `const lineHeight = 13 * 1.2`, tying
  scroll-line math to the pinned 13px font. This breaks the instant font size
  becomes a variable, which this spec introduces.
- `TerminalPane.tsx` (502 lines) currently: pins `fontSize: 13` on the shared
  `Terminal` constructor (line 206, used by both desktop and mobile — the
  desktop/mobile split is in the init branches around lines 231-242 and
  277-287, not in the constructor itself), implements pinch-to-zoom as a CSS
  `transform: scale()` on an inner wrapper clamped between `minScale`
  (fit-all) and `2.0`, wires `term.onData` straight to `sendInput` on the
  desktop path (line ~281-283), and renders `MobileInputBar` (line ~494) and
  `PaneHeader` (line ~411, driven by the WS `meta` message) unconditionally.
  **There is no WIDE/FIT toggle in code today** — the design doc calls
  fit-width "the *removed* WIDE/FIT affordance" (it existed once; `CLAUDE.md`'s
  stale "Mobile Features" section still advertises it) — so "fit-width" must
  be defined fresh, not reused. This is the component the design doc's
  font-size-zoom rewrite targets, but **it cannot be reused verbatim** for the
  run-detail Terminal tab — see requirement changes below.
- `useLayout.ts` persists pane-split/focus layout state to a single
  `houston-layout` localStorage key, shared with `#/panes`. It is the
  existing precedent for "persisted per-device preference" the design doc
  calls for the font-size setting; adding a font-size field there is in
  scope per the design doc, but note it's read by the legacy `#/panes` view
  too — don't repurpose or remove any existing field.
- `Shell.tsx` holds tab state (`fleet | crews | workspace | dispatch`) as
  in-memory `useState`, not reflected in the URL hash. `App.tsx` matches the
  hash by **exact equality** (`window.location.hash === '#/fleet'`,
  `initialView()`) — a hash like `#/fleet/pane-307/terminal` falls through to
  the `agents` view today. This must change to a prefix match for `#/fleet`
  to keep supporting a nested detail route; that edit belongs to this task
  (now listed in Scope above).
- `FleetView.tsx` renders `RunCard`s from a `runs: Run[]` prop with an
  `onOpen?: (r: Run) => void` callback already declared in its props (currently
  unused by any caller — `Shell.tsx` doesn't pass it). `RunCard` presumably
  invokes it on tap; verify during execution.
- `FleetView`'s scroll container is `.fleet` itself
  (`fleet.css`: `position: absolute; inset: 0; overflow-y: auto`), nested
  inside `Shell.tsx`'s `hidden={tab !== 'fleet'}` wrapper. `hidden` maps to
  `display: none`, which collapses the scroll box and loses `scrollTop` on
  re-show — so that existing pattern (used today to preserve *filter* state
  across tab switches) does **not** by itself preserve scroll position, and
  must not be assumed to. The detail-view mount strategy needs its own
  answer to this — see requirement 1 in "What to build".
- `useRuns.ts`'s `applyEvent` deletes a run from its map entirely on a
  `removed: true` delta (the #20 eviction path) — a different code path from
  "id simply isn't in the snapshot" but the same externally-visible symptom
  for a mounted `RunDetail`: the looked-up run disappears out from under it.

## What to build

### 1. Run detail route (`ui/src/fleet/`, `ui/src/App.tsx`)

- A deep-linkable, reload-surviving route for "run detail, tab X" nested under
  `#/fleet`, e.g. `#/fleet/<run-id>/terminal` and `#/fleet/<run-id>/activity`.
  `Run.ID` is already a single URL-safe path segment (see "Current state") —
  no extra encoding needed for the id itself.
- `App.tsx`'s `initialView`/hash-equality check must become a prefix match on
  `#/fleet` (currently exact-equality; see "Current state") so this nested
  route actually reaches `Shell` instead of falling through to `agents`.
- Scroll preservation is an **observable** requirement, not an implementation
  hint: after navigating from a fleet card into detail and back, **the run
  card that was tapped must still be within the viewport** (not necessarily
  pixel-identical scrollTop — `FleetView` re-sorts by `updated_at` on every
  stream tick, so exact offset isn't a stable target). The existing
  `hidden={tab !== 'fleet'}` pattern does **not** achieve this on its own —
  `hidden` is `display: none`, which collapses `.fleet`'s scroll box and
  loses `scrollTop` (see "Current state") — so keeping `FleetView` mounted
  under `hidden` is necessary but not sufficient; verify whether `hidden`
  actually resets `scrollTop` in this codebase's usage before assuming a fix
  is needed, and if it does, either avoid unmounting the scroll container at
  all (route the detail view as an overlay/sibling rather than a replacement)
  or restore scroll position explicitly on return.
  - **Back from a cold deep link** (opened detail directly via URL/reload,
    no prior fleet-list visit in this session): back goes to `#/fleet` at
    its default scroll position (top) — there is no prior position to
    restore.
- A `RunDetail` component with `Activity` / `Terminal` tabs (no `Diff` tab —
  do not stub it, do not add a disabled placeholder for it; the design doc
  says it's hidden until M3, and this repo's own defect record shows
  speculative UI shipped un-verified before, so don't add surface no one can
  see yet).
- Look up the open run by id from the existing `useRuns()` snapshot (already
  the single store powering `Shell`). Two distinct "run not there" cases,
  both rendering the same "not found, go back" state rather than crashing:
  (a) deep link to an id never present in the current snapshot, and (b) a
  run present when the view opened that is later evicted — `useRuns.ts`'s
  `applyEvent` deletes it from the map on a `removed: true` delta (see
  "Current state"); the lookup silently starts returning `undefined` on a
  later render, it isn't a thrown error.
  - A third case is not "not found": a deep link to `.../terminal` for a run
    that **exists** but has `caps.terminal === false`. This isn't an error —
    fall back to the Activity tab (the same behavior as if the Terminal tab
    were simply never offered) rather than showing "not found".
  - **A fourth case is not "not found" either, and matters most for a cold
    deep link:** `useRuns()`'s `connected` flips true on the `EventSource`'s
    `open` event, which fires *before* the initial `snapshot` event lands —
    so on a fresh page load the runs map is legitimately empty for one tick
    before real data arrives, and every cold deep link would otherwise flash
    "not found" on exactly the path this feature's deep-link acceptance
    criterion tests. `RunDetail` must distinguish "haven't received the
    first snapshot yet" (show a loading state) from "received a snapshot and
    this id isn't in it" (the real not-found case). `useRuns()` returns only
    `{runs, connected}` today; add a `hasSnapshot` (or equivalent) flag to it
    — it's in scope under `ui/src/hooks/`.
- Activity tab: render the run's existing `Activity` fields (tool, hint,
  trail, preview, message) — reuse whatever `RunCard` already renders for
  activity rather than inventing new markup; check `RunCard.tsx` before
  designing this from scratch.
- Terminal tab: shown **only when `run.caps.terminal` is true** at the moment
  the tab button is rendered (trust it per #20 — do not re-derive from
  `run.tmux` presence or run state). If `caps.terminal` is false up front,
  don't render the tab button at all (not a disabled button — the design
  doc's capability model is "render affordances from capabilities", not
  "render and disable").
  - **While the Terminal tab is open, four things can happen to the
    underlying pane, and each must degrade the same way** — a "session
    ended" state replacing the xterm view, with the WebSocket closed and no
    reconnect attempted:
    1. `caps.terminal` flips `false` on the run (tmux layer stops seeing the
       pane — the design doc's `deriveCaps`/#20 path).
    2. The run itself is evicted (`removed: true`) — same visible symptom as
       the deep-link-to-evicted-run case above, but reached while already
       mounted.
    3. The pane WebSocket closes on its own (server-side write loop returns
       because the pane died) while `caps.terminal` is still stale-true —
       this is expected to be the *common* path, since the tmux-layer poll
       that would flip `caps.terminal` lags the pane's actual death. **The
       server closes with a bare `conn.Close()`** (`pane_ws.go:140`) — no
       close code, no reason — so the browser sees an ordinary `1006` on
       *every* close, identical to a phone screen lock or a network blip;
       `usePaneSocket` already reconnects immediately on `visibilitychange`
       for exactly that case (`usePaneSocket.ts:120-127`). A single close
       must **not** trigger "session ended" — that would kill a connection
       that was about to recover on unlock. Instead: treat it as ended only
       once `connected` has stayed `false` continuously for a fixed timeout
       (a few multiples of the reconnect backoff, e.g. ~10s) while
       `caps.terminal` is still true, and at that point set the socket's
       `target` to `null` to stop `usePaneSocket`'s backoff loop rather than
       letting it retry a pane that no longer exists.
    4. The top-level SSE stream disconnects (`connected: false` from
       `useRuns()`) — `caps.terminal` on the stale snapshot may still read
       `true`. Treat this distinctly from 1-3 if practical (it's "we don't
       know", not "it's gone") — e.g. a "reconnecting" indicator rather than
       "session ended" — but do not leave the terminal silently frozen with
       no indication either way.
    - **Recovery:** if `caps.terminal` later flips back `true` on the same
      run id (pane respawned) while the "session ended" state is showing,
      re-offer the Terminal tab / a reconnect action rather than staying
      permanently stuck in the ended state.

### 2. The mobile terminal rework

Target files: `ui/src/components/TerminalPane.tsx` (or a new component/hook
split out of it — execution's call, see "Design it twice" below),
`ui/src/hooks/useTouchGestures.ts`, `ui/src/hooks/useLayout.ts` (persisted
font-size preference).

Required behavior, each independently demonstrable:

1. **Zoom selects a font size (9–24px), not a scale.** During an active pinch,
   keep using the CSS transform (60fps, no re-layout mid-gesture). On gesture
   end, snap to the nearest of a small discrete set of real font sizes within
   [9, 24]px, call `term.options.fontSize = <snapped>`, and reset the wrapper
   transform to scale 1.0/no translate-scale residue. At rest (no gesture in
   flight) the transform's scale component is always 1.0 — text is rendered
   at its real size, never resampled.
2. **Pan is the primary gesture, with magnetism to column 0.** One-finger
   horizontal drag pans; on release, if the pan is within some small threshold
   of column 0 (or below some velocity), ease back to column 0 rather than
   leaving it at a near-zero-but-not-zero offset. Column 0 is the left edge of
   real content — the terminal's own leftmost column, not the padded
   container edge.
3. **A column scrubber** — a persistent, coarse horizontal position
   indicator/control below the terminal viewport, showing what slice of the
   full pane width is currently visible and draggable to jump.
4. **Follow-the-cursor auto-pan** — when new terminal data arrives (the
   `output` message, and the `seed` message on an initial connect/re-seed —
   see "Current state": there is no `reseed` message type on the wire, both
   cases arrive as `seed`) and no touch gesture is active, ease the
   horizontal pan so the cursor's column stays in view. Must be suppressed
   while a touch gesture is in progress (don't fight the user's finger).
5. **Double-tap toggles readable ⇄ fit-width.** There is no existing WIDE/FIT
   toggle in code to reuse (see "Current state" — it was removed before this
   codebase's current state and only survives as a stale `CLAUDE.md`
   mention); define fit-width fresh: the font size at which the pane's full
   column count fits the viewport width, **still floor-clamped at 9px** — if
   the full width doesn't fit even at 9px, fit-width shows as much as fits at
   9px rather than going smaller (the 9px floor is absolute, per the design
   doc's "text can't be too small" rationale; fit-width does not override
   it). "Readable" restores the persisted font size at column 0.
6. **`useTouchGestures.ts:43`'s `lineHeight = 13 * 1.2` must read the
   terminal's actual cell metrics** (xterm exposes actual cell dimensions,
   e.g. via the renderer/`term._core` internals already touched elsewhere in
   this file, or a public API if one covers it — verify which before using
   private internals) so vertical scroll math stays correct as font size
   varies.
7. **Font size is a persisted per-device preference**, following `useLayout`'s
   existing localStorage precedent — not derived from viewport size, and not
   reset by opening a different run's terminal.
8. **A mid-session `seed` message resets pan to column 0** (it already clears
   scrollback via `writeSnapshot` — see "Current state" — so any prior pan
   offset is now pointing at content that no longer exists at that
   coordinate). Font size is a preference and is unaffected by a `seed`.

**The run-detail Terminal tab is read-only and header-less — this decides the
reuse shape.** It must not render `MobileInputBar`, must not wire
`term.onData` to `sendInput` (desktop or mobile — the input non-goal above is
not mobile-only), and must not render `PaneHeader` or otherwise act on the WS
`meta` message (which is unreliable under bare-pane-id addressing anyway —
see "Current state"). `TerminalPane.tsx` as it stands does all three
unconditionally, so **it cannot be mounted verbatim inside `RunDetail`.**
Concretely this means either: extract a smaller, headless terminal-rendering
piece (xterm instance + `usePaneSocket` wiring + the zoom/pan/scrubber
behavior, with no input, no `PaneHeader`) that both `TerminalPane.tsx` and
`RunDetail`'s Terminal tab consume; or add an explicit read-only mode to
`TerminalPane.tsx` itself (a prop that suppresses `MobileInputBar`,
`PaneHeader`, and the `onData` wiring) and mount it that way from `RunDetail`.
Either is acceptable; state which in the plan and why.

**`#/panes` is allowed — expected — to inherit the font-size zoom rework**,
since it's the same shared component and the same defect (the design doc's
"Mobile terminal" section describes fixing `TerminalPane.tsx`'s zoom
mechanism, not a fork of it for one view only; `useTouchGestures.ts` is
already shared and its line-43 fix is required regardless of which view is
open). "Out of scope: `#/panes`" therefore means *don't change its
navigation, sidebar, split-layout, or input behavior* — not "the shared
zoom/pan mechanism must stay byte-for-byte identical there." If the chosen
reuse shape above happens to leave `#/panes` on the old scale-zoom because
that's what the minimally-invasive path produces, that's acceptable too;
just state the outcome in the plan rather than treating "unmodified" as a
hard constraint that conflicts with fixing shared code.

**Desktop**: `App.tsx` renders `<Shell />` for `#/fleet` at any viewport, so
`RunDetail`'s Terminal tab will also render on desktop. Requirements 1-8
above are touch-gesture-specific; on desktop the same read-only terminal
renders with those gestures simply inert (no touch events fire). No input
wiring applies on desktop either, per the read-only requirement above — this
is a deliberate difference from `TerminalPane.tsx`'s existing desktop path,
which does wire `onData`.

**Socket lifecycle**: each open pane WebSocket drives a server-side
`capture-pane` roughly every second while attached. Closing the detail view,
or switching from the Terminal tab to Activity, must close the pane socket
(not leave it polling in the background) — `usePaneSocket`'s existing cleanup
(closes on `target` transitioning away, including to `null`) already does
this; just make sure the calling component actually passes `null`/unmounts
rather than keeping the hook alive with a stale target.

**Design it twice before writing code** (per house convention): sketch (a) a
minimally-invasive extraction — pull just the zoom/pan/scrubber/gesture logic
into a shared hook consumed by both a new headless terminal component (for
`RunDetail`) and unmodified by `TerminalPane.tsx` (for `#/panes`) — vs. (b) a
`readOnly` prop on `TerminalPane.tsx` itself that both views share, with
`RunDetail` passing `readOnly`. Pick based on how much of the 502-line file
is actually mobile-zoom-specific vs. input/header/layout-specific, and write
the chosen approach and why in the plan.

## Non-goals

- No changes to `#/agents`, `#/panes`, or their routes/components beyond what
  `TerminalPane.tsx` reuse strictly requires. "Existing behavior" here means
  navigation, sidebar, split-layout, and input — **not** the shared
  zoom/pan mechanism, which this task's own acceptance criteria require
  fixing and which `#/panes` may end up inheriting as a side effect (see the
  explicit "`#/panes` is allowed to inherit the font-size zoom rework"
  paragraph under "The mobile terminal rework" — that paragraph is
  authoritative over any apparent tension with this bullet).
- No new server endpoints. No changes to `pane_ws.go`'s protocol.
- No `Diff` tab, not even a stub.
- No resizing of the tmux pane from the client (`ignore-size` stays; already
  server-side, not touched).
- **No typing/input from the run-detail Terminal tab in this PR, on desktop or
  mobile.** The acceptance criteria (below, unchanged from `WORKER_TASK.md`)
  only require viewing and gesture navigation — none mention sending
  keystrokes. Do not wire in `MobileInputBar.tsx` (it POSTs to the legacy,
  out-of-scope `/api/pane/:target/send`) and do not wire `term.onData` to
  `sendInput` the way `TerminalPane.tsx`'s existing desktop path does.
  `usePaneSocket` already exposes `sendInput` if a later task adds this; note
  the gap in the PR body as a candidate follow-up rather than expanding scope
  to build it now.
- **No `PaneHeader` / WS-`meta`-driven UI in the run-detail Terminal tab.**
  Agent identity, state and activity for the header-equivalent area come from
  the `Run` object already available to `RunDetail`, not from the pane's
  `meta` message (see "Current state" — `meta` is unreliable under
  bare-pane-id addressing, and re-deriving status from pane output is exactly
  what the `Run` model exists to avoid).

## Verification (binding — see WORKER_TASK.md "a green build proves nothing here")

- Before writing UI around it, confirm the double-encoded pane-id addressing
  scheme (see "Current state") with a Go-side unit test on `parsePaneTarget`
  (input: the string as it would look after `net/http`'s automatic first
  decode — i.e. after one `encodeURIComponent` pass — asserting the resulting
  `Pane.Target()` equals the bare `pane_id`), *and* a live browser check
  against a real pane before trusting it end-to-end.
- Use browser automation (playwright/firefox-devtools MCP) against the real
  running app with a real active run, at a mobile viewport. Confirm:
  - the WS target derived from `run.tmux.pane_id` actually connects and
    streams real pane output.
  - the terminal renders crisply at multiple font sizes (screenshot each).
  - pan + column-0 magnetism, the column scrubber, follow-the-cursor, and the
    double-tap toggle each visibly work (screenshot or short capture per
    gesture where a still can show it).
  - reload on a `#/fleet/<id>/terminal` deep link restores the same view.
  - back from detail leaves the previously-tapped run card within the
    viewport (see requirement 1's observable definition — not a pixel-exact
    scrollTop check).
  - each of the four "Terminal tab open, pane goes away" paths in requirement
    1 degrades as specified — at minimum, force `caps.terminal` false and
    force the underlying tmux pane/session to end while the socket is still
    open (the more likely real-world path per the "common path" note).
- Grep the **built** bundle (`ui/dist`) for any new CSS class names you add —
  this repo has twice shipped CSS that never made it into the imported
  stylesheet.
- Prove the `useTouchGestures.ts` line-height bug before fixing it (show
  scroll drift/incorrect line counts at a non-13px font size against
  unfixed code), then prove the fix and the regression test both bite.
- `npx vitest run`, `npx tsc -b`, `npx eslint .`, `go test ./...`, and the
  production build (`just ui-build && go build`) must all pass.

## Acceptance criteria

Restated from `WORKER_TASK.md` — this spec adds no new acceptance criteria
beyond making them concrete:

- [ ] Run detail view reachable from `#/fleet` with Activity/Terminal tabs,
      surviving reload and direct deep link, without losing fleet scroll
      position on back.
- [ ] Terminal tab offered only when `Caps.Terminal` is true; degrades
      correctly when it drops while open.
- [ ] Font-size-based zoom (9–24px), pan with column-0 magnetism, column
      scrubber, follow-the-cursor, double-tap toggle — each demonstrated in a
      screenshot or a test.
- [ ] `useTouchGestures.ts` no longer assumes a fixed line height.
- [ ] `CLAUDE.md`'s WebSocket Protocol section matches the code (including
      dropping the stale `resize-done`/string-prefix description, correcting
      the stale WIDE/FIT mention in "Mobile Features", and relocating rather
      than deleting the `special` mention since it still describes the
      legacy `/api/pane/:target/send` route).
- [ ] `npx vitest run`, `npx tsc -b`, `npx eslint .`, `go test ./...`, and the
      production build all pass; CI green on the PR.

## Implementation Plan


Built from `SPEC.md` (accepted after 2 spec-critic revisions — read it for the
full rationale behind every decision below; this plan doesn't repeat the why).

Design decisions locked in by the spec, restated here so execution doesn't
re-litigate them:
- WS target for a run's pane = `encodeURIComponent(encodeURIComponent(run.tmux.pane_id))`
  (double-encoded, to survive the double percent-decode in
  `server/api.go` + `server/server.go:parsePaneTarget`).
- **Verify `tmux -CC attach-session -t %<pane-id>` attaches, by hand, before
  step 2.** If it doesn't, stop and escalate — do not silently switch to the
  `session:window.0` fallback described in `SPEC.md`.
- `TerminalPane.tsx` gets a `readOnly?: boolean` prop (default `false`). When
  `true`: no `PaneHeader`, no `MobileInputBar`, no `term.onData → sendInput`
  wiring, on both desktop and mobile. `RunDetail`'s Terminal tab renders
  `<TerminalPane pane={{id: run.id, target: <double-encoded>}} isFocused
  readOnly onFocus={noop} onClose={...} />` — `PaneInstance` is already just
  `{id, target}` (`useLayout.ts`), so no dependency on the split-layout
  reducer is needed. `#/panes` is unaffected (prop defaults to `false`) and
  automatically inherits the shared zoom-mechanism fixes below, which is
  expected per spec.
- Font size is persisted via a **dedicated hook in `useLayout.ts`**
  (`useTerminalFontSize()`), its own localStorage key
  (`houston-terminal-font-size`), plain scalar get/set — **not** folded into
  `LayoutState`/`STORAGE_KEY`. `useLayout()` is called exactly once today
  (`App.tsx`, inside `PanesApp`) and every call is an independent
  `useReducer` that snapshots localStorage at mount and rewrites the *entire*
  `LayoutState` object on every change (`useLayout.ts:159-161`); `#/panes`
  mounts several `TerminalPane`s under one such instance, and `RunDetail`
  would need its own separate instance (it isn't under `PanesApp`) — two
  independent reducers writing the same key would let a stale one clobber
  panes/layout/focus on save. A separate key with its own tiny hook avoids
  this entirely and still satisfies "persisted per-device preference,
  following `useLayout`'s existing localStorage precedent" (same file, same
  pattern, no shared reducer state).

## Steps

- [ ] **Step 1: Verify the tmux addressing scheme end-to-end, by hand, before
      writing any code.**
  - Start the app against a real running Claude Code pane (`just ui-build &&
    go build && ./houston`, or `just dev` — must be the production build per
    spec, not `just ui-dev`'s Vite proxy).
  - Find a real pane id (`tmux list-panes -a -F '#{pane_id}'`).
  - By hand, confirm `tmux -CC attach-session -t %<id>` attaches without
    error (Ctrl-C out immediately after).
  - Confirm the double-encoded string round-trips: in a scratch Node/browser
    console, compute `encodeURIComponent(encodeURIComponent('%307'))` and
    manually hit `ws://localhost:<port>/api/pane/<that-string>/ws` (e.g. via
    a scratch script using the `ws` package, or a browser console
    `new WebSocket(...)`) and confirm it streams real output for that pane.
  - **If either check fails, stop and post a `blocked` status** (per the
    worker harness's block→await protocol — see the crew bus / dispatcher
    escalation path this session is running under, not a file in this repo)
    rather than improvising — this is
    the one step whose failure invalidates steps 2, 3, and 6 (the ones that
    implement or test the double-encoded addressing scheme itself; the
    routing/shell work in steps 4-5 doesn't depend on it).
  - Record the pass/fail and any output in a scratch note for the PR body.

- [ ] **Step 2: Go-side regression test for the double-decode addressing
      scheme.**
  - File: `server/server_test.go` (or wherever `parsePaneTarget` is already
    tested — check first).
  - Add a test case: call `parsePaneTarget("/pane/%25307/ws")` (the real
    call shape — `/api` and `/pane/` prefixes plus the `ws` suffix are
    already stripped by the caller/parser before the final unescape; `"%25307"`
    is the string as it looks *after* `net/http`'s automatic first decode,
    simulating what `r.URL.Path` contains for a client that sent
    `%2525307` on the wire), assert it returns
    `Pane{Session: "%307", Window: 0, Index: 0}` and `.Target() == "%307"`.
  - Prove it bites: temporarily revert to asserting the single-encode
    behavior (`"%307"` as input) to confirm that mangles to `"07"`, then
    restore the real test.
  - Run `go test ./server/...`.

- [ ] **Step 3: Client-side pane-target helper + `useRuns` `hasSnapshot`
      flag.**
  - New file (or add to `ui/src/api/runs.ts`): a small function
    `paneWsTarget(paneId: string): string` returning
    `encodeURIComponent(encodeURIComponent(paneId))`, with a one-line comment
    citing the double-decode in `server/server.go:parsePaneTarget` (state the
    *mechanism*, not just "needed for encoding" — a wrong-reason comment is
    worse than none, per house convention).
  - Unit test: given `"%307"`, assert the exact output string
    (`"%2525307"`) — this is the client-side half of step 1/2's proof.
  - `ui/src/hooks/useRuns.ts`: add a `hasSnapshot: boolean` to the returned
    state, `false` until the first `snapshot` SSE event is applied, `true`
    after (don't touch `connected`'s existing semantics). Drive this with
    `ui/src/testing/fakeEventSource.ts`, and add the test to
    `ui/src/hooks/useRuns.lifecycle.test.ts` (already wired to
    `installFakeEventSource` via `renderHook`) rather than the plain
    `useRuns.test.ts`, which only unit-tests `applyEvent` directly — assert
    `hasSnapshot` is `false` before the first snapshot event and `true`
    after.

- [ ] **Step 4: `App.tsx` prefix-match routing for `#/fleet`.**
  - `ui/src/App.tsx`: change `initialView`'s `#/fleet` check from exact
    equality to a prefix match (`window.location.hash.startsWith('#/fleet')`),
    keeping `#/panes`'s check as-is (or also prefixing it if that's more
    consistent — check whether `#/panes` ever needs sub-routes; if not,
    leave it exact to minimize blast radius).
  - No new tests needed here if `Shell`/`FleetView` component tests (steps
    5-6) cover the resulting behavior; otherwise add a minimal test.

- [ ] **Step 5: Run-detail route + `RunDetail` shell (Activity tab only,
      Terminal tab stubbed to "not implemented yet").**
  - New file `ui/src/fleet/RunDetail.tsx`. `Shell.tsx` already calls
    `useRuns()` once (line 19, "the single store" per spec) — `RunDetail`
    must receive `runs`/`hasSnapshot` as **props from `Shell`**, not call
    `useRuns()` itself, which would open a second `EventSource`
    (`useRuns.ts:31`) per detail view.
  - `ui/src/fleet/Shell.tsx`: parse `#/fleet/<id>/<tab>` (via
    `hashchange`/`window.location.hash`, matching the existing lightweight
    pattern — no router library). Keep `FleetView` mounted (don't unmount it
    to show `RunDetail`) — render `RunDetail` as a sibling overlay/replacement
    panel *without* using `hidden` on `.fleet`'s own scroll container (see
    spec: `hidden` collapses `scrollTop`). Concretely: keep `.fleet`'s
    existing DOM position and scroll state completely untouched, and render
    `RunDetail` in a separate stacked layer (e.g. `position: fixed`/`absolute`
    covering the tab content) that mounts/unmounts independently.
  - Run lookup: `runs.find(r => r.id === id)`. Three non-crash states per
    spec: (a) `!hasSnapshot` → loading; (b) `hasSnapshot && !found` → "not
    found, go back"; (c) found but `tab === 'terminal' && !caps.terminal` →
    fall back to rendering the Activity tab content instead (URL stays as
    typed, just don't show a Terminal panel).
  - Activity tab: render `run.activity` (tool, hint, trail chips, preview,
    message, turn) plus header info (name/state/age) — factor
    `RunCard.tsx`'s `subtitle`/`nameLabel`/`agoLabel` helpers into a shared
    export (`ui/src/fleet/format.ts` or similar) so both `RunCard` and
    `RunDetail` use the same logic, then add a fuller trail/preview render
    for the detail view (RunCard's card only shows the one-line subtitle).
  - Terminal tab button: render only when `run.caps.terminal` is true; for
    now (this step) clicking it can show a placeholder — the real terminal
    lands in step 6.
  - Back affordance: a header back button plus browser back (hash change)
    both return to `#/fleet` and are equivalent.
  - Wire the fleet card tap to this route: `RunCard` already invokes
    `onOpen?.(run)` on click (`RunCard.tsx:46`, verified — no change needed
    there), but `Shell.tsx` currently doesn't pass `onOpen` to `FleetView` at
    all. Add
    `onOpen={(r) => { window.location.hash = \`#/fleet/${r.id}/activity\` }}`
    to the `<FleetView>` call in `Shell.tsx`. Without this the route is only
    reachable by typing a URL, which fails the "reachable from `#/fleet`"
    acceptance criterion.
  - Tests: a DOM test (using happy-dom + `@testing-library/react`, following
    `useRuns.lifecycle.test.ts`'s pattern, plus `fakeEventSource.ts` to drive
    the snapshot/update stream) for the deep-link/not-found/loading states,
    and a manual note that scroll preservation is verified by browser
    automation later (step 16), not unit tests (scroll restoration across a
    `hidden`-avoiding layer is easiest to prove visually).

- [ ] **Step 6: Mount a read-only terminal in the run-detail Terminal tab.**
  - `ui/src/components/TerminalPane.tsx`: add `readOnly?: boolean` to
    `Props` (default `false`, so `#/panes`'s existing call sites are
    unaffected without any change there). When `true`, on both the desktop
    and mobile paths: don't render `PaneHeader` (~line 411) or
    `MobileInputBar` (~line 494), and don't wire `term.onData → sendInput`
    in the desktop mount effect (lines ~277-287). Also pass
    `disableStdin: readOnly || !isDesktop` (or equivalent) to the `Terminal`
    constructor — suppressing only the `onData` wiring leaves desktop stdin
    enabled, so the terminal would stay focusable/typeable with keystrokes
    that go nowhere.
  - `RunDetail.tsx`'s Terminal tab (replacing the step 5 placeholder): when
    `run.tmux` is present (a TypeScript narrowing — `run.caps.terminal`
    being `true` is what actually authorizes showing the tab per spec; the
    `run.tmux` check here is just to satisfy the compiler that `pane_id`
    exists, since `Run.tmux` is typed optional) and `caps.terminal` is
    `true`, render
    `<TerminalPane pane={{id: run.id, target: paneWsTarget(run.tmux.pane_id)}}
    isFocused readOnly onFocus={() => {}} onClose={() => { /* back to fleet, see step 5 */ }} />`
    using the `paneWsTarget` helper from step 3. `PaneInstance` is already
    just `{id, target}` (`useLayout.ts:3-6`) so this needs no dependency on
    the split-layout reducer.
  - This step is what actually implements "the WS target derived from
    `run.tmux.pane_id` connects and streams real pane output" and the
    Terminal tab acceptance criterion — steps 8-13 below build the gesture
    behavior *on top of* this mounted terminal, they don't mount it.
  - Test: a DOM test asserting `readOnly` suppresses `PaneHeader`/
    `MobileInputBar` rendering (mock `usePaneSocket` to avoid a real
    WebSocket in tests, matching whatever mocking pattern
    `useRuns.lifecycle.test.ts`/`pane_ws_harness_test.go`-adjacent frontend
    tests already use — check for an existing WS-mocking helper before
    inventing one).

- [ ] **Step 7: `useTouchGestures.ts` line-height fix.**
  - Prove the bug first: write a component/DOM test that renders at a
    non-13px font size (e.g. mount with `fontSize: 20`) and shows the
    hardcoded `13 * 1.2` producing wrong scroll-line counts for a scroll
    gesture of a known pixel delta — confirm this test fails against
    *today's* code before changing anything. Note: happy-dom has no layout
    engine (`clientWidth`/`getBoundingClientRect()` read `0`), so this test
    needs injected/mocked dimensions rather than relying on a real
    `term.open()` layout — same caveat applies to steps 8-12's tests (the
    font-size zoom, magnetism, scrubber, follow-cursor, and double-tap work,
    all of which reason about real pixel dimensions).
  - Fix: `13 * 1.2` is `options.fontSize * options.lineHeight`, both set at
    construction (`TerminalPane.tsx:206-207`) and both public on
    `term.options` — **there is no `term._core` usage anywhere in `ui/src`
    to pattern-match against** (verified: none exists), so don't introduce
    one. Replace the hardcoded literal with
    `term.options.fontSize! * (term.options.lineHeight ?? 1)` (or measure
    the actual rendered row height off the `.xterm-rows` DOM if that proves
    more accurate in the live browser check) — use whichever a quick live
    check confirms matches real scroll behavior, and prefer the public
    `term.options` computation unless it's demonstrably wrong.
  - **Compute this inside the scroll handler, per event — not once at effect
    setup.** `useTouchGestures`'s effect (where the current `const lineHeight
    = 13 * 1.2` lives, line 43) has deps `[enabled, innerRef, outerRef,
    termRef]` and does not re-run when `term.options.fontSize` changes at
    runtime (step 8's pinch-end snap does exactly that). A value computed
    once at effect scope would go stale the instant the user zooms, silently
    reintroducing the same drift this step exists to fix — and wouldn't be
    caught by a test that mounts at one fixed font size. Read
    `termRef.current?.options.fontSize`/`.lineHeight` inside the `touchmove`
    handler itself (where `lineHeight` is currently consumed, line ~124-126),
    not as a closed-over constant.
  - Confirm the test from the first bullet now passes.

- [ ] **Step 8: Font-size zoom rework in `TerminalPane.tsx` +
      `useTouchGestures.ts`.**
  - `useLayout.ts`: add `useTerminalFontSize()` — a small standalone hook
    (its own `useState` + `useEffect` persisting to the dedicated
    `houston-terminal-font-size` localStorage key, default e.g. `14`) — **not**
    a field on `LayoutState`/the `useLayout()` reducer, per the design
    decision at the top of this plan (avoids two independent reducer
    instances clobbering `panes`/`layout` on save). Export it alongside
    `useLayout`.
  - `TerminalPane.tsx`: replace the hardcoded `fontSize: 13` with the value
    from `useTerminalFontSize()`. During an active pinch (2-finger),
    keep the existing CSS `transform: scale()` behavior for 60fps tracking.
    On gesture end (`touchend` with 0 remaining touches after a pinch),
    snap to the nearest of a small discrete set of real font sizes clamped
    to `[9, 24]`, call `term.options.fontSize = <snapped>`, call
    `useTerminalFontSize()`'s setter to persist it, and reset the transform's
    scale component to `1.0` (keep/reset translate as appropriate — re-run the existing
    `resetTransform`/`clampPan` logic against the new dimensions after
    `fontSize` changes, since cell metrics change).
  - `useTouchGestures.ts`: the pinch-end snap logic likely belongs here
    (it already owns gesture state) — add a callback prop (e.g.
    `onPinchEnd: (scale: number) => void`) rather than reaching into
    `TerminalPane`'s internals from outside.
  - Verify: at rest, the wrapper transform's scale is always `1.0` (no
    resampling) — add an assertion to whatever DOM test covers this, or a
    manual browser check (screenshot text crispness at 2-3 font sizes).

- [ ] **Step 9: Column-0 magnetism.**
  - `useTouchGestures.ts`: on pan-gesture end (`onTouchEnd`, single-finger
    pan), if the resulting `translateXRef.current` is within a small
    threshold of `0` (or the drag's final velocity is low and heading
    toward `0`), ease `translateXRef.current` to exactly `0` via a short
    animation (a `requestAnimationFrame` loop or CSS transition swapped in
    temporarily) rather than a hard snap.
  - Test: a DOM/unit test simulating a pan ending near column 0 asserts the
    final transform lands at translateX `0`; a pan ending far from column 0
    is untouched.

- [ ] **Step 10: Column scrubber.**
  - New component, e.g. `ui/src/components/ColumnScrubber.tsx` — a thin bar
    below the terminal viewport showing the visible-width fraction
    (`viewportWidth / totalTermWidthPx`) and its offset
    (`-translateX / totalTermWidthPx`), draggable to set `translateX`
    directly (clamped via the existing `clampPan` logic — expose it from
    `useTouchGestures` if it's currently a closure-local function).
  - Wire into `TerminalPane.tsx`'s mobile render path only (desktop doesn't
    need it — `fitAddon` already shows the full width there).
  - Test: a DOM test rendering the scrubber at a known scale/translate
    asserts its visible-fraction indicator's width/position.

- [ ] **Step 11: Follow-the-cursor auto-pan.**
  - `TerminalPane.tsx`: on `onOutput`/`onSeed` from `usePaneSocket` (see
    spec — there is no `reseed` on the wire), if no touch gesture is
    currently active (expose a `gestureActiveRef`/similar from
    `useTouchGestures`), read the cursor's column (`term.buffer.active.cursorX`
    or equivalent public xterm API) and ease `translateX` so that column is
    within the visible viewport, using the same easing mechanism as step 9.
  - Also implement requirement 8 here since it's adjacent: on a mid-session
    `seed` (not the very first one), reset `translateX` to `0` (scrollback
    just got cleared, per spec's reasoning) — distinguish "first seed after
    connect" from "later seed" with a ref flag.
  - Test: simulate an `output` event with the cursor near the right edge and
    assert the pan eases toward it; simulate a second `seed` event and
    assert `translateX` resets to `0`.

- [ ] **Step 12: Double-tap toggles readable ⇄ fit-width.**
  - `useTouchGestures.ts`: detect a double-tap (two `touchend` events within
    a short window at roughly the same point, single-finger, no drag in
    between).
  - Fit-width: compute the font size at which the pane's known column count
    fits the current viewport width, floor-clamped at 9px (per spec — never
    go below 9px even if that leaves content clipped).
  - Readable: the persisted value from `useTerminalFontSize()` (step 8),
    `translateX` reset to `0`.
  - Test: assert the toggle alternates between the two states and that
    fit-width respects the 9px floor when the math would otherwise go lower.

- [ ] **Step 13: `Caps.Terminal` degradation while the Terminal tab is open.**
  - `RunDetail.tsx` (or a small hook it uses, e.g. `useTerminalCapsWatch`):
    implement the four cases from `SPEC.md`'s requirement list. **Mechanism
    for "close the socket, no reconnect" in all cases below: unmount the
    `<TerminalPane>` element** (conditionally render it only in the "live"
    state) rather than trying to pass `target: null` through — `PaneInstance`
    (`useLayout.ts:3-6`) types `target` as a plain `string`, shared with
    `#/panes`/`SplitContainer`, so it can't be widened to nullable here.
    Unmounting triggers `usePaneSocket`'s existing cleanup effect
    (`usePaneSocket.ts:129-135`, closes the socket and clears the reconnect
    timer) exactly as passing `null` would have. Render the "session ended"
    (or "reconnecting", case 4) state in its place.
    1. `caps.terminal` flips `false` on the live run → unmount the terminal,
       show "session ended", no reconnect.
    2. Run evicted (`removed: true` via `useRuns`) → same treatment (this
       likely shares code with `RunDetail`'s "run disappeared" handling from
       step 5).
    3. Socket-closed-while-caps-stale-true: track `connected` from
       `usePaneSocket` — the simplest wiring is an optional
       `onConnectionChange?: (connected: boolean) => void` prop threaded
       through `TerminalPane` down to its `usePaneSocket` call, so
       `RunDetail` observes it without `TerminalPane` needing to know why.
       If it stays `false` continuously for ~10s while `caps.terminal` is
       still `true`, treat as case 1. Do **not** trigger on a single close —
       that would fight `usePaneSocket`'s existing `visibilitychange`
       recovery.
    4. SSE stream disconnect (`connected: false` from `useRuns()`) → a
       distinct "reconnecting" indicator (terminal can stay mounted here —
       this is "we don't know", not "it's gone", per spec).
    - Recovery: if `caps.terminal` flips back `true` after case 1/3 while
      still viewing the same run id, re-offer the Terminal tab / a
      "reconnect" action (re-mounting `<TerminalPane>` recreates the socket
      from scratch, which is the correct behavior here — the old one is
      gone).
  - Tests: DOM tests driving `fakeEventSource` through each transition,
    asserting the resulting UI state. The ~10s timeout in case 3 should be
    tested with fake timers, not a real 10-second wait.

- [ ] **Step 14: Socket lifecycle on tab switch / view close.**
  - Confirm (add a test if not already covered by step 13's work) that
    switching from Terminal to Activity, or navigating back to `#/fleet`,
    unmounts `<TerminalPane>` (same mechanism as step 13) rather than
    leaving it polling in the background.

- [ ] **Step 15: `CLAUDE.md` protocol + feature docs correction.**
  - Rewrite the "WebSocket Protocol" section: the real JSON envelope
    (`dims`/`seed`/`output`/`meta` server→client, `input`/`resize`
    client→server), explicitly note there is no `reseed` type and no
    `resize-done` ack.
  - Correct "Mobile Features"' stale WIDE/FIT toggle mention — describe the
    new font-size zoom, pan-with-magnetism, column scrubber, follow-cursor,
    and double-tap behavior instead.
  - Relocate (don't delete) the `special` mention: note it's a form
    parameter on the legacy `POST /api/pane/:target/send` route, not a
    WebSocket message type.
  - Final check, same step: grep the finished doc for `resize-done`, `WIDE`,
    `FIT` (as a toggle), and `special:` as a WS message type — none should
    remain describing the *current* WebSocket protocol.

- [ ] **Step 16: Full verification pass (binding — this is not optional
      polish).**
  - `npx vitest run`, `npx tsc -b`, `npx eslint .` in `ui/`.
  - `go test ./...` (and `-race` if that's part of the repo's usual gate —
    check `justfile`/CI config).
  - `just ui-build && go build` — production build.
  - Browser automation (playwright/firefox-devtools MCP) against the
    **production build**, real running app, real active run, mobile
    viewport:
    - Terminal connects and streams real output (re-confirms step 1 through
      the finished UI).
    - Screenshot text crispness at 2-3 font sizes.
    - Screenshot/short capture: pan + column-0 magnetism, the scrubber,
      follow-the-cursor, double-tap toggle.
    - Reload on a `#/fleet/<id>/terminal` deep link restores the same view.
    - Back from detail leaves the previously-tapped run card in the
      viewport.
    - Force `caps.terminal` false (or kill the underlying tmux
      pane/session) while the Terminal tab is open; confirm degradation.
  - Grep `ui/dist` for any new CSS class names added in this plan (repo has
    twice shipped unimported CSS — any new class must land in
    `ui/src/fleet/fleet.css` or another file actually imported by the
    component that uses it, checked as each piece of CSS is written, not
    only here at the end).
  - Attach screenshots to the PR for every visual claim above.

## Notes for the PR body

- Note the accepted addressing-scheme risk from step 1 and its outcome
  (attach-session behavior confirmed / fallback used / escalated).
- Note that typing input into the run-detail Terminal tab is explicitly out
  of scope (view/gesture only) — flag as a candidate follow-up issue per
  `WORKER_TASK.md`'s "open a GitHub issue for out-of-scope findings"
  instruction, since it's a real, deliberate gap worth tracking.
- Note the accepted `meta`-message degradation and duplicate control-client
  cost from bare-pane-id addressing (SPEC.md "Current state") as a known,
  accepted tradeoff — not a defect to fix in this PR.
