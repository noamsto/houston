import type { useLayout } from '../hooks/useLayout'
import { SplitContainer } from './SplitContainer'

interface Props {
  layout: ReturnType<typeof useLayout>
  onMenuClick: () => void
  isDesktop: boolean
}

export function TerminalArea({ layout, onMenuClick, isDesktop }: Props) {
  const handleFocus = (paneId: string) => {
    layout.dispatch({ type: 'FOCUS_PANE', paneId })
  }

  const handleClose = (paneId: string) => {
    layout.dispatch({ type: 'CLOSE_PANE', paneId })
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
          <span style={{ fontSize: 14, fontFamily: 'var(--font-mono)' }}>houston</span>
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
              gap: 8,
            }}
          >
            <p style={{ color: 'var(--text-muted)', fontSize: 14 }}>Select a session to start</p>
            <p style={{ color: 'var(--text-muted)', fontSize: 12 }}>Click a window in the sidebar</p>
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
