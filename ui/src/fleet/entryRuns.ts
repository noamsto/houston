import type { Run } from '../api/runs'
import type { FleetEntry } from './fleetEntries'
import { isHistory } from './staleness'

/** The runs an entry puts on screen: a group's head plus its shown members. */
export function entryRuns(e: FleetEntry): Run[] {
  if (e.kind === 'run') return [e.run]
  return e.kind === 'dispatcher' ? [e.head, ...e.shown] : e.shown
}

/** "3 workers · 1 need you" for a dispatcher card, counting its live workers
 *  whatever the filter; null when none is live. */
export function dispatcherCrewLine(e: Extract<FleetEntry, { kind: 'dispatcher' }>, now: number): string | null {
  const live = e.members.filter((m) => !isHistory(m, now)).length
  if (live === 0) return null
  const count = `${live} ${live === 1 ? 'worker' : 'workers'}`
  return e.needsYou > 0 ? `${count} · ${e.needsYou} need you` : count
}
