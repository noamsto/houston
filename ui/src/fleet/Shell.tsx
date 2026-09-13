import { useState } from 'react'
import { useRuns } from '../hooks/useRuns'
import { needsYou } from './staleness'
import { useNow } from './useNow'
import { FleetView } from './FleetView'
import { CrewsView } from './CrewsView'
import { WorkspaceView } from './WorkspaceView'
import { RunDetail } from './RunDetail'
import { runHash, useDetailRoute } from './routes'
import '../theme/mocha.css'
import './fleet.css'

type Tab = 'fleet' | 'crews' | 'workspace' | 'dispatch'

const PLACEHOLDER: Record<'dispatch', string> = {
  dispatch: 'Dispatch arrives with the dispatcher milestone — pick a repo, task, tier and engine, and start work from your phone.',
}

export function Shell() {
  const [tab, setTab] = useState<Tab>('fleet')
  const { runs, connected, hasSnapshot } = useRuns()
  const now = useNow()
  const attention = runs.some((r) => needsYou(r, now))

  const detail = useDetailRoute()

  return (
    <div className="shell mocha">
      <div className="shell-body">
        {/* Kept mounted across tabs: switching tabs must not lose the chosen
            filter or reopen the EventSource with a fresh snapshot. */}
        <div hidden={tab !== 'fleet'}>
          <FleetView
            runs={runs}
            connected={connected}
            now={now}
            onOpen={(r) => { window.location.hash = runHash(r.id) }}
          />
        </div>
        {/* Kept mounted like FleetView, so a half-typed reply survives a tab switch. */}
        <div hidden={tab !== 'crews'}>
          <CrewsView
            runs={runs}
            now={now}
            onOpen={(r) => { window.location.hash = runHash(r.id) }}
          />
        </div>
        {tab === 'dispatch' && <div className="shell-placeholder">{PLACEHOLDER.dispatch}</div>}
        {tab === 'workspace' && <WorkspaceView onOpen={(id) => { window.location.hash = runHash(id) }} />}
        {/* A stacked overlay, not a `hidden`-swapped replacement of `.fleet` —
            `hidden` maps to display:none, which would collapse `.fleet`'s own
            scroll container and lose its scrollTop on return. */}
        {detail && (
          <RunDetail
            runs={runs}
            hasSnapshot={hasSnapshot}
            streamConnected={connected}
            now={now}
            id={detail.id}
            tab={detail.tab}
          />
        )}
      </div>

      <nav className="shell-tabs" aria-label="sections">
        <button className={tab === 'fleet' ? 'on' : ''} aria-current={tab === 'fleet' ? 'true' : undefined} onClick={() => setTab('fleet')}>
          <span className="glyph" aria-hidden>▤</span>
          Fleet
          {attention && tab !== 'fleet' && <span className="dot" role="status" aria-label="runs need you" />}
        </button>
        <button className={tab === 'crews' ? 'on' : ''} aria-current={tab === 'crews' ? 'true' : undefined} onClick={() => setTab('crews')}>
          <span className="glyph" aria-hidden>◆</span>Crews
        </button>
        <button className={tab === 'workspace' ? 'on' : ''} aria-current={tab === 'workspace' ? 'true' : undefined} onClick={() => setTab('workspace')}>
          <span className="glyph" aria-hidden>▣</span>Workspace
        </button>
        <button className={tab === 'dispatch' ? 'on' : ''} aria-current={tab === 'dispatch' ? 'true' : undefined} onClick={() => setTab('dispatch')}>
          <span className="glyph" aria-hidden>✦</span>Dispatch
        </button>
      </nav>
    </div>
  )
}
