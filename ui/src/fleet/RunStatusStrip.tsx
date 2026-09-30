import type { Run } from '../api/runs'
import { agoLabel } from './format'
import { ReplyComposer } from './ReplyComposer'
import { runHash } from './routes'
import { isFresh, needsYou } from './staleness'

export function RunStatusStrip({ run, now }: { run: Run; now: number }) {
  const blocked = run.state === 'blocked'
  const needsClass = needsYou(run, now) ? ' needs-you' : blocked && !isFresh(run, now) ? ' needs-you muted' : ''
  const { tool, hint, turn } = run.activity
  const crew = run.crew
  const crewInfo = crew ? [crew.tier, run.agent, crew.model].filter(Boolean).join(' · ') : ''
  const detail = crew?.detail && crew.detail !== run.question?.text ? crew.detail : null

  return (
    <div className="run-status">
      <div className="run-status-line">
        <span
          className={`run-status-state${needsClass}`}
          style={{ color: `var(--state-${run.state}, var(--text-faint))` }}
        >
          {blocked ? 'needs you' : run.state}
        </span>
        {tool && !blocked && (
          <span className="run-status-tool">
            {tool}
            {hint && <span className="run-status-hint"> · {hint}</span>}
          </span>
        )}
        <span className="run-status-meta">
          {typeof turn === 'number' && <span>Turn {turn}</span>}
          <span>{agoLabel(run.updated_at, now)}</span>
        </span>
      </div>
      {crew && (
        <div className="run-status-line run-status-crew">
          {crew.codename && run.role !== 'dispatcher' && <span className="run-status-codename">{crew.codename}</span>}
          <span className="run-status-engine">{crewInfo}</span>
          {detail && <span className="run-status-detail">{detail}</span>}
        </div>
      )}
    </div>
  )
}

export function RunQuestion({ run, onTerminal = false }: { run: Run; onTerminal?: boolean }) {
  if (!run.question) return null
  const blocked = run.state === 'blocked'
  const { via } = run.question
  return (
    <>
      <div className="run-detail-question">{run.question.text}</div>
      {blocked && via === 'crew' && <ReplyComposer runId={run.id} />}
      {blocked && via === 'pane' && run.caps.terminal && !onTerminal && (
        <button
          type="button"
          className="run-detail-question-reply"
          onClick={() => { window.location.hash = runHash(run.id, 'terminal') }}
        >
          Reply in Terminal
        </button>
      )}
    </>
  )
}
