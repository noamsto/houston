import { describe, expect, it } from 'vitest'
import type { Run } from '../api/runs'
import { bucket, countsLabel, dispatchHref, type CrewCounts } from './crewsModel'

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

describe('bucket', () => {
  it('counts fresh flags by attention, idle and ended runs apart, and stale flags as ended', () => {
    const cases: [Partial<Run>, keyof CrewCounts][] = [
      [{ state: 'blocked', attention: 'needs-you' }, 'needsYou'],
      [{ state: 'running', attention: 'stuck' }, 'stuck'],
      [{ state: 'idle', attention: 'done' }, 'done'],
      [{ state: 'idle' }, 'idle'],
      [{ state: 'done' }, 'ended'],
      [{ state: 'blocked', attention: 'needs-you', updated_at: old }, 'ended'],
      [{ state: 'running', updated_at: old }, 'working'],
      [{ state: 'running' }, 'working'],
    ]
    for (const [p, expected] of cases) expect(bucket(run(p), now)).toBe(expected)
  })
})

describe('countsLabel', () => {
  it('joins non-zero buckets in a fixed order', () => {
    expect(countsLabel({ working: 2, needsYou: 1, stuck: 1, done: 1, idle: 0, ended: 0 }))
      .toBe('2 working · 1 needs you · 1 stuck · 1 done')
  })

  it('is empty when there is nothing to count', () => {
    expect(countsLabel({ working: 0, needsYou: 0, stuck: 0, done: 0, idle: 0, ended: 0 })).toBe('')
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
