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
