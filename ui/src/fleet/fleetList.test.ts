import { describe, expect, it } from 'vitest'
import type { Run } from '../api/runs'
import { crewShortId, filterRuns, groupByHost } from './fleetList'

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
