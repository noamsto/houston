import { useId, useRef, useState } from 'react'
import {
  fetchDispatchOptions,
  restartDispatcher,
  type DispatchOptions,
  type DispatcherOutcome,
  type RestartRequest,
} from '../api/dispatch'
import type { Run } from '../api/runs'
import { isBusy } from './crewsModel'
import { crewShortId } from './fleetList'
import { switchRunTab } from './nav'
import { markPendingRun } from './pendingRuns'

// Keeps "leave unchanged" apart from "" (the launcher's own default).
const SAME = '\0same'

type Phase = 'closed' | 'confirm' | 'sending' | 'failed'
type OptionsState =
  | { status: 'idle' }
  | { status: 'loading' }
  | { status: 'ready'; options: DispatchOptions }
  | { status: 'error'; error: string }

type Failure = Extract<DispatcherOutcome, { kind: 'failed' }>

export function RestartDispatcher({ run }: { run: Run }) {
  const ids = useId()
  const [phase, setPhase] = useState<Phase>('closed')
  const [showSettings, setShowSettings] = useState(false)
  const [optionsState, setOptionsState] = useState<OptionsState>({ status: 'idle' })
  const [engine, setEngine] = useState(SAME)
  const [model, setModel] = useState(SAME)
  const [effort, setEffort] = useState(SAME)
  const [busyReply, setBusyReply] = useState(false)
  const [failure, setFailure] = useState<Failure | null>(null)
  const sending = useRef(false)
  const lastReq = useRef<RestartRequest>({})

  const crew = run.crew?.name ?? ''
  const force = busyReply || isBusy(run)

  function loadOptions(): void {
    setOptionsState({ status: 'loading' })
    fetchDispatchOptions().then(
      (options) => setOptionsState({ status: 'ready', options }),
      (e: unknown) => setOptionsState({ status: 'error', error: e instanceof Error ? e.message : String(e) }),
    )
  }

  function toggleSettings(): void {
    const open = !showSettings
    setShowSettings(open)
    if (open && optionsState.status === 'idle') loadOptions()
  }

  function chooseEngine(next: string): void {
    setEngine(next)
    setModel(SAME)
  }

  function request(): RestartRequest {
    const req: RestartRequest = {}
    if (engine !== SAME) req.engine = engine
    if (model !== SAME) req.model = model
    if (effort !== SAME) req.effort = effort
    if (force) req.force = true
    return req
  }

  function close(): void {
    setPhase('closed')
    setShowSettings(false)
    setBusyReply(false)
    setFailure(null)
    setEngine(SAME)
    setModel(SAME)
    setEffort(SAME)
  }

  async function send(req: RestartRequest): Promise<void> {
    if (sending.current) return
    sending.current = true
    lastReq.current = req
    setPhase('sending')
    const result = await restartDispatcher(run.id, req)
    sending.current = false
    if (result.kind === 'started') {
      markPendingRun(result.runId, Date.now())
      switchRunTab(result.runId, 'crew')
      return
    }
    if (result.busy) {
      setBusyReply(true)
      setFailure(null)
      setPhase('confirm')
      return
    }
    setFailure(result)
    setPhase('failed')
  }

  if (phase === 'closed') {
    return (
      <button type="button" className="crews-restart" onClick={() => setPhase('confirm')}>
        Restart
      </button>
    )
  }

  if (phase === 'failed' && failure) {
    return (
      <div className="crews-restart-confirm dispatch-result failure">
        <pre>{failure.error}</pre>
        {failure.crew && <p>Crew <code>{failure.crew}</code> is unchanged.</p>}
        {failure.output && (
          <details>
            <summary>Output</summary>
            <pre>{failure.output}</pre>
          </details>
        )}
        <div className="crews-restart-actions">
          <button type="button" className="crews-restart" onClick={() => { void send(lastReq.current) }}>
            Retry
          </button>
          <button type="button" className="crews-restart" onClick={close}>
            Cancel
          </button>
        </div>
      </div>
    )
  }

  const isSending = phase === 'sending'
  const dispatcherEngines = optionsState.status === 'ready' ? (optionsState.options.dispatcher_engines ?? []) : []
  const models = optionsState.status === 'ready' && engine !== SAME ? (optionsState.options.engines[engine] ?? []) : []
  const efforts = optionsState.status === 'ready' ? optionsState.options.efforts : []

  return (
    <div className="crews-restart-confirm">
      <p>
        Ends this dispatcher&apos;s session and starts a new one on crew {crewShortId(crew)}. Workers keep running.
      </p>
      {force && <p className="crews-restart-warn">It is in the middle of a turn — restarting interrupts it.</p>}

      <button
        type="button"
        className="crews-restart"
        aria-expanded={showSettings}
        disabled={isSending}
        onClick={toggleSettings}
      >
        Settings
      </button>
      {showSettings && optionsState.status === 'loading' && <p className="dispatch-hint">Loading…</p>}
      {showSettings && optionsState.status === 'error' && <p className="dispatch-hint dispatch-error">{optionsState.error}</p>}
      {showSettings && optionsState.status === 'ready' && (
        <div className="dispatch-grid crews-restart-settings">
          <div className="dispatch-field">
            <label htmlFor={`${ids}-engine`}>Engine</label>
            <select id={`${ids}-engine`} value={engine} disabled={isSending} onChange={(e) => chooseEngine(e.target.value)}>
              <option value={SAME}>same as now</option>
              {dispatcherEngines.map((eng) => <option key={eng} value={eng}>{eng}</option>)}
            </select>
          </div>
          <div className="dispatch-field">
            <label htmlFor={`${ids}-model`}>Model</label>
            <select id={`${ids}-model`} value={model} disabled={isSending} onChange={(e) => setModel(e.target.value)}>
              <option value={SAME}>same as now</option>
              <option value="">launcher default</option>
              {models.map((m) => <option key={m} value={m}>{m}</option>)}
            </select>
          </div>
          <div className="dispatch-field">
            <label htmlFor={`${ids}-effort`}>Effort</label>
            <select id={`${ids}-effort`} value={effort} disabled={isSending} onChange={(e) => setEffort(e.target.value)}>
              <option value={SAME}>same as now</option>
              <option value="">launcher default</option>
              {efforts.map((ef) => <option key={ef} value={ef}>{ef}</option>)}
            </select>
          </div>
        </div>
      )}

      <div className="crews-restart-actions">
        <button
          type="button"
          className="crews-restart crews-restart-go"
          disabled={isSending}
          onClick={() => { void send(request()) }}
        >
          {isSending ? 'Restarting…' : force ? 'Force restart' : 'Restart'}
        </button>
        <button type="button" className="crews-restart" disabled={isSending} onClick={close}>
          Cancel
        </button>
      </div>
    </div>
  )
}
