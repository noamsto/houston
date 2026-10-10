import { memo, useState } from 'react'
import type { Run } from '../api/runs'
import { agoLabel } from './format'
import { COLLAPSE_OVER, MEMBER_CAP } from './fleetEntries'
import { PRChip } from './RunCard'
import { isFresh } from './staleness'

const ATTENTION_LABEL = { 'needs-you': 'needs you', stuck: 'stuck', done: 'done' } as const

interface WorkerRowProps {
  run: Run
  now: number
  onOpen?: (r: Run) => void
  selected?: boolean
}

export const WorkerRow = memo(function WorkerRow({ run, now, onOpen, selected }: WorkerRowProps) {
  const stale = !isFresh(run, now)
  // The server flags every blocked run needs-you; deriving it keeps the row
  // in step with the group's count when a run carries only the state.
  const attention = run.attention ?? (run.state === 'blocked' ? 'needs-you' : undefined)
  const meta = [run.crew?.tier, run.agent, run.crew?.model].filter(Boolean).join(' · ')
  return (
    <div className={`worker-row${attention ? ` ${attention}` : ''}${attention && stale ? ' muted' : ''}${selected ? ' selected' : ''}`}>
      <button
        type="button"
        className="worker-row-main"
        aria-current={selected ? 'true' : undefined}
        onClick={() => onOpen?.(run)}
      >
        <span className="worker-row-head">
          <span className="worker-row-swatch" style={{ background: run.crew?.color || 'var(--text-faint)' }} />
          <span className="worker-row-codename">{run.crew?.codename || 'worker'}</span>
          <span className="worker-row-title">{run.crew?.title || run.issue?.title || run.branch}</span>
          <span className={`run-age${stale ? ' stale' : ''}`}>{agoLabel(run.updated_at, now)}</span>
        </span>
        <span className="worker-row-sub">
          <span className="run-dot" style={{ background: `var(--state-${run.state}, var(--text-faint))` }} />
          {attention && <span className={`run-chip worker-row-attention ${attention}`}>{ATTENTION_LABEL[attention]}</span>}
          {!attention && run.state === 'idle' && <span className="run-chip worker-row-idle">idle</span>}
          <span className="worker-row-meta">{meta}</span>
        </span>
      </button>
      {run.pr && (
        <span className="worker-row-pr">
          <PRChip pr={run.pr} />
        </span>
      )}
    </div>
  )
})

interface MemberRowsProps {
  shown: Run[]
  now: number
  onOpen?: (r: Run) => void
  selectedId?: string
}

/** A group's worker rows; past COLLAPSE_OVER only MEMBER_CAP render until expanded. */
export function MemberRows({ shown, now, onOpen, selectedId }: MemberRowsProps) {
  const [expanded, setExpanded] = useState(false)
  const collapsible = shown.length > COLLAPSE_OVER
  const rows = collapsible && !expanded ? shown.slice(0, MEMBER_CAP) : shown
  return (
    <div className="fleet-group-members">
      {rows.map((r) => (
        <WorkerRow key={r.id} run={r} now={now} onOpen={onOpen} selected={selectedId === r.id} />
      ))}
      {collapsible && (
        <button type="button" className="fleet-group-more" aria-expanded={expanded} onClick={() => setExpanded(!expanded)}>
          {expanded ? 'Show less' : `Show ${shown.length - MEMBER_CAP} more`}
        </button>
      )}
    </div>
  )
}
