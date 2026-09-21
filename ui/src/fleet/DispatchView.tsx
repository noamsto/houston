import { useCallback, useEffect, useRef, useState } from 'react'
import type { FormEvent } from 'react'
import { fetchDispatchOptions, submitDispatch, type DispatchOptions, type DispatchOutcome } from '../api/dispatch'
import './fleet.css'

export function DispatchView() {
  const [options, setOptions] = useState<DispatchOptions | null>(null)
  const [optionsError, setOptionsError] = useState<string | null>(null)
  const [loadingOptions, setLoadingOptions] = useState(true)

  const [repo, setRepo] = useState('')
  const [title, setTitle] = useState('')
  const [spec, setSpec] = useState('')
  const [tier, setTier] = useState('standard')
  const [engine, setEngine] = useState('claude')
  const [model, setModel] = useState('')
  const [effort, setEffort] = useState('high')
  const [crew, setCrew] = useState('')
  const [issue, setIssue] = useState('')

  const [submitting, setSubmitting] = useState(false)
  const [outcome, setOutcome] = useState<DispatchOutcome | null>(null)
  const resultRef = useRef<HTMLDivElement>(null)

  // The result lands below the submit button — off-screen on a phone.
  useEffect(() => {
    if (outcome) resultRef.current?.scrollIntoView({ block: 'nearest', behavior: 'smooth' })
  }, [outcome])

  // Pure fetch: every setState it makes happens inside a promise callback,
  // never synchronously in the caller's tick, so it's safe to invoke directly
  // from the mount effect as well as from the Retry button's click handler.
  const runFetch = useCallback(() => {
    let cancelled = false
    fetchDispatchOptions()
      .then((o) => {
        if (cancelled) return
        setOptions(o)
        const firstRepo = o.repos[0]
        setRepo(firstRepo?.path ?? '')
        setCrew(firstRepo?.crews[0] ?? '')
        const firstEngine = o.engine_order[0] ?? ''
        setEngine(firstEngine)
        setModel(o.engines[firstEngine]?.[0] ?? '')
        setTier(o.tiers.includes('standard') ? 'standard' : (o.tiers[0] ?? ''))
        setEffort(o.efforts.includes('high') ? 'high' : (o.efforts[0] ?? ''))
        setLoadingOptions(false)
      })
      .catch((e: unknown) => {
        if (cancelled) return
        setOptionsError(e instanceof Error ? e.message : String(e))
        setLoadingOptions(false)
      })
    return () => { cancelled = true }
  }, [])

  useEffect(() => runFetch(), [runFetch])

  function retryOptions(): void {
    setLoadingOptions(true)
    setOptionsError(null)
    runFetch()
  }

  const selectedRepo = options?.repos.find((r) => r.path === repo)
  const models = options?.engines[engine] ?? []

  function chooseRepo(path: string): void {
    setRepo(path)
    const r = options?.repos.find((x) => x.path === path)
    setCrew(r?.crews[0] ?? '')
  }

  function chooseEngine(eng: string): void {
    setEngine(eng)
    setModel(options?.engines[eng]?.[0] ?? '')
  }

  async function handleSubmit(e: FormEvent): Promise<void> {
    e.preventDefault()
    if (submitting || !title.trim() || !crew) return
    setSubmitting(true)
    const result = await submitDispatch({
      repo,
      title: title.trim(),
      spec: spec || undefined,
      tier,
      engine,
      model,
      effort,
      crew,
      issue: issue.trim() || undefined,
    })
    setSubmitting(false)
    setOutcome(result)
    if (result.kind === 'started') {
      setTitle('')
      setSpec('')
    }
  }

  return (
    <div className="dispatch mocha">
      <header className="fleet-nav">
        <h1>Dispatch</h1>
      </header>

      {loadingOptions && <div className="fleet-empty">Loading dispatch options…</div>}

      {!loadingOptions && optionsError && (
        <div className="fleet-empty">
          {optionsError}
          <div><button type="button" onClick={retryOptions}>Retry</button></div>
        </div>
      )}

      {!loadingOptions && options && (
        <form className="dispatch-form" onSubmit={(e) => { void handleSubmit(e) }}>
          <div className="dispatch-field">
            <label htmlFor="dispatch-repo">Repo</label>
            <select id="dispatch-repo" value={repo} onChange={(e) => chooseRepo(e.target.value)}>
              {options.repos.map((r) => (
                <option key={r.path} value={r.path}>{r.name}</option>
              ))}
            </select>
          </div>

          <div className="dispatch-field">
            <label htmlFor="dispatch-title">Title</label>
            <input
              id="dispatch-title"
              type="text"
              value={title}
              maxLength={200}
              disabled={submitting}
              onChange={(e) => setTitle(e.target.value)}
            />
          </div>

          <div className="dispatch-field">
            <label htmlFor="dispatch-spec">Task</label>
            <textarea
              id="dispatch-spec"
              value={spec}
              disabled={submitting}
              onChange={(e) => setSpec(e.target.value)}
            />
            <span className="dispatch-hint">Up to 64 KB. Falls back to the title when left empty.</span>
          </div>

          <div className="dispatch-field">
            <label htmlFor="dispatch-tier">Tier</label>
            <select id="dispatch-tier" value={tier} onChange={(e) => setTier(e.target.value)}>
              {options.tiers.map((t) => <option key={t} value={t}>{t}</option>)}
            </select>
          </div>

          <div className="dispatch-field">
            <label htmlFor="dispatch-engine">Engine</label>
            <select id="dispatch-engine" value={engine} onChange={(e) => chooseEngine(e.target.value)}>
              {options.engine_order.map((eng) => <option key={eng} value={eng}>{eng}</option>)}
            </select>
          </div>

          <div className="dispatch-field">
            <label htmlFor="dispatch-model">Model</label>
            <select id="dispatch-model" value={model} onChange={(e) => setModel(e.target.value)}>
              {models.map((m) => <option key={m} value={m}>{m}</option>)}
            </select>
          </div>

          <div className="dispatch-field">
            <label htmlFor="dispatch-effort">Effort</label>
            <select id="dispatch-effort" value={effort} onChange={(e) => setEffort(e.target.value)}>
              {options.efforts.map((ef) => <option key={ef} value={ef}>{ef}</option>)}
            </select>
          </div>

          <div className="dispatch-field">
            <label htmlFor="dispatch-crew">Crew</label>
            {selectedRepo && selectedRepo.crews.length > 0 ? (
              <select id="dispatch-crew" value={crew} onChange={(e) => setCrew(e.target.value)}>
                {selectedRepo.crews.map((c) => <option key={c} value={c}>{c}</option>)}
              </select>
            ) : (
              <p className="dispatch-hint">No crews for this repo — start one with <code>crew new</code> in that repo.</p>
            )}
          </div>

          <div className="dispatch-field">
            <label htmlFor="dispatch-issue">Issue (optional)</label>
            <input
              id="dispatch-issue"
              type="text"
              value={issue}
              disabled={submitting}
              onChange={(e) => setIssue(e.target.value)}
            />
          </div>

          <button type="submit" className="dispatch-submit" disabled={submitting || !title.trim() || !crew}>
            {submitting ? 'Dispatching…' : 'Dispatch'}
          </button>
        </form>
      )}

      <div ref={resultRef}>
        {outcome?.kind === 'started' && (
          <div className="dispatch-result success">
            <p>Worker <strong>{outcome.workerId}</strong> started on <code>{outcome.branch}</code>.</p>
            {outcome.issueUrl && (
              <p><a href={outcome.issueUrl} target="_blank" rel="noreferrer">View issue</a></p>
            )}
            <p className="dispatch-hint">It will appear in Fleet shortly.</p>
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
    </div>
  )
}
