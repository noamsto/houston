import type { Run } from '../api/runs'
import { isFresh, isHistory, needsYou } from './staleness'

function agoLabel(updatedAt: number, now: number): string {
  if (!updatedAt) return '—'
  const s = Math.max(0, Math.floor(now / 1000 - updatedAt))
  if (s < 60) return `${s}s`
  const m = Math.floor(s / 60)
  if (m < 60) return `${m}m`
  const h = Math.floor(m / 60)
  if (h < 24) return `${h}h`
  return `${Math.floor(h / 24)}d`
}

/** The one line describing what this run is doing right now. */
function subtitle(run: Run): string {
  const a = run.activity
  if (run.state === 'blocked') return a.message || 'waiting on you'
  if (a.tool) return a.hint ? `${a.tool} · ${a.hint}` : a.tool
  if (a.task) return a.task
  if (run.pr) return `PR #${run.pr.number}${run.pr.check_state ? ` · ${run.pr.check_state}` : ''}`
  return run.state
}

/** repo/branch when both are present, otherwise whichever half exists. */
function nameLabel(run: Run): string {
  if (run.repo && run.branch) return `${run.repo}/${run.branch}`
  return run.repo || run.branch || run.id
}

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
        {run.crew && <span className="run-chip">{run.crew.name}</span>}
      </div>
    </button>
  )
}
