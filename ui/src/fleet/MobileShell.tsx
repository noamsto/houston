import { useState } from 'react'
import type { Run } from '../api/runs'
import { needsYou } from './staleness'
import { FleetView } from './FleetView'
import { CrewsView } from './CrewsView'
import { WorkspaceView } from './WorkspaceView'
import { RunDetail } from './RunDetail'
import { runHash, useDetailRoute } from './routes'
import { useKeyboardInset } from '../hooks/useKeyboardInset'
import { DISPATCH_PLACEHOLDER } from './copy'

type Tab = 'fleet' | 'crews' | 'workspace' | 'dispatch'

export interface ShellData {
  runs: Run[]
  connected: boolean
  hasSnapshot: boolean
  now: number
}

export function MobileShell({ runs, connected, hasSnapshot, now }: ShellData) {
  const [tab, setTab] = useState<Tab>('fleet')
  const attention = runs.some((r) => needsYou(r, now))

  const detail = useDetailRoute()
  const keyboardInset = useKeyboardInset()

  return (
    <div
      className={`shell mocha${keyboardInset > 0 ? ' keyboard-open' : ''}`}
      style={keyboardInset > 0 ? { bottom: keyboardInset } : undefined}
    >
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
        {tab === 'dispatch' && <div className="shell-placeholder">{DISPATCH_PLACEHOLDER}</div>}
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
