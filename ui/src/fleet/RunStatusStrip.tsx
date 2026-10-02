import { useState } from 'react'
import type { Run } from '../api/runs'
import { agoLabel } from './format'
import { ReplyComposer } from './ReplyComposer'
import { runHash } from './routes'
import { isFresh, needsYou } from './staleness'

type Task = NonNullable<Run['background']>[number]

function plural(n: number, word: string) {
  return `${n} ${word}${n === 1 ? '' : 's'}`
}

function bgSummary(tasks: Task[], now: number) {
  if (tasks.length === 1) return tasks[0].hint || tasks[0].id
  const monitors = tasks.filter((t) => t.kind === 'monitor').length
  const shells = tasks.length - monitors
  const counts = [monitors && plural(monitors, 'monitor'), shells && plural(shells, 'shell')].filter(Boolean)
  const since = Math.min(...tasks.map((t) => t.since || Infinity))
  return Number.isFinite(since) ? `${counts.join(' · ')} · oldest ${agoLabel(since, now)}` : counts.join(' · ')
}

function BackgroundTasks({ tasks, now }: { tasks: Task[]; now: number }) {
  const [open, setOpen] = useState(false)
  return (
    <div className="run-status-bg-wrap">
      <button
        type="button"
        className="run-status-bg-toggle"
        aria-expanded={open}
        onClick={() => setOpen((o) => !o)}
      >
        <span className="run-status-bg-chevron" aria-hidden="true">{open ? '▾' : '▸'}</span>
        <span className="run-status-bg-summary">{bgSummary(tasks, now)}</span>
      </button>
      {open && (
        <ul className="run-status-bg" aria-label="Background tasks">
          {tasks.map((t) => (
            <li key={t.id} className="run-status-bg-task">
              <span className="run-status-bg-kind">{t.kind}</span>
              <span className="run-status-bg-hint">{t.hint || t.id}</span>
              {!!t.since && <span className="run-status-bg-age">{agoLabel(t.since, now)}</span>}
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}

export function RunStatusStrip({ run, now }: { run: Run; now: number }) {
  const blocked = run.state === 'blocked'
  const needsClass = needsYou(run, now) ? ' needs-you' : blocked && !isFresh(run, now) ? ' needs-you muted' : ''
  const { tool, hint, turn } = run.activity
  const crew = run.crew
  const crewParts = crew ? [crew.tier, crew.model].filter(Boolean) : []
  const crewInfo = crewParts.length ? [crew?.tier, run.agent, crew?.model].filter(Boolean).join(' · ') : ''
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
      {!!run.background?.length && <BackgroundTasks tasks={run.background} now={now} />}
      {crew && (crewInfo || detail || (crew.codename && run.role !== 'dispatcher')) && (
        <div className="run-status-line run-status-crew">
          {crew.codename && run.role !== 'dispatcher' && <span className="run-status-codename">{crew.codename}</span>}
          {crewInfo && <span className="run-status-engine">{crewInfo}</span>}
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
