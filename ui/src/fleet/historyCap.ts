import type { FleetEntry } from './fleetEntries'
import { isEnded } from './staleness'

/** Ended runs a section lists before "Show more". */
export const HISTORY_PAGE = 30

/**
 * Hides top-level ended runs past the first `limit`. Live, needs-you, stuck
 * and done-attention runs and dispatcher/crew groups are never hidden, nor is
 * the run with id `keepId` (the open detail).
 */
export function capHistory(
  entries: FleetEntry[],
  limit: number,
  expanded: boolean,
  keepId?: string,
): { shown: FleetEntry[]; hidden: number } {
  if (expanded) return { shown: entries, hidden: 0 }
  let seen = 0
  let hidden = 0
  const shown = entries.filter((e) => {
    if (e.kind !== 'run' || !isEnded(e.run)) return true
    if (++seen <= limit || e.run.id === keepId) return true
    hidden++
    return false
  })
  return { shown, hidden }
}
