import { describe, expect, it } from 'vitest'
import { capHistory } from './historyCap'
import type { FleetEntry } from './fleetEntries'
import type { Run } from '../api/runs'

function run(p: Partial<Run>): Run {
  return { id: 'r', agent: 'claude', state: 'done', activity: {}, tokens: { input: 0, output: 0 }, ...p } as Run
}
const entry = (p: Partial<Run>): FleetEntry => ({ kind: 'run', key: p.id ?? 'r', run: run(p) })
const ended = (n: number, prefix = 'e') => Array.from({ length: n }, (_, i) => entry({ id: `${prefix}${i}` }))
const ids = (es: FleetEntry[]) => es.map((e) => (e.kind === 'run' ? e.run.id : e.key))

describe('capHistory', () => {
  it('hides ended runs beyond the limit and counts them', () => {
    const { shown, hidden } = capHistory(ended(40), 30, false)
    expect(shown.length).toBe(30)
    expect(hidden).toBe(10)
    expect(ids(shown)).toEqual(ids(ended(30)))
  })

  it('reveals everything when expanded', () => {
    const { shown, hidden } = capHistory(ended(40), 30, true)
    expect(shown.length).toBe(40)
    expect(hidden).toBe(0)
  })

  it('never caps live, needs-you, stuck or done-attention runs, and keeps order', () => {
    const keep = [
      entry({ id: 'live', state: 'running' }),
      entry({ id: 'need', state: 'blocked' }),
      entry({ id: 'stuck', state: 'failed', attention: 'stuck' }),
      entry({ id: 'crewdone', state: 'done', attention: 'done' }),
    ]
    const list = [keep[0], ...ended(3), keep[1], keep[2], ...ended(3, 'x'), keep[3]]
    const { shown, hidden } = capHistory(list, 2, false)
    expect(hidden).toBe(4)
    expect(ids(shown)).toEqual(['live', 'e0', 'e1', 'need', 'stuck', 'crewdone'])
  })

  it('never caps dispatcher or crew groups', () => {
    const group = { kind: 'crew', key: 'g', crew: 'c', host: 'local', shown: [], members: [], needsYou: 0, counts: {} } as unknown as FleetEntry
    const { shown, hidden } = capHistory([...ended(3), group], 1, false)
    expect(hidden).toBe(2)
    expect(ids(shown)).toEqual(['e0', 'g'])
  })

  it('keeps the selected run even past the limit', () => {
    const { shown, hidden } = capHistory(ended(5), 2, false, 'e4')
    expect(ids(shown)).toEqual(['e0', 'e1', 'e4'])
    expect(hidden).toBe(2)
  })
})
