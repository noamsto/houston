import type { SessionsData } from '../api/types'

interface Props {
  sessions: SessionsData | null
  connected: boolean
  open: boolean
  onClose: () => void
  onSelectWindow: (target: string) => void
  onSplitWindow: (target: string) => void
  isDesktop: boolean
}

export function Sidebar({ sessions, open, onClose, onSelectWindow, isDesktop }: Props) {
  if (!open) return null

  return (
    <aside
      style={{
        width: isDesktop ? 'var(--sidebar-width)' : '100vw',
        background: 'var(--bg-sidebar)',
        borderRight: '1px solid var(--border)',
        overflow: 'auto',
        flexShrink: 0,
        position: isDesktop ? 'relative' : 'fixed',
        zIndex: isDesktop ? 'auto' : 100,
        height: '100%',
      }}
    >
      <div style={{ padding: '12px' }}>
        <div
          style={{
            display: 'flex',
            alignItems: 'center',
            justifyContent: 'space-between',
            marginBottom: 12,
          }}
        >
          <h2 style={{ fontSize: 14, color: 'var(--text-secondary)', fontFamily: 'var(--font-mono)' }}>
            houston
          </h2>
          {!isDesktop && (
            <button
              onClick={onClose}
              style={{ background: 'none', border: 'none', color: 'var(--text-muted)', cursor: 'pointer', fontSize: 16 }}
            >
              ✕
            </button>
          )}
        </div>

        {!sessions && (
          <p style={{ color: 'var(--text-muted)', fontSize: 12 }}>Connecting...</p>
        )}

        {sessions && (
          <div style={{ fontSize: 11, color: 'var(--text-secondary)' }}>
            {/* Placeholder: will be replaced by SessionTree in Task 10 */}
            {[
              { label: 'ATTENTION', items: sessions.needs_attention },
              { label: 'ACTIVE', items: sessions.active },
              { label: 'IDLE', items: sessions.idle },
            ].map(({ label, items }) =>
              items.length === 0 ? null : (
                <div key={label} style={{ marginBottom: 12 }}>
                  <div style={{ color: 'var(--text-muted)', fontSize: 10, marginBottom: 4 }}>
                    {label} ({items.length})
                  </div>
                  {items.map((s) => (
                    <div key={s.session.name} style={{ marginBottom: 4 }}>
                      <div style={{ color: 'var(--text-primary)' }}>{s.session.name}</div>
                      {s.windows.map((w) => {
                        const target = `${s.session.name}:${w.window.index}.${w.pane.index}`
                        return (
                          <div
                            key={w.window.index}
                            style={{
                              paddingLeft: 12,
                              color: 'var(--text-secondary)',
                              cursor: 'pointer',
                              padding: '2px 8px',
                            }}
                            onClick={() => onSelectWindow(target)}
                          >
                            {w.window.name}
                          </div>
                        )
                      })}
                    </div>
                  ))}
                </div>
              ),
            )}
          </div>
        )}
      </div>
    </aside>
  )
}
