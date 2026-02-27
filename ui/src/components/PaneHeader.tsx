import type { AgentType, ResultType, WSMeta } from '../api/types'

interface Props {
  target: string
  meta: WSMeta | null
  onClose: () => void
  wideMode?: boolean
  onToggleWide?: () => void
  onCopy?: () => void
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

export function PaneHeader({ target, meta, onClose, wideMode, onToggleWide, onCopy }: Props) {
  const icon = meta ? (AGENT_ICONS[meta.agent] ?? '◆') : '·'
  const color = statusColor(meta?.status)
  const modeBadge = meta?.mode === 'normal' ? 'NOR' : meta?.mode === 'insert' ? 'INS' : null
  // Show activity text if available, otherwise show the window portion of target
  const label = meta?.activity || (target.split(':')[1] ?? target)
  const isMobile = !!onToggleWide // mobile passes onToggleWide, desktop doesn't

  const headerBtn: React.CSSProperties = isMobile
    ? {
        background: 'none',
        border: '1px solid var(--border)',
        borderRadius: 4,
        cursor: 'pointer',
        fontFamily: 'var(--font-mono)',
        fontSize: 12,
        lineHeight: 1,
        padding: '6px 10px',
        flexShrink: 0,
      }
    : {
        background: 'none',
        border: '1px solid var(--border)',
        borderRadius: 2,
        cursor: 'pointer',
        fontFamily: 'var(--font-mono)',
        fontSize: 9,
        lineHeight: 1,
        padding: '1px 3px',
        flexShrink: 0,
      }

  return (
    <div
      style={{
        display: 'flex',
        alignItems: 'center',
        gap: isMobile ? 8 : 6,
        padding: isMobile ? '0 10px' : '0 8px',
        height: isMobile ? 36 : 24,
        background: 'var(--bg-header)',
        borderBottom: '1px solid var(--border)',
        fontSize: isMobile ? 13 : 11,
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
            fontSize: isMobile ? 11 : 9,
            fontFamily: 'var(--font-mono)',
            color: 'var(--text-muted)',
            border: '1px solid var(--border)',
            borderRadius: isMobile ? 4 : 2,
            padding: isMobile ? '4px 6px' : '0 3px',
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
            ...headerBtn,
            color: wideMode ? 'var(--text-secondary)' : 'var(--text-muted)',
          }}
        >
          {wideMode ? 'WIDE' : 'FIT'}
        </button>
      )}

      {onCopy && (
        <button
          onClick={(e) => {
            e.stopPropagation()
            onCopy()
          }}
          title="Copy terminal text"
          style={{
            ...headerBtn,
            color: 'var(--text-muted)',
          }}
        >
          CPY
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
          fontSize: isMobile ? 20 : 14,
          lineHeight: 1,
          padding: isMobile ? '4px 6px' : '0 2px',
          flexShrink: 0,
        }}
      >
        ×
      </button>
    </div>
  )
}
