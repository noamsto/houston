import { describe, expect, it } from 'vitest'
import type { Run } from '../api/runs'
import { crewShortId, filterRuns, groupByHost, groupByProject, projectOf } from './fleetList'

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

describe('filterRuns', () => {
  it('active hides stale done/failed but keeps a stale blocked run', () => {
    const runs = [
      run({ id: 'stale-done', state: 'done', updated_at: nowSec - 2 * HOUR }),
      run({ id: 'stale-failed', state: 'failed', updated_at: nowSec - 2 * HOUR }),
      run({ id: 'stale-blocked', state: 'blocked', updated_at: nowSec - 2 * HOUR }),
    ]
    const result = filterRuns(runs, 'active', now)
    expect(result.map((r) => r.id)).toEqual(['stale-blocked'])
  })

  it('needs-you returns fresh and stale blocked, fresh first', () => {
    const runs = [
      run({ id: 'stale-blocked', state: 'blocked', updated_at: nowSec - 2 * HOUR }),
      run({ id: 'fresh-blocked', state: 'blocked', updated_at: nowSec }),
      run({ id: 'running', state: 'running' }),
    ]
    const result = filterRuns(runs, 'needs-you', now)
    expect(result.map((r) => r.id)).toEqual(['fresh-blocked', 'stale-blocked'])
  })

  it('all keeps history', () => {
    const runs = [
      run({ id: 'stale-done', state: 'done', updated_at: nowSec - 2 * HOUR }),
      run({ id: 'running', state: 'running' }),
    ]
    const result = filterRuns(runs, 'all', now)
    expect(result.map((r) => r.id).sort()).toEqual(['running', 'stale-done'])
  })

  it('active drops a week-old no-pane review run but All keeps it in history', () => {
    const runs = [
      run({
        id: 'stale-review',
        state: 'review',
        updated_at: nowSec - 7 * 24 * HOUR,
        caps: { terminal: false, reply: true, kill: false },
      }),
      run({ id: 'running', state: 'running' }),
    ]
    expect(filterRuns(runs, 'active', now).map((r) => r.id)).toEqual(['running'])
    expect(filterRuns(runs, 'all', now).map((r) => r.id).sort()).toEqual(['running', 'stale-review'])
  })

  it('active keeps a review run with a live pane however old', () => {
    const live = run({ id: 'live-review', state: 'review', updated_at: nowSec - 7 * 24 * HOUR })
    expect(live.caps.terminal).toBe(true)
    expect(filterRuns([live], 'active', now).map((r) => r.id)).toEqual(['live-review'])
  })

  it('active sort puts fresh blocked first then newest-started first', () => {
    const runs = [
      run({ id: 'old-running', state: 'running', since: 100 }),
      run({ id: 'fresh-blocked', state: 'blocked', since: 50 }),
      run({ id: 'new-running', state: 'running', since: 200 }),
    ]
    const result = filterRuns(runs, 'active', now)
    expect(result.map((r) => r.id)).toEqual(['fresh-blocked', 'new-running', 'old-running'])
  })

  it('stuck keeps only stuck runs, fresh first then recency', () => {
    const runs = [
      run({ id: 'plain' }),
      run({ id: 'stale-stuck', attention: 'stuck', updated_at: nowSec - 2 * HOUR }),
      run({ id: 'older-stuck', attention: 'stuck', since: 10 }),
      run({ id: 'fresh-stuck', attention: 'stuck', since: 20 }),
      run({ id: 'done', attention: 'done' }),
    ]
    expect(filterRuns(runs, 'stuck', now).map((r) => r.id)).toEqual(['fresh-stuck', 'older-stuck', 'stale-stuck'])
  })

  it('done keeps only done-attention runs, fresh first then recency', () => {
    const runs = [
      run({ id: 'plain' }),
      run({ id: 'stale-done', state: 'done', attention: 'done', updated_at: nowSec - 2 * HOUR }),
      run({ id: 'fresh-done', state: 'done', attention: 'done' }),
      run({ id: 'stuck', attention: 'stuck' }),
    ]
    expect(filterRuns(runs, 'done', now).map((r) => r.id)).toEqual(['fresh-done', 'stale-done'])
  })

  it('active excludes a fresh ended run and all includes it', () => {
    const runs = [
      run({ id: 'ended', state: 'done' }),
      run({ id: 'finished', state: 'done', attention: 'done' }),
      run({ id: 'running' }),
    ]
    expect(filterRuns(runs, 'active', now).map((r) => r.id).sort()).toEqual(['finished', 'running'])
    expect(filterRuns(runs, 'all', now).map((r) => r.id).sort()).toEqual(['ended', 'finished', 'running'])
  })

  it('sorts fresh needs-you > stuck > done > rest, ties by start time', () => {
    const runs = [
      run({ id: 'rest-new', since: 60 }),
      run({ id: 'done-old', state: 'done', attention: 'done', since: 30 }),
      run({ id: 'done-new', state: 'done', attention: 'done', since: 40 }),
      run({ id: 'stuck', attention: 'stuck', since: 50 }),
      run({ id: 'blocked', state: 'blocked', since: 10 }),
      run({ id: 'rest-old', since: 20 }),
    ]
    expect(filterRuns(runs, 'active', now).map((r) => r.id)).toEqual([
      'blocked', 'stuck', 'done-new', 'done-old', 'rest-new', 'rest-old',
    ])
    expect(filterRuns(runs, 'all', now).map((r) => r.id)).toEqual([
      'blocked', 'stuck', 'done-new', 'done-old', 'rest-new', 'rest-old',
    ])
  })

  it('keeps a run in place when its updated_at changes', () => {
    const mk = (u: number) => [
      run({ id: 'a', since: 100, updated_at: nowSec }),
      run({ id: 'b', since: 200, updated_at: u }),
    ]
    expect(filterRuns(mk(nowSec - 50), 'active', now).map((r) => r.id)).toEqual(['b', 'a'])
    expect(filterRuns(mk(nowSec), 'active', now).map((r) => r.id)).toEqual(['b', 'a'])
  })

  it('moves a run when its rank changes', () => {
    const base = [run({ id: 'a', since: 100 }), run({ id: 'b', since: 200 })]
    expect(filterRuns(base, 'active', now).map((r) => r.id)).toEqual(['b', 'a'])
    const blocked = [run({ id: 'a', since: 100, state: 'blocked' }), base[1]]
    expect(filterRuns(blocked, 'active', now).map((r) => r.id)).toEqual(['a', 'b'])
  })

  it('orders equal keys deterministically by id', () => {
    const runs = [run({ id: 'z' }), run({ id: 'a' }), run({ id: 'm' })]
    expect(filterRuns(runs, 'active', now).map((r) => r.id)).toEqual(['a', 'm', 'z'])
    expect(filterRuns([...runs].reverse(), 'active', now).map((r) => r.id)).toEqual(['a', 'm', 'z'])
  })

  it('ranks a stale stuck run as rest', () => {
    const runs = [
      run({ id: 'stale-stuck', attention: 'stuck', updated_at: nowSec - 2 * HOUR }),
      run({ id: 'rest', updated_at: nowSec - 100 }),
    ]
    expect(filterRuns(runs, 'active', now).map((r) => r.id)).toEqual(['rest', 'stale-stuck'])
  })

  it('does not mutate the input array', () => {
    const runs = [
      run({ id: 'a', updated_at: nowSec - 100 }),
      run({ id: 'b', updated_at: nowSec }),
    ]
    const original = [...runs]
    filterRuns(runs, 'all', now)
    expect(runs).toEqual(original)
  })
})

describe('groupByHost', () => {
  it('maps "" to local, preserving order', () => {
    const runs = [
      run({ id: 'a', host: '' }),
      run({ id: 'b', host: 'remote-1' }),
      run({ id: 'c', host: '' }),
    ]
    const groups = groupByHost(runs)
    expect(groups.map(([host]) => host)).toEqual(['local', 'remote-1'])
    expect(groups.find(([host]) => host === 'local')?.[1].map((r) => r.id)).toEqual(['a', 'c'])
  })
})

describe('crewShortId', () => {
  it('shortens a long name', () => {
    expect(crewShortId('a-very-long-crew-name')).toBe('a-very-lon…')
  })

  it('leaves a short name untouched', () => {
    expect(crewShortId('crew-a')).toBe('crew-a')
  })
})

describe('projectOf', () => {
  it('prefers project, then repo, then unknown', () => {
    expect(projectOf(run({ project: 'p', repo: 'r' }))).toBe('p')
    expect(projectOf(run({ repo: 'r' }))).toBe('r')
    expect(projectOf(run())).toBe('unknown')
  })
})

describe('groupByProject', () => {
  it('orders projects by first appearance', () => {
    const runs = [
      run({ id: 'b1', project: 'beta' }),
      run({ id: 'a1', project: 'alpha' }),
      run({ id: 'b2', project: 'beta' }),
    ]
    const groups = groupByProject(runs)
    expect(groups.map(([p]) => p)).toEqual(['beta', 'alpha'])
    expect(groups[0][1].map((r) => r.id)).toEqual(['b1', 'b2'])
  })

  it('puts a dispatcher before its workers, keeping input order otherwise', () => {
    const runs = [
      run({ id: 'w1', project: 'p', role: 'worker' }),
      run({ id: 'solo', project: 'p' }),
      run({ id: 'd', project: 'p', role: 'dispatcher' }),
      run({ id: 'w2', project: 'p', role: 'worker' }),
    ]
    expect(groupByProject(runs)[0][1].map((r) => r.id)).toEqual(['d', 'w1', 'solo', 'w2'])
  })

  it('groups runs without a project or repo under unknown', () => {
    expect(groupByProject([run()]).map(([p]) => p)).toEqual(['unknown'])
  })
})
