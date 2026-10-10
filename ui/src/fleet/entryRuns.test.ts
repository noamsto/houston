import { describe, expect, it } from 'vitest'
import type { Run } from '../api/runs'
import { dispatcherCrewLine, entryRuns } from './entryRuns'
import { fleetEntries } from './fleetEntries'

const now = 1_800_000_000_000 // fixed ms
const nowSec = Math.floor(now / 1000)

function run(p: Partial<Run> = {}): Run {
  return {
    id: 'pane-1', agent: 'claude', state: 'running',
    activity: {}, tokens: { input: 0, output: 0 },
    caps: { terminal: true, reply: true, kill: true },
    updated_at: nowSec,
    ...p,
  } as Run
}

const crew = { name: '1700000000-11' }
const head = run({ id: 'd', role: 'dispatcher', state: 'idle', crew })
const worker = (id: string, p: Partial<Run> = {}) => run({ id, role: 'worker', crew, ...p })

function dispatcherEntry(runs: Run[], filter: 'active' | 'needs-you' | 'all' = 'active') {
  const e = fleetEntries(runs, filter, now, 'dispatcher').find((x) => x.kind === 'dispatcher')
  if (e?.kind !== 'dispatcher') throw new Error('expected a dispatcher entry')
  return e
}

describe('dispatcherCrewLine', () => {
  it('counts workers and omits the need-you part at zero', () => {
    expect(dispatcherCrewLine(dispatcherEntry([head, worker('a'), worker('b')]), now)).toBe('2 workers')
  })

  it('singularises one worker and appends fresh needs-you workers', () => {
    expect(dispatcherCrewLine(dispatcherEntry([head, worker('a', { state: 'blocked' })]), now)).toBe('1 worker · 1 need you')
  })

  it('does not count a stale blocked worker as needing you', () => {
    const stale = worker('a', { state: 'blocked', updated_at: nowSec - 2 * 3600 })
    expect(dispatcherCrewLine(dispatcherEntry([head, stale, worker('b')]), now)).toBe('2 workers')
  })

  it('counts live workers whatever the filter, and none for ended ones', () => {
    const ended = worker('old', { state: 'done' })
    const runs = [head, worker('a', { state: 'blocked' }), worker('b'), ended]
    expect(dispatcherCrewLine(dispatcherEntry(runs, 'needs-you'), now)).toBe('2 workers · 1 need you')
    expect(dispatcherCrewLine(dispatcherEntry([head, ended]), now)).toBeNull()
    expect(dispatcherCrewLine(dispatcherEntry([head]), now)).toBeNull()
  })
})

describe('entryRuns', () => {
  it('is the run, the head plus shown members, or the shown members', () => {
    const runs = [head, worker('a', { state: 'blocked' }), worker('b'), run({ id: 'solo' }), worker('o', { crew: { name: 'other' } })]
    const es = fleetEntries(runs, 'needs-you', now, 'dispatcher')
    const byKind = Object.fromEntries(es.map((e) => [e.kind, entryRuns(e).map((r) => r.id)]))
    expect(byKind.dispatcher).toEqual(['d', 'a'])
    expect(entryRuns(fleetEntries([run({ id: 'solo' })], 'all', now, 'tmux')[0])).toHaveLength(1)
    expect(fleetEntries([worker('o', { crew: { name: 'other' } })], 'all', now, 'dispatcher').flatMap(entryRuns)).toHaveLength(1)
  })
})
