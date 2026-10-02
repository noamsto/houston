import { useEffect, useRef, useState } from 'react'
import type { FormEvent } from 'react'
import { submitDispatcher, type DispatchOptions, type DispatcherOutcome } from '../api/dispatch'
import type { Run } from '../api/runs'
import { buildDispatcherRequest, MAX_TASKS, taskRowsProblem } from './dispatcherForm'
import { runHash } from './routes'

export function DispatcherForm({ options, repo, runs }: { options: DispatchOptions; repo: string; runs: Run[] }) {
  const engines = options.dispatcher_engines ?? []
  const [rows, setRows] = useState<string[]>([''])
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
  const problem = taskRowsProblem(rows)
  const disabledReason =
    engines.length === 0
      ? (options.dispatcher_engines_error ?? 'No dispatcher engines available')
      : problem
  const canSubmit = !submitting && !disabledReason

  function setRow(i: number, text: string): void {
    setRows((rs) => rs.map((r, j) => (j === i ? text : r)))
  }

  function chooseEngine(eng: string): void {
    setChosenEngine(eng)
    setModel('')
  }

  async function handleSubmit(e: FormEvent): Promise<void> {
    e.preventDefault()
    if (!canSubmit) return
    setSubmitting(true)
    const result = await submitDispatcher(buildDispatcherRequest({ repo, rows, engine, model, effort }))
    setSubmitting(false)
    setOutcome(result)
    if (result.kind === 'started') setRows([''])
  }

  const startedRun = outcome?.kind === 'started' ? runs.find((r) => r.id === outcome.runId) : undefined

  return (
    <>
      <form className="dispatch-form" onSubmit={(e) => { void handleSubmit(e) }}>
        {rows.map((text, i) => (
          <div key={i} className="dispatch-field dispatch-task-row">
            <label htmlFor={`dispatcher-task-${i}`}>{`Task ${i + 1}`}</label>
            <div className="dispatch-task-input">
              <textarea
                id={`dispatcher-task-${i}`}
                value={text}
                disabled={submitting}
                rows={3}
                onChange={(e) => setRow(i, e.target.value)}
              />
              {rows.length > 1 && (
                <button
                  type="button"
                  className="dispatch-task-remove"
                  aria-label={`Remove task ${i + 1}`}
                  disabled={submitting}
                  onClick={() => setRows((rs) => rs.filter((_, j) => j !== i))}
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
          onClick={() => setRows((rs) => [...rs, ''])}
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
              <p><a href={runHash(startedRun.id)}>Open run</a></p>
            ) : (
              <p className="dispatch-hint">Waiting for it to appear in Fleet…</p>
            )}
          </div>
        )}

        {outcome?.kind === 'failed' && (
          <div className="dispatch-result failure">
            <pre>{outcome.error}</pre>
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
