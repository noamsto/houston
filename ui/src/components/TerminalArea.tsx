import type { useLayout } from '../hooks/useLayout'
import type { AgentType, ResultType, SessionsData } from '../api/types'
import { SplitContainer } from './SplitContainer'

interface Props {
  layout: ReturnType<typeof useLayout>
  sessions: SessionsData | null
  onMenuClick: () => void
  isDesktop: boolean
}

const AGENT_ICONS: Record<AgentType, string> = {
  'claude-code': '\u2726',
  'amp': '\u26A1',
  'generic': '\u25C6',
}

function statusColor(status: ResultType | undefined): string {
  switch (status) {
    case 'done':     return 'var(--accent-done)'
    case 'working':  return 'var(--accent-working)'
    case 'question':
    case 'choice':   return 'var(--accent-attention)'
    case 'error':    return 'var(--accent-error)'
    default:         return 'var(--text-muted)'
  }
}

interface TargetEntry {
  target: string
  session: string
  agent: AgentType
  status: ResultType
}

function buildTargetList(sessions: SessionsData): TargetEntry[] {
  const list: TargetEntry[] = []
  for (const group of [sessions.needs_attention, sessions.active, sessions.idle]) {
    for (const s of group) {
      for (const w of s.windows) {
        list.push({
          target: `${s.session.name}:${w.window.index}.${w.pane.index}`,
          session: s.session.name,
          agent: w.agent_type,
          status: w.parse_result.type,
        })
      }
    }
  }
  return list
}

export function TerminalArea({ layout, sessions, onMenuClick, isDesktop }: Props) {
  const handleFocus = (paneId: string) => {
    layout.dispatch({ type: 'FOCUS_PANE', paneId })
  }

  const handleClose = (paneId: string) => {
    layout.dispatch({ type: 'CLOSE_PANE', paneId })
  }

  // Build flat target list for mobile nav
  const targetList = sessions ? buildTargetList(sessions) : []

  // Find the current pane's target
  const focusedPane = layout.panes.find(p => p.id === layout.focusedPaneId)
  const currentTarget = focusedPane?.target
  const currentIdx = currentTarget ? targetList.findIndex(t => t.target === currentTarget) : -1
  const currentEntry = currentIdx >= 0 ? targetList[currentIdx] : null

  const handlePrev = () => {
    if (targetList.length === 0) return
    const idx = currentIdx <= 0 ? targetList.length - 1 : currentIdx - 1
    layout.dispatch({ type: 'OPEN_PANE', target: targetList[idx].target })
  }

  const handleNext = () => {
    if (targetList.length === 0) return
    const idx = currentIdx < 0 || currentIdx >= targetList.length - 1 ? 0 : currentIdx + 1
    layout.dispatch({ type: 'OPEN_PANE', target: targetList[idx].target })
  }

  const navBtn: React.CSSProperties = {
    background: 'none',
    border: 'none',
    color: 'var(--text-secondary)',
    cursor: 'pointer',
    fontSize: 20,
    padding: '4px 10px',
    lineHeight: 1,
  }

  return (
    <main
      style={{
        flex: 1,
        background: 'var(--bg-terminal)',
        display: 'flex',
        flexDirection: 'column',
        minWidth: 0,
      }}
    >
      {!isDesktop && (
        <header
          style={{
            padding: '8px 12px',
            background: 'var(--bg-header)',
            display: 'flex',
            alignItems: 'center',
            gap: 8,
            flexShrink: 0,
            borderBottom: '1px solid var(--border)',
          }}
        >
          <button
            onClick={onMenuClick}
            style={{
              background: 'none',
              border: 'none',
              color: 'var(--text-primary)',
              cursor: 'pointer',
              fontSize: 16,
            }}
          >
            ☰
          </button>
          {currentEntry ? (
            <>
              <span style={{ color: statusColor(currentEntry.status), flexShrink: 0, fontSize: 14 }}>
                {AGENT_ICONS[currentEntry.agent] ?? '\u25C6'}
              </span>
              <span
                style={{
                  flex: 1,
                  overflow: 'hidden',
                  textOverflow: 'ellipsis',
                  whiteSpace: 'nowrap',
                  fontSize: 14,
                  fontFamily: 'var(--font-mono)',
                  color: 'var(--text-primary)',
                }}
              >
                {currentEntry.session}
              </span>
            </>
          ) : (
            <span style={{ flex: 1, fontSize: 14, fontFamily: 'var(--font-mono)' }}>houston</span>
          )}
          {targetList.length > 1 && (
            <>
              <button onClick={handlePrev} style={navBtn} aria-label="Previous session">◂</button>
              <button onClick={handleNext} style={navBtn} aria-label="Next session">▸</button>
            </>
          )}
        </header>
      )}

      <div style={{ flex: 1, overflow: 'hidden', minHeight: 0 }}>
        {layout.layout.type === 'empty' ? (
          <div
            style={{
              display: 'flex',
              height: '100%',
              alignItems: 'center',
              justifyContent: 'center',
              flexDirection: 'column',
              gap: 16,
              padding: 24,
            }}
          >
            <span style={{ fontSize: 32, opacity: 0.3 }}>◆</span>
            <p style={{ color: 'var(--text-secondary)', fontSize: 14 }}>Select a session to start</p>
            <p style={{ color: 'var(--text-muted)', fontSize: 12 }}>
              Click a window in the sidebar{isDesktop ? ' · Ctrl+Meta to split' : ''}
            </p>
          </div>
        ) : (
          <SplitContainer
            layout={layout.layout}
            panes={layout.panes}
            focusedPaneId={layout.focusedPaneId}
            onFocus={handleFocus}
            onClose={handleClose}
          />
        )}
      </div>
    </main>
  )
}
