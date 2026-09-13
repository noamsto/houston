# Desktop console shell — implementation plan

Spec: `docs/superpowers/specs/2026-09-13-desktop-console-shell.md` (binding). Closes #42.

All paths are under `ui/src/`. Test runner: `cd ui && npx vitest run <file>`; types
`npx tsc -b`; lint `npx eslint .`. Every test file follows the existing pattern
(`afterEach(cleanup)`, a local `run()` factory with `now = 1_800_000_000_000`, see
`fleet/CrewsView.test.tsx`). No step touches Go, `mocha.css`, or any existing mobile CSS
rule; `fleet.css` only gains new `.console*` and `.run-card.selected` rules.

Steps are ordered so every step leaves the tree green (tsc + vitest).

---

- [x] **Step 0: before screenshot (done before any code change)** — the pre-change UI was
  built (`npx vite build` + `go build`) and captured headless at 400×860 `#/fleet`:
  `/tmp/claude-1000/-home-noams-Data-git--worktrees-git-houston-feat-42-desktop-console-shell-and-new-ui-as-defa/b6e84ed8-a69e-407f-8f42-e281994d5818/scratchpad/phone-before.png`.

**Test query rule for every console test below:** `CrewsView` stays mounted (hidden) in
the list column and renders a `RunCard` per crewed run, so text queries over `list` or
the whole container also match hidden copies. Scope fleet-list assertions with
`within(screen.getByLabelText('fleet list'))` and detail assertions with
`within(screen.getByLabelText('detail'))`; for headings use `getByRole('heading', {
level: 1, name })`, which skips `hidden` subtrees.

- [ ] **Step 1: shared route module** — new `fleet/routes.ts`

  Move `DetailRoute` and `parseDetailRoute` out of `fleet/Shell.tsx` into
  `fleet/routes.ts` and export them, plus:
  - `export type DetailTab = 'activity' | 'terminal'`
  - `export function runHash(id: string, tab: DetailTab = 'activity'): string` →
    `` `#/fleet/${id}/${tab}` ``
  - `export function useDetailRoute(): DetailRoute | null` — `useState(() =>
    parseDetailRoute(window.location.hash))` + a `hashchange` listener effect (exactly the
    code currently inline in `Shell.tsx:35-40`).

  New regex: `/^#\/fleet\/([^/]+)(?:\/([^/]*))?$/` → `{ id, tab: m[2] === 'terminal' ?
  'terminal' : 'activity' }`. Results: `#/fleet` → null, `#/fleet/` → null (the `[^/]+`
  needs ≥1 char), `#/fleet/pane-1` → activity, `#/fleet/pane-1/terminal` → terminal,
  `#/fleet/pane-1/bogus` → activity, `#/fleet/pane-1/` → activity, `#/agents` → null,
  `#/fleet/a/b/c` → null.

  `fleet/Shell.tsx`: import `parseDetailRoute`/`DetailRoute` from `./routes` instead of
  the local copies (no other change yet); replace its three
  `` `#/fleet/${r.id}/activity` `` literals with `runHash(r.id)`.

  Test `fleet/routes.test.ts`: every row above; `useDetailRoute` via `renderHook` —
  set `window.location.hash = '#/fleet/pane-1'`, render, expect `{id:'pane-1',
  tab:'activity'}`; then `act(() => { window.location.hash = '#/fleet/pane-2/terminal';
  window.dispatchEvent(new HashChangeEvent('hashchange')) })` → updates. (Dispatch the
  event explicitly; don't rely on happy-dom firing it on assignment.) Reset hash to `''`
  in `afterEach` **after** `cleanup()`.

- [ ] **Step 2: extract fleet list logic** — new `fleet/fleetList.ts`

  Pure functions lifted verbatim from `FleetView.tsx`'s `useMemo` bodies (keep the
  comments that explain the sort):
  - `export type Filter = 'active' | 'needs-you' | 'all'`
  - `export function filterRuns(runs: Run[], filter: Filter, now: number): Run[]` — the
    `keep` + sort block. Must sort a **copy** (`runs.filter(...)` already copies).
  - `export function groupByHost(runs: Run[]): [string, Run[]][]` — the `groups` block.
  - `export function crewShortId(name: string): string` — moved from `CrewsView.tsx`'s
    `shortId`; `CrewsView` imports it.

  `FleetView.tsx`: `import { filterRuns, groupByHost, type Filter }`, memo bodies become
  `filterRuns(runs, filter, now)` / `groupByHost(visible)`. JSX untouched.

  Test `fleet/fleetList.test.ts`: active hides stale done/failed but keeps a stale
  blocked; needs-you returns fresh and stale blocked, fresh first; all keeps history;
  active sort puts fresh blocked first then `updated_at` desc; groupByHost maps `''` →
  `local` preserving order; input array not mutated. Existing `FleetView`/`CrewsView`
  tests (if any) and `RunCard.test.tsx` still pass.

- [ ] **Step 3: `RunCard` selected marker** — `fleet/RunCard.tsx`, `fleet/fleet.css`

  Add optional `selected?: boolean` prop: appends ` selected` to the className and sets
  `aria-current={selected ? 'true' : undefined}`. Mobile never passes it → identical DOM.
  CSS (new rule, appended after `.run-card.history`):
  `.run-card.selected { background: var(--fill); border-color: var(--accent); }`
  — placed after `.attention` rules so a selected blocked card shows selection; the red
  dot and question box still mark it blocked.

  Test (add to `fleet/RunCard.test.tsx`): `selected` → `aria-current="true"` and class
  `selected`; without it neither is present.

- [ ] **Step 4: `ConsoleShell`** — new `fleet/ConsoleShell.tsx`, CSS in `fleet/fleet.css`

  Props: `{ runs: Run[]; connected: boolean; hasSnapshot: boolean; now: number }` (the
  store is owned by `Shell`, Step 5). Imports `../theme/mocha.css` and `./fleet.css`
  like `Shell.tsx`.

  State: `section: 'fleet' | 'crews' | 'workspace' | 'dispatch'` (default fleet),
  `filter: Filter` (default active), `crew: string | null`. Route: `useDetailRoute()`.

  Derived (all `useMemo` over `runs, now`, global — never crew-narrowed):
  - `attentionCount = runs.filter(r => needsYou(r, now)).length`
  - `counts = { active: filterRuns(runs,'active',now).length, 'needs-you': …, all: … }`
  - `hosts`: `groupByHost(runs)` → `[host, count]`
  - `crews`: distinct non-empty `r.crew?.name` → `{ name, members, blocked:
    needsYou-count }`, sorted blocked-first then name.
  - `base = filterRuns(runs, filter, now)`; `visible = crew ? base.filter(r =>
    r.crew?.name === crew) : base`; `groups = groupByHost(visible)`.

  Actions:
  - `chooseFilter(f)`: `setSection('fleet'); setFilter(f); if (f === 'needs-you')
    setCrew(null)`
  - badge click → `chooseFilter('needs-you')`
  - `toggleCrew(name)`: `setSection('fleet'); setCrew(c => c === name ? null : name)`
  - `open(r)`: `window.location.hash = runHash(r.id)`

  Markup (`<div className="console mocha" aria-label="console">`):
  - `<nav className="console-rail" aria-label="rail">`:
    `<div className="console-rail-head">` with `<h1>houston</h1>` + badge button (`className="fleet-badge"` + `quiet`, text rule
    copied from `FleetView`: `N needs you` / `all quiet` / `offline`);
    section "Fleet": three `console-rail-item` buttons (`aria-pressed` when
    `section==='fleet' && filter===f`) label + `<span className="console-count">`;
    section "Hosts": non-button `console-rail-item static` rows `host` + count (host
    colour class `host-local`/`host-remote`, styled by new rules
    `.console-rail .host-local` / `.console-rail .host-remote` (see CSS) since the
    existing ones are scoped to `.fleet-group`);
    section "Crews" (rendered only when crews exist): buttons, `title={name}`, label
    `crewShortId(name)`, count `members` and `· N blocked` when >0,
    `aria-pressed={crew === name}`;
    section "Views": Crews / Workspace / Dispatch buttons (`aria-pressed` on section);
    footer `<div className="console-rail-foot">` with a `fleet-classic` button → `#/agents` (same aria-label as `FleetView`).
  - `<section className="console-list" aria-label="list">`:
    - `<div hidden={section !== 'fleet'} className="console-pane" aria-label="fleet list">` containing
      `<div className="fleet mocha">` with `fleet-nav` header: `<h1>` = `Active` /
      `Needs you` / `All`; when `crew`: `<span className="run-chip codename">` crew
      short id, `<span className="console-narrow">{visible.length} of
      {base.length}</span>`, and a `fleet-classic` button "Clear" (`aria-label="Clear
      crew filter"`) → `setCrew(null)`. Empty state `fleet-empty` identical copy to
      `FleetView`. Groups rendered exactly like `FleetView` with `RunCard
      selected={route?.id === r.id}`.
    - `<div hidden={section !== 'crews'} className="console-pane"><CrewsView runs now
      onOpen={open} /></div>`
    - `section === 'workspace' && <WorkspaceView onOpen={(id) => {
      window.location.hash = runHash(id) }} />`
    - `section === 'dispatch' && <div className="shell-placeholder">` + the dispatch
      copy. Move `Shell.tsx`'s `PLACEHOLDER.dispatch` string to new `fleet/copy.ts` as
      `export const DISPATCH_PLACEHOLDER`; `Shell.tsx` imports it too (single-sourced).
  - `<section className="console-detail" aria-label="detail">`: `route ? <RunDetail
    runs hasSnapshot streamConnected={connected} now id={route.id} tab={route.tab} /> :
    <div className="console-detail-empty">Select a run</div>`.

  CSS appended to `fleet.css` (tokens only):
  ```css
  .console {
    position: absolute; inset: 0;
    display: grid; grid-template-columns: 220px minmax(320px, 400px) minmax(0, 1fr);
    grid-template-rows: minmax(0, 1fr);
    background: var(--bg); color: var(--text-dim);
    font-family: var(--font-ui); font-size: 14px;
  }
  /* The reused views (.fleet, .crews, .workspace, .shell-placeholder, .run-detail)
     are all absolute/inset:0 — each cell must be their containing block. */
  .console-rail, .console-list, .console-detail { position: relative; overflow: hidden; min-width: 0; }
  .console-rail { overflow-y: auto; background: var(--bg-sunken); border-right: 1px solid var(--line); padding: 12px 8px; display: flex; flex-direction: column; gap: 14px; }
  .console-list { border-right: 1px solid var(--line); }
  .console-pane { position: absolute; inset: 0; }
  .console-rail-head { display: flex; align-items: center; justify-content: space-between; padding: 0 6px; }
  .console-rail-head h1 { margin: 0; font-size: 15px; font-weight: 700; color: var(--text); }
  .console-rail h2 { margin: 0 0 4px; padding: 0 8px; font-size: 11px; font-weight: 600; text-transform: uppercase; letter-spacing: 0.04em; color: var(--text-faint); }
  .console-rail-item { display: flex; align-items: center; gap: 8px; width: 100%; min-height: 32px; padding: 0 8px; border: 0; border-radius: 6px; background: none; color: var(--text-dim); font: inherit; font-size: 13px; text-align: left; cursor: pointer; }
  .console-rail-item.static { cursor: default; }
  .console-rail-item[aria-pressed='true'] { background: var(--fill); color: var(--text); font-weight: 600; }
  .console-rail-item:not(.static):hover { background: var(--bg-raised); }
  .console-count { margin-left: auto; font-size: 12px; color: var(--text-faint); }
  .console-rail-foot { margin-top: auto; }
  .console-narrow { font-size: 12px; color: var(--text-faint); }
  .console-detail-empty { position: absolute; inset: 0; display: flex; align-items: center; justify-content: center; color: var(--text-faint); font-size: 13px; }
  .console-rail .host-local { color: var(--host-local); }
  .console-rail .host-remote { color: var(--host-remote); }
  .console-rail-item:focus-visible { outline: 2px solid var(--accent); outline-offset: 2px; }
  ```
  The `.fleet-nav` header inside the list inherits `position: sticky` from mobile CSS —
  fine inside the `.fleet` scroll container.

  Test `fleet/ConsoleShell.test.tsx` (mock `./useWorkspace` → `{ workspace: null,
  error: null, loading: false }`; mock `../hooks/usePaneSocket` as in
  `RunDetail.test.tsx`; runs without `tmux` so no terminal mounts; reset hash in
  `afterEach`):
  1. Renders three regions (`getByLabelText('rail'|'list'|'detail')`) and "Select a
     run" with an empty hash.
  2. Hash `#/fleet/b` set before render → `within(detail)` shows run b's `nameLabel`
     (give runs distinct `repo`/`branch`); in `within(fleet list)` the card for b has
     `aria-current="true"`.
  3. Click card a (in fleet list) → `window.location.hash === '#/fleet/a/activity'`;
     dispatch `hashchange` → `within(detail)` shows a; fleet-list card a selected, b not.
  4. Rail counts: runs = fresh blocked, stale blocked, stale done, running → badge
     `1 needs you`; Active 3, Needs you 2, All 4.
  5. Crew narrowing: fixture = x1 (crew X, running), y1 and y2 (crew Y, running), n1
     (no crew, running), all fresh, distinct repo/branch. Click the rail crew X button
     (found by `title="X"` within rail) → `within(fleet list)` shows x1 only (y1, y2, n1
     absent), header text contains `1 of 4` and a "Clear crew filter" button; rail
     Active count still `4`. Click Clear → all four back, no `of` text.
  6. **Invariant:** fixture = x1 (crew X, running, fresh), y-fresh (crew Y, blocked,
     fresh), y-stale (crew Y, blocked, updated 2h ago), n-stale (no crew, blocked, 2h
     ago). Click crew X → `within(fleet list)` shows only x1 (proves the filter is on and
     would hide the blocked runs). Click the badge (`1 needs you`) → `within(fleet list)`
     shows y-fresh, y-stale and n-stale; x1 absent; no `of` narrowing text; no Clear
     button. This fails if the badge leaves the crew filter on.
  7. Views (fixture with no crews, so the only "Crews" button is the view entry): click
     rail "Crews" → `getByRole('heading', { level: 1, name: 'Crews' })` present and
     `queryByRole('heading', { level: 1, name: 'Active' })` null; Workspace → heading
     `Workspace`; Dispatch → placeholder copy visible; click rail "Active" → heading
     `Active`.

- [ ] **Step 5: split `Shell` into selector + `MobileShell`** — `fleet/Shell.tsx`, new
  `fleet/MobileShell.tsx`

  `MobileShell.tsx`: the current `Shell` function body moved verbatim, renamed
  `MobileShell`, signature `({ runs, connected, hasSnapshot, now }: ShellData)`; delete
  its `useRuns()`/`useNow()` calls and compute `attention` from props; replace the
  inline detail state + effect with `const detail = useDetailRoute()`. JSX and class
  names byte-identical otherwise (keep all existing comments).

  `Shell.tsx` becomes:
  ```tsx
  export function Shell() {
    const { runs, connected, hasSnapshot } = useRuns()
    const now = useNow()
    const isDesktop = useIsDesktop()
    const Layout = isDesktop ? ConsoleShell : MobileShell
    return <Layout runs={runs} connected={connected} hasSnapshot={hasSnapshot} now={now} />
  }
  ```
  (Two distinct component types, so a breakpoint crossing remounts the layout but not
  `Shell`, whose `useRuns` effect — and EventSource — persists.) Export the shared props
  type `interface ShellData` from `MobileShell.tsx`; also edit `ConsoleShell.tsx` to
  replace its inline props type with `ShellData` (imported from `./MobileShell`).
  The css imports (`mocha.css`, `fleet.css`) stay in `Shell.tsx`.

  Test `fleet/Shell.test.tsx`: `installFakeEventSource()` from `../testing/fakeEventSource`;
  a `stubMatchMedia(matches)` helper assigning `window.matchMedia = vi.fn(() => mql)`
  where `mql = { matches, addEventListener(_, h) { handler = h }, removeEventListener() {} }`
  and a `flip(matches)` that sets `mql.matches` and calls `handler({ matches })` in `act`.
  Mock `./useWorkspace`. Restore `window.matchMedia` in `afterEach`.
  1. `matches=false` → `getByLabelText('sections')` (mobile tab nav) present, no
     `console` label.
  2. `matches=true` → `getByLabelText('console')` present, no `sections` nav.
  3. Start false, emit a snapshot with one run, `flip(true)` → console shows; the run's
     card is still listed (no second snapshot needed); `instances.length === 1` and
     `instances[0].closed === false`.

- [ ] **Step 6: new shell is the default view** — new `src/view.ts`, `App.tsx`, new
  `App.test.tsx`

  `react-refresh/only-export-components` is an **error** in this repo's eslint config and
  checks `.tsx`, so the pure function cannot live in `App.tsx` (deliberate deviation from
  spec R1's "exported from App.tsx"). Create `src/view.ts` with
  ```ts
  export function viewForHash(hash: string): View {
    if (hash === '#/agents') return 'agents'
    if (hash === '#/panes') return 'panes'
    return 'fleet'
  }
  ```
  and `export type View = 'agents' | 'panes' | 'fleet'`. In `App.tsx` delete the local
  `View` type and `initialView`, import both from `./view`, and use
  `useState<View>(() => viewForHash(window.location.hash))` and
  `setView(viewForHash(window.location.hash))` in the `hashchange` handler. Nothing else
  in `App.tsx` changes (ViewSwitch, PanesApp untouched).

  Test `App.test.tsx` (imports `viewForHash` from `./view`): table for `viewForHash` — `''`, `'#'`, `'#/'`, `'#/fleet'`,
  `'#/fleet/pane-1/terminal'`, `'#/nonsense'` → fleet; `'#/agents'` → agents;
  `'#/panes'` → panes. Render test: `vi.mock('./fleet/Shell', () => ({ Shell: () =>
  'fleet-shell' }))`, `vi.mock('./components/agents/AgentsView', () => ({
  AgentsView: () => 'agents-view' }))` (strings, not JSX, in mock factories); with hash `''` render `<App />` →
  `fleet-shell`; with `'#/agents'` → `agents-view`. (`#/panes` is covered by the pure
  table; rendering `PanesApp` pulls in the sessions stream.)

- [ ] **Step 7: docs** — none. `CLAUDE.md` does not list `ui/src/fleet/`, and the
  state-of-play doc is a dated snapshot; the spec and this plan are the record.

- [ ] **Step 8: verify** (worker, not a subagent)
  - `cd ui && npx vitest run && npx tsc -b && npx eslint .`
  - Grep the built CSS for `.console{` / `grid-template-columns` after `npx vite build`
    (state-of-play: a passing build proves nothing for CSS).
  - `go build`, run on a spare port, headless Chromium `--screenshot`:
    desktop 1400×900 at `#/` (empty hash) and at `#/fleet/<a live run id>/activity`;
    phone 400×860 at `#/fleet`, compared against Step 0's
    `/tmp/claude-1000/-home-noams-Data-git--worktrees-git-houston-feat-42-desktop-console-shell-and-new-ui-as-defa/b6e84ed8-a69e-407f-8f42-e281994d5818/scratchpad/phone-before.png`; also 400×860 at `#/` to prove the default route on a phone.

## Acceptance → step map
| Acceptance | Step |
|---|---|
| Empty hash → new shell; `#/agents` `#/panes` work | 6 |
| ≥1024 console / <1024 mobile unchanged | 4, 5, 8 |
| shell selection, default routing, hash selection tests | 5, 6, 1 + 4 |
| nothing blocked hidden | 4 (test 6) |
| tsc/eslint/tests | 8 |
| screenshots | 8 |
