import { useCallback, useEffect, useRef, useState } from 'react'
import { Sidebar } from './components/Sidebar'
import { TerminalArea } from './components/TerminalArea'
import { AgentsView } from './components/agents/AgentsView'
import { useIsDesktop } from './hooks/useMediaQuery'
import { useLayout } from './hooks/useLayout'
import { useSessionsStream } from './hooks/useSessionsStream'
import { useAttentionNotifications } from './hooks/useAttentionNotifications'
import './theme/tokens.css'

type View = 'agents' | 'panes'

function initialView(): View {
  return window.location.hash === '#/panes' ? 'panes' : 'agents'
}

export default function App() {
  const [view, setView] = useState<View>(initialView)

  useEffect(() => {
    const onHash = () => setView(initialView())
    window.addEventListener('hashchange', onHash)
    return () => window.removeEventListener('hashchange', onHash)
  }, [])

  const goAgents = () => { window.location.hash = '#/agents'; setView('agents') }
  const goPanes  = () => { window.location.hash = '#/panes';  setView('panes') }

  if (view === 'agents') {
    return (
      <>
        <AgentsView />
        <ViewSwitch current="agents" onAgents={goAgents} onPanes={goPanes} />
      </>
    )
  }
  return (
    <>
      <PanesApp />
      <ViewSwitch current="panes" onAgents={goAgents} onPanes={goPanes} />
    </>
  )
}

function ViewSwitch({
  current, onAgents, onPanes,
}: { current: View; onAgents: () => void; onPanes: () => void }) {
  return (
    <div
      style={{
        position: 'fixed',
        bottom: 12,
        right: 12,
        zIndex: 300,
        display: 'flex',
        gap: 0,
        border: '1px solid #2a3444',
        borderRadius: 2,
        background: 'rgba(10,13,18,0.9)',
        backdropFilter: 'blur(8px)',
        fontFamily: 'Oswald, system-ui, sans-serif',
        fontSize: 10,
        letterSpacing: '0.22em',
        textTransform: 'uppercase',
      }}
    >
      <button
        onClick={onAgents}
        style={{
          appearance: 'none',
          border: 'none',
          padding: '8px 12px',
          cursor: 'pointer',
          background: current === 'agents' ? 'rgba(94,255,154,0.12)' : 'transparent',
          color: current === 'agents' ? '#5eff9a' : '#768391',
          fontWeight: 600,
        }}
      >Agents</button>
      <button
        onClick={onPanes}
        style={{
          appearance: 'none',
          border: 'none',
          padding: '8px 12px',
          cursor: 'pointer',
          background: current === 'panes' ? 'rgba(255,255,255,0.06)' : 'transparent',
          color: current === 'panes' ? '#e4ebf3' : '#768391',
          fontWeight: 600,
        }}
      >Panes</button>
    </div>
  )
}

function PanesApp() {
  const { sessions, connected } = useSessionsStream()
  useAttentionNotifications(sessions)
  const layout = useLayout()
  const isDesktop = useIsDesktop()
  const [sidebarOpen, setSidebarOpen] = useState(false)
  const rootRef = useRef<HTMLDivElement>(null)

  // Track visual viewport height so the layout shrinks when the mobile
  // keyboard opens instead of leaving a black gap behind the terminal.
  useEffect(() => {
    const vv = window.visualViewport
    if (!vv || isDesktop) return
    const update = () => {
      if (rootRef.current) {
        rootRef.current.style.height = `${vv.height}px`
      }
    }
    update()
    vv.addEventListener('resize', update)
    return () => vv.removeEventListener('resize', update)
  }, [isDesktop])

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
    <div ref={rootRef} style={{ display: 'flex', height: '100dvh', overflow: 'hidden' }}>
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
        sessions={sessions}
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
