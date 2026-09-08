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
