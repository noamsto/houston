import { describe, expect, it } from 'vitest'
import type { Run } from '../api/runs'
import {
  COLLAPSE_OVER,
  MEMBER_CAP,
  entryHost,
  entryProject,
  fleetEntries,
  groupEntriesByHost,
  groupEntriesByProject,
  narrowToCrew,
  type FleetEntry,
} from './fleetEntries'
import { filterRuns } from './fleetList'
import { needsYou } from './staleness'

const now = 1_800_000_000_000 // fixed ms
const nowSec = Math.floor(now / 1000)
const HOUR = 3600

function run(p: Partial<Run> = {}): Run {
  return {
    id: 'pane-1', agent: 'claude', state: 'running',
    activity: {}, tokens: { input: 0, output: 0 },
    caps: { terminal: true, reply: true, kill: true },
    updated_at: nowSec,
    ...p,
  } as Run
}

const dispatcher = (id: string, crew: string, p: Partial<Run> = {}) =>
  run({ id, role: 'dispatcher', crew: { name: crew }, ...p })
const worker = (id: string, crew: string, p: Partial<Run> = {}) =>
  run({ id, role: 'worker', crew: { name: crew }, ...p })

const keys = (es: FleetEntry[]) => es.map((e) => e.key)
const ids = (rs: Run[]) => rs.map((r) => r.id)

describe('fleetEntries: dispatcher groups', () => {
  it('raises the dispatcher entry for a needs-you worker without changing the global count', () => {
    const runs = [
      run({ id: 'solo', since: 100 }),
      dispatcher('d1', '1700-1', { state: 'idle', since: 50 }),
      worker('w1', '1700-1', { state: 'blocked', attention: 'needs-you' }),
      worker('w2', '1700-1'),
    ]
    const es = fleetEntries(runs, 'active', now, 'dispatcher')
    expect(keys(es)).toEqual(['d1', 'solo'])
    const d = es[0]
    if (d.kind !== 'dispatcher') throw new Error('expected a dispatcher entry')
    expect(d.needsYou).toBe(1)
    expect(d.counts.needsYou).toBe(1)
    expect(d.counts.working).toBe(1)
    expect(ids(d.shown)).toEqual(['w1', 'w2'])
    expect(ids(d.members).sort()).toEqual(['w1', 'w2'])
    expect(runs.filter((r) => needsYou(r, now)).length).toBe(1)
    expect(keys(es)).not.toContain('w1')
  })

  it('gives two dispatchers in one project only their own crew', () => {
    const runs = [
      dispatcher('d1', '1700-1', { project: 'p', since: 2 }),
      dispatcher('d2', '1700-2', { project: 'p', since: 1 }),
      worker('a', '1700-1', { project: 'p' }),
      worker('b', '1700-2', { project: 'p' }),
      worker('c', '1700-2', { project: 'p' }),
    ]
    const es = fleetEntries(runs, 'all', now, 'dispatcher')
    const byKey = new Map(es.map((e) => [e.key, e]))
    const d1 = byKey.get('d1')
    const d2 = byKey.get('d2')
    if (d1?.kind !== 'dispatcher' || d2?.kind !== 'dispatcher') throw new Error('expected dispatcher entries')
    expect(ids(d1.members)).toEqual(['a'])
    expect(ids(d2.members).sort()).toEqual(['b', 'c'])
  })

  it('does not match a crew across hosts, and treats undefined and empty host alike', () => {
    const runs = [
      dispatcher('d1', '1700-1', { host: '' }),
      worker('w-local', '1700-1'),
      worker('w-remote', '1700-1', { host: 'box' }),
    ]
    const es = fleetEntries(runs, 'all', now, 'dispatcher')
    const d = es.find((e) => e.kind === 'dispatcher')
    if (d?.kind !== 'dispatcher') throw new Error('expected a dispatcher entry')
    expect(ids(d.members)).toEqual(['w-local'])
    const orphan = es.find((e) => e.kind === 'crew')
    if (orphan?.kind !== 'crew') throw new Error('expected a crew entry')
    expect(orphan.host).toBe('box')
    expect(ids(orphan.members)).toEqual(['w-remote'])
  })

  it('groups workers with no dispatcher into a crew entry per crew and host', () => {
    const runs = [
      worker('a', '1700-9', { project: 'p' }),
      worker('b', '1700-9', { project: 'p', state: 'blocked' }),
      worker('c', '1700-8', { project: 'q' }),
    ]
    const es = fleetEntries(runs, 'active', now, 'dispatcher')
    expect(keys(es)).toEqual(['crew:local:1700-9', 'crew:local:1700-8'])
    const c = es[0]
    if (c.kind !== 'crew') throw new Error('expected a crew entry')
    expect(c.crew).toBe('1700-9')
    expect(c.project).toBe('p')
    expect(c.needsYou).toBe(1)
    expect(ids(c.shown)).toEqual(['b', 'a'])
  })

  it('leaves out a project when the members disagree', () => {
    const runs = [worker('a', '1-1', { project: 'p' }), worker('b', '1-1', { project: 'q' })]
    const [e] = fleetEntries(runs, 'all', now, 'dispatcher')
    if (e.kind !== 'crew') throw new Error('expected a crew entry')
    expect(e.project).toBeUndefined()
    expect(entryProject(e)).toBe('unknown')
  })

  it('keeps a worker with no crew.name as a top-level run', () => {
    const runs = [
      worker('plain', '', { crew: { name: '' } }),
      run({ id: 'noref', role: 'worker' }),
      run({ id: 'd-bare', role: 'dispatcher' }),
    ]
    const es = fleetEntries(runs, 'all', now, 'dispatcher')
    expect(es.every((e) => e.kind === 'run')).toBe(true)
    expect(keys(es).sort()).toEqual(['d-bare', 'noref', 'plain'])
  })

  it('orders crew entries by crew id seconds, newest first, among equal ranks', () => {
    const runs = [worker('a', '1000-1'), worker('b', '2000-1'), worker('c', '2000-0')]
    expect(keys(fleetEntries(runs, 'all', now, 'dispatcher'))).toEqual([
      'crew:local:2000-0',
      'crew:local:2000-1',
      'crew:local:1000-1',
    ])
  })
})

describe('fleetEntries: flat modes', () => {
  const runs = [
    dispatcher('d1', '1700-1', { since: 5 }),
    worker('w1', '1700-1', { state: 'blocked', since: 9 }),
    worker('w2', '1700-1', { since: 8 }),
    run({ id: 'solo', since: 7 }),
    run({ id: 'old', state: 'done', updated_at: nowSec - 2 * HOUR }),
  ]
  for (const mode of ['tmux', null] as const) {
    for (const filter of ['active', 'needs-you', 'stuck', 'done', 'all'] as const) {
      it(`${String(mode)} / ${filter} returns filterRuns order as run entries`, () => {
        const es = fleetEntries(runs, filter, now, mode)
        expect(es.every((e) => e.kind === 'run')).toBe(true)
        expect(keys(es)).toEqual(ids(filterRuns(runs, filter, now)))
      })
    }
  }
})

describe('fleetEntries: ranking and stability', () => {
  it('ranks a dispatcher with a needs-you worker above a quiet solo run', () => {
    const runs = [
      run({ id: 'solo', since: 1000 }),
      dispatcher('d1', '1-1', { state: 'idle', since: 1 }),
      worker('w1', '1-1', { state: 'blocked' }),
    ]
    expect(keys(fleetEntries(runs, 'active', now, 'dispatcher'))).toEqual(['d1', 'solo'])
    const quiet = [runs[0], dispatcher('d1', '1-1', { state: 'idle', since: 1 }), worker('w1', '1-1')]
    expect(keys(fleetEntries(quiet, 'active', now, 'dispatcher'))).toEqual(['solo', 'd1'])
  })

  it('does not reorder when a member only changes its activity text', () => {
    const build = (text: string) => [
      dispatcher('d1', '1-1', { since: 10 }),
      dispatcher('d2', '1-2', { since: 20 }),
      worker('w1', '1-1', { activity: { tool: text } as Run['activity'], updated_at: nowSec - 5 }),
      worker('w2', '1-2'),
    ]
    expect(keys(fleetEntries(build('a'), 'active', now, 'dispatcher'))).toEqual(['d2', 'd1'])
    expect(keys(fleetEntries(build('b'), 'active', now, 'dispatcher'))).toEqual(['d2', 'd1'])
  })

  it('reorders when a member turns blocked', () => {
    const build = (state: Run['state']) => [
      dispatcher('d1', '1-1', { since: 10 }),
      dispatcher('d2', '1-2', { since: 20 }),
      worker('w1', '1-1', { state }),
      worker('w2', '1-2'),
    ]
    expect(keys(fleetEntries(build('running'), 'active', now, 'dispatcher'))).toEqual(['d2', 'd1'])
    expect(keys(fleetEntries(build('blocked'), 'active', now, 'dispatcher'))).toEqual(['d1', 'd2'])
  })

  it('sorts members by rank then start order', () => {
    const runs = [
      dispatcher('d1', '1-1'),
      worker('old', '1-1', { since: 1 }),
      worker('new', '1-1', { since: 9 }),
      worker('blk', '1-1', { since: 5, state: 'blocked' }),
    ]
    const [d] = fleetEntries(runs, 'active', now, 'dispatcher')
    if (d.kind !== 'dispatcher') throw new Error('expected a dispatcher entry')
    expect(ids(d.shown)).toEqual(['blk', 'new', 'old'])
  })

  it('uses freshness first under the needs-you filter', () => {
    const runs = [
      dispatcher('d-stale', '1-1', { since: 99, state: 'idle', updated_at: nowSec - 2 * HOUR }),
      worker('w-stale', '1-1', { state: 'blocked', updated_at: nowSec - 2 * HOUR }),
      dispatcher('d-fresh', '1-2', { since: 1, state: 'idle' }),
      worker('w-fresh', '1-2', { state: 'blocked' }),
    ]
    expect(keys(fleetEntries(runs, 'needs-you', now, 'dispatcher'))).toEqual(['d-fresh', 'd-stale'])
  })
})

describe('fleetEntries: freshness under a filter', () => {
  it('ignores a head that does not match the filter when ranking by freshness', () => {
    const runs = [
      dispatcher('d', '1-1', { since: 99 }),
      worker('w-stale', '1-1', { state: 'blocked', updated_at: nowSec - 2 * HOUR }),
      run({ id: 'solo-fresh', state: 'blocked', since: 1 }),
    ]
    expect(keys(fleetEntries(runs, 'needs-you', now, 'dispatcher'))).toEqual(['solo-fresh', 'd'])
  })

  it('still counts a head that matches the filter', () => {
    const runs = [
      dispatcher('d', '1-1', { since: 99, state: 'blocked' }),
      worker('w-stale', '1-1', { state: 'blocked', updated_at: nowSec - 2 * HOUR }),
      run({ id: 'solo-fresh', state: 'blocked', since: 1 }),
    ]
    expect(keys(fleetEntries(runs, 'needs-you', now, 'dispatcher'))).toEqual(['d', 'solo-fresh'])
  })
})

describe('fleetEntries: filters', () => {
  it('needs-you keeps the head as container with only the matching members', () => {
    const runs = [
      dispatcher('d1', '1-1', { state: 'idle' }),
      worker('w1', '1-1', { state: 'blocked' }),
      worker('w2', '1-1'),
      dispatcher('d2', '1-2', { state: 'idle' }),
      worker('w3', '1-2'),
    ]
    const es = fleetEntries(runs, 'needs-you', now, 'dispatcher')
    expect(keys(es)).toEqual(['d1'])
    const d = es[0]
    if (d.kind !== 'dispatcher') throw new Error('expected a dispatcher entry')
    expect(ids(d.shown)).toEqual(['w1'])
    expect(ids(d.members).sort()).toEqual(['w1', 'w2'])
  })

  it('keeps a matching head with no matching members', () => {
    const runs = [dispatcher('d1', '1-1', { state: 'blocked' }), worker('w1', '1-1')]
    const [d] = fleetEntries(runs, 'needs-you', now, 'dispatcher')
    if (d.kind !== 'dispatcher') throw new Error('expected a dispatcher entry')
    expect(d.shown).toEqual([])
    expect(ids(d.members)).toEqual(['w1'])
  })

  it('active excludes history members but counts them as ended', () => {
    const runs = [
      dispatcher('d1', '1-1'),
      worker('live', '1-1'),
      worker('gone', '1-1', { state: 'done', updated_at: nowSec - 2 * HOUR }),
    ]
    const [d] = fleetEntries(runs, 'active', now, 'dispatcher')
    if (d.kind !== 'dispatcher') throw new Error('expected a dispatcher entry')
    expect(ids(d.shown)).toEqual(['live'])
    expect(ids(d.members).sort()).toEqual(['gone', 'live'])
    expect(d.counts.ended).toBe(1)
    expect(fleetEntries(runs, 'all', now, 'dispatcher').flatMap((e) => (e.kind === 'dispatcher' ? ids(e.shown) : [])).sort())
      .toEqual(['gone', 'live'])
  })

  it('drops an orphan crew with no matching member', () => {
    const runs = [worker('w1', '1-1'), run({ id: 'solo' })]
    expect(keys(fleetEntries(runs, 'needs-you', now, 'dispatcher'))).toEqual([])
  })

  it('exposes the member constants', () => {
    expect(MEMBER_CAP).toBe(5)
    expect(COLLAPSE_OVER).toBe(6)
  })
})

describe('entry grouping', () => {
  const runs = [
    dispatcher('d1', '1-1', { project: 'alpha', host: 'a' }),
    worker('w1', '1-1', { project: 'alpha', host: 'a' }),
    run({ id: 'solo-a', project: 'alpha', host: 'a' }),
    run({ id: 'solo-b', project: 'beta' }),
    worker('orphan', '1-2', { project: 'beta' }),
  ]
  const es = fleetEntries(runs, 'all', now, 'dispatcher')

  it('reads host and project from the head or the crew', () => {
    const by = new Map(es.map((e) => [e.key, e]))
    expect(entryHost(by.get('d1')!)).toBe('a')
    expect(entryHost(by.get('solo-b')!)).toBe('local')
    expect(entryHost(by.get('crew:local:1-2')!)).toBe('local')
    expect(entryProject(by.get('d1')!)).toBe('alpha')
    expect(entryProject(by.get('crew:local:1-2')!)).toBe('beta')
  })

  it('groups by host in order of first appearance', () => {
    const groups = groupEntriesByHost(es)
    expect(groups.map(([h]) => h).sort()).toEqual(['a', 'local'])
    const a = groups.find(([h]) => h === 'a')![1]
    expect(keys(a).sort()).toEqual(['d1', 'solo-a'])
  })

  it('groups by project with dispatchers leading', () => {
    const groups = groupEntriesByProject([...es].reverse())
    const alpha = groups.find(([p]) => p === 'alpha')![1]
    expect(alpha[0].kind).toBe('dispatcher')
    expect(keys(alpha)).toEqual(['d1', 'solo-a'])
    const beta = groups.find(([p]) => p === 'beta')![1]
    expect(keys(beta).sort()).toEqual(['crew:local:1-2', 'solo-b'])
  })
})

describe('narrowToCrew', () => {
  it('keeps the dispatcher and crew entries of the crew plus its own runs', () => {
    const runs = [
      dispatcher('d1', '1-1'),
      worker('w1', '1-1'),
      dispatcher('d2', '1-2'),
      worker('w2', '1-2'),
      worker('orphan', '1-3'),
      run({ id: 'solo' }),
    ]
    const es = fleetEntries(runs, 'all', now, 'dispatcher')
    expect(keys(narrowToCrew(es, '1-1'))).toEqual(['d1'])
    expect(keys(narrowToCrew(es, '1-3'))).toEqual(['crew:local:1-3'])
    const flat = fleetEntries(runs, 'all', now, 'tmux')
    expect(keys(narrowToCrew(flat, '1-2')).sort()).toEqual(['d2', 'w2'])
  })
})
