import { useCallback, useEffect, useState } from 'react'
import { Sidebar } from './components/Sidebar'
import { TerminalArea } from './components/TerminalArea'
import { useIsDesktop } from './hooks/useMediaQuery'
import { useLayout } from './hooks/useLayout'
import { useSessionsStream } from './hooks/useSessionsStream'
import './theme/tokens.css'

export default function App() {
  const { sessions, connected } = useSessionsStream()
  const layout = useLayout()
  const isDesktop = useIsDesktop()
  const [sidebarOpen, setSidebarOpen] = useState(false)

  const handleSelectWindow = (target: string) => {
    layout.dispatch({ type: 'OPEN_PANE', target })
    if (!isDesktop) setSidebarOpen(false)
  }

  const handleSplitWindow = (target: string) => {
    layout.dispatch({ type: 'SPLIT_PANE', target, direction: 'horizontal' })
    if (!isDesktop) setSidebarOpen(false)
  }

  // Keyboard shortcuts for pane navigation
  const handleKeyDown = useCallback((e: KeyboardEvent) => {
    // Ctrl+] or Ctrl+[ to cycle panes
    if (e.ctrlKey && e.key === ']') {
      e.preventDefault()
      const panes = layout.panes
      if (panes.length < 2) return
      const idx = panes.findIndex(p => p.id === layout.focusedPaneId)
      const next = panes[(idx + 1) % panes.length]
      if (next) layout.dispatch({ type: 'FOCUS_PANE', paneId: next.id })
    }
    if (e.ctrlKey && e.key === '[') {
      e.preventDefault()
      const panes = layout.panes
      if (panes.length < 2) return
      const idx = panes.findIndex(p => p.id === layout.focusedPaneId)
      const prev = panes[(idx - 1 + panes.length) % panes.length]
      if (prev) layout.dispatch({ type: 'FOCUS_PANE', paneId: prev.id })
    }
  }, [layout])

  useEffect(() => {
    window.addEventListener('keydown', handleKeyDown)
    return () => window.removeEventListener('keydown', handleKeyDown)
  }, [handleKeyDown])

  return (
    <div style={{ display: 'flex', height: '100vh', overflow: 'hidden' }}>
      <Sidebar
        sessions={sessions}
        connected={connected}
        open={isDesktop || sidebarOpen}
        onClose={() => setSidebarOpen(false)}
        onSelectWindow={handleSelectWindow}
        onSplitWindow={handleSplitWindow}
        isDesktop={isDesktop}
      />
      <TerminalArea
        layout={layout}
        onMenuClick={() => setSidebarOpen(true)}
        isDesktop={isDesktop}
      />
      {!connected && (
        <div
          style={{
            position: 'fixed',
            top: 0,
            left: 0,
            right: 0,
            height: 3,
            background: 'var(--accent-error)',
            zIndex: 200,
            animation: 'reconnect-pulse 1.5s ease-in-out infinite',
          }}
          title="Reconnecting..."
        />
      )}
    </div>
  )
}
