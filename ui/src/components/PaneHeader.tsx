import type { AgentType, ResultType, WSMeta } from '../api/types'

interface Props {
  target: string
  meta: WSMeta | null
  connected: boolean
  onClose: () => void
  onResize?: () => void
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

export function PaneHeader({ target, meta, connected, onClose, onResize }: Props) {
  const icon = meta ? (AGENT_ICONS[meta.agent] ?? '◆') : '·'
  const color = connected ? statusColor(meta?.status) : 'var(--accent-error)'
  const modeBadge = meta?.mode === 'normal' ? 'NOR' : meta?.mode === 'insert' ? 'INS' : null
  // Primary: session + window name (e.g. "mono — pl-521-add-user...")
  // Fallback: target string (e.g. "mono:5.1")
  const session = target.split(':')[0] ?? target
  const windowName = meta?.window_name
  const title = windowName ? `${session} — ${windowName}` : target
  const activity = meta?.activity

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
          overflow: 'hidden',
          textOverflow: 'ellipsis',
          whiteSpace: 'nowrap',
          color: 'var(--text-primary)',
          fontFamily: 'var(--font-mono)',
          flexShrink: 1,
          minWidth: 0,
        }}
      >
        {title}
      </span>
      {activity && (
        <span
          style={{
            flex: 1,
            overflow: 'hidden',
            textOverflow: 'ellipsis',
            whiteSpace: 'nowrap',
            color: 'var(--text-muted)',
            fontFamily: 'var(--font-mono)',
            fontSize: 10,
            minWidth: 0,
          }}
        >
          {activity}
        </span>
      )}

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

      {onResize && (
        <button
          onClick={(e) => {
            e.stopPropagation()
            onResize()
          }}
          title="Resize tmux to match browser"
          style={{
            background: 'none',
            border: 'none',
            color: 'var(--text-muted)',
            cursor: 'pointer',
            fontSize: 11,
            lineHeight: 1,
            padding: '0 2px',
            flexShrink: 0,
          }}
        >
          ⊞
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
