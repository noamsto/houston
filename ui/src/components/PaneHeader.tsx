import type { AgentType, ResultType, WSMeta } from '../api/types'

interface Props {
  target: string
  meta: WSMeta | null
  onClose: () => void
  wideMode?: boolean
  onToggleWide?: () => void
}

const AGENT_ICONS: Record<AgentType, string> = {
  'claude-code': '✦',
  'amp': '⚡',
  'generic': '◆',
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

export function PaneHeader({ target, meta, onClose, wideMode, onToggleWide }: Props) {
  const icon = meta ? (AGENT_ICONS[meta.agent] ?? '◆') : '·'
  const color = statusColor(meta?.status)
  const modeBadge = meta?.mode === 'normal' ? 'NOR' : meta?.mode === 'insert' ? 'INS' : null
  // Show activity text if available, otherwise show the window portion of target
  const label = meta?.activity || (target.split(':')[1] ?? target)

  return (
    <div
      style={{
        display: 'flex',
        alignItems: 'center',
        gap: 6,
        padding: '0 8px',
        height: 24,
        background: 'var(--bg-header)',
        borderBottom: '1px solid var(--border)',
        fontSize: 11,
        flexShrink: 0,
        userSelect: 'none',
      }}
    >
      <span style={{ color, flexShrink: 0 }}>{icon}</span>
      <span
        style={{
          flex: 1,
          overflow: 'hidden',
          textOverflow: 'ellipsis',
          whiteSpace: 'nowrap',
          color: 'var(--text-secondary)',
          fontFamily: 'var(--font-mono)',
        }}
      >
        {label}
      </span>

      {modeBadge && (
        <span
          style={{
            fontSize: 9,
            fontFamily: 'var(--font-mono)',
            color: 'var(--text-muted)',
            border: '1px solid var(--border)',
            borderRadius: 2,
            padding: '0 3px',
            flexShrink: 0,
          }}
        >
          {modeBadge}
        </span>
      )}

      {onToggleWide && (
        <button
          onClick={(e) => {
            e.stopPropagation()
            onToggleWide()
          }}
          title={wideMode ? 'Fit to screen' : 'Wide terminal (~120 cols)'}
          style={{
            background: 'none',
            border: '1px solid var(--border)',
            borderRadius: 2,
            color: wideMode ? 'var(--text-secondary)' : 'var(--text-muted)',
            cursor: 'pointer',
            fontSize: 9,
            fontFamily: 'var(--font-mono)',
            lineHeight: 1,
            padding: '1px 3px',
            flexShrink: 0,
          }}
        >
          {wideMode ? 'WIDE' : 'FIT'}
        </button>
      )}

      <button
        onClick={(e) => {
          e.stopPropagation()
          onClose()
        }}
        style={{
          background: 'none',
          border: 'none',
          color: 'var(--text-muted)',
          cursor: 'pointer',
          fontSize: 14,
          lineHeight: 1,
          padding: '0 2px',
          flexShrink: 0,
        }}
      >
        ×
      </button>
    </div>
  )
}
