import { useCallback, useEffect, useRef } from 'react'
import type { Run } from '../api/runs'
import type { Mode } from '../api/mode'
import { needsYou } from './staleness'
import { FleetView } from './FleetView'
import { WorkspaceView } from './WorkspaceView'
import { DispatchView } from './DispatchView'
import { RunDetail } from './RunDetail'
import { tabHash, useDetailRoute, useShellTab, type ShellTab } from './routes'
import { back, backLabel, openRun, popToRoot, rootLabel, switchRunTab, useNavEntry } from './nav'
import { activeTab, DETAIL_TAB_LABEL, offeredTabs } from './runTabs'
import { useKeyboardInset } from '../hooks/useKeyboardInset'

export interface ShellData {
  runs: Run[]
  connected: boolean
  hasSnapshot: boolean
  now: number
  mode: Mode | null
}

const RUN_TAB_GLYPH = { crew: '◉', chat: '✉', terminal: '▸' } as const
const LONG_PRESS_MS = 500

/** The bottom bar's Back, in thumb reach; a long press pops to the root. */
function BarBack({ label, root, onBack, onRoot }: { label: string; root: string; onBack: () => void; onRoot: () => void }) {
  const timer = useRef<number | null>(null)
  const longPressed = useRef(false)
  const cancel = () => {
    if (timer.current !== null) window.clearTimeout(timer.current)
    timer.current = null
  }
  useEffect(() => cancel, [])
  return (
    <button
      type="button"
      className="run-tabs-back"
      aria-label={`Back to ${label}`}
      title={`Back to ${label} — hold for ${root}`}
      onPointerDown={() => {
        cancel()
        longPressed.current = false
        timer.current = window.setTimeout(() => {
          timer.current = null
          longPressed.current = true
          onRoot()
        }, LONG_PRESS_MS)
      }}
      onPointerUp={cancel}
      onPointerLeave={cancel}
      onPointerCancel={cancel}
      onContextMenu={(e) => e.preventDefault()}
      onClick={() => {
        if (longPressed.current) longPressed.current = false
        else onBack()
      }}
    >
      <span className="glyph" aria-hidden>‹</span>
      <span className="run-tabs-back-label">{label}</span>
    </button>
  )
}

export function MobileShell({ runs, connected, hasSnapshot, now, mode }: ShellData) {
  const [tab, goTab] = useShellTab(mode)
  const dispatcherMode = mode === 'dispatcher'
  const attention = runs.some((r) => needsYou(r, now))

  const open = useCallback((r: Run) => openRun(r.id), [])

  const detail = useDetailRoute()
  const entry = useNavEntry()
  const goBack = useCallback(() => back(tabHash(tab)), [tab])
  const goRoot = useCallback(() => popToRoot(tabHash(tab)), [tab])
  const parentLabel = backLabel(entry, runs, tab)
  const root = rootLabel(entry, tab)
  const bodyRef = useRef<HTMLDivElement>(null)
  const selectTab = (next: ShellTab) => {
    // iOS convention: re-tapping the tab you are on scrolls its list to the top.
    if (next === tab && !detail) {
      const list = bodyRef.current?.querySelector(`[data-shell-tab="${next}"] > *`)
      if (list) list.scrollTop = 0
      return
    }
    goTab(next, true)
  }
  const keyboardInset = useKeyboardInset()
  const detailRun = detail && hasSnapshot ? runs.find((r) => r.id === detail.id) : undefined
  const runTabs = detailRun ? offeredTabs(detailRun, mode) : []
  const activeRunTab = detailRun ? activeTab(detailRun, mode, detail?.tab) : 'chat'

  return (
    <div
      className={`shell mocha${keyboardInset > 0 ? ' keyboard-open' : ''}`}
      style={keyboardInset > 0 ? { bottom: keyboardInset } : undefined}
    >
      <div className="shell-body" ref={bodyRef}>
        {/* Kept mounted across tabs: switching tabs must not lose the chosen
            filter or reopen the EventSource with a fresh snapshot. */}
        <div hidden={tab !== 'fleet'} data-shell-tab="fleet">
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
          <div hidden={tab !== 'dispatch'} data-shell-tab="dispatch">
            <DispatchView runs={runs} />
          </div>
        )}
        {tab === 'workspace' && (
          <div data-shell-tab="workspace">
            <WorkspaceView onOpen={openRun} />
          </div>
        )}
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
            onBack={goBack}
            backLabel={parentLabel}
            onRoot={entry && entry.depth >= 2 ? goRoot : undefined}
            rootLabel={root}
            edgeSwipeBack
            tabsInBar
          />
        )}
      </div>

      {detail ? (
        <div className="shell-tabs run-tabs">
          <BarBack label={parentLabel} root={root} onBack={goBack} onRoot={goRoot} />
          <div className="run-tabs-list" role="tablist" aria-label="run tabs">
            {runTabs.map((t) => (
              <button
                key={t}
                type="button"
                role="tab"
                className={activeRunTab === t ? 'on' : ''}
                aria-selected={activeRunTab === t}
                onClick={() => switchRunTab(detail.id, t)}
              >
                <span className="glyph" aria-hidden>{RUN_TAB_GLYPH[t]}</span>{DETAIL_TAB_LABEL[t]}
              </button>
            ))}
          </div>
        </div>
      ) : (
      <nav className="shell-tabs" aria-label="sections">
        <button className={tab === 'fleet' ? 'on' : ''} aria-current={tab === 'fleet' ? 'true' : undefined} onClick={() => selectTab('fleet')}>
          <span className="glyph" aria-hidden>▤</span>
          Fleet
          {attention && tab !== 'fleet' && <span className="dot" role="status" aria-label="runs need you" />}
        </button>
        <button className={tab === 'workspace' ? 'on' : ''} aria-current={tab === 'workspace' ? 'true' : undefined} onClick={() => selectTab('workspace')}>
          <span className="glyph" aria-hidden>▣</span>Workspace
        </button>
        {dispatcherMode && (
          <button className={tab === 'dispatch' ? 'on' : ''} aria-current={tab === 'dispatch' ? 'true' : undefined} onClick={() => selectTab('dispatch')}>
            <span className="glyph" aria-hidden>✦</span>Dispatch
          </button>
        )}
      </nav>
      )}
    </div>
  )
}
