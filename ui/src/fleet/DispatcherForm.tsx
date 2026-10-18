import { useRef, useState } from 'react'
import type { FormEvent } from 'react'
import type { DispatchOptions, DispatcherOutcome, DispatcherRequest } from '../api/dispatch'
import type { Run } from '../api/runs'
import { buildDispatcherRequest, MAX_TASKS, taskRowsProblem } from './dispatcherForm'
import { runIdFor } from './dispatchForm'
import { runHash } from './routes'
import { onAppLink, openRun } from './nav'

interface TaskRow {
  id: number
  text: string
}

// The parent owns the launch and its result, so a host switch that unmounts
// this form cannot lose either.
export function DispatcherForm({
  host,
  options,
  repo,
  busy,
  onSubmit,
  onStarted,
}: {
  host: string
  options: DispatchOptions
  repo: string
  busy: boolean
  onSubmit: (req: DispatcherRequest) => Promise<DispatcherOutcome>
  onStarted: (host: string) => void
}) {
  const engines = options.dispatcher_engines ?? []
  const nextRowId = useRef(1)
  const [rows, setRows] = useState<TaskRow[]>([{ id: 0, text: '' }])
  const [chosenEngine, setChosenEngine] = useState('')
  const [model, setModel] = useState('')
  const [effort, setEffort] = useState('')
  const [submitting, setSubmitting] = useState(false)

  const engine = engines.includes(chosenEngine) ? chosenEngine : (engines[0] ?? '')
  const models = options.engines[engine] ?? []
  const problem = taskRowsProblem(rows.map((r) => r.text))
  const disabledReason =
    repo === ''
      ? 'Add a repo first'
      : engines.length === 0
        ? (options.dispatcher_engines_error ?? 'No dispatcher engines available')
        : problem
  const canSubmit = !busy && !disabledReason

  function newRow(): TaskRow {
    return { id: nextRowId.current++, text: '' }
  }

  function setRow(id: number, text: string): void {
    setRows((rs) => rs.map((r) => (r.id === id ? { ...r, text } : r)))
  }

  function chooseEngine(eng: string): void {
    setChosenEngine(eng)
    setModel('')
  }

  async function handleSubmit(e: FormEvent): Promise<void> {
    e.preventDefault()
    if (!canSubmit) return
    setSubmitting(true)
    const result = await onSubmit(
      buildDispatcherRequest({ repo, rows: rows.map((r) => r.text), engine, model, effort }),
    )
    setSubmitting(false)
    if (result.kind !== 'started') return
    setRows([newRow()])
    onStarted(host)
  }

  return (
    <form className="dispatch-form" onSubmit={(e) => { void handleSubmit(e) }}>
      {rows.map((row, i) => (
        <div key={row.id} className="dispatch-field dispatch-task-row">
          <label htmlFor={`dispatcher-task-${row.id}`}>{`Task ${i + 1}`}</label>
          <div className="dispatch-task-input">
            <textarea
              id={`dispatcher-task-${row.id}`}
              value={row.text}
              disabled={busy}
              rows={3}
              onChange={(e) => setRow(row.id, e.target.value)}
            />
            {rows.length > 1 && (
              <button
                type="button"
                className="dispatch-task-remove"
                aria-label={`Remove task ${i + 1}`}
                disabled={busy}
                onClick={() => setRows((rs) => rs.filter((r) => r.id !== row.id))}
              >
                ×
              </button>
            )}
          </div>
        </div>
      ))}
      <button
        type="button"
        className="dispatch-add-task"
        disabled={busy || rows.length >= MAX_TASKS}
        onClick={() => setRows((rs) => [...rs, newRow()])}
      >
        Add task
      </button>

      <div className="dispatch-grid">
        <div className="dispatch-field">
          <label htmlFor="dispatcher-engine">Engine</label>
          <select
            id="dispatcher-engine"
            value={engine}
            disabled={busy}
            onChange={(e) => chooseEngine(e.target.value)}
          >
            {engines.map((eng) => <option key={eng} value={eng}>{eng}</option>)}
          </select>
        </div>

        <div className="dispatch-field">
          <label htmlFor="dispatcher-model">Model</label>
          <select id="dispatcher-model" value={model} disabled={busy} onChange={(e) => setModel(e.target.value)}>
            <option value="">launcher default</option>
            {models.map((m) => <option key={m} value={m}>{m}</option>)}
          </select>
        </div>

        <div className="dispatch-field">
          <label htmlFor="dispatcher-effort">Effort</label>
          <select id="dispatcher-effort" value={effort} disabled={busy} onChange={(e) => setEffort(e.target.value)}>
            <option value="">launcher default</option>
            {options.efforts.map((ef) => <option key={ef} value={ef}>{ef}</option>)}
          </select>
        </div>
      </div>

      <button type="submit" className="dispatch-submit" disabled={!canSubmit}>
        {submitting ? 'Starting…' : 'Start dispatcher'}
      </button>
      {!busy && disabledReason && <span className="dispatch-hint dispatch-error">{disabledReason}</span>}
    </form>
  )
}

/** A launch's result, on the host it was sent to (`currentHost` is the one
 *  the form now targets). */
export function DispatcherResult({
  host,
  currentHost,
  outcome,
  runs,
}: {
  host: string
  currentHost: string
  outcome: DispatcherOutcome
  runs: Run[]
}) {
  const hostNote = host !== currentHost && <p className="dispatch-hint">on {host}</p>
  if (outcome.kind === 'started') {
    const startedId = runIdFor(host, outcome.runId)
    const startedRun = startedId ? runs.find((r) => r.id === startedId) : undefined
    return (
      <div className="dispatch-result success">
        {hostNote}
        <p>Dispatcher started in session <strong>{outcome.session}</strong>.</p>
        <p>Crew <code>{outcome.crew}</code></p>
        {startedRun ? (
          <p><a href={runHash(startedRun.id)} onClick={onAppLink(() => openRun(startedRun.id))}>Open run</a></p>
        ) : (
          <p className="dispatch-hint">Waiting for it to appear in Fleet…</p>
        )}
      </div>
    )
  }
  return (
    <div className="dispatch-result failure">
      {hostNote}
      <pre>{outcome.error}</pre>
      {outcome.crew && <p>Crew <code>{outcome.crew}</code> was left behind.</p>}
      {(outcome.session || outcome.window || outcome.pane) && (
        <p className="dispatch-hint">
          {[
            outcome.session && `session ${outcome.session}`,
            outcome.window && `window ${outcome.window}`,
            outcome.pane && `pane ${outcome.pane}`,
          ].filter(Boolean).join(', ')}
        </p>
      )}
      {outcome.output && (
        <details>
          <summary>Output</summary>
          <pre>{outcome.output}</pre>
        </details>
      )}
    </div>
  )
}
