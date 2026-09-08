import { useMemo, useState } from 'react'
import type { Run } from '../api/runs'
import { isFresh, isHistory, needsYou } from './staleness'
import { RunCard } from './RunCard'
import './fleet.css'

type Filter = 'active' | 'needs-you' | 'all'

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

  const visible = useMemo(() => {
    // Needs-you shows every blocked run, not just fresh ones — the badge
    // and the sort answer "how many need me now", this filter answers
    // "what asked for me at all".
    const keep = runs.filter((r) => {
      if (filter === 'needs-you') return r.state === 'blocked'
      if (filter === 'active') return !isHistory(r, now)
      return true
    })
    return keep.sort((a, b) => {
      if (filter === 'needs-you') {
        const af = isFresh(a, now) ? 1 : 0
        const bf = isFresh(b, now) ? 1 : 0
        if (af !== bf) return bf - af
        return b.updated_at - a.updated_at
      }
      // Anything asking for input first, then most recently active. Stale
      // blocked runs deliberately stay out of this bucket — see RunCard's
      // muted attention border for how they stay findable in place instead.
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
