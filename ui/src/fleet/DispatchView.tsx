import { useCallback, useEffect, useRef, useState } from 'react'
import type { FormEvent } from 'react'
import { fetchDispatchOptions, submitDispatch, NEW_CREW, type DispatchOptions, type DispatchOutcome } from '../api/dispatch'
import type { Run } from '../api/runs'
import {
  crewAgeLabel,
  defaultCrew,
  defaultModel,
  findDispatchedRun,
  loadPrefs,
  resolveInitial,
  savePrefs,
  taskMaxHeight,
} from './dispatchForm'
import { runHash, useDispatchRoute } from './routes'
import { useNow } from './useNow'
import './fleet.css'

const UNKNOWN_REPO_NOTICE = "Linked repo isn't one houston knows — pick one below."

function autoGrow(el: HTMLTextAreaElement): void {
  el.style.height = 'auto'
  const cap = taskMaxHeight(window.visualViewport?.height ?? window.innerHeight)
  el.style.height = `${Math.min(Math.max(el.scrollHeight, 120), cap)}px`
  el.style.overflowY = el.scrollHeight > cap ? 'auto' : 'hidden'
}

export function DispatchView({ runs }: { runs: Run[] }) {
  const [options, setOptions] = useState<DispatchOptions | null>(null)
  const [optionsError, setOptionsError] = useState<string | null>(null)
  const [loadingOptions, setLoadingOptions] = useState(true)

  const [repo, setRepo] = useState('')
  const [title, setTitle] = useState('')
  const [spec, setSpec] = useState('')
  const [tier, setTier] = useState('')
  const [engine, setEngine] = useState('')
  const [model, setModel] = useState('')
  const [effort, setEffort] = useState('')
  const [crew, setCrew] = useState('')
  const [issue, setIssue] = useState('')
  const [linkNotice, setLinkNotice] = useState<string | null>(null)

  const [submitting, setSubmitting] = useState(false)
  const [outcome, setOutcome] = useState<DispatchOutcome | null>(null)
  const [outcomeWasNewCrew, setOutcomeWasNewCrew] = useState(false)
  const resultRef = useRef<HTMLDivElement>(null)
  const taskRef = useRef<HTMLTextAreaElement>(null)
  const now = useNow()

  const route = useDispatchRoute()
  const [appliedSeq, setAppliedSeq] = useState<number | null>(null)

  // Adjusted during render rather than in an effect: each hashchange (seq)
  // applies its link exactly once, and a link that arrived before the options
  // is held until they load. Only repo/crew are touched, so a draft survives.
  if (options && route && route.seq !== appliedSeq) {
    setAppliedSeq(route.seq)
    if (route.repo || route.crew) {
      const link = resolveInitial(options, { repo }, route)
      setRepo(link.repo)
      setCrew(link.crew)
      setLinkNotice(link.linkRepoUnknown ? UNKNOWN_REPO_NOTICE : null)
    }
  }

  // Once applied, drop the params so a reload doesn't re-apply a stale link
  // over a hand-edited repo/crew; following the same link again changes the
  // hash, which fires hashchange and bumps seq.
  const hasLinkParams = !!(route?.repo || route?.crew)
  useEffect(() => {
    if (route && hasLinkParams && route.seq === appliedSeq) {
      history.replaceState(null, '', '#/dispatch')
    }
  }, [route, hasLinkParams, appliedSeq])

  // The result lands below the submit button — off-screen on a phone.
  useEffect(() => {
    if (outcome) resultRef.current?.scrollIntoView({ block: 'nearest', behavior: 'smooth' })
  }, [outcome])

  useEffect(() => {
    if (taskRef.current) autoGrow(taskRef.current)
  }, [spec, options])

  // The cap tracks visualViewport height (keyboard open/close, rotation), so
  // it must re-run when that changes, not just when the textarea content does.
  useEffect(() => {
    const target: VisualViewport | Window = window.visualViewport ?? window
    const onResize = () => {
      if (taskRef.current) autoGrow(taskRef.current)
    }
    target.addEventListener('resize', onResize)
    return () => target.removeEventListener('resize', onResize)
  }, [])

  // Pure fetch: every setState it makes happens inside a promise callback,
  // never synchronously in the caller's tick, so it's safe to invoke directly
  // from the mount effect as well as from the Retry button's click handler.
  const runFetch = useCallback(() => {
    let cancelled = false
    fetchDispatchOptions()
      .then((o) => {
        if (cancelled) return
        const init = resolveInitial(o, loadPrefs(), null)
        setOptions(o)
        setRepo(init.repo)
        setCrew(init.crew)
        setEngine(init.engine)
        setTier(init.tier)
        setModel(init.model)
        setEffort(init.effort)
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
    setCrew(defaultCrew(options?.repos.find((x) => x.path === path)))
    setLinkNotice(null)
  }

  function chooseEngine(eng: string): void {
    setEngine(eng)
    if (options) setModel(defaultModel(options, eng, tier))
  }

  function chooseTier(t: string): void {
    setTier(t)
    if (options) setModel(defaultModel(options, engine, t))
  }

  async function handleSubmit(e: FormEvent): Promise<void> {
    e.preventDefault()
    if (submitting || !title.trim()) return
    setSubmitting(true)
    const req = {
      repo,
      title: title.trim(),
      spec: spec || undefined,
      tier,
      engine,
      model,
      effort,
      crew,
      issue: issue.trim() || undefined,
    }
    const result = await submitDispatch(req)
    setSubmitting(false)
    setOutcome(result)
    setOutcomeWasNewCrew(req.crew === NEW_CREW)
    const minted = result.crew
    if (req.crew === NEW_CREW && minted) {
      // Join the minted crew on the next attempt — success or a retryable
      // failure alike — instead of minting another. `home` is deliberately
      // left untouched: the server wouldn't list this crew as home either,
      // since no dispatcher pane file exists yet for one the UI just minted.
      setOptions((o) => o && {
        ...o,
        repos: o.repos.map((r) =>
          r.path === req.repo && !r.crews.includes(minted) ? { ...r, crews: [minted, ...r.crews] } : r,
        ),
      })
      setCrew(minted)
    }
    if (result.kind !== 'started') return
    savePrefs({ repo: req.repo, engine: req.engine, model: req.model, tier: req.tier, effort: req.effort })
    setTitle('')
    setSpec('')
  }

  const dispatchedRun =
    outcome?.kind === 'started' ? findDispatchedRun(runs, outcome.branch, outcome.crew || undefined) : undefined
  const issueUrl = outcome?.kind === 'started' ? (outcome.issueUrl ?? dispatchedRun?.issue?.url) : undefined

  return (
    <div className="dispatch mocha">
      <header className="fleet-nav">
        <h1>Dispatch</h1>
      </header>

      {linkNotice && <p className="dispatch-notice" role="status">{linkNotice}</p>}

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
              ref={taskRef}
              className="dispatch-task"
              value={spec}
              disabled={submitting}
              style={{ touchAction: 'pan-y' }}
              onChange={(e) => setSpec(e.target.value)}
            />
            <span className="dispatch-hint">Up to 64 KB. Falls back to the title when left empty.</span>
          </div>

          <div className="dispatch-grid">
            <div className="dispatch-field">
              <label htmlFor="dispatch-tier">Tier</label>
              <select id="dispatch-tier" value={tier} onChange={(e) => chooseTier(e.target.value)}>
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
          </div>

          <div className="dispatch-field">
            <label htmlFor="dispatch-crew">Crew</label>
            <select id="dispatch-crew" value={crew} onChange={(e) => setCrew(e.target.value)}>
              {selectedRepo?.crews.map((c) => <option key={c} value={c}>{crewAgeLabel(c, now)}</option>)}
              <option value={NEW_CREW}>New crew</option>
            </select>
            {selectedRepo && selectedRepo.crews.length === 0 && (
              <span className="dispatch-hint">No crew yet — dispatching starts a new one.</span>
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

          <button type="submit" className="dispatch-submit" disabled={submitting || !title.trim()}>
            {submitting ? 'Dispatching…' : 'Dispatch'}
          </button>
        </form>
      )}

      <div ref={resultRef}>
        {outcome?.kind === 'started' && (
          <div className="dispatch-result success">
            <p>Worker <strong>{outcome.workerId}</strong> started on <code>{outcome.branch}</code>.</p>
            {outcome.crew && (
              <p>Crew <code>{outcome.crew}</code>{outcomeWasNewCrew && ' (new crew)'}</p>
            )}
            {dispatchedRun ? (
              <p><a href={runHash(dispatchedRun.id)}>Open run</a></p>
            ) : (
              <p className="dispatch-hint">Waiting for it to appear in Fleet…</p>
            )}
            {issueUrl && (
              <p><a href={issueUrl} target="_blank" rel="noreferrer">View issue</a></p>
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
    </div>
  )
}
