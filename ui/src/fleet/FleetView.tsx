import { useEffect, useMemo, useState } from 'react'
import { useRuns } from '../hooks/useRuns'
import type { Run } from '../api/runs'
import { isHistory, needsYou } from './staleness'
import { RunCard } from './RunCard'
import './fleet.css'

type Filter = 'active' | 'needs-you' | 'all'

/** Ticks once a minute so relative ages and freshness stay honest. */
function useNow(): number {
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    const id = window.setInterval(() => setNow(Date.now()), 60_000)
    return () => window.clearInterval(id)
  }, [])
  return now
}

export function FleetView({ onOpen }: { onOpen?: (r: Run) => void }) {
  const { runs, connected } = useRuns()
  const now = useNow()
  const [filter, setFilter] = useState<Filter>('active')

  const attentionCount = useMemo(
    () => runs.filter((r) => needsYou(r, now)).length,
    [runs, now],
  )

  const visible = useMemo(() => {
    const keep = runs.filter((r) => {
      if (filter === 'needs-you') return needsYou(r, now)
      if (filter === 'active') return !isHistory(r, now)
      return true
    })
    // Anything asking for input first, then most recently active.
    return keep.sort((a, b) => {
      const an = needsYou(a, now) ? 1 : 0
      const bn = needsYou(b, now) ? 1 : 0
      if (an !== bn) return bn - an
      return b.updated_at - a.updated_at
    })
  }, [runs, filter, now])

  const groups = useMemo(() => {
    const byHost = new Map<string, Run[]>()
    for (const r of visible) {
      const host = r.host || 'local'
      const list = byHost.get(host)
      if (list) list.push(r)
      else byHost.set(host, [r])
    }
    return Array.from(byHost.entries())
  }, [visible])

  return (
    <div className="fleet mocha">
      <header className="fleet-nav">
        <h1>Fleet</h1>
        <span className={`fleet-badge${attentionCount === 0 ? ' quiet' : ''}`}>
          {attentionCount > 0 ? `${attentionCount} needs you` : connected ? 'all quiet' : 'offline'}
        </span>
      </header>

      <nav className="fleet-filters" aria-label="filter runs">
        <button className={filter === 'active' ? 'on' : ''} onClick={() => setFilter('active')}>Active</button>
        <button className={filter === 'needs-you' ? 'on' : ''} onClick={() => setFilter('needs-you')}>Needs you</button>
        <button className={filter === 'all' ? 'on' : ''} onClick={() => setFilter('all')}>All</button>
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
