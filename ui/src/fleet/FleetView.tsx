import { useMemo, useState } from 'react'
import type { Run } from '../api/runs'
import type { Mode } from '../api/mode'
import { needsYou } from './staleness'
import { fleetEntries, groupEntriesByHost } from './fleetEntries'
import type { Filter } from './fleetList'
import { RunList } from './RunList'
import { useGroupByProject } from '../hooks/useLayout'
import './fleet.css'

interface FleetViewProps {
  runs: Run[]
  connected: boolean
  now: number
  mode: Mode | null
  onOpen?: (r: Run) => void
}

export function FleetView({ runs, connected, now, mode, onOpen }: FleetViewProps) {
  const [filter, setFilter] = useState<Filter>('active')
  const [grouped, setGrouped] = useGroupByProject()

  const attentionCount = useMemo(
    () => runs.filter((r) => needsYou(r, now)).length,
    [runs, now],
  )

  const entries = useMemo(() => fleetEntries(runs, filter, now, mode), [runs, filter, now, mode])

  const groups = useMemo(() => groupEntriesByHost(entries), [entries])

  return (
    <div className="fleet mocha">
      <header className="fleet-nav">
        <h1>Fleet</h1>
        <div className="fleet-nav-actions">
          <button
            type="button"
            className={`fleet-badge${attentionCount === 0 ? ' quiet' : ''}`}
            onClick={() => setFilter('needs-you')}
          >
            {attentionCount > 0 ? `${attentionCount} needs you` : connected ? 'all quiet' : 'offline'}
          </button>
        </div>
      </header>

      <nav className="fleet-filters" aria-label="filter runs">
        <button aria-pressed={filter === 'active'} className={filter === 'active' ? 'on' : ''} onClick={() => setFilter('active')}>Active</button>
        <button aria-pressed={filter === 'needs-you'} className={filter === 'needs-you' ? 'on' : ''} onClick={() => setFilter('needs-you')}>Needs you</button>
        <button aria-pressed={filter === 'stuck'} className={filter === 'stuck' ? 'on' : ''} onClick={() => setFilter('stuck')}>Stuck</button>
        <button aria-pressed={filter === 'done'} className={filter === 'done' ? 'on' : ''} onClick={() => setFilter('done')}>Done</button>
        <button aria-pressed={filter === 'all'} className={filter === 'all' ? 'on' : ''} onClick={() => setFilter('all')}>All</button>
      </nav>

      <div className="fleet-group-row">
        <button
          type="button"
          className={`fleet-group-toggle${grouped ? ' on' : ''}`}
          aria-pressed={grouped}
          onClick={() => setGrouped(!grouped)}
        >
          Group by project
        </button>
      </div>

      {entries.length === 0 && (
        <div className="fleet-empty">
          {connected ? 'No runs match this filter.' : 'Connecting to the run stream…'}
        </div>
      )}

      <RunList groups={groups} now={now} onOpen={onOpen} groupByProject={grouped} />
    </div>
  )
}
