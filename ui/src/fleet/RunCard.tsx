import type { Run } from '../api/runs'
import { isFresh, isHistory, needsYou } from './staleness'
import { agoLabel, nameLabel, subtitle } from './format'

export function RunCard({ run, now, onOpen }: { run: Run; now: number; onOpen?: (r: Run) => void }) {
  const attention = needsYou(run, now)
  // Stale blocked runs are never in the sort's attention bucket, but they
  // still need a visible marker wherever they land — a muted version of the
  // same border, not a whole new signal.
  const staleBlocked = run.state === 'blocked' && !isFresh(run, now)
  const history = isHistory(run, now)
  const stale = !isFresh(run, now)

  const attentionClass = attention ? ' attention' : staleBlocked ? ' attention muted' : ''

  return (
    <button
      type="button"
      className={`run-card${attentionClass}${history ? ' history' : ''}`}
      onClick={() => onOpen?.(run)}
      style={{ '--run-accent': run.crew?.color } as React.CSSProperties}
    >
      <div className="run-head">
        <span className="run-dot" style={{ background: `var(--state-${run.state}, var(--text-faint))` }} />
        <span className="run-name">{nameLabel(run)}</span>
        <span className={`run-age${stale ? ' stale' : ''}`}>{agoLabel(run.updated_at, now)}</span>
      </div>

      <div className="run-sub">{subtitle(run)}</div>

      {run.question && <div className="run-question">{run.question.text}</div>}

      <div className="run-chips">
        <span className="run-chip">{run.agent}</span>
        {run.issue && <span className="run-chip issue">{run.issue.id}</span>}
        {run.pr && (
          <span className={`run-chip pr${run.pr.check_state === 'failure' ? ' failing' : ''}`}>
            #{run.pr.number}
          </span>
        )}
        {run.crew?.codename && <span className="run-chip codename">{run.crew.codename}</span>}
        {run.crew?.tier && <span className="run-chip tier">{run.crew.tier}</span>}
      </div>
    </button>
  )
}
