# Verify and fix the Mocha fleet shell (#15)

## Context

`#/fleet` (PR #8) was built with no browser available, so its rendered result
was never seen. This plan implements the fixes found by an actual browser
verification pass (headless Firefox, mobile viewport 320/390/428px, against
live production run data) of the six checks flagged in
`docs/superpowers/2026-09-08-state-of-play.md` and `WORKER_TASK.md`.

**Revision 1** (after `plan-critic`, verdict `revise`, 6 blocking findings —
all independently re-verified against the actual code before incorporating):
the original draft under-scoped the contrast fix (missed `.fleet-badge.quiet`
on `--fill`, a worse surface than `--bg-raised`; wrongly cited `.fleet-classic`
as failing when it doesn't; missed `.run-age`, which does), under-scoped the
safe-area fix (bottom-only; `viewport-fit=cover` affects all four edges, and
`.fleet-nav`'s sticky header would start rendering under the status bar/notch
without a matching top inset), didn't flag that `viewport-fit=cover` in
`ui/index.html` is global and the classic `/agents` `/panes` view has zero
safe-area handling of its own, and the `:active` step as drafted would have
shipped a real CSS specificity bug. All fixed below.

**Revision 1 re-review** (verdict `accept`, with 3 implementation-time items
and non-blocking notes, all adopted): the attention-dot and desktop-viewport
evidence gaps were already covered by screenshots taken during verification
(non-Fleet-tab screenshot for the dot; 1440×900 for desktop) — just needed to
land in the PR body. Two real adoptions: `.fleet` itself (not just its header
and tab bar) needs left/right safe-area insets for landscape/cutout devices;
and `.fleet-badge.quiet`'s fix is better done by swapping its **background**
from `--fill` to `--bg-raised` (keeping `color: var(--text-faint)`, now
overlay2 after Step 3 → 5.81:1) rather than bumping its *text* to
`--text-strong`, which would have made the "all quiet" badge render louder
than the "N needs you" badge it replaces — the opposite of "quiet". Also
adopted: `.run-chip`'s `--text-dim` on `--fill` has the identical 4.45:1
near-miss as `.fleet-badge.quiet` did; fixed by bumping its `color` to
`--text-strong` (a background swap doesn't work there — chips sit inside a
`.run-card` whose own background is already `--bg-raised`, so swapping the
chip to the same tone would make it blend invisibly into the card, losing
the pill affordance).

## Findings driving this plan

1. Renders in Mocha — confirmed working, no fix.
2. Attention dot position — confirmed fine at 320/390/428px, no fix.
3. **`--text-faint` legibility — real defect, wider than first drafted.**
   Exact WCAG contrast (sRGB relative luminance, computed and reconfirmed):

   | surface | `--ctp-overlay1` (current) | `--ctp-overlay2` (candidate) |
   |---|---|---|
   | `--bg` (mantle, `.fleet-classic`'s effective ground) | 4.75:1 ✅ | 6.22:1 |
   | `--bg-raised` (base — `.fleet-filters button`, `.run-age`) | 4.44:1 ❌ | 5.81:1 ✅ |
   | `--bg-sunken` (crust) | 5.07:1 ✅ | 6.64:1 |
   | `--fill`/`--line` (surface0 — **`.fleet-badge.quiet`**) | 3.40:1 ❌ | **4.45:1 ❌ (still fails, by 0.05)** |

   So bumping the token alone fixes `.fleet-filters button` and `.run-age`,
   but **not** `.fleet-badge.quiet` — the "all quiet" badge, which is the
   default state whenever nothing needs attention (`FleetView.tsx` renders it
   when `attentionCount === 0`), so it's a commonly-seen surface. `.fleet-classic`
   was misdiagnosed in the first draft — it already passes (`background: none`
   inside `.fleet-nav`, which composites to `--bg`), so it needs no change.
4. Tap feedback — confirmed present on the primary surfaces (`run-card`,
   filter buttons, tab bar). Minor gap: `.fleet-classic` / `.fleet-badge`
   have no `:active` state, only `:focus-visible`.
5. **Notched-phone tab bar — real defect, and the direct fix is incomplete
   on its own.** `ui/index.html`'s viewport meta tag has no
   `viewport-fit=cover`, so **every** `env(safe-area-inset-*)` in the app
   resolves to `0px` regardless of device — confirmed by `rg` that the one
   `env()` in the whole repo is `fleet.css:117`'s
   `padding-bottom: env(safe-area-inset-bottom)` on `.shell-tabs`.
   Adding `viewport-fit=cover` extends the layout into *all four* safe
   areas, not just the bottom: without a matching top inset,
   `.fleet-nav` (`position: sticky; top: 0`, the "Fleet" header) would start
   rendering under the status bar/notch — trading a dead bottom pad for a
   live top overlap. Also: `viewport-fit=cover` is a single global
   `index.html` meta tag — it will affect the classic `/agents` `/panes`
   view too (`App.tsx:174`, `height: '100dvh'`), which has **zero**
   safe-area handling anywhere. That view is out of this task's declared
   scope (`ui/src/fleet/` and `ui/src/theme/` only) and slated for deletion
   in plan D, so this plan fixes the new shell fully and documents the old
   view's exposure rather than silently expanding scope into `App.tsx` /
   `MobileInputBar.tsx`.
6. Stale-vs-fresh distinguishability — confirmed working and matches the
   oracle, verified against live data (badge counted 4 fresh, "Needs you"
   filter showed 8 total including 1h/2h/7h-old blocked runs); border color
   difference (~53 RGB euclidean distance) confirmed visible in screenshots.

## Steps

- [ ] **Step 1: `viewport-fit=cover`** — add it to the `<meta
  name="viewport">` `content` attribute in `ui/index.html` (append to the
  existing `width=device-width, initial-scale=1.0, maximum-scale=1,
  user-scalable=no` string). Only line that changes in this file.

- [ ] **Step 2: full safe-area coverage for the new shell**, in
  `ui/src/fleet/fleet.css`:
  - `.fleet-nav` (currently `padding: 12px 14px;`, `position: sticky; top:
    0`): change to inset-aware padding so the sticky header cannot render
    under a notch/status bar or side cutout:
    `padding: calc(12px + env(safe-area-inset-top)) calc(14px +
    env(safe-area-inset-right)) 12px calc(14px + env(safe-area-inset-left));`
  - `.shell-tabs` (currently only `padding-bottom:
    env(safe-area-inset-bottom);`): add `padding-left:
    env(safe-area-inset-left); padding-right: env(safe-area-inset-right);`
    for landscape/rounded-corner devices.
  - `.fleet` itself (`position: absolute; inset: 0`, the scrolling container
    whose `.run-card`s carry only `margin: 0 10px`): add `padding-left:
    env(safe-area-inset-left); padding-right: env(safe-area-inset-right);`
    so cards don't clip under a landscape cutout — the header and tab bar
    alone don't cover the scrolling content between them.

- [ ] **Step 3: fix `--text-faint` contrast**, in `ui/src/theme/mocha.css`:
  - Change `--text-faint: var(--ctp-overlay1);` to
    `--text-faint: var(--ctp-overlay2);`.
  - Rewrite the comment above it with the real, re-verified numbers: overlay1
    fails AA (4.5:1) on `--bg-raised` (4.44:1); overlay2 clears every
    background the token is actually used on (`--bg`/`--bg-raised`/
    `--bg-sunken`, 5.81-6.64:1). Do not mention `--fill` in this comment —
    after Step 4 swaps `.fleet-badge.quiet` to a `--bg-raised` background
    instead of a text-color fix, no `--text-faint` consumer sits on `--fill`
    any more, so the token alone genuinely covers everywhere it's used.
  - Leave `--text-dim` alone even though it becomes the same literal value as
    `--text-faint` — they stay distinct semantic aliases for distinct roles
    (dim = subtitle text, faint = chrome labels), per the file's own stated
    convention.

- [ ] **Step 4: fix the surfaces the token bump doesn't reach**, in
  `ui/src/fleet/fleet.css`:
  - `.fleet-badge.quiet`: change `background` from `var(--fill)` to
    `var(--bg-raised)` (keep `color: var(--text-faint)`, which is overlay2 →
    5.81:1 after Step 3). A background swap, not a text-color swap, so the
    "all quiet" state stays visually quieter than the red "N needs you"
    badge it replaces.
  - `.run-chip`: change `color` from `var(--text-dim)` to
    `var(--text-strong)` (measured 7.10:1 on `--fill`; `--text-dim` is
    overlay2, same 4.45:1 near-miss as the badge had). Background stays
    `var(--fill)` here — chips sit inside `.run-card`, whose own background
    is already `--bg-raised`, so swapping the chip to that same tone would
    make it blend into the card instead of reading as a pill.

- [ ] **Step 5: `:active` feedback for the two header buttons**, in
  `ui/src/fleet/fleet.css` — add `.fleet-classic:active, .fleet-badge:active
  { opacity: 0.7; }` near the existing `:active` rules. Use `opacity`, not a
  `background` swap: `.fleet-badge.quiet` and default `.fleet-badge` share
  selector specificity with a plain `:active` background rule, and source
  order would make pressing the "all quiet" badge flash the wrong tint
  (color-neutral `opacity` sidesteps that entirely and works identically for
  both badge states).

- [ ] **Step 6: rebuild and re-verify in the browser** — `cd ui && npm run
  build`, restart `just dev` if needed, reload the app in the already-running
  headless Firefox session at 390×844, and capture **after** screenshots for:
  - `.fleet-filters` unselected buttons and `.run-age` (contrast fix)
  - the "all quiet" `.fleet-badge` state (needs the badge count to be 0, or
    inspect via devtools computed-style if live data has no quiet moment)
  - `.fleet-nav` header padding is unchanged in a non-notched viewport (a
    `calc(12px + 0px)` should render pixel-identical to before — confirms the
    inset addition doesn't regress the common case)
  - Note honestly in the PR body that the actual notch overlap can't be
    shown in a plain browser viewport without device emulation with inset
    values — cite the `viewport-fit=cover` + `env()` addition and the known
    iOS Safari behavior rather than presenting a screenshot that can't exist
  - pressing (or CSS-inspecting) `.fleet-classic` / `.fleet-badge` to confirm
    the new `:active` opacity rule applies

- [ ] **Step 7: verification gate** — `cd ui && npx tsc -b`, `npx eslint .`,
  and `npm run build` must all pass. Then, because the entire reason this
  task exists is that a previous session's CSS change silently failed to
  ship:
  - grep the rebuilt `ui/dist/assets/*.css` for the literal string
    `--text-faint:` followed by `var(--ctp-overlay2)` — Vite does not
    resolve custom-property indirection, so the bundle keeps the `var()`
    reference, not a hex value; grepping for a hex string would false-negative.
  - grep the rebuilt `ui/dist/index.html` for `viewport-fit=cover`.

- [ ] **Step 8: PR body** — write up all six checks with evidence (screenshots
  for the visual ones, the contrast numbers and the live-badge-vs-filter
  counts for the numeric ones), before/after screenshots for every visual
  change, a note on the classic-view safe-area exposure (Finding 5) with a
  filed follow-up GitHub issue reference, and `Closes #15`.

## Explicitly not doing

- Not touching `.github/workflows/`, `server/`, `tmux/`, `runs/` (owned by
  concurrent workers).
- Not touching `App.tsx`, `MobileInputBar.tsx`, or any other classic-view
  file to backfill safe-area handling there — out of declared scope
  (`ui/src/fleet/` and `ui/src/theme/` only) and that view is slated for
  deletion in plan D. Filing a GitHub issue instead, per the task's own
  "too large to fix in scope" clause.
- Not building the desktop shell (confirmed not broken; separate later plan).
- Not starting the run detail view or mobile terminal rework (separate plan
  A, per `docs/superpowers/2026-09-08-state-of-play.md`).
- Not touching the attention-dot position, `--state-*` tokens, or the
  fresh/stale staleness logic in `staleness.ts` — all independently verified
  correct via live-data testing in a real browser.
