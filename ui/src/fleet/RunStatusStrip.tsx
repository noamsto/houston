import { useState } from 'react'
import type { Run } from '../api/runs'
import { agoLabel } from './format'
import { ReplyComposer } from './ReplyComposer'
import { switchRunTab } from './nav'
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

function tokensLabel(n: number) {
  const k = Math.round(n / 1000)
  return k >= 1000 ? `${+(n / 1_000_000).toFixed(1)}M` : n >= 1000 ? `${k}k` : `${n}`
}

function ContextMeter({ context, spend }: { context?: Run['context']; spend?: number }) {
  const { used = 0, limit } = context ?? {}
  const pct = limit ? Math.min(100, Math.round((used / limit) * 100)) : null
  const label = limit ? `${tokensLabel(used)} / ${tokensLabel(limit)} ctx` : `${tokensLabel(used)} ctx`
  return (
    <div className="run-status-line run-status-ctx">
      {context && <span className="run-status-ctx-label">{label}</span>}
      {pct !== null && (
        <span
          className="run-status-ctx-bar"
          role="progressbar"
          aria-label="Context window"
          aria-valuemin={0}
          aria-valuemax={100}
          aria-valuenow={pct}
        >
          <span className="run-status-ctx-fill" style={{ width: `${pct}%` }} />
        </span>
      )}
      {spend !== undefined && <span className="run-status-spend">${spend.toFixed(2)}</span>}
    </div>
  )
}

export function RunStatusStrip({ run, now }: { run: Run; now: number }) {
  const blocked = run.state === 'blocked'
  const needsClass = needsYou(run, now) ? ' needs-you' : blocked && !isFresh(run, now) ? ' needs-you muted' : ''
  const { tool, hint, turn } = run.activity
  const crew = run.crew
  const crewInfo = crew && (crew.tier || crew.model) ? [crew.tier, run.agent, crew.model].filter(Boolean).join(' · ') : ''
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
      {(run.context || run.spend_usd !== undefined) && <ContextMeter context={run.context} spend={run.spend_usd} />}
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

/** `answeredHere`: this view already offers the answer, so the pane reply
 *  shortcut would be a second, competing affordance. */
export function RunQuestion({ run, onTerminal = false, answeredHere = false }: {
  run: Run
  onTerminal?: boolean
  answeredHere?: boolean
}) {
  if (!run.question) return null
  const blocked = run.state === 'blocked'
  const { via } = run.question
  return (
    <>
      <div className="run-detail-question">{run.question.text}</div>
      {blocked && via === 'crew' && <ReplyComposer runId={run.id} />}
      {blocked && via === 'pane' && run.caps.terminal && !onTerminal && !answeredHere && (
        <button
          type="button"
          className="run-detail-question-reply"
          onClick={() => switchRunTab(run.id, 'terminal')}
        >
          Reply in Terminal
        </button>
      )}
    </>
  )
}
