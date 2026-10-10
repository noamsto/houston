import { useEffect, useRef, useState } from 'react'
import type { FormEvent } from 'react'
import { submitDispatcher, type DispatchOptions, type DispatcherOutcome } from '../api/dispatch'
import type { Run } from '../api/runs'
import { buildDispatcherRequest, MAX_TASKS, taskRowsProblem } from './dispatcherForm'
import { runHash } from './routes'
import { onAppLink, openRun } from './nav'

interface TaskRow {
  id: number
  text: string
}

export function DispatcherForm({
  options,
  repo,
  runs,
  onStarted,
}: {
  options: DispatchOptions
  repo: string
  runs: Run[]
  onStarted: () => void
}) {
  const engines = options.dispatcher_engines ?? []
  const nextRowId = useRef(1)
  const [rows, setRows] = useState<TaskRow[]>([{ id: 0, text: '' }])
  const [chosenEngine, setChosenEngine] = useState('')
  const [model, setModel] = useState('')
  const [effort, setEffort] = useState('')
  const [submitting, setSubmitting] = useState(false)
  const [outcome, setOutcome] = useState<DispatcherOutcome | null>(null)
  const resultRef = useRef<HTMLDivElement>(null)

  // The result lands below the submit button — off-screen on a phone.
  useEffect(() => {
    if (outcome) resultRef.current?.scrollIntoView({ block: 'nearest', behavior: 'smooth' })
  }, [outcome])

  const engine = engines.includes(chosenEngine) ? chosenEngine : (engines[0] ?? '')
  const models = options.engines[engine] ?? []
  const problem = taskRowsProblem(rows.map((r) => r.text))
  const disabledReason =
    repo === ''
      ? 'Add a repo first'
      : engines.length === 0
        ? (options.dispatcher_engines_error ?? 'No dispatcher engines available')
        : problem
  const canSubmit = !submitting && !disabledReason

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
    const result = await submitDispatcher(
      buildDispatcherRequest({ repo, rows: rows.map((r) => r.text), engine, model, effort }),
    )
    setSubmitting(false)
    setOutcome(result)
    if (result.kind !== 'started') return
    setRows([newRow()])
    onStarted()
  }

  const startedRun = outcome?.kind === 'started' ? runs.find((r) => r.id === outcome.runId) : undefined

  return (
    <>
      <form className="dispatch-form" onSubmit={(e) => { void handleSubmit(e) }}>
        {rows.map((row, i) => (
          <div key={row.id} className="dispatch-field dispatch-task-row">
            <label htmlFor={`dispatcher-task-${row.id}`}>{`Task ${i + 1}`}</label>
            <div className="dispatch-task-input">
              <textarea
                id={`dispatcher-task-${row.id}`}
                value={row.text}
                disabled={submitting}
                rows={3}
                onChange={(e) => setRow(row.id, e.target.value)}
              />
              {rows.length > 1 && (
                <button
                  type="button"
                  className="dispatch-task-remove"
                  aria-label={`Remove task ${i + 1}`}
                  disabled={submitting}
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
          disabled={submitting || rows.length >= MAX_TASKS}
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
              disabled={submitting}
              onChange={(e) => chooseEngine(e.target.value)}
            >
              {engines.map((eng) => <option key={eng} value={eng}>{eng}</option>)}
            </select>
          </div>

          <div className="dispatch-field">
            <label htmlFor="dispatcher-model">Model</label>
            <select id="dispatcher-model" value={model} disabled={submitting} onChange={(e) => setModel(e.target.value)}>
              <option value="">launcher default</option>
              {models.map((m) => <option key={m} value={m}>{m}</option>)}
            </select>
          </div>

          <div className="dispatch-field">
            <label htmlFor="dispatcher-effort">Effort</label>
            <select id="dispatcher-effort" value={effort} disabled={submitting} onChange={(e) => setEffort(e.target.value)}>
              <option value="">launcher default</option>
              {options.efforts.map((ef) => <option key={ef} value={ef}>{ef}</option>)}
            </select>
          </div>
        </div>

        <button type="submit" className="dispatch-submit" disabled={!canSubmit}>
          {submitting ? 'Starting…' : 'Start dispatcher'}
        </button>
        {!submitting && disabledReason && <span className="dispatch-hint dispatch-error">{disabledReason}</span>}
      </form>

      <div ref={resultRef}>
        {outcome?.kind === 'started' && (
          <div className="dispatch-result success">
            <p>Dispatcher started in session <strong>{outcome.session}</strong>.</p>
            <p>Crew <code>{outcome.crew}</code></p>
            {startedRun ? (
              <p><a href={runHash(startedRun.id)} onClick={onAppLink(() => openRun(startedRun.id))}>Open run</a></p>
            ) : (
              <p className="dispatch-hint">Waiting for it to appear in Fleet…</p>
            )}
          </div>
        )}

        {outcome?.kind === 'failed' && (
          <div className="dispatch-result failure">
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
        )}
      </div>
    </>
  )
}
