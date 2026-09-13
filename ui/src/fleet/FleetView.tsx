import { useMemo, useState } from 'react'
import type { Run } from '../api/runs'
import { needsYou } from './staleness'
import { filterRuns, groupByHost, type Filter } from './fleetList'
import { RunCard } from './RunCard'
import './fleet.css'

interface FleetViewProps {
  runs: Run[]
  connected: boolean
  now: number
  onOpen?: (r: Run) => void
}

export function FleetView({ runs, connected, now, onOpen }: FleetViewProps) {
  const [filter, setFilter] = useState<Filter>('active')

  const attentionCount = useMemo(
    () => runs.filter((r) => needsYou(r, now)).length,
    [runs, now],
  )

  const visible = useMemo(() => filterRuns(runs, filter, now), [runs, filter, now])

  const groups = useMemo(() => groupByHost(visible), [visible])

  return (
    <div className="fleet mocha">
      <header className="fleet-nav">
        <h1>Fleet</h1>
        <div className="fleet-nav-actions">
          <button
            type="button"
            className="fleet-classic"
            aria-label="Switch to the classic view"
            onClick={() => { window.location.hash = '#/agents' }}
          >
            Classic
          </button>
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
        <button aria-pressed={filter === 'all'} className={filter === 'all' ? 'on' : ''} onClick={() => setFilter('all')}>All</button>
      </nav>

      {visible.length === 0 && (
        <div className="fleet-empty">
          {connected ? 'No runs match this filter.' : 'Connecting to the run stream…'}
        </div>
      )}

      {groups.map(([host, list]) => (
        <section key={host}>
          <div className="fleet-group">
            <span className={host === 'local' ? 'host-local' : 'host-remote'}>{host}</span>
            <span>{list.length}</span>
          </div>
          {list.map((r) => (
            <RunCard key={r.id} run={r} now={now} onOpen={onOpen} />
          ))}
        </section>
      ))}
    </div>
  )
}
