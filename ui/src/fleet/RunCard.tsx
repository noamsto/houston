import type { Run } from '../api/runs'
import { isFresh, isHistory, needsYou } from './staleness'
import { agoLabel, nameLabel, subtitle } from './format'
import { projectOf } from './fleetList'

// The card is a <button>, so a link inside it must not bubble to onOpen.
function PRChip({ pr }: { pr: NonNullable<Run['pr']> }) {
  const cls = `run-chip pr${pr.check_state === 'failure' ? ' failing' : ''}`
  const label = pr.number ? `#${pr.number}` : 'PR'
  if (!pr.url || !/^https?:\/\//i.test(pr.url)) return <span className={cls}>{label}</span>
  return (
    <a className={cls} href={pr.url} target="_blank" rel="noreferrer" onClick={(e) => e.stopPropagation()}>
      {label}
    </a>
  )
}

interface RunCardProps {
  run: Run
  now: number
  onOpen?: (r: Run) => void
  selected?: boolean
  crewLine?: string | null
}

export function RunCard({ run, now, onOpen, selected, crewLine }: RunCardProps) {
  const attention = needsYou(run, now)
  // Stale blocked runs are never in the sort's attention bucket, but they
  // still need a visible marker wherever they land — a muted version of the
  // same border, not a whole new signal.
  const staleBlocked = run.state === 'blocked' && !isFresh(run, now)
  const history = isHistory(run, now)
  const stale = !isFresh(run, now)
  // Transport-stale (the session's control connection is down) is distinct
  // from time-stale (updated_at is old); a run can be both, either, or neither.
  const connStale = run.stale === true

  const attentionClass = attention ? ' attention' : staleBlocked ? ' attention muted' : ''

  return (
    <button
      type="button"
      className={`run-card${attentionClass}${history ? ' history' : ''}${selected ? ' selected' : ''}`}
      aria-current={selected ? 'true' : undefined}
      onClick={() => onOpen?.(run)}
      style={{ '--run-accent': run.crew?.color } as React.CSSProperties}
    >
      <div className="run-head">
        <span className="run-dot" style={{ background: `var(--state-${run.state}, var(--text-faint))` }} />
        <span className="run-project">{projectOf(run)}</span>
        <span className="run-name">{run.branch || nameLabel(run)}</span>
        <span className={`run-age${stale ? ' stale' : ''}`}>{agoLabel(run.updated_at, now)}</span>
      </div>

      {run.crew?.title && <div className="run-title">{run.crew.title}</div>}

      {!(run.question && subtitle(run) === run.question.text) && <div className="run-sub">{subtitle(run)}</div>}

      {run.crew?.detail && run.crew.detail !== run.question?.text && <div className="run-card-detail">{run.crew.detail}</div>}

      {crewLine && <div className="run-crew">{crewLine}</div>}

      {run.question && <div className="run-question">{run.question.text}</div>}

      <div className="run-chips">
        {run.role && <span className={`run-role ${run.role}`}>{run.role}</span>}
        <span className="run-chip">{run.agent}</span>
        {run.issue && <span className="run-chip issue">{run.issue.id}</span>}
        {run.pr && <PRChip pr={run.pr} />}
        {run.crew?.codename && run.role !== 'dispatcher' && <span className="run-chip codename">{run.crew.codename}</span>}
        {run.crew?.tier && <span className="run-chip tier">{run.crew.tier}</span>}
        {run.crew?.model && <span className="run-chip model">{run.crew.model}</span>}
        {connStale && <span className="run-chip stale">stale</span>}
      </div>
    </button>
  )
}
