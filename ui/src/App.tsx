import { useState } from 'react'
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
    </div>
  )
}
