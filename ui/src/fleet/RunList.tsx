import { needsYou } from './staleness'
import { entryRuns } from './entryRuns'
import { groupEntriesByProject, type FleetEntry } from './fleetEntries'
import { CrewGroup, DispatcherGroup } from './DispatcherGroup'
import { RunCard } from './RunCard'
import type { Run } from '../api/runs'

interface RunListProps {
  groups: [string, FleetEntry[]][]
  now: number
  onOpen?: (r: Run) => void
  selectedId?: string
  groupByProject: boolean
}

// Header counts are runs on screen: a group counts its head plus the members
// it shows, so a filtered-out worker is not counted.
function runsIn(entries: FleetEntry[]): Run[] {
  return entries.flatMap(entryRuns)
}

export function RunList({ groups, now, onOpen, selectedId, groupByProject: grouped }: RunListProps) {
  const entry = (e: FleetEntry) => {
    if (e.kind === 'dispatcher') return <DispatcherGroup key={e.key} entry={e} now={now} onOpen={onOpen} selectedId={selectedId} />
    if (e.kind === 'crew') return <CrewGroup key={e.key} entry={e} now={now} onOpen={onOpen} selectedId={selectedId} />
    return <RunCard key={e.key} run={e.run} now={now} onOpen={onOpen} selected={selectedId === e.run.id} />
  }

  return (
    <>
      {groups.map(([host, list]) => (
        <section key={host}>
          <div className="fleet-group">
            <span className={host === 'local' ? 'host-local' : 'host-remote'}>{host}</span>
            <span>{runsIn(list).length}</span>
          </div>
          {grouped
            ? groupEntriesByProject(list).map(([project, entries]) => {
                const runs = runsIn(entries)
                const attention = runs.filter((r) => needsYou(r, now)).length
                return (
                  <div key={project} className="fleet-project-section">
                    <div className="fleet-project-group">
                      <span className="fleet-project-name">{project}</span>
                      <span className="fleet-project-count">
                        <span>{runs.length}</span>
                        {attention > 0 && <span className="fleet-project-attention">{attention} need you</span>}
                      </span>
                    </div>
                    {entries.map(entry)}
                  </div>
                )
              })
            : list.map(entry)}
        </section>
      ))}
    </>
  )
}
