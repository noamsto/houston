import { useMemo, useState } from 'react'
import type { Run } from '../api/runs'
import { needsYou } from './staleness'
import { filterRuns, groupByHost, crewShortId, type Filter } from './fleetList'
import { RunCard } from './RunCard'
import { CrewsView } from './CrewsView'
import { WorkspaceView } from './WorkspaceView'
import { RunDetail } from './RunDetail'
import { runHash, useDetailRoute } from './routes'
import { DISPATCH_PLACEHOLDER } from './copy'
import type { ShellData } from './MobileShell'
import '../theme/mocha.css'
import './fleet.css'

type Section = 'fleet' | 'crews' | 'workspace' | 'dispatch'

const FILTER_LABEL: Record<Filter, string> = {
  active: 'Active',
  'needs-you': 'Needs you',
  all: 'All',
}

export function ConsoleShell({ runs, connected, hasSnapshot, now }: ShellData) {
  const [section, setSection] = useState<Section>('fleet')
  const [filter, setFilter] = useState<Filter>('active')
  const [crew, setCrew] = useState<string | null>(null)
  const route = useDetailRoute()

  const attentionCount = useMemo(
    () => runs.filter((r) => needsYou(r, now)).length,
    [runs, now],
  )

  const counts = useMemo<Record<Filter, number>>(
    () => ({
      active: filterRuns(runs, 'active', now).length,
      'needs-you': filterRuns(runs, 'needs-you', now).length,
      all: filterRuns(runs, 'all', now).length,
    }),
    [runs, now],
  )

  const hosts = useMemo<[string, number][]>(
    () => groupByHost(runs).map(([host, list]) => [host, list.length]),
    [runs],
  )

  const crews = useMemo(() => {
    const byCrew = new Map<string, Run[]>()
    for (const r of runs) {
      const name = r.crew?.name
      if (!name) continue
      const list = byCrew.get(name)
      if (list) list.push(r)
      else byCrew.set(name, [r])
    }
    const entries = Array.from(byCrew.entries()).map(([name, members]) => ({
      name,
      members: members.length,
      blocked: members.filter((m) => needsYou(m, now)).length,
    }))
    entries.sort((a, b) => {
      const ab = a.blocked > 0 ? 1 : 0
      const bb = b.blocked > 0 ? 1 : 0
      if (ab !== bb) return bb - ab
      return a.name.localeCompare(b.name)
    })
    return entries
  }, [runs, now])

  const base = useMemo(() => filterRuns(runs, filter, now), [runs, filter, now])
  const visible = useMemo(
    () => (crew ? base.filter((r) => r.crew?.name === crew) : base),
    [base, crew],
  )
  const groups = useMemo(() => groupByHost(visible), [visible])

  function chooseFilter(f: Filter): void {
    setSection('fleet')
    setFilter(f)
    // Needs-you must show every blocked run, fresh or stale — a lingering
    // crew filter would silently hide the ones that live in another crew.
    if (f === 'needs-you') setCrew(null)
  }

  function toggleCrew(name: string): void {
    setSection('fleet')
    setCrew((c) => (c === name ? null : name))
  }

  function open(r: Run): void {
    window.location.hash = runHash(r.id)
  }

  return (
    <div className="console mocha" aria-label="console">
      <nav className="console-rail" aria-label="rail">
        <div className="console-rail-head">
          <span className="console-rail-title">houston</span>
          <button
            type="button"
            className={`fleet-badge${attentionCount === 0 ? ' quiet' : ''}`}
            onClick={() => chooseFilter('needs-you')}
          >
            {attentionCount > 0 ? `${attentionCount} needs you` : connected ? 'all quiet' : 'offline'}
          </button>
        </div>

        <div>
          <h2>Fleet</h2>
          <button
            type="button"
            className="console-rail-item"
            aria-pressed={section === 'fleet' && filter === 'active'}
            onClick={() => chooseFilter('active')}
          >
            Active<span className="console-count">{counts.active}</span>
          </button>
          <button
            type="button"
            className="console-rail-item"
            aria-pressed={section === 'fleet' && filter === 'needs-you'}
            onClick={() => chooseFilter('needs-you')}
          >
            Needs you<span className="console-count">{counts['needs-you']}</span>
          </button>
          <button
            type="button"
            className="console-rail-item"
            aria-pressed={section === 'fleet' && filter === 'all'}
            onClick={() => chooseFilter('all')}
          >
            All<span className="console-count">{counts.all}</span>
          </button>
        </div>

        <div>
          <h2>Hosts</h2>
          {hosts.map(([host, count]) => (
            <div key={host} className="console-rail-item static">
              <span className={host === 'local' ? 'host-local' : 'host-remote'}>{host}</span>
              <span className="console-count">{count}</span>
            </div>
          ))}
        </div>

        {crews.length > 0 && (
          <div>
            <h2>Crews</h2>
            {crews.map((c) => (
              <button
                key={c.name}
                type="button"
                className="console-rail-item"
                title={c.name}
                aria-pressed={crew === c.name}
                onClick={() => toggleCrew(c.name)}
              >
                {crewShortId(c.name)}
                <span className="console-count">
                  {c.members}{c.blocked > 0 ? ` · ${c.blocked} blocked` : ''}
                </span>
              </button>
            ))}
          </div>
        )}

        <div>
          <h2>Views</h2>
          <button type="button" className="console-rail-item" aria-pressed={section === 'crews'} onClick={() => setSection('crews')}>Crews</button>
          <button type="button" className="console-rail-item" aria-pressed={section === 'workspace'} onClick={() => setSection('workspace')}>Workspace</button>
          <button type="button" className="console-rail-item" aria-pressed={section === 'dispatch'} onClick={() => setSection('dispatch')}>Dispatch</button>
        </div>

        <div className="console-rail-foot">
          <button
            type="button"
            className="fleet-classic"
            aria-label="Switch to the classic view"
            onClick={() => { window.location.hash = '#/agents' }}
          >
            Classic
          </button>
        </div>
      </nav>

      <section className="console-list" aria-label="list">
        {/* Kept mounted like mobile's Fleet/Crews tabs: switching sections
            must not lose the chosen filter, crew narrowing or a half-typed
            reply. `hidden` is display:none, so scroll position is not kept. */}
        <div hidden={section !== 'fleet'} className="console-pane" aria-label="fleet list">
          <div className="fleet mocha">
            <header className="fleet-nav">
              <h1>{FILTER_LABEL[filter]}</h1>
              {crew && (
                <div className="fleet-nav-actions">
                  <span className="run-chip codename">{crewShortId(crew)}</span>
                  <span className="console-narrow">{visible.length} of {base.length}</span>
                  <button type="button" className="fleet-classic" aria-label="Clear crew filter" onClick={() => setCrew(null)}>Clear</button>
                </div>
              )}
            </header>

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
                  <RunCard key={r.id} run={r} now={now} onOpen={open} selected={route?.id === r.id} />
                ))}
              </section>
            ))}
          </div>
        </div>

        <div hidden={section !== 'crews'} className="console-pane">
          <CrewsView runs={runs} now={now} onOpen={open} />
        </div>

        {section === 'workspace' && (
          <WorkspaceView onOpen={(id) => { window.location.hash = runHash(id) }} />
        )}

        {section === 'dispatch' && <div className="shell-placeholder">{DISPATCH_PLACEHOLDER}</div>}
      </section>

      <section className="console-detail" aria-label="detail">
        {route ? (
          <RunDetail
            runs={runs}
            hasSnapshot={hasSnapshot}
            streamConnected={connected}
            now={now}
            id={route.id}
            tab={route.tab}
          />
        ) : (
          <div className="console-detail-empty">Select a run</div>
        )}
      </section>
    </div>
  )
}
