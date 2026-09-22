import { describe, expect, it } from 'vitest'
import type { Run } from '../api/runs'
import { crewShortId, crewSummary, filterRuns, groupByHost, groupByProject, projectOf } from './fleetList'

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

  it('active sort puts fresh blocked first then updated_at desc', () => {
    const runs = [
      run({ id: 'old-running', state: 'running', updated_at: nowSec - 100 }),
      run({ id: 'fresh-blocked', state: 'blocked', updated_at: nowSec - 200 }),
      run({ id: 'new-running', state: 'running', updated_at: nowSec }),
    ]
    const result = filterRuns(runs, 'active', now)
    expect(result.map((r) => r.id)).toEqual(['fresh-blocked', 'new-running', 'old-running'])
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

describe('crewSummary', () => {
  const disp = run({ id: 'd', project: 'p', role: 'dispatcher' })
  const worker = (id: string, p: Partial<Run> = {}) => run({ id, project: 'p', role: 'worker', ...p })

  it('counts workers and omits the blocked part at zero', () => {
    expect(crewSummary(disp, [disp, worker('w1'), worker('w2'), worker('w3')], now)).toBe('3 workers')
  })

  it('singularises one worker', () => {
    expect(crewSummary(disp, [disp, worker('w1')], now)).toBe('1 worker')
  })

  it('appends the blocked count using needs-you rules', () => {
    const runs = [
      disp,
      worker('w1', { state: 'blocked' }),
      worker('w2', { state: 'blocked', updated_at: nowSec - 2 * HOUR }),
      worker('w3'),
    ]
    expect(crewSummary(disp, runs, now)).toBe('3 workers · 1 blocked')
  })

  it('excludes history workers', () => {
    const runs = [disp, worker('w1'), worker('old', { state: 'done', updated_at: nowSec - 2 * HOUR })]
    expect(crewSummary(disp, runs, now)).toBe('1 worker')
  })

  it('excludes workers of another project or host', () => {
    const runs = [
      disp,
      worker('other-project', { project: 'q' }),
      worker('other-host', { host: 'box' }),
    ]
    expect(crewSummary(disp, runs, now)).toBeNull()
  })

  it('returns null with no workers', () => {
    expect(crewSummary(disp, [disp, run({ id: 'solo', project: 'p' })], now)).toBeNull()
  })

  it('returns null when two dispatchers share the project and host', () => {
    const other = run({ id: 'd2', project: 'p', role: 'dispatcher' })
    expect(crewSummary(disp, [disp, other, worker('w1')], now)).toBeNull()
  })

  it('ignores a finished dispatcher when deciding whether the workers are ambiguous', () => {
    const dead = run({ id: 'd-old', project: 'p', role: 'dispatcher', state: 'done', updated_at: nowSec - 2 * HOUR })
    expect(crewSummary(disp, [disp, dead, worker('w1')], now)).toBe('1 worker')
  })

  it('still summarises when the second dispatcher is on another host', () => {
    const other = run({ id: 'd2', project: 'p', role: 'dispatcher', host: 'box' })
    expect(crewSummary(disp, [disp, other, worker('w1')], now)).toBe('1 worker')
  })
})
