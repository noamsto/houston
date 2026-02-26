import { useState } from 'react'
import type { SessionsData, SessionWithWindows, WindowWithStatus } from '../api/types'

interface WindowRowProps {
  w: WindowWithStatus
  sessionName: string
  onSelect: (target: string) => void
  onSplit: (target: string) => void
}

function WindowRow({ w, sessionName, onSelect, onSplit }: WindowRowProps) {
  const target = `${sessionName}:${w.window.index}.${w.pane.index}`
  const { type } = w.parse_result

  const dotColor =
    w.needs_attention ? 'var(--accent-attention)' :
    type === 'working' ? 'var(--accent-working)' :
    type === 'done'    ? 'var(--accent-done)' :
                         'var(--accent-idle)'

  return (
    <div
      style={{
        display: 'flex',
        alignItems: 'center',
        gap: 6,
        padding: '3px 8px 3px 24px',
        cursor: 'pointer',
        borderRadius: 4,
        color: 'var(--text-secondary)',
        fontSize: 12,
      }}
      onClick={(e) => {
        if (e.ctrlKey || e.metaKey) {
          onSplit(target)
        } else {
          onSelect(target)
        }
      }}
      onMouseEnter={(e) => {
        ;(e.currentTarget as HTMLDivElement).style.background = 'var(--bg-surface)'
      }}
      onMouseLeave={(e) => {
        ;(e.currentTarget as HTMLDivElement).style.background = 'transparent'
      }}
    >
      <span style={{ width: 6, height: 6, borderRadius: '50%', background: dotColor, flexShrink: 0 }} />
      <span style={{ flex: 1, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
        {w.window.name}
      </span>
      {w.branch && (
        <span style={{ color: 'var(--text-muted)', fontSize: 10, flexShrink: 0, maxWidth: 60, overflow: 'hidden', textOverflow: 'ellipsis' }}>
          {w.branch}
        </span>
      )}
    </div>
  )
}

interface SessionRowProps {
  s: SessionWithWindows
  onSelect: (target: string) => void
  onSplit: (target: string) => void
}

function SessionRow({ s, onSelect, onSplit }: SessionRowProps) {
  const [expanded, setExpanded] = useState(true)
  const hasAttention = s.attention_count > 0

  return (
    <div
      style={hasAttention ? {
        animation: 'attention-pulse 2s ease-in-out infinite',
        borderRadius: 4,
        marginBottom: 2,
      } : { marginBottom: 2 }}
    >
      <div
        style={{
          display: 'flex',
          alignItems: 'center',
          gap: 6,
          padding: '4px 8px',
          cursor: 'pointer',
          borderRadius: 4,
          color: hasAttention ? 'var(--accent-attention)' : 'var(--text-primary)',
          fontSize: 13,
          fontWeight: 500,
        }}
        onClick={() => setExpanded((x) => !x)}
        onMouseEnter={(e) => {
          ;(e.currentTarget as HTMLDivElement).style.background = 'var(--bg-surface)'
        }}
        onMouseLeave={(e) => {
          ;(e.currentTarget as HTMLDivElement).style.background = 'transparent'
        }}
      >
        <span style={{ fontSize: 10, color: 'var(--text-muted)', width: 10 }}>
          {expanded ? '▾' : '▸'}
        </span>
        <span style={{ flex: 1, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
          {s.session.name}
        </span>
        {s.attention_count > 0 && (
          <span style={{
            background: 'var(--accent-attention)',
            color: '#000',
            fontSize: 10,
            fontWeight: 700,
            borderRadius: 8,
            padding: '1px 5px',
            flexShrink: 0,
          }}>
            {s.attention_count}
          </span>
        )}
      </div>

      {expanded && s.windows.map((w) => (
        <WindowRow
          key={w.window.index}
          w={w}
          sessionName={s.session.name}
          onSelect={onSelect}
          onSplit={onSplit}
        />
      ))}
    </div>
  )
}

interface GroupProps {
  label: string
  items: SessionWithWindows[]
  onSelect: (target: string) => void
  onSplit: (target: string) => void
}

function Group({ label, items, onSelect, onSplit }: GroupProps) {
  if (items.length === 0) return null

  return (
    <div style={{ marginBottom: 12 }}>
      <div style={{
        fontSize: 10,
        color: 'var(--text-muted)',
        fontWeight: 600,
        letterSpacing: '0.08em',
        padding: '0 8px 4px',
      }}>
        {label} ({items.length})
      </div>
      {items.map((s) => (
        <SessionRow key={s.session.name} s={s} onSelect={onSelect} onSplit={onSplit} />
      ))}
    </div>
  )
}

interface Props {
  sessions: SessionsData
  onSelect: (target: string) => void
  onSplit: (target: string) => void
}

export function SessionTree({ sessions, onSelect, onSplit }: Props) {
  const [filter, setFilter] = useState('')

  const filterSession = (s: SessionWithWindows) => {
    if (!filter) return true
    const q = filter.toLowerCase()
    return (
      s.session.name.toLowerCase().includes(q) ||
      s.windows.some(
        (w) =>
          w.window.name.toLowerCase().includes(q) ||
          w.branch.toLowerCase().includes(q),
      )
    )
  }

  const filtered = {
    needs_attention: sessions.needs_attention.filter(filterSession),
    active: sessions.active.filter(filterSession),
    idle: sessions.idle.filter(filterSession),
  }

  return (
    <div style={{ display: 'flex', flexDirection: 'column', height: '100%' }}>
      <div style={{ padding: '8px 8px 4px' }}>
        <input
          type="text"
          placeholder="filter..."
          value={filter}
          onChange={(e) => setFilter(e.target.value)}
          style={{
            width: '100%',
            background: 'var(--bg-surface)',
            border: '1px solid var(--border)',
            borderRadius: 4,
            padding: '4px 8px',
            color: 'var(--text-primary)',
            fontSize: 12,
            outline: 'none',
          }}
        />
      </div>

      <div style={{ flex: 1, overflow: 'auto', padding: '4px 0' }}>
        <Group label="ATTENTION" items={filtered.needs_attention} onSelect={onSelect} onSplit={onSplit} />
        <Group label="ACTIVE"    items={filtered.active}          onSelect={onSelect} onSplit={onSplit} />
        <Group label="IDLE"      items={filtered.idle}            onSelect={onSelect} onSplit={onSplit} />

        {filtered.needs_attention.length === 0 &&
         filtered.active.length === 0 &&
         filtered.idle.length === 0 && (
          <p style={{ color: 'var(--text-muted)', fontSize: 12, padding: '8px 12px' }}>
            {filter ? 'No matches' : 'No sessions'}
          </p>
        )}
      </div>
    </div>
  )
}
