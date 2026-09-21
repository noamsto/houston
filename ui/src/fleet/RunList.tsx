import type { Run } from '../api/runs'
import { needsYou } from './staleness'
import { crewSummary, groupByProject } from './fleetList'
import { RunCard } from './RunCard'

interface RunListProps {
  groups: [string, Run[]][]
  /** Unfiltered: a dispatcher's crew summary must not shrink with the view. */
  allRuns: Run[]
  now: number
  onOpen?: (r: Run) => void
  selectedId?: string
  groupByProject: boolean
}

export function RunList({ groups, allRuns, now, onOpen, selectedId, groupByProject: grouped }: RunListProps) {
  const card = (r: Run) => (
    <RunCard
      key={r.id}
      run={r}
      now={now}
      onOpen={onOpen}
      selected={selectedId === r.id}
      crewLine={r.role === 'dispatcher' ? crewSummary(r, allRuns, now) : null}
    />
  )

  return (
    <>
      {groups.map(([host, list]) => (
        <section key={host}>
          <div className="fleet-group">
            <span className={host === 'local' ? 'host-local' : 'host-remote'}>{host}</span>
            <span>{list.length}</span>
          </div>
          {grouped
            ? groupByProject(list).map(([project, runs]) => {
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
                    {runs.map(card)}
                  </div>
                )
              })
            : list.map(card)}
        </section>
      ))}
    </>
  )
}
