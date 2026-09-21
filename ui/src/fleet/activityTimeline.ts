import type { TrailChip } from '../api/runs'

export interface TrailRow {
  tool: string
  hint?: string
  done: boolean
  error: boolean
  count: number
}

/**
 * Merges consecutive chips of the same tool into one row. An error never
 * merges with a non-error, so a failure stays visible between its neighbours.
 * The row keeps the hint and done state of its newest chip.
 */
export function collapseTrail(trail?: TrailChip[]): TrailRow[] {
  const rows: TrailRow[] = []
  for (const t of trail ?? []) {
    const error = Boolean(t.error)
    const last = rows[rows.length - 1]
    if (last && last.tool === t.tool && last.error === error) {
      last.hint = t.hint
      last.done = t.done
      last.count++
    } else {
      rows.push({ tool: t.tool, hint: t.hint, done: t.done, error, count: 1 })
    }
  }
  return rows
}
