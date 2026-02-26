import { useEffect, useState } from 'react'
import type { SessionsData } from '../api/types'
import { SessionTree } from './SessionTree'

interface Props {
  sessions: SessionsData | null
  connected: boolean
  open: boolean
  onClose: () => void
  onSelectWindow: (target: string) => void
  onSplitWindow: (target: string) => void
  isDesktop: boolean
}

function useTheme() {
  const [theme, setTheme] = useState<'dark' | 'light'>(() => {
    return (localStorage.getItem('houston-theme') as 'dark' | 'light') ?? 'dark'
  })

  useEffect(() => {
    if (theme === 'light') {
      document.documentElement.classList.add('light')
    } else {
      document.documentElement.classList.remove('light')
    }
    localStorage.setItem('houston-theme', theme)
  }, [theme])

  const toggle = () => setTheme((t) => (t === 'dark' ? 'light' : 'dark'))
  return { theme, toggle }
}

export function Sidebar({ sessions, connected, open, onClose, onSelectWindow, onSplitWindow, isDesktop }: Props) {
  const { theme, toggle } = useTheme()

  if (!open) return null

  return (
    <aside
      style={{
        width: isDesktop ? 'var(--sidebar-width)' : '85vw',
        maxWidth: isDesktop ? undefined : 320,
        background: isDesktop ? 'var(--bg-sidebar)' : 'rgba(15,16,23,0.92)',
        backdropFilter: isDesktop ? undefined : 'blur(12px)',
        WebkitBackdropFilter: isDesktop ? undefined : 'blur(12px)',
        borderRight: '1px solid var(--border)',
        flexShrink: 0,
        position: isDesktop ? 'relative' : 'fixed',
        zIndex: isDesktop ? 'auto' : 100,
        height: '100%',
        display: 'flex',
        flexDirection: 'column',
      }}
    >
      <header
        style={{
          display: 'flex',
          alignItems: 'center',
          justifyContent: 'space-between',
          padding: '10px 12px 8px',
          borderBottom: '1px solid var(--border)',
          flexShrink: 0,
        }}
      >
        <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
          <span
            style={{
              width: 7,
              height: 7,
              borderRadius: '50%',
              background: connected ? 'var(--accent-done)' : 'var(--accent-error)',
              flexShrink: 0,
            }}
          />
          <h2 style={{ fontSize: 13, color: 'var(--text-secondary)', fontFamily: 'var(--font-mono)', fontWeight: 600 }}>
            houston
          </h2>
        </div>

        <div style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
          <button
            onClick={toggle}
            title={`Switch to ${theme === 'dark' ? 'light' : 'dark'} theme`}
            style={{
              background: 'none',
              border: 'none',
              color: 'var(--text-muted)',
              cursor: 'pointer',
              fontSize: 13,
              padding: '2px 4px',
              borderRadius: 4,
            }}
          >
            {theme === 'dark' ? '☀' : '◑'}
          </button>
          {!isDesktop && (
            <button
              onClick={onClose}
              style={{ background: 'none', border: 'none', color: 'var(--text-muted)', cursor: 'pointer', fontSize: 16 }}
            >
              ✕
            </button>
          )}
        </div>
      </header>

      <div style={{ flex: 1, overflow: 'hidden' }}>
        {!sessions ? (
          <p style={{ color: 'var(--text-muted)', fontSize: 12, padding: '12px' }}>
            {connected ? 'Loading...' : 'Connecting...'}
          </p>
        ) : (
          <SessionTree
            sessions={sessions}
            onSelect={onSelectWindow}
            onSplit={onSplitWindow}
          />
        )}
      </div>
    </aside>
  )
}
