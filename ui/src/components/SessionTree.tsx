import { useMemo, useState } from 'react'
import type { ResultType, SessionsData, SessionWithWindows, WindowWithStatus } from '../api/types'

// Strip Private Use Area Unicode characters (Nerd Font glyphs) that won't render on mobile.
// Ranges: U+E000-U+F8FF (BMP PUA), U+F0000-U+FFFFD, U+100000-U+10FFFD (Supplementary PUA)
const PUA_RE = /[\uE000-\uF8FF]|[\u{F0000}-\u{FFFFD}]|[\u{100000}-\u{10FFFD}]/gu
function stripPUA(s: string): string {
  return s.replace(PUA_RE, '').replace(/\s{2,}/g, ' ').trim()
}

interface WindowRowProps {
  w: WindowWithStatus
  sessionName: string
  onSelect: (target: string) => void
  onSplit: (target: string) => void
}

function statusColor(type: ResultType, needsAttention: boolean): string {
  if (needsAttention) return 'var(--accent-attention)'
  switch (type) {
    case 'working': return 'var(--accent-working)'
    case 'done':    return 'var(--accent-done)'
    case 'error':   return 'var(--accent-error)'
    default:        return 'var(--accent-idle)'
  }
}

interface PillStyle {
  bg: string
  color: string
}

function pillStyle(type: ResultType, needsAttention: boolean): PillStyle | null {
  if (needsAttention || type === 'question' || type === 'choice')
    return { bg: 'var(--accent-attention)', color: '#000' }
  if (type === 'error')
    return { bg: 'var(--accent-error)', color: '#fff' }
  if (type === 'working')
    return { bg: 'var(--accent-working)', color: '#fff' }
  if (type === 'done')
    return { bg: 'transparent', color: 'var(--accent-done)' }
  return null
}

function statusLabel(w: WindowWithStatus): string | null {
  const { type, activity } = w.parse_result
  if (type === 'error') return 'Error'
  if (type === 'question') return 'Waiting for input'
  if (type === 'choice') return 'Waiting for choice'
  if (type === 'working') return stripPUA(activity || 'Working...')
  if (type === 'done') return 'Done'
  return null
}

function displayName(w: WindowWithStatus): string {
  if (w.branch && w.branch !== 'main' && w.branch !== 'master') return w.branch
  return stripPUA(w.window.name)
}

function dirName(w: WindowWithStatus): string {
  if (!w.window.path) return ''
  return w.window.path.split('/').filter(Boolean).pop() || ''
}

function WindowRow({ w, sessionName, onSelect, onSplit }: WindowRowProps) {
  const target = `${sessionName}:${w.window.index}.${w.pane.index}`
  const color = statusColor(w.parse_result.type, w.needs_attention)
  const pill = pillStyle(w.parse_result.type, w.needs_attention)
  const label = statusLabel(w)
  const name = displayName(w)
  const dir = dirName(w)
  const showDir = dir && dir !== sessionName && dir !== name

  return (
    <div
      className="tree-row"
      style={{
        padding: '6px 10px 6px 12px',
        cursor: 'pointer',
        borderRadius: 4,
        borderLeft: `3px solid ${color}`,
        marginLeft: 4,
        marginRight: 4,
        marginBottom: 2,
        background: w.needs_attention ? 'rgba(245, 158, 11, 0.06)' : undefined,
      }}
      onClick={(e) => {
        if (e.ctrlKey || e.metaKey) {
          onSplit(target)
        } else {
          onSelect(target)
        }
      }}
    >
      {/* Line 1: branch/window name */}
      <div style={{
        fontSize: 13,
        fontWeight: 500,
        color: 'var(--text-primary)',
        overflow: 'hidden',
        textOverflow: 'ellipsis',
        whiteSpace: 'nowrap',
      }}>
        {name}
      </div>

      {/* Line 2: dir + status pill */}
      <div style={{
        display: 'flex',
        alignItems: 'center',
        gap: 6,
        marginTop: 2,
        fontSize: 11,
        overflow: 'hidden',
      }}>
        {showDir && (
          <span style={{
            color: 'var(--text-muted)',
            flexShrink: 1,
            overflow: 'hidden',
            textOverflow: 'ellipsis',
            whiteSpace: 'nowrap',
            minWidth: 0,
          }}>
            {dir}
          </span>
        )}
        {label && pill && (
          <span style={{
            background: pill.bg,
            color: pill.color,
            fontSize: 10,
            fontWeight: 600,
            borderRadius: 8,
            padding: '1px 6px',
            whiteSpace: 'nowrap',
            flexShrink: 0,
          }}>
            {label}
          </span>
        )}
      </div>
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
  const multiWindow = s.windows.length > 1

  return (
    <div
      style={hasAttention ? {
        animation: 'attention-pulse 2s ease-in-out infinite',
        borderRadius: 4,
        marginBottom: 2,
      } : { marginBottom: 2 }}
    >
      {/* Session header */}
      <div
        className="tree-row"
        style={{
          display: 'flex',
          alignItems: 'center',
          gap: 6,
          padding: '4px 8px',
          cursor: multiWindow ? 'pointer' : 'default',
          borderRadius: 4,
          color: hasAttention ? 'var(--accent-attention)' : 'var(--text-primary)',
          fontSize: 13,
          fontWeight: 500,
        }}
        onClick={() => { if (multiWindow) setExpanded((x) => !x) }}
      >
        {multiWindow && (
          <span style={{ fontSize: 10, color: 'var(--text-muted)', width: 10 }}>
            {expanded ? '\u25BE' : '\u25B8'}
          </span>
        )}
        <span style={{ flex: 1, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
          {s.session.name}
          {multiWindow && (
            <span style={{ color: 'var(--text-muted)', fontWeight: 400 }}> ({s.windows.length})</span>
          )}
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

  const filtered = useMemo(() => {
    const match = (s: SessionWithWindows) => {
      if (!filter) return true
      const q = filter.toLowerCase()
      return (
        s.session.name.toLowerCase().includes(q) ||
        s.windows.some(
          (w) =>
            w.window.name.toLowerCase().includes(q) ||
            w.branch.toLowerCase().includes(q) ||
            w.window.path.toLowerCase().includes(q),
        )
      )
    }
    return {
      needs_attention: sessions.needs_attention.filter(match),
      active: sessions.active.filter(match),
      idle: sessions.idle.filter(match),
    }
  }, [sessions, filter])

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
