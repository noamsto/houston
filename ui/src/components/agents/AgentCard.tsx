import { useEffect, useState, useCallback, memo } from 'react'
import type { AgentState, SessionView } from '../../api/types'

const stateLabel: Record<AgentState, string> = {
  'starting':          'Starting',
  'thinking':          'Thinking',
  'tool-running':      'Tool Running',
  'waiting':           'Awaiting Input',
  'waiting:permission':'Permission Needed',
  'compacting':        'Compacting',
  'ended':             'Ended',
}

function displayName(v: SessionView): string {
  return v.tmux_session || v.cwd?.split('/').pop() || v.session_id.slice(0, 4)
}

function isWaiting(state: AgentState): boolean {
  return state === 'waiting' || state === 'waiting:permission'
}

interface Props {
  view: SessionView
  onOpen?: (view: SessionView) => void
}

export const AgentCard = memo(function AgentCard({ view, onOpen }: Props) {
  const [peek, setPeek] = useState(false)
  const elapsed = useElapsed(view.since ?? view.updated_at)

  const togglePeek = useCallback((e: React.MouseEvent) => {
    e.stopPropagation()
    setPeek((p) => !p)
  }, [])

  const activity = buildActivity(view)
  const name = displayName(view)
  const windowName = view.tmux_window ? `window :${view.tmux_window}` : ''

  return (
    <article
      className="card"
      data-state={view.state}
      onClick={() => onOpen?.(view)}
    >
      <div className="card-head">
        <span className="status-label">
          <span className="status-dot" aria-hidden />
          {stateLabel[view.state] ?? view.state}
        </span>
        <span className="elapsed">{formatElapsed(elapsed)}</span>
      </div>

      <div className="card-id">
        <div className="agent-glyph">{name.slice(0, 1).toUpperCase()}</div>
        <div className="id-meta">
          <div className="id-agent">claude-code</div>
          <div className="id-session" title={view.session_id}>{name}</div>
          {windowName && <div className="id-window">{windowName}</div>}
        </div>
      </div>

      <div className="activity">{activity}</div>

      {view.trail && view.trail.length > 0 && (
        <div className="trail">
          {view.trail.map((chip, i) => (
            <span
              key={i}
              className={`chip ${chip.done ? 'done' : 'cur'} ${chip.error ? 'err' : ''}`}
              title={chip.hint}
            >
              <span className="g">{chip.tool.toLowerCase()}</span>
              {chip.hint && <span className="hint">{truncate(chip.hint, 28)}</span>}
            </span>
          ))}
        </div>
      )}

      {view.preview && (
        <div className={`preview ${peek ? 'peek' : ''}`} onClick={togglePeek}>
          <span className="peek-hint">{peek ? 'Collapse' : 'Peek'}</span>
          <pre>{view.preview}</pre>
        </div>
      )}

      <div className="card-foot">
        {isWaiting(view.state) ? (
          <div className="quick-actions">
            <button className="qa primary" onClick={(e) => { e.stopPropagation(); /* TODO: wire Y */ }}>
              Approve · Y
            </button>
            <button className="qa" onClick={(e) => { e.stopPropagation(); /* TODO: wire N */ }}>
              Deny · N
            </button>
            <button className="qa ghost" onClick={(e) => e.stopPropagation()}>⋯</button>
          </div>
        ) : (
          <div className="foot-stats">
            <span>Turn <em>{view.turn || 0}</em></span>
            <span>In <em>{fmtTokens(view.input_tokens)}</em></span>
            <span>Out <em>{fmtTokens(view.output_tokens)}</em></span>
          </div>
        )}
      </div>
    </article>
  )
})

function buildActivity(v: SessionView): React.ReactNode {
  if (v.state === 'tool-running' && v.tool) {
    return (
      <>
        <span className="verb">{v.tool}</span>
        {v.tool_input_hint ? <> <em>{v.tool_input_hint}</em></> : null}
      </>
    )
  }
  if (v.state === 'waiting:permission' && v.last_message) return v.last_message
  if (v.state === 'waiting')   return v.last_message || 'Awaiting your input.'
  if (v.state === 'thinking')  return 'Planning next step…'
  if (v.state === 'starting')  return 'Session starting…'
  if (v.state === 'compacting')return 'Compacting context…'
  if (v.state === 'ended')     return 'Session ended.'
  return ''
}

function fmtTokens(n: number | undefined): string {
  const x = n ?? 0
  if (x >= 1000) return `${(x / 1000).toFixed(1)}k`
  return String(x)
}

function truncate(s: string, n: number): string {
  return s.length <= n ? s : s.slice(0, n - 1) + '…'
}

function formatElapsed(seconds: number): string {
  if (seconds < 0) return '00:00'
  const m = Math.floor(seconds / 60)
  const s = Math.floor(seconds % 60)
  if (m < 60) return `${pad(m)}:${pad(s)}`
  const h = Math.floor(m / 60)
  return `${pad(h)}:${pad(m % 60)}:${pad(s)}`
}

function pad(n: number): string {
  return n < 10 ? `0${n}` : String(n)
}

function useElapsed(sinceUnix: number) {
  const [now, setNow] = useState(() => Date.now() / 1000)
  useEffect(() => {
    const id = window.setInterval(() => setNow(Date.now() / 1000), 1000)
    return () => window.clearInterval(id)
  }, [])
  return now - sinceUnix
}
