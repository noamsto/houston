import { useCallback } from 'react'
import type { Run } from '../api/runs'
import type { Mode } from '../api/mode'
import { needsYou } from './staleness'
import { FleetView } from './FleetView'
import { WorkspaceView } from './WorkspaceView'
import { DispatchView } from './DispatchView'
import { RunDetail } from './RunDetail'
import { runHash, tabHash, TAB_LABEL, useDetailRoute, useShellTab } from './routes'
import { DETAIL_TAB_LABEL, offeredTabs } from './runTabs'
import { useKeyboardInset } from '../hooks/useKeyboardInset'

export interface ShellData {
  runs: Run[]
  connected: boolean
  hasSnapshot: boolean
  now: number
  mode: Mode | null
}

const RUN_TAB_GLYPH = { crew: '◉', chat: '✉', terminal: '▸' } as const

export function MobileShell({ runs, connected, hasSnapshot, now, mode }: ShellData) {
  const [tab, goTab] = useShellTab(mode)
  const dispatcherMode = mode === 'dispatcher'
  const attention = runs.some((r) => needsYou(r, now))

  const open = useCallback((r: Run) => { window.location.hash = runHash(r.id) }, [])

  const detail = useDetailRoute()
  const keyboardInset = useKeyboardInset()
  const detailRun = detail && hasSnapshot ? runs.find((r) => r.id === detail.id) : undefined
  const runTabs = detailRun ? offeredTabs(detailRun, mode) : []
  // Mirrors RunDetail's choice of tab: a dispatcher defaults to Crew, others to Chat.
  const activeRunTab = detail?.tab === 'terminal' && detailRun?.caps.terminal ? 'terminal'
    : runTabs.includes('crew') && (detail?.tab === undefined || detail.tab === 'crew') ? 'crew'
      : 'chat'

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
            mode={mode}
            onOpen={open}
          />
        </div>
        {/* Kept mounted like FleetView, so a half-typed dispatch form survives a tab switch. */}
        {dispatcherMode && (
          <div hidden={tab !== 'dispatch'}>
            <DispatchView runs={runs} />
          </div>
        )}
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
            mode={mode}
            onBack={() => { window.location.hash = tabHash(tab) }}
            backLabel={TAB_LABEL[tab]}
            edgeSwipeBack
            tabsInBar
          />
        )}
      </div>

      {detailRun && detail ? (
        <div className="shell-tabs run-tabs" role="tablist" aria-label="run tabs">
          {runTabs.map((t) => (
            <button
              key={t}
              type="button"
              role="tab"
              className={activeRunTab === t ? 'on' : ''}
              aria-selected={activeRunTab === t}
              onClick={() => { window.location.hash = runHash(detail.id, t) }}
            >
              <span className="glyph" aria-hidden>{RUN_TAB_GLYPH[t]}</span>{DETAIL_TAB_LABEL[t]}
            </button>
          ))}
        </div>
      ) : (
      <nav className="shell-tabs" aria-label="sections">
        <button className={tab === 'fleet' ? 'on' : ''} aria-current={tab === 'fleet' ? 'true' : undefined} onClick={() => goTab('fleet', true)}>
          <span className="glyph" aria-hidden>▤</span>
          Fleet
          {attention && tab !== 'fleet' && <span className="dot" role="status" aria-label="runs need you" />}
        </button>
        <button className={tab === 'workspace' ? 'on' : ''} aria-current={tab === 'workspace' ? 'true' : undefined} onClick={() => goTab('workspace', true)}>
          <span className="glyph" aria-hidden>▣</span>Workspace
        </button>
        {dispatcherMode && (
          <button className={tab === 'dispatch' ? 'on' : ''} aria-current={tab === 'dispatch' ? 'true' : undefined} onClick={() => goTab('dispatch', true)}>
            <span className="glyph" aria-hidden>✦</span>Dispatch
          </button>
        )}
      </nav>
      )}
    </div>
  )
}
