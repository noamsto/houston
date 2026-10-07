import { describe, expect, it } from 'vitest'
import { FRESH_MS, isDone, isEnded, isFresh, isHistory, isStuck, needsYou } from './staleness'
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
  it('accepts a bare {state, updated_at} such as a Workspace pane', () => {
    expect(needsYou({ state: 'blocked', updated_at: agoSec(30 * 60_000) }, now)).toBe(true)
    expect(needsYou({ state: 'blocked', updated_at: agoSec(2 * 60 * 60_000) }, now)).toBe(false)
  })

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

  it('treats a fresh ended run as history so it leaves Active at once', () => {
    expect(isHistory(run({ state: 'done', updated_at: agoSec(1000) }), now)).toBe(true)
  })

  it('keeps a fresh run that finished its work but is awaiting review out of history', () => {
    expect(isHistory(run({ state: 'done', attention: 'done', updated_at: agoSec(1000) }), now)).toBe(false)
    expect(isHistory(run({ state: 'idle', attention: 'done', updated_at: agoSec(1000) }), now)).toBe(false)
  })

  it('keeps a fresh stuck run on a dead worker out of history', () => {
    expect(isHistory(run({ state: 'done', attention: 'stuck', updated_at: agoSec(1000) }), now)).toBe(false)
  })

  it('ages a done-attention run into history once stale', () => {
    expect(isHistory(run({ state: 'done', attention: 'done', updated_at: agoSec(2 * 60 * 60_000) }), now)).toBe(true)
  })

  it('never treats a running or thinking run as history', () => {
    for (const state of ['running', 'thinking', 'compacting'] as const) {
      const old = run({ state, updated_at: agoSec(FRESH_MS * 5) })
      expect(isHistory(old, now)).toBe(false)
    }
  })

  it('treats a week-old review run with no live session as history', () => {
    const old = run({ state: 'review', updated_at: agoSec(7 * 24 * 60 * 60_000) })
    expect(old.caps.terminal).toBe(false)
    expect(isHistory(old, now)).toBe(true)
  })

  it('keeps a review run with a live pane out of history however old', () => {
    const old = run({
      state: 'review',
      updated_at: agoSec(7 * 24 * 60 * 60_000),
      caps: { terminal: true, reply: true, kill: true },
    })
    expect(isHistory(old, now)).toBe(false)
  })

  it('keeps a just-ended review run out of history so it does not vanish mid-glance', () => {
    const recent = run({ state: 'review', updated_at: agoSec(2 * 60 * 60_000) })
    expect(isHistory(recent, now)).toBe(false)
  })

  it('does not treat a malformed review run with no timestamp as history', () => {
    expect(isHistory(run({ state: 'review', updated_at: 0 }), now)).toBe(false)
  })

  it('reports freshness from updated_at', () => {
    expect(isFresh(run({ updated_at: agoSec(1000) }), now)).toBe(true)
    expect(isFresh(run({ updated_at: agoSec(FRESH_MS + 1) }), now)).toBe(false)
  })

  it('isEnded marks a done run without done-attention', () => {
    expect(isEnded(run({ state: 'done' }))).toBe(true)
    expect(isEnded(run({ state: 'done', attention: 'done' }))).toBe(false)
    expect(isEnded(run({ state: 'done', attention: 'stuck' }))).toBe(false)
    expect(isEnded(run({ state: 'idle' }))).toBe(false)
  })

  it('isStuck and isDone respect freshness', () => {
    const stale = agoSec(FRESH_MS + 60_000)
    expect(isStuck(run({ attention: 'stuck' }), now)).toBe(true)
    expect(isStuck(run({ attention: 'stuck', updated_at: stale }), now)).toBe(false)
    expect(isStuck(run({ attention: 'done' }), now)).toBe(false)
    expect(isDone(run({ attention: 'done' }), now)).toBe(true)
    expect(isDone(run({ attention: 'done', updated_at: stale }), now)).toBe(false)
    expect(isDone(run({ attention: 'stuck' }), now)).toBe(false)
  })

  it('needsYou ignores stuck and done attention', () => {
    expect(needsYou(run({ state: 'idle', attention: 'stuck' }), now)).toBe(false)
    expect(needsYou(run({ state: 'idle', attention: 'done' }), now)).toBe(false)
  })
})
