import { useCallback, useEffect, useRef, useState } from 'react'
import type { FormEvent } from 'react'
import {
  fetchDispatchOptions,
  submitDispatch,
  submitDispatcher,
  NEW_CREW,
  type DispatchOptions,
  type DispatchOutcome,
  type DispatcherOutcome,
  type DispatcherRequest,
} from '../api/dispatch'
import { fetchHosts, type HostHealth } from '../api/hosts'
import type { Run } from '../api/runs'
import {
  crewAgeLabel,
  defaultCrew,
  defaultModel,
  findDispatchedRun,
  isValidIssue,
  loadPrefs,
  resolveInitial,
  safeIssueURL,
  workerEngines,
  savePrefs,
  taskMaxHeight,
  type DispatchPrefs,
} from './dispatchForm'
import { DispatcherForm, DispatcherResult } from './DispatcherForm'
import { RepoPicker } from './RepoPicker'
import { runHash, useDispatchRoute } from './routes'
import { onAppLink, openRun } from './nav'
import { useNow } from './useNow'
import './fleet.css'

const UNKNOWN_REPO_NOTICE = "Linked repo isn't one houston knows — pick one below."
const HOSTS_POLL_MS = 30_000

interface Results {
  worker?: { host: string; outcome: DispatchOutcome; wasNewCrew: boolean }
  dispatcher?: { host: string; outcome: DispatcherOutcome }
}

function peerArg(host: string): string | undefined {
  return host === 'local' ? undefined : host
}

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
  const [hosts, setHosts] = useState<HostHealth[] | null>(null)
  const [host, setHost] = useState('local')
  const [optionsHost, setOptionsHost] = useState<string | null>(null)

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
  const [mode, setMode] = useState<NonNullable<DispatchPrefs['mode']>>(() => loadPrefs().mode ?? 'worker')
  const [showPicker, setShowPicker] = useState(false)
  const [refreshError, setRefreshError] = useState<string | null>(null)

  // Results carry the host they were sent to and outlive a host switch.
  const [results, setResults] = useState<Results>({})
  const [inFlight, setInFlight] = useState<{ kind: keyof Results; host: string } | null>(null)
  const resultRef = useRef<HTMLDivElement>(null)
  const refreshSeq = useRef(0)
  const pendingAdded = useRef<string | null>(null)
  const repoRef = useRef(repo)
  const hostRef = useRef(host)
  const taskRef = useRef<HTMLTextAreaElement>(null)
  const now = useNow()

  const route = useDispatchRoute()
  const [appliedSeq, setAppliedSeq] = useState<number | null>(null)
  const peers = (hosts ?? []).filter((h) => !h.local)
  const busy = inFlight !== null
  const submitting = inFlight?.kind === 'worker'

  // State setters only: it runs from the render-phase link prefill as well as
  // from the picker; the [host] effect then fetches that host's options.
  function switchHost(h: string): void {
    if (h === host) return
    setHost(h)
    hostRef.current = h
    setOptions(null)
    setOptionsHost(null)
    setLoadingOptions(true)
    setOptionsError(null)
    setRefreshError(null)
    setLinkNotice(null)
  }

  // One submission at a time, whichever form and host it targets.
  async function submit<K extends keyof Results>(
    kind: K,
    h: string,
    fn: () => Promise<NonNullable<Results[K]>>,
  ): Promise<NonNullable<Results[K]>> {
    setInFlight({ kind, host: h })
    try {
      const r = await fn()
      setResults((rs) => ({ ...rs, [kind]: r }))
      return r
    } finally {
      setInFlight(null)
    }
  }

  async function launchDispatcher(req: DispatcherRequest): Promise<DispatcherOutcome> {
    const h = host
    const r = await submit('dispatcher', h, async () => ({ host: h, outcome: await submitDispatcher(req, peerArg(h)) }))
    return r.outcome
  }

  function onDispatcherStarted(h: string): void {
    if (h === hostRef.current) refreshOptions()
  }

  // Picking a host by hand abandons a link still waiting on its host's
  // options, which would otherwise switch straight back.
  function chooseHost(h: string): void {
    if (route) setAppliedSeq(route.seq)
    switchHost(h)
  }

  // Adjusted during render rather than in an effect: each hashchange (seq)
  // applies its link exactly once. The host switches at once; repo/crew wait
  // for that host's options, whose fetch is the authority on an unknown or
  // unreachable host. Only host/repo/crew are touched, so a draft survives.
  if (route && route.seq !== appliedSeq && (route.host || route.repo || route.crew)) {
    const wanted = route.host || 'local'
    if (wanted !== host) {
      switchHost(wanted)
    } else if (options && optionsHost === wanted) {
      setAppliedSeq(route.seq)
      const link = resolveInitial(options, { repo }, route)
      setRepo(link.repo)
      setCrew(link.crew)
      setLinkNotice(
        link.linkRepoUnknown
          ? UNKNOWN_REPO_NOTICE
          : link.linkCrewUnknown
            ? `Linked crew isn't listed on ${wanted} — pick one below.`
            : null,
      )
      setMode('worker')
    }
  }

  // Once applied, drop the params so a reload doesn't re-apply a stale link
  // over a hand-edited host/repo/crew; following the same link again changes
  // the hash, which fires hashchange and bumps seq.
  const hasLinkParams = !!(route?.host || route?.repo || route?.crew)
  useEffect(() => {
    if (route && hasLinkParams && route.seq === appliedSeq) {
      history.replaceState(null, '', '#/dispatch')
    }
  }, [route, hasLinkParams, appliedSeq])

  // The result lands below the submit button — off-screen on a phone.
  useEffect(() => {
    if (results.worker || results.dispatcher) resultRef.current?.scrollIntoView({ block: 'nearest', behavior: 'smooth' })
  }, [results])

  useEffect(() => {
    if (taskRef.current) autoGrow(taskRef.current)
  }, [spec, options, mode])

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

  useEffect(() => {
    let active = true
    const load = () => {
      fetchHosts()
        .then((list) => {
          if (active) setHosts(list)
        })
        .catch(() => {
          // A failed poll keeps the last list, so a chosen peer stays pickable.
        })
    }
    load()
    const timer = setInterval(load, HOSTS_POLL_MS)
    return () => {
      active = false
      clearInterval(timer)
    }
  }, [])

  // Pure fetch: every setState it makes happens inside a promise callback,
  // never synchronously in the caller's tick, so it's safe to invoke directly
  // from the host effect as well as from the Retry button's click handler.
  // A result for a host no longer chosen (a Retry overtaken by a switch) is
  // dropped.
  const runFetch = useCallback((h: string) => {
    let cancelled = false
    fetchDispatchOptions(peerArg(h))
      .then((o) => {
        if (cancelled || h !== hostRef.current) return
        const init = resolveInitial(o, loadPrefs(h), null)
        setOptions(o)
        setOptionsHost(h)
        setRepo(init.repo)
        setCrew(init.crew)
        setEngine(init.engine)
        setTier(init.tier)
        setModel(init.model)
        setEffort(init.effort)
        setLoadingOptions(false)
      })
      .catch((e: unknown) => {
        if (cancelled || h !== hostRef.current) return
        setOptionsError(e instanceof Error ? e.message : String(e))
        setLoadingOptions(false)
      })
    return () => { cancelled = true }
  }, [])

  // A host switch also orphans the previous host's refreshes and any repo
  // added through its picker.
  useEffect(() => {
    hostRef.current = host
    refreshSeq.current++
    pendingAdded.current = null
    return runFetch(host)
  }, [host, runFetch])

  function retryOptions(): void {
    setLoadingOptions(true)
    setOptionsError(null)
    runFetch(host)
  }

  // A late response from the previous host can store options before the new
  // host's own land; they are shown only for the host they belong to.
  const shown = options && optionsHost === host ? options : null
  const selectedRepo = options?.repos.find((r) => r.path === repo)
  const models = options?.engines[engine] ?? []

  useEffect(() => {
    repoRef.current = repo
  }, [repo])

  // Only the latest refresh applies, reading the repo at response time; an
  // added repo waits in pendingAdded for whichever refresh wins.
  function refreshOptions(added?: string): void {
    if (added) pendingAdded.current = added
    const seq = ++refreshSeq.current
    const h = hostRef.current
    fetchDispatchOptions(peerArg(h))
      .then((o) => {
        if (seq !== refreshSeq.current || hostRef.current !== h) return
        setOptions(o)
        setRefreshError(null)
        const current = repoRef.current
        const keep = o.repos.some((r) => r.path === current)
        const wanted = pendingAdded.current
        pendingAdded.current = null
        const next = wanted && o.repos.some((r) => r.path === wanted) ? wanted : keep ? current : (o.repos[0]?.path ?? '')
        if (next !== current) chooseRepoIn(o, next)
      })
      .catch((e: unknown) => {
        if (seq === refreshSeq.current && hostRef.current === h) {
          setRefreshError(e instanceof Error ? e.message : String(e))
        }
      })
  }

  function chooseRepoIn(o: DispatchOptions, path: string): void {
    setRepo(path)
    setCrew(defaultCrew(o.repos.find((x) => x.path === path)))
    setLinkNotice(null)
  }

  function chooseMode(m: NonNullable<DispatchPrefs['mode']>): void {
    setMode(m)
    savePrefs({ ...loadPrefs(), mode: m })
  }

  function chooseRepo(path: string): void {
    if (options) chooseRepoIn(options, path)
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
    if (busy || !title.trim() || !isValidIssue(issue)) return
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
    const submitHost = host
    const { outcome: result } = await submit('worker', submitHost, async () => ({
      host: submitHost,
      outcome: await submitDispatch(req, peerArg(submitHost)),
      wasNewCrew: req.crew === NEW_CREW,
    }))
    const minted = result.crew
    if (req.crew === NEW_CREW && minted && hostRef.current === submitHost) {
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
    savePrefs(
      { ...loadPrefs(submitHost), repo: req.repo, engine: req.engine, model: req.model, tier: req.tier, effort: req.effort },
      submitHost,
    )
    setTitle('')
    setSpec('')
  }

  const issueValid = isValidIssue(issue)
  const submitDisabledReason = !title.trim() ? 'Title is required.' : !issueValid ? 'Fix the issue id to dispatch.' : null

  const worker = results.worker
  const outcome = worker?.outcome
  const dispatchedRun =
    worker && outcome?.kind === 'started'
      ? findDispatchedRun(runs, outcome.branch, outcome.crew || undefined, peerArg(worker.host))
      : undefined
  const issueUrl =
    outcome?.kind === 'started' ? (safeIssueURL(outcome.issueUrl) ?? safeIssueURL(dispatchedRun?.issue?.url)) : undefined
  const workerHostNote = worker && worker.host !== host && <p className="dispatch-hint">on {worker.host}</p>
  const listedHost = host === 'local' || peers.some((p) => p.name === host)

  return (
    <div className="dispatch mocha">
      <header className="fleet-nav">
        <h1>Dispatch</h1>
      </header>

      {(peers.length > 0 || host !== 'local') && (
        <div className="dispatch-field dispatch-host">
          <label htmlFor="dispatch-host">Host</label>
          <select id="dispatch-host" value={host} onChange={(e) => chooseHost(e.target.value)}>
            <option value="local">local</option>
            {peers.map((p) => (
              <option key={p.name} value={p.name} disabled={p.health === 'unreachable' && p.name !== host}>
                {p.health === 'unreachable' ? `${p.name} (unreachable)` : p.name}
              </option>
            ))}
            {!listedHost && <option value={host}>{host}</option>}
          </select>
        </div>
      )}

      {linkNotice && <p className="dispatch-notice" role="status">{linkNotice}</p>}

      {loadingOptions && <div className="fleet-empty">Loading dispatch options…</div>}

      {!loadingOptions && optionsError && (
        <div className="fleet-empty">
          {optionsError}
          <div><button type="button" onClick={retryOptions}>Retry</button></div>
        </div>
      )}

      {refreshError && <p className="dispatch-notice" role="alert">{refreshError}</p>}

      {!loadingOptions && shown && (
        <div className="dispatch-shared">
          <div className="dispatch-modes" role="group" aria-label="Dispatch mode">
            <button type="button" aria-pressed={mode === 'worker'} onClick={() => chooseMode('worker')}>
              Add worker
            </button>
            <button type="button" aria-pressed={mode === 'dispatcher'} onClick={() => chooseMode('dispatcher')}>
              New dispatcher
            </button>
          </div>

          <div className="dispatch-field">
            <label htmlFor="dispatch-repo">Repo</label>
            <div className="dispatch-repo-row">
              <select id="dispatch-repo" value={repo} onChange={(e) => chooseRepo(e.target.value)}>
                {shown.repos.map((r) => (
                  <option key={r.path} value={r.path}>{r.name}</option>
                ))}
              </select>
              <button type="button" className="dispatch-manage" onClick={() => setShowPicker((v) => !v)}>
                Manage repos
              </button>
            </div>
          </div>

          {showPicker && <RepoPicker key={host} host={host} onChanged={refreshOptions} onClose={() => setShowPicker(false)} />}
        </div>
      )}

      {/* Kept mounted so the draft and an in-flight launch survive a mode toggle. */}
      {!loadingOptions && shown && (
        <div hidden={mode !== 'dispatcher'}>
          <DispatcherForm
            host={host}
            options={shown}
            repo={repo}
            busy={busy}
            onSubmit={launchDispatcher}
            onStarted={onDispatcherStarted}
          />
        </div>
      )}

      {!loadingOptions && shown && mode === 'worker' && (
        <form className="dispatch-form" onSubmit={(e) => { void handleSubmit(e) }}>
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
                {shown.tiers.map((t) => <option key={t} value={t}>{t}</option>)}
              </select>
            </div>

            <div className="dispatch-field">
              <label htmlFor="dispatch-engine">Engine</label>
              <select id="dispatch-engine" value={engine} onChange={(e) => chooseEngine(e.target.value)}>
                {workerEngines(shown).map((eng) => <option key={eng} value={eng}>{eng}</option>)}
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
                {shown.efforts.map((ef) => <option key={ef} value={ef}>{ef}</option>)}
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
              aria-invalid={!issueValid}
              onChange={(e) => setIssue(e.target.value)}
            />
            {!issueValid && (
              <span className="dispatch-hint dispatch-error">Must be a GitHub issue number (123) or a Linear id (ENG-123).</span>
            )}
          </div>

          <button type="submit" className="dispatch-submit" disabled={busy || !title.trim() || !issueValid}>
            {submitting ? 'Dispatching…' : 'Dispatch'}
          </button>
          {!submitting && submitDisabledReason && (
            <span className="dispatch-hint">{submitDisabledReason}</span>
          )}
        </form>
      )}

      {/* Not gated on the mode or the options: a link that switches either
          must not hide a result that is still landing. */}
      <div ref={resultRef}>
        {outcome?.kind === 'started' && (
          <div className="dispatch-result success">
            {workerHostNote}
            <p>Worker <strong>{outcome.workerId}</strong> started on <code>{outcome.branch}</code>.</p>
            {outcome.crew && (
              <p>Crew <code>{outcome.crew}</code>{worker?.wasNewCrew && ' (new crew)'}</p>
            )}
            {dispatchedRun ? (
              <p><a href={runHash(dispatchedRun.id)} onClick={onAppLink(() => openRun(dispatchedRun.id))}>Open run</a></p>
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
            {workerHostNote}
            <pre>{outcome.error}</pre>
            {outcome.output && (
              <details>
                <summary>Output</summary>
                <pre>{outcome.output}</pre>
              </details>
            )}
          </div>
        )}

        {results.dispatcher && (
          <DispatcherResult
            host={results.dispatcher.host}
            currentHost={host}
            outcome={results.dispatcher.outcome}
            runs={runs}
          />
        )}
      </div>
    </div>
  )
}
