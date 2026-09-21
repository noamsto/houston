import { describe, expect, it } from 'vitest'
import type { Run } from '../api/runs'
import { countsLabel, dispatchHref, groupCrews } from './crewsModel'

const now = 1_800_000_000_000 // ms
const nowSec = now / 1000

function run(p: Partial<Run> = {}): Run {
  return {
    id: 'r', agent: 'claude', state: 'running',
    activity: {}, tokens: { input: 0, output: 0 },
    caps: { terminal: true, reply: true, kill: true },
    updated_at: nowSec,
    ...p,
  } as Run
}

const old = nowSec - 10 * 3600

describe('groupCrews', () => {
  it('excludes runs with no crew or an empty crew name', () => {
    const { live, finished } = groupCrews([run({ id: 'a' }), run({ id: 'b', crew: { name: '' } })], now)
    expect(live).toEqual([])
    expect(finished).toEqual([])
  })

  it('splits live from finished crews', () => {
    const runs = [
      run({ id: 'a', crew: { name: 'crew-live' }, state: 'running' }),
      run({ id: 'b', crew: { name: 'crew-done' }, state: 'done', updated_at: old }),
    ]
    const { live, finished } = groupCrews(runs, now)
    expect(live.map((g) => g.name)).toEqual(['crew-live'])
    expect(finished.map((g) => g.name)).toEqual(['crew-done'])
  })

  it('does not let a stale blocked member keep its crew live', () => {
    const runs = [run({ id: 'a', crew: { name: 'c' }, state: 'blocked', updated_at: old })]
    const { live, finished } = groupCrews(runs, now)
    expect(live).toEqual([])
    expect(finished[0].needsYou).toBe(0)
  })

  it('ranks a running member with a down control connection below a healthy one', () => {
    const runs = [
      run({ id: 'down', crew: { name: 'c' }, state: 'running', stale: true, updated_at: nowSec }),
      run({ id: 'up', crew: { name: 'c' }, state: 'running', updated_at: nowSec - 30 }),
    ]
    expect(groupCrews(runs, now).live[0].members.map((m) => m.id)).toEqual(['up', 'down'])
  })

  it('does not let a ghost running member that stopped reporting keep its crew live', () => {
    const runs = [run({ id: 'a', crew: { name: 'c' }, state: 'running', updated_at: old })]
    const { live, finished } = groupCrews(runs, now)
    expect(live).toEqual([])
    expect(finished.map((g) => g.name)).toEqual(['c'])
  })

  it('counts a freshly reporting running member as live', () => {
    const runs = [run({ id: 'a', crew: { name: 'c' }, state: 'running', updated_at: nowSec - 30 })]
    expect(groupCrews(runs, now).live.map((g) => g.name)).toEqual(['c'])
  })

  it('treats a recently updated finished crew as live (updated_at is seconds, now is ms)', () => {
    const runs = [run({ id: 'a', crew: { name: 'c' }, state: 'done', updated_at: nowSec - 60 })]
    expect(groupCrews(runs, now).live.map((g) => g.name)).toEqual(['c'])
  })

  it('orders members needsYou first, then live, then the rest, each by recency', () => {
    const runs = [
      run({ id: 'done-new', crew: { name: 'c' }, state: 'done', updated_at: nowSec - 5 }),
      run({ id: 'run-old', crew: { name: 'c' }, state: 'running', updated_at: nowSec - 100 }),
      run({ id: 'run-new', crew: { name: 'c' }, state: 'running', updated_at: nowSec - 10 }),
      run({ id: 'blocked', crew: { name: 'c' }, state: 'blocked', updated_at: nowSec - 200 }),
    ]
    const [g] = groupCrews(runs, now).live
    expect(g.members.map((m) => m.id)).toEqual(['blocked', 'run-new', 'run-old', 'done-new'])
  })

  it('counts states into buckets and tracks needsYou and lastActive', () => {
    const runs = [
      run({ id: 'a', crew: { name: 'c' }, state: 'running' }),
      run({ id: 'b', crew: { name: 'c' }, state: 'thinking' }),
      run({ id: 'c', crew: { name: 'c' }, state: 'blocked', updated_at: nowSec - 30 }),
      run({ id: 'd', crew: { name: 'c' }, state: 'idle', updated_at: old }),
    ]
    const [g] = groupCrews(runs, now).live
    expect(g.counts).toEqual({ blocked: 1, running: 2, review: 0, done: 0, failed: 0, other: 1 })
    expect(g.needsYou).toBe(1)
    expect(g.lastActive).toBe(nowSec)
  })

  it('sorts live crews needing you first, then by lastActive; finished by lastActive', () => {
    const runs = [
      run({ id: 'a', crew: { name: 'older' }, state: 'running', updated_at: nowSec - 500 }),
      run({ id: 'b', crew: { name: 'newer' }, state: 'running', updated_at: nowSec - 5 }),
      run({ id: 'c', crew: { name: 'blocked' }, state: 'blocked', updated_at: nowSec - 900 }),
      run({ id: 'd', crew: { name: 'fin-old' }, state: 'done', updated_at: old - 100 }),
      run({ id: 'e', crew: { name: 'fin-new' }, state: 'done', updated_at: old }),
    ]
    const { live, finished } = groupCrews(runs, now)
    expect(live.map((g) => g.name)).toEqual(['blocked', 'newer', 'older'])
    expect(finished.map((g) => g.name)).toEqual(['fin-new', 'fin-old'])
  })
})

describe('countsLabel', () => {
  it('joins non-zero buckets in a fixed order', () => {
    expect(countsLabel({ blocked: 1, running: 2, review: 0, done: 1, failed: 0, other: 0 }))
      .toBe('1 blocked · 2 running · 1 done')
  })

  it('is empty when there is nothing to count', () => {
    expect(countsLabel({ blocked: 0, running: 0, review: 0, done: 0, failed: 0, other: 0 })).toBe('')
  })
})

describe('dispatchHref', () => {
  it('includes the encoded repo when known', () => {
    expect(dispatchHref('/home/me/my repo', '1-2')).toBe('#/dispatch?repo=%2Fhome%2Fme%2Fmy%20repo&crew=1-2')
  })

  it('omits the repo when unknown', () => {
    expect(dispatchHref(undefined, 'a/b')).toBe('#/dispatch?crew=a%2Fb')
  })
})
