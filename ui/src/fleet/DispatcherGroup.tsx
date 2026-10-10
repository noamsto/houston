import type { Run } from '../api/runs'
import { CrewGroupHeader } from './CrewGroupHeader'
import { dispatcherCrewLine } from './entryRuns'
import type { FleetEntry } from './fleetEntries'
import { RunCard } from './RunCard'
import { MemberRows } from './WorkerRow'

interface GroupProps<K extends FleetEntry['kind']> {
  entry: Extract<FleetEntry, { kind: K }>
  now: number
  onOpen?: (r: Run) => void
  selectedId?: string
}

// The head card is a <button>, so the member rows are its siblings inside the
// container, never its children.
export function DispatcherGroup({ entry, now, onOpen, selectedId }: GroupProps<'dispatcher'>) {
  return (
    <div className={`fleet-group-card${entry.needsYou > 0 ? ' needs-you' : ''}`}>
      <RunCard
        run={entry.head}
        now={now}
        onOpen={onOpen}
        selected={selectedId === entry.head.id}
        crewLine={dispatcherCrewLine(entry, now)}
      />
      {entry.needsYou > 0 && <span className="fleet-group-badge">{entry.needsYou} need you</span>}
      <MemberRows shown={entry.shown} now={now} onOpen={onOpen} selectedId={selectedId} />
    </div>
  )
}

export function CrewGroup({ entry, now, onOpen, selectedId }: GroupProps<'crew'>) {
  return (
    <div className={`fleet-group-card crew${entry.needsYou > 0 ? ' needs-you' : ''}`}>
      <CrewGroupHeader entry={entry} />
      <MemberRows shown={entry.shown} now={now} onOpen={onOpen} selectedId={selectedId} />
    </div>
  )
}
