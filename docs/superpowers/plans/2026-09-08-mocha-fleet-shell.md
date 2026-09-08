# Mocha design system and the Fleet shell — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Put the agent-run fleet on screen — a Catppuccin Mocha design system, a `useRuns()` store over `/api/runs/stream`, and the mobile 4-tab shell with the Fleet tab live.

**Architecture:** Additive. The existing `#/agents` and `#/panes` views keep working untouched; the new shell mounts at `#/fleet`. Mocha tokens are added alongside the current ones rather than replacing them, so nothing existing changes colour until the old views are deleted in a later plan.

**Tech Stack:** React 19, TypeScript, Vite. No new dependencies — no CSS framework, no component library, no state library. CSS custom properties and plain `useState`/`useEffect`, matching what `ui/` already does.

**Spec:** `docs/superpowers/specs/2026-09-07-houston-overhaul-design.md` — "UI: two shells, one store". The visual direction (Instrument Mocha) and the 4-tab shell were chosen from mockups in an earlier design session.

## Global Constraints

- **No new dependencies.** `ui/package.json` gains nothing.
- **Additive only.** Do not modify or delete `AgentsView`, `AgentCard`, `agents.css`,
  `Sidebar`, `TerminalArea`, or any existing hook. The old views must still work.
- **Palette is Catppuccin Mocha**, and every colour comes from a token. No hex
  literals in component files.
- **Mobile first.** The target is a phone held in one hand. Tap targets ≥ 44px.
- **Lint and types must pass:** `cd ui && npx eslint .` and `npx tsc -b`. Both are
  pre-commit hooks (`flake.nix:32,42`).
- The dev server proxies `/api` to `localhost:9090` (`ui/vite.config.ts`). To see real
  data, run `just dev` in one shell and `just ui-dev` in another, and open the Go
  server's port once so the browser holds the auth cookie.

---

## File Structure

| File | Responsibility | Change |
|---|---|---|
| `ui/src/theme/mocha.css` | Catppuccin Mocha palette + semantic aliases | **Create** |
| `ui/src/api/runs.ts` | TS mirror of the Go `Run` types | **Create** |
| `ui/src/hooks/useRuns.ts` | SSE store over `/api/runs/stream` | **Create** |
| `ui/src/fleet/staleness.ts` | The freshness rule, in one place | **Create** |
| `ui/src/fleet/RunCard.tsx` | One run | **Create** |
| `ui/src/fleet/FleetView.tsx` | Grouped list, filters, badge | **Create** |
| `ui/src/fleet/Shell.tsx` | 4-tab shell | **Create** |
| `ui/src/fleet/fleet.css` | Shell + card styles | **Create** |
| `ui/src/App.tsx` | Route `#/fleet` to the new shell | Modify (additive) |

A new `ui/src/fleet/` directory rather than growing `components/`: it is a self-contained
surface that a later plan will promote to the default, and keeping it separate makes the
eventual deletion of the old views a directory removal rather than an untangling.

---

### Task 1: Mocha tokens and the run type

**Files:**
- Create: `ui/src/theme/mocha.css`, `ui/src/api/runs.ts`
- Test: none — these are declarations. Task 3 exercises the types.

**Interfaces:**
- Produces: CSS custom properties under `.mocha`; TS types `Run`, `RunState`, `TmuxRef`, `IssueRef`, `PRRef`, `CrewRef`, `Activity`, `TrailChip`, `Question`, `Tokens`, `Caps`.

- [ ] **Step 1: Write `ui/src/theme/mocha.css`**

```css
/*
 * Catppuccin Mocha. Scoped to .mocha so it can coexist with the old views'
 * tokens until those are deleted.
 *
 * Raw palette first, then semantic aliases. Components use only the semantic
 * names — a component naming --ctp-mauve directly is a bug, because it pins a
 * colour to a meaning it does not own.
 */
.mocha {
  --ctp-base: #1e1e2e;
  --ctp-mantle: #181825;
  --ctp-crust: #11111b;
  --ctp-surface0: #313244;
  --ctp-surface1: #45475a;
  --ctp-surface2: #585b70;
  --ctp-overlay0: #6c7086;
  --ctp-overlay1: #7f849c;
  --ctp-overlay2: #9399b2;
  --ctp-subtext0: #a6adc8;
  --ctp-subtext1: #bac2de;
  --ctp-text: #cdd6f4;
  --ctp-green: #a6e3a1;
  --ctp-red: #f38ba8;
  --ctp-yellow: #f9e2af;
  --ctp-peach: #fab387;
  --ctp-blue: #89b4fa;
  --ctp-mauve: #cba6f7;
  --ctp-teal: #94e2d5;

  --bg: var(--ctp-mantle);
  --bg-raised: var(--ctp-base);
  --bg-sunken: var(--ctp-crust);
  --line: var(--ctp-surface0);
  --line-strong: var(--ctp-surface1);
  --text: var(--ctp-text);
  --text-dim: var(--ctp-overlay2);
  --text-faint: var(--ctp-overlay0);
  --accent: var(--ctp-mauve);

  /* One colour per run state. blocked is the only "needs you" colour and must
     not be reused for anything else, or the badge stops meaning one thing. */
  --state-blocked: var(--ctp-red);
  --state-running: var(--ctp-green);
  --state-thinking: var(--ctp-yellow);
  --state-review: var(--ctp-blue);
  --state-done: var(--ctp-overlay0);
  --state-failed: var(--ctp-peach);
  --state-idle: var(--ctp-overlay0);
  --state-compacting: var(--ctp-teal);

  --host-local: var(--ctp-blue);
  --host-remote: var(--ctp-peach);

  --font-ui: -apple-system, BlinkMacSystemFont, 'Inter', 'Segoe UI', sans-serif;
  --font-mono: 'JetBrains Mono', ui-monospace, SFMono-Regular, Menlo, monospace;

  --tap: 44px; /* minimum touch target */
}
```

- [ ] **Step 2: Write `ui/src/api/runs.ts`**

Mirror the Go types exactly. Field names come from the `json:` tags in `runs/run.go`; do not invent camelCase.

```ts
// Mirror of runs.State (Go). Exactly one state means "a human is required".
export type RunState =
  | 'thinking'
  | 'running'
  | 'blocked'
  | 'compacting'
  | 'review'
  | 'done'
  | 'failed'
  | 'idle'

export interface TmuxRef {
  session: string
  window: number
  pane_id: string
}

export interface IssueRef {
  id: string
  title?: string
  url?: string
  provider?: string
}

export interface PRRef {
  number: string
  state?: string
  check_state?: string
  mergeable?: string
  draft?: boolean
  url?: string
}

export interface CrewRef {
  name: string
  color?: string
  tier?: string
}

export interface TrailChip {
  tool: string
  hint?: string
  done: boolean
  error?: boolean
}

export interface Activity {
  tool?: string
  hint?: string
  message?: string
  task?: string
  trail?: TrailChip[]
  preview?: string
  turn?: number
}

export interface Question {
  text: string
  via: string // "pane" | "crew"
}

export interface Tokens {
  input: number
  output: number
}

export interface Caps {
  terminal: boolean
  reply: boolean
  kill: boolean
}

// Mirror of runs.Run.
//
// A removal arrives as an ordinary `update` event carrying only `id` and
// `removed: true` — every other field is empty, so consumers must check
// `removed` before reading anything else.
export interface Run {
  id: string
  host?: string
  agent: string
  state: RunState
  repo?: string
  branch?: string
  worktree?: string
  tmux?: TmuxRef
  issue?: IssueRef
  pr?: PRRef
  crew?: CrewRef
  activity: Activity
  question?: Question
  tokens: Tokens
  since?: number
  updated_at: number
  stale?: boolean
  caps: Caps
  removed?: boolean
}
```

- [ ] **Step 3: Verify types and lint**

Run: `cd ui && npx tsc -b && npx eslint .`
Expected: no output from either.

- [ ] **Step 4: Commit**

```bash
git add ui/src/theme/mocha.css ui/src/api/runs.ts
git commit -m "feat(ui): Catppuccin Mocha tokens and the Run type"
```

---

### Task 2: The freshness rule, in one place

The live API returns finished and detached sessions alongside live ones — on a real
server, 22 of 32 runs, six of them reporting `blocked` and aged up to 5.3 hours. If the
Fleet badge counts those, it is permanently lit and the one signal this redesign exists
for is dead.

Age alone is the wrong discriminator: a genuinely blocked agent waiting five hours is
exactly what you want to see. So the rule is deliberately asymmetric — **nothing that
reports `blocked` is ever hidden**, but the badge only counts recent ones.

**Files:**
- Create: `ui/src/fleet/staleness.ts`, `ui/src/fleet/staleness.test.ts`

**Interfaces:**
- Produces: `FRESH_MS`, `isFresh(run, now)`, `needsYou(run, now)`, `isHistory(run, now)`

- [ ] **Step 1: Write the failing test**

`ui/src/fleet/staleness.test.ts`:

```ts
import { describe, expect, it } from 'vitest'
import { FRESH_MS, isFresh, isHistory, needsYou } from './staleness'
import type { Run } from '../api/runs'

const now = 1_800_000_000_000 // fixed ms

function run(p: Partial<Run>): Run {
  return {
    id: 'pane-1', agent: 'claude', state: 'idle',
    activity: {}, tokens: { input: 0, output: 0 },
    caps: { terminal: false, reply: false, kill: false },
    updated_at: Math.floor(now / 1000),
    ...p,
  } as Run
}

const agoSec = (ms: number) => Math.floor((now - ms) / 1000)

describe('staleness', () => {
  it('counts a recently blocked run as needing you', () => {
    expect(needsYou(run({ state: 'blocked' }), now)).toBe(true)
  })

  it('does not count a long-stale blocked run toward the badge', () => {
    const old = run({ state: 'blocked', updated_at: agoSec(FRESH_MS + 60_000) })
    expect(needsYou(old, now)).toBe(false)
  })

  it('never treats a blocked run as history, however old', () => {
    // Hiding something that asked for input is the worse failure.
    const old = run({ state: 'blocked', updated_at: agoSec(FRESH_MS * 10) })
    expect(isHistory(old, now)).toBe(false)
  })

  it('treats a long-finished run as history', () => {
    expect(isHistory(run({ state: 'done', updated_at: agoSec(FRESH_MS + 1) }), now)).toBe(true)
    expect(isHistory(run({ state: 'failed', updated_at: agoSec(FRESH_MS + 1) }), now)).toBe(true)
  })

  it('keeps a just-finished run out of history so it does not vanish mid-glance', () => {
    expect(isHistory(run({ state: 'done', updated_at: agoSec(1000) }), now)).toBe(false)
  })

  it('never treats a running or thinking run as history', () => {
    for (const state of ['running', 'thinking', 'review', 'compacting'] as const) {
      const old = run({ state, updated_at: agoSec(FRESH_MS * 5) })
      expect(isHistory(old, now)).toBe(false)
    }
  })

  it('reports freshness from updated_at', () => {
    expect(isFresh(run({ updated_at: agoSec(1000) }), now)).toBe(true)
    expect(isFresh(run({ updated_at: agoSec(FRESH_MS + 1) }), now)).toBe(false)
  })
})
```

- [ ] **Step 2: Add vitest**

`ui/` has no test runner. Add one — it is a devDependency for the UI's own tests, not a
runtime dependency, and the plan's "no new dependencies" constraint is about shipping
code:

```bash
cd ui && npm install -D vitest
```

Add to `ui/package.json` scripts: `"test": "vitest run"`.

If you would rather not add vitest, return BLOCKED and say so — do **not** silently drop
the tests. This rule is the difference between the badge working and not, and it must be
pinned.

- [ ] **Step 3: Run the test to verify it fails**

Run: `cd ui && npx vitest run`
Expected: FAIL — `Cannot find module './staleness'`.

- [ ] **Step 4: Write `ui/src/fleet/staleness.ts`**

```ts
import type { Run } from '../api/runs'

/**
 * How recently a run must have reported to count as fresh.
 *
 * Tuned against real data: a live server carried 22 finished-or-detached runs
 * alongside 10 live ones, six of them still reporting `blocked` from up to five
 * hours earlier. Move this if an hour proves wrong in practice — it is the only
 * number that decides what the badge counts.
 */
export const FRESH_MS = 60 * 60 * 1000

export function isFresh(run: Run, now: number): boolean {
  if (!run.updated_at) return false
  return now - run.updated_at * 1000 <= FRESH_MS
}

/**
 * needsYou drives the badge and the sort. A run that asked for input long ago
 * is almost always a session that ended while waiting, and counting it would
 * leave the badge permanently lit — but see isHistory: it is still shown.
 */
export function needsYou(run: Run, now: number): boolean {
  return run.state === 'blocked' && isFresh(run, now)
}

/**
 * isHistory marks a run as foldable behind the "All" filter. Only terminal
 * states qualify, and only once stale: a run that is blocked, running or
 * thinking is never history however old it looks, because hiding something
 * that is waiting on you is the worse failure.
 */
export function isHistory(run: Run, now: number): boolean {
  if (run.state !== 'done' && run.state !== 'failed') return false
  return !isFresh(run, now)
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `cd ui && npx vitest run`
Expected: PASS, 7 tests.

- [ ] **Step 6: Commit**

```bash
git add ui/src/fleet/staleness.ts ui/src/fleet/staleness.test.ts ui/package.json ui/package-lock.json
git commit -m "feat(ui): freshness rule for the fleet badge"
```

---

### Task 3: `useRuns()` — the store

**Files:**
- Create: `ui/src/hooks/useRuns.ts`
- Test: `ui/src/hooks/useRuns.test.ts`

**Interfaces:**
- Consumes: `Run` from Task 1.
- Produces: `useRuns()` returning `{ runs: Run[]; connected: boolean }`, and
  `applyEvent(map, run)` exported for the test.

- [ ] **Step 1: Write the failing test**

`ui/src/hooks/useRuns.test.ts`:

```ts
import { describe, expect, it } from 'vitest'
import { applyEvent } from './useRuns'
import type { Run } from '../api/runs'

function run(id: string, p: Partial<Run> = {}): Run {
  return {
    id, agent: 'claude', state: 'running',
    activity: {}, tokens: { input: 0, output: 0 },
    caps: { terminal: true, reply: true, kill: true },
    updated_at: 1000,
    ...p,
  } as Run
}

describe('applyEvent', () => {
  it('adds an unseen run', () => {
    const next = applyEvent(new Map(), run('pane-1'))
    expect(next.get('pane-1')?.state).toBe('running')
  })

  it('replaces an existing run wholesale', () => {
    const first = applyEvent(new Map(), run('pane-1', { state: 'running' }))
    const next = applyEvent(first, run('pane-1', { state: 'blocked' }))
    expect(next.get('pane-1')?.state).toBe('blocked')
    expect(next.size).toBe(1)
  })

  it('deletes on a removal, which carries only id and removed', () => {
    // The server sends Run{ID, Removed:true} with every other field empty, as
    // an ordinary `update` — not a distinct event type.
    const first = applyEvent(new Map(), run('pane-1'))
    const next = applyEvent(first, { id: 'pane-1', removed: true } as Run)
    expect(next.has('pane-1')).toBe(false)
  })

  it('ignores a removal for a run it never had', () => {
    const next = applyEvent(new Map(), { id: 'ghost', removed: true } as Run)
    expect(next.size).toBe(0)
  })

  it('does not mutate the map it was given', () => {
    const first = applyEvent(new Map(), run('pane-1'))
    applyEvent(first, run('pane-2'))
    expect(first.size).toBe(1)
  })
})
```

- [ ] **Step 2: Run it to verify it fails**

Run: `cd ui && npx vitest run useRuns`
Expected: FAIL — `Cannot find module './useRuns'`.

- [ ] **Step 3: Write `ui/src/hooks/useRuns.ts`**

```ts
import { useEffect, useState } from 'react'
import type { Run } from '../api/runs'

/**
 * applyEvent folds one streamed Run into the map, returning a new map.
 *
 * A removal arrives as an ordinary update carrying only `id` and `removed`, so
 * it must be checked before any other field is read.
 */
export function applyEvent(runs: Map<string, Run>, r: Run): Map<string, Run> {
  const next = new Map(runs)
  if (r.removed) {
    next.delete(r.id)
    return next
  }
  next.set(r.id, r)
  return next
}

/**
 * Subscribes to /api/runs/stream. The server sends one `snapshot` event on
 * connect and one `update` per composed change; it also re-sends a full
 * `snapshot` when it detects this subscriber missed an update, so a snapshot
 * arriving mid-stream is a resync and replaces the map wholesale.
 */
export function useRuns() {
  const [runs, setRuns] = useState<Map<string, Run>>(new Map())
  const [connected, setConnected] = useState(false)

  useEffect(() => {
    const es = new EventSource('/api/runs/stream')

    es.addEventListener('open', () => setConnected(true))

    es.addEventListener('snapshot', (ev: MessageEvent<string>) => {
      try {
        const arr = JSON.parse(ev.data) as Run[]
        setRuns(new Map(arr.map((r) => [r.id, r])))
        setConnected(true)
      } catch (e) {
        console.error('runs snapshot parse failed', e)
      }
    })

    es.addEventListener('update', (ev: MessageEvent<string>) => {
      try {
        const r = JSON.parse(ev.data) as Run
        setRuns((prev) => applyEvent(prev, r))
      } catch (e) {
        console.error('runs update parse failed', e)
      }
    })

    es.onerror = () => setConnected(false) // EventSource retries on its own

    return () => es.close()
  }, [])

  return { runs: Array.from(runs.values()), connected }
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd ui && npx vitest run`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add ui/src/hooks/useRuns.ts ui/src/hooks/useRuns.test.ts
git commit -m "feat(ui): useRuns store over the runs stream"
```

---

### Task 4: The Fleet view

**Files:**
- Create: `ui/src/fleet/RunCard.tsx`, `ui/src/fleet/FleetView.tsx`, `ui/src/fleet/fleet.css`

**Interfaces:**
- Consumes: `useRuns`, `Run`, `needsYou`, `isHistory`, `isFresh`.
- Produces: `<FleetView />`, `<RunCard run={} now={} />`.

- [ ] **Step 1: Write `ui/src/fleet/fleet.css`**

Card structure from the approved "Instrument Mocha" direction: rounded raised surfaces
on a mantle ground, sans labels with mono for data, a status dot, one accent reserved
for "needs you".

```css
.fleet {
  position: absolute;
  inset: 0;
  overflow-y: auto;
  background: var(--bg);
  color: var(--text-dim);
  font-family: var(--font-ui);
  font-size: 14px;
  -webkit-tap-highlight-color: transparent;
}

.fleet-nav {
  position: sticky;
  top: 0;
  z-index: 10;
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 12px 14px;
  background: color-mix(in srgb, var(--bg) 92%, transparent);
  backdrop-filter: blur(12px);
  border-bottom: 1px solid var(--line);
}
.fleet-nav h1 { margin: 0; font-size: 16px; font-weight: 600; color: var(--text); }
.fleet-badge {
  font-size: 12px; font-weight: 600; padding: 3px 10px; border-radius: 999px;
  background: color-mix(in srgb, var(--state-blocked) 22%, transparent);
  color: var(--state-blocked);
}
.fleet-badge.quiet { background: var(--ctp-surface0); color: var(--text-faint); }

.fleet-filters { display: flex; gap: 4px; padding: 10px 12px 4px; }
.fleet-filters button {
  flex: 1; min-height: 34px; border: 1px solid var(--line); border-radius: 8px;
  background: var(--bg-raised); color: var(--text-faint);
  font: inherit; font-size: 12.5px; cursor: pointer;
}
.fleet-filters button.on {
  background: var(--ctp-surface0); color: var(--text); font-weight: 600;
  border-color: var(--line-strong);
}

.fleet-group {
  display: flex; justify-content: space-between;
  padding: 14px 14px 6px; font-size: 12px; font-weight: 600; color: var(--text-faint);
}
.fleet-group .host-local { color: var(--host-local); }
.fleet-group .host-remote { color: var(--host-remote); }

.run-card {
  display: block; width: calc(100% - 20px); margin: 0 10px 8px;
  padding: 11px 12px; border-radius: 10px; text-align: left;
  background: var(--bg-raised); border: 1px solid var(--line);
  color: inherit; font: inherit; cursor: pointer;
  min-height: var(--tap);
}
.run-card.attention { border-color: color-mix(in srgb, var(--state-blocked) 45%, var(--line)); }
.run-card.history { opacity: 0.55; }

.run-head { display: flex; align-items: center; gap: 8px; margin-bottom: 5px; }
.run-dot { width: 8px; height: 8px; border-radius: 50%; flex: none; }
.run-name {
  color: var(--text); font-size: 14px; font-weight: 600; letter-spacing: -0.01em;
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
.run-age { margin-left: auto; flex: none; font-size: 12px; color: var(--text-faint); }
.run-age.stale { font-style: italic; }

.run-sub {
  font-family: var(--font-mono); font-size: 12px; color: var(--text-dim);
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
.run-question {
  margin-top: 9px; padding: 9px 10px; border-radius: 8px;
  background: var(--bg-sunken); color: var(--ctp-subtext1);
  font-size: 13px; line-height: 1.45;
}
.run-chips { display: flex; flex-wrap: wrap; gap: 5px; margin-top: 8px; }
.run-chip {
  font-size: 11px; padding: 2.5px 7px; border-radius: 5px;
  background: var(--ctp-surface0); color: var(--text-dim);
}
.run-chip.pr { background: color-mix(in srgb, var(--state-running) 18%, transparent); color: var(--state-running); }
.run-chip.pr.failing { background: color-mix(in srgb, var(--state-blocked) 18%, transparent); color: var(--state-blocked); }
.run-chip.issue { background: color-mix(in srgb, var(--accent) 18%, transparent); color: var(--accent); }

.fleet-empty { padding: 40px 20px; text-align: center; color: var(--text-faint); font-size: 13px; }
```

- [ ] **Step 2: Write `ui/src/fleet/RunCard.tsx`**

```tsx
import type { Run } from '../api/runs'
import { isFresh, isHistory, needsYou } from './staleness'

function agoLabel(updatedAt: number, now: number): string {
  const s = Math.max(0, Math.floor(now / 1000 - updatedAt))
  if (s < 60) return `${s}s`
  const m = Math.floor(s / 60)
  if (m < 60) return `${m}m`
  const h = Math.floor(m / 60)
  if (h < 24) return `${h}h`
  return `${Math.floor(h / 24)}d`
}

/** The one line describing what this run is doing right now. */
function subtitle(run: Run): string {
  const a = run.activity
  if (run.state === 'blocked') return a.message || 'waiting on you'
  if (a.tool) return a.hint ? `${a.tool} · ${a.hint}` : a.tool
  if (a.task) return a.task
  if (run.pr) return `PR #${run.pr.number}${run.pr.check_state ? ` · ${run.pr.check_state}` : ''}`
  return run.state
}

export function RunCard({ run, now, onOpen }: { run: Run; now: number; onOpen?: (r: Run) => void }) {
  const attention = needsYou(run, now)
  const history = isHistory(run, now)
  const stale = !isFresh(run, now)

  return (
    <button
      type="button"
      className={`run-card${attention ? ' attention' : ''}${history ? ' history' : ''}`}
      onClick={() => onOpen?.(run)}
    >
      <div className="run-head">
        <span className="run-dot" style={{ background: `var(--state-${run.state})` }} />
        <span className="run-name">{run.repo ? `${run.repo}/${run.branch ?? ''}` : run.branch || run.id}</span>
        <span className={`run-age${stale ? ' stale' : ''}`}>{agoLabel(run.updated_at, now)}</span>
      </div>

      <div className="run-sub">{subtitle(run)}</div>

      {run.question && <div className="run-question">{run.question.text}</div>}

      <div className="run-chips">
        <span className="run-chip">{run.agent}</span>
        {run.issue && <span className="run-chip issue">{run.issue.id}</span>}
        {run.pr && (
          <span className={`run-chip pr${run.pr.check_state === 'failure' ? ' failing' : ''}`}>
            #{run.pr.number}
          </span>
        )}
        {run.crew && <span className="run-chip">{run.crew.name}</span>}
      </div>
    </button>
  )
}
```

- [ ] **Step 3: Write `ui/src/fleet/FleetView.tsx`**

```tsx
import { useEffect, useMemo, useState } from 'react'
import { useRuns } from '../hooks/useRuns'
import type { Run } from '../api/runs'
import { isHistory, needsYou } from './staleness'
import { RunCard } from './RunCard'
import './fleet.css'

type Filter = 'active' | 'needs-you' | 'all'

/** Ticks once a minute so relative ages and freshness stay honest. */
function useNow(): number {
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    const id = window.setInterval(() => setNow(Date.now()), 60_000)
    return () => window.clearInterval(id)
  }, [])
  return now
}

export function FleetView({ onOpen }: { onOpen?: (r: Run) => void }) {
  const { runs, connected } = useRuns()
  const now = useNow()
  const [filter, setFilter] = useState<Filter>('active')

  const attentionCount = useMemo(
    () => runs.filter((r) => needsYou(r, now)).length,
    [runs, now],
  )

  const visible = useMemo(() => {
    const keep = runs.filter((r) => {
      if (filter === 'needs-you') return needsYou(r, now)
      if (filter === 'active') return !isHistory(r, now)
      return true
    })
    // Anything asking for input first, then most recently active.
    return keep.sort((a, b) => {
      const an = needsYou(a, now) ? 1 : 0
      const bn = needsYou(b, now) ? 1 : 0
      if (an !== bn) return bn - an
      return b.updated_at - a.updated_at
    })
  }, [runs, filter, now])

  const groups = useMemo(() => {
    const byHost = new Map<string, Run[]>()
    for (const r of visible) {
      const host = r.host || 'local'
      const list = byHost.get(host)
      if (list) list.push(r)
      else byHost.set(host, [r])
    }
    return Array.from(byHost.entries())
  }, [visible])

  return (
    <div className="fleet mocha">
      <header className="fleet-nav">
        <h1>Fleet</h1>
        <span className={`fleet-badge${attentionCount === 0 ? ' quiet' : ''}`}>
          {attentionCount > 0 ? `${attentionCount} needs you` : connected ? 'all quiet' : 'offline'}
        </span>
      </header>

      <nav className="fleet-filters" aria-label="filter runs">
        <button className={filter === 'active' ? 'on' : ''} onClick={() => setFilter('active')}>Active</button>
        <button className={filter === 'needs-you' ? 'on' : ''} onClick={() => setFilter('needs-you')}>Needs you</button>
        <button className={filter === 'all' ? 'on' : ''} onClick={() => setFilter('all')}>All</button>
      </nav>

      {visible.length === 0 && (
        <div className="fleet-empty">
          {connected ? 'No runs match this filter.' : 'Connecting to the run stream…'}
        </div>
      )}

      {groups.map(([host, list]) => (
        <section key={host}>
          <div className="fleet-group">
            <span className={host === 'local' ? 'host-local' : 'host-remote'}>{host}</span>
            <span>{list.length}</span>
          </div>
          {list.map((r) => (
            <RunCard key={r.id} run={r} now={now} onOpen={onOpen} />
          ))}
        </section>
      ))}
    </div>
  )
}
```

- [ ] **Step 4: Verify types and lint**

Run: `cd ui && npx tsc -b && npx eslint . && npx vitest run`
Expected: all clean.

- [ ] **Step 5: Commit**

```bash
git add ui/src/fleet/
git commit -m "feat(ui): fleet view over /api/runs"
```

---

### Task 5: The 4-tab shell, mounted at `#/fleet`

Fleet is live; Crews, Workspace and Dispatch render a placeholder naming the milestone
that fills them. Building the bar now means the shell can be held in a hand and judged
before three more surfaces are built on top of it.

**Files:**
- Create: `ui/src/fleet/Shell.tsx`
- Modify: `ui/src/fleet/fleet.css` (tab bar), `ui/src/App.tsx` (route)

- [ ] **Step 1: Add the tab bar to `ui/src/fleet/fleet.css`**

```css
.shell { position: absolute; inset: 0; display: flex; flex-direction: column; background: var(--bg); }
.shell-body { flex: 1; position: relative; overflow: hidden; }

.shell-tabs {
  display: flex; flex: none;
  background: var(--bg-sunken); border-top: 1px solid var(--line);
  padding-bottom: env(safe-area-inset-bottom);
}
.shell-tabs button {
  flex: 1; min-height: var(--tap);
  display: flex; flex-direction: column; align-items: center; justify-content: center; gap: 2px;
  border: 0; background: none; color: var(--text-faint);
  font: inherit; font-size: 10px; cursor: pointer;
}
.shell-tabs button.on { color: var(--accent); font-weight: 700; }
.shell-tabs .glyph { font-size: 15px; line-height: 1; }
.shell-tabs .dot {
  position: absolute; transform: translate(14px, -10px);
  width: 6px; height: 6px; border-radius: 50%; background: var(--state-blocked);
}

.shell-placeholder {
  position: absolute; inset: 0; display: flex; align-items: center; justify-content: center;
  padding: 24px; text-align: center; color: var(--text-faint); font-size: 13px; line-height: 1.6;
}
```

- [ ] **Step 2: Write `ui/src/fleet/Shell.tsx`**

```tsx
import { useMemo, useState } from 'react'
import { useRuns } from '../hooks/useRuns'
import { needsYou } from './staleness'
import { FleetView } from './FleetView'
import './fleet.css'

type Tab = 'fleet' | 'crews' | 'workspace' | 'dispatch'

const PLACEHOLDER: Record<Exclude<Tab, 'fleet'>, string> = {
  crews: 'Crews arrives with the dispatcher milestone — crew grouping, tier and PR badges, and answering a blocked worker from here.',
  workspace: 'Workspace arrives next — the tmux tree across every host, including panes that are not agents.',
  dispatch: 'Dispatch arrives with the dispatcher milestone — pick a repo, task, tier and engine, and start work from your phone.',
}

export function Shell() {
  const [tab, setTab] = useState<Tab>('fleet')
  const { runs } = useRuns()
  const attention = useMemo(() => {
    const now = Date.now()
    return runs.some((r) => needsYou(r, now))
  }, [runs])

  return (
    <div className="shell mocha">
      <div className="shell-body">
        {tab === 'fleet' ? <FleetView /> : <div className="shell-placeholder">{PLACEHOLDER[tab]}</div>}
      </div>

      <nav className="shell-tabs" aria-label="sections">
        <button className={tab === 'fleet' ? 'on' : ''} onClick={() => setTab('fleet')}>
          <span className="glyph" aria-hidden>▤</span>
          Fleet
          {attention && tab !== 'fleet' && <span className="dot" />}
        </button>
        <button className={tab === 'crews' ? 'on' : ''} onClick={() => setTab('crews')}>
          <span className="glyph" aria-hidden>◆</span>Crews
        </button>
        <button className={tab === 'workspace' ? 'on' : ''} onClick={() => setTab('workspace')}>
          <span className="glyph" aria-hidden>▣</span>Workspace
        </button>
        <button className={tab === 'dispatch' ? 'on' : ''} onClick={() => setTab('dispatch')}>
          <span className="glyph" aria-hidden>✦</span>Dispatch
        </button>
      </nav>
    </div>
  )
}
```

Note `Shell` calls `useRuns()` for the badge dot while `FleetView` calls it again — two
EventSource connections to the same endpoint. That is deliberate for now: sharing the
store means lifting it to context, which belongs with the plan that makes this shell the
default. If the duplicate connection shows up in testing as a real cost, say so rather
than fixing it here.

- [ ] **Step 3: Route `#/fleet` in `ui/src/App.tsx`**

Add `'fleet'` to the `View` union, return `<Shell />` for it, and add a `#/fleet` case to
`initialView()`. Add a third button to the existing `ViewSwitch` so the new shell is
reachable. **Do not remove or restyle the existing agents/panes views** — they stay until
a later plan retires them.

- [ ] **Step 4: Verify**

Run: `cd ui && npx tsc -b && npx eslint . && npx vitest run && npm run build`
Expected: all clean, and the production build succeeds.

- [ ] **Step 5: Check it against real data by hand**

```bash
just build && ./houston -addr 127.0.0.1:9095 &
```

Open `http://127.0.0.1:9095/#/fleet` once so the browser holds the auth cookie. Confirm,
and report each: runs appear grouped by host; a run with a linked issue or PR shows those
chips; the badge counts only recently-blocked runs; the "All" filter reveals finished
runs that "Active" hides; and the existing `#/agents` and `#/panes` views still work.

Kill your server afterwards. Do **not** run `tmux kill-server`, `tmux kill-session`, or
any tmux command touching a server or session you did not create.

- [ ] **Step 6: Commit**

```bash
git add ui/src/fleet/Shell.tsx ui/src/fleet/fleet.css ui/src/App.tsx
git commit -m "feat(ui): 4-tab shell with the fleet tab live"
```

---

## Self-Review

**Spec coverage:**

| Spec requirement | Task |
|---|---|
| Mocha token system | 1 |
| One `useRuns()` store over the runs stream | 3 |
| Mobile 4-tab shell — Fleet · Crews · Workspace · Dispatch | 5 |
| Fleet grouped by host, filters, needs-you badge | 4 |
| Desktop console shell | **out of scope** — a later plan |
| Run detail with Activity/Terminal/Diff tabs | **out of scope** — the terminal plan |
| `agents.css`'s `--ag-*` block removed | **out of scope** — the old views still use it |

**Placeholder scan:** none. The three inert tabs render explanatory copy naming the
milestone that fills them, which is content, not a stub.

**Type consistency:** `Run`, `RunState`, `Caps`, `useRuns`, `applyEvent`, `FRESH_MS`,
`isFresh`, `needsYou`, `isHistory`, `RunCard`, `FleetView`, `Shell` are spelled
identically everywhere. Field names come from the Go `json:` tags verbatim — snake_case,
not camelCase.

**Two things a reviewer should push on.** First, `Shell` and `FleetView` each open their
own `EventSource`; that is a deliberate deferral, not an oversight, but it is two
connections per client and worth a second opinion. Second, `FRESH_MS` is one hour chosen
from a single observation of real data — if a reviewer thinks the asymmetry is wrong
(never hide `blocked`, but only badge it when fresh), that is the decision to challenge,
because everything else in the view follows from it.
