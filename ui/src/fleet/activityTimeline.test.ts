import { describe, expect, it } from 'vitest'
import { collapseTrail } from './activityTimeline'

describe('collapseTrail', () => {
  it('returns no rows for an empty or missing trail', () => {
    expect(collapseTrail([])).toEqual([])
    expect(collapseTrail(undefined)).toEqual([])
  })

  it('preserves order and leaves distinct tools alone', () => {
    const rows = collapseTrail([
      { tool: 'read', hint: 'a', done: true },
      { tool: 'edit', hint: 'b', done: true },
      { tool: 'read', hint: 'c', done: false },
    ])
    expect(rows.map((r) => [r.tool, r.count])).toEqual([['read', 1], ['edit', 1], ['read', 1]])
  })

  it('collapses consecutive repeats, keeping the newest hint and done state', () => {
    const rows = collapseTrail([
      { tool: 'read', hint: 'a', done: true },
      { tool: 'read', hint: 'b', done: true },
      { tool: 'read', hint: 'c', done: false },
    ])
    expect(rows).toEqual([{ tool: 'read', hint: 'c', done: false, error: false, count: 3 }])
  })

  it('does not merge an error into non-errors of the same tool', () => {
    const rows = collapseTrail([
      { tool: 'bash', hint: 'ok', done: true },
      { tool: 'bash', hint: 'boom', done: true, error: true },
      { tool: 'bash', hint: 'ok2', done: true },
    ])
    expect(rows.map((r) => [r.error, r.count])).toEqual([[false, 1], [true, 1], [false, 1]])
  })

  it('merges consecutive errors of the same tool', () => {
    const rows = collapseTrail([
      { tool: 'bash', done: true, error: true },
      { tool: 'bash', done: true, error: true },
    ])
    expect(rows).toEqual([{ tool: 'bash', hint: undefined, done: true, error: true, count: 2 }])
  })
})
