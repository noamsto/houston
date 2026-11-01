import { useCallback, useEffect, useRef, useState } from 'react'
import { answerBack, answerStep, answerSubmit, readQuestionState } from '../api/answer'
import type { QuestionState, StepResult } from '../api/answer'
import type { ToolCall } from './chatModel'
import { emptyStage, isStageComplete, setText, stageFromSelection, toAnswer, toggleOption, toggleOther } from './questionStage'
import type { Stage } from './questionStage'
import { FallbackRow, PickerBlock, QuestionBlock, TerminalLink } from './questionParts'
import type { Question } from './questionParts'

const POLL_MS = 2000

const squash = (s: string) => s.replace(/\s+/g, '')

type Notice =
  // `what` names the request that was refused or stopped part-way, e.g. "Step 2".
  | { kind: 'moved'; what: string }
  | { kind: 'partial'; what: string }
  | { kind: 'error'; message: string }

/** Steps through one AskUserQuestion call, one verified request per tab. The
 *  pane is the source of truth: every screen renders from the last `state` it
 *  reported, and nothing here ever re-sends a key on its own. */
export function QuestionWizard({ runId, call, fallbackQuestions, onLayout }: {
  runId: string
  call: ToolCall
  fallbackQuestions: Question[]
  onLayout: () => void
}) {
  const [state, setState] = useState<QuestionState | null>(null)
  // The pane shows no dialog for this call (404).
  const [missing, setMissing] = useState(false)
  const [loaded, setLoaded] = useState(false)
  const [stages, setStages] = useState<Stage[]>([])
  const [busy, setBusy] = useState(false)
  const [notice, setNotice] = useState<Notice | null>(null)
  // Tied to the status it was sent under: a status change ends the wait.
  const [sent, setSent] = useState<string | undefined | false>(false)

  const stateRef = useRef<QuestionState | null>(null)
  const stagesRef = useRef<Stage[]>([])
  // Steps the user has edited since they were shown; a poll never overwrites them.
  const touched = useRef(new Set<number>())
  const busyRef = useRef(false)
  // Bumped when a request starts, so a poll already in flight is dropped.
  const epoch = useRef(0)
  const alive = useRef(true)
  const sentRef = useRef(false)

  const isSent = sent !== false && sent === call.status
  useEffect(() => {
    sentRef.current = isSent
  }, [isSent])
  useEffect(() => {
    alive.current = true
    return () => { alive.current = false }
  }, [])

  const commitStages = (next: Stage[]) => {
    stagesRef.current = next
    setStages(next)
  }

  const apply = useCallback((next: QuestionState) => {
    const prev = stateRef.current
    stateRef.current = next
    setState(next)
    setMissing(false)
    const shown = next.step
    if (!prev || prev.step !== shown) touched.current.delete(shown)
    const q = next.questions[shown]
    if (!q || touched.current.has(shown)) return
    const seeded = stageFromSelection(next.options, next.text, q.multiSelect)
    const remembered = stagesRef.current[shown]
    if (remembered && seeded.otherOn && squash(remembered.text) === squash(seeded.text)) seeded.text = remembered.text
    const list = next.questions.map((_, i) => (i === shown ? seeded : (stagesRef.current[i] ?? emptyStage())))
    stagesRef.current = list
    setStages(list)
  }, [])

  const read = useCallback(async () => {
    const mine = epoch.current
    try {
      const next = await readQuestionState(runId, call.toolCallId)
      if (!alive.current || mine !== epoch.current) return
      if (next === null) setMissing(true)
      else apply(next)
    } catch {
      // A failed poll keeps the last state.
    } finally {
      if (alive.current) setLoaded(true)
    }
  }, [runId, call.toolCallId, apply])

  useEffect(() => {
    void read()
    const timer = setInterval(() => {
      if (!busyRef.current && !sentRef.current && document.visibilityState === 'visible') void read()
    }, POLL_MS)
    return () => clearInterval(timer)
  }, [read])

  const begin = () => {
    busyRef.current = true
    epoch.current++
    setBusy(true)
    setNotice(null)
  }
  const finish = () => {
    busyRef.current = false
    setBusy(false)
  }

  /** Applies one request's result; true when it moved the card on. */
  const settle = (res: StepResult, what: string): boolean => {
    if ('ok' in res) {
      if (res.state) apply(res.state)
      else setSent(call.status)
      return true
    }
    if ('moved' in res) setNotice({ kind: res.partial ? 'partial' : 'moved', what })
    else setNotice({ kind: 'error', message: res.error })
    return false
  }

  const act = async (what: string, request: () => Promise<StepResult>) => {
    begin()
    const res = await request()
    if (!alive.current) return
    const ok = settle(res, what)
    // A 409 with nothing typed means the pane moved on: read it, never key it.
    const reread = !ok && 'moved' in res && !res.partial
    finish()
    if (reread) await read()
  }

  const recheck = async () => {
    begin()
    const mine = epoch.current
    try {
      const next = await readQuestionState(runId, call.toolCallId)
      if (!alive.current || mine !== epoch.current) return
      if (next === null) setMissing(true)
      else apply(next)
    } catch {
      setNotice({ kind: 'error', message: 'Could not read the terminal' })
    } finally {
      if (alive.current) finish()
    }
  }

  const jumpTo = async (target: number) => {
    const at = stateRef.current
    if (!at) return
    begin()
    let reread = false
    for (let from = at.count; from > target; from--) {
      const res = await answerBack(runId, call.toolCallId, from)
      if (!alive.current) return
      const what = from === at.count ? 'Going back from the review' : `Going back from step ${from + 1}`
      if (settle(res, what)) continue
      reread = 'moved' in res && !res.partial
      break
    }
    finish()
    if (reread) await read()
  }

  const editStage = (i: number, f: (s: Stage) => Stage) => {
    touched.current.add(i)
    const list = (stateRef.current?.questions ?? []).map((_, j) => (j === i ? f(stagesRef.current[j] ?? emptyStage()) : (stagesRef.current[j] ?? emptyStage())))
    commitStages(list)
  }

  const shape = state ? `${state.tabbed}:${state.step}:${state.count}` : ''
  useEffect(() => {
    if (loaded) onLayout()
  }, [loaded, missing, shape, isSent, notice, onLayout])

  const questions: Question[] = state?.questions ?? fallbackQuestions
  if (questions.length === 0) return <FallbackRow call={call} terminalRunId={loaded ? runId : undefined} />

  if (!state || (missing && !isSent)) {
    return (
      <div className="chat-question-wizard">
        {questions.map((q, i) => <QuestionBlock key={i} q={q} />)}
        {loaded && (
          <div className="chat-question-wizard-hint" role="status">
            The terminal isn't showing this question right now — answer it in the Terminal tab
            <TerminalLink runId={runId} />
          </div>
        )}
      </div>
    )
  }

  const statusLine = (
    <div className="chat-question-status" role="status">
      {isSent && 'Sent — waiting for Claude'}
      {notice?.kind === 'moved' && (
        <>
          <span>{notice.what}: the terminal moved on — nothing was typed</span> <TerminalLink runId={runId} />
        </>
      )}
      {notice?.kind === 'partial' && (
        <>
          <span>{notice.what} didn't finish — check the Terminal</span>{' '}
          <TerminalLink runId={runId} />{' '}
          <button type="button" className="chat-question-wizard-recheck" disabled={busy} onClick={recheck}>Re-check</button>
        </>
      )}
      {notice?.kind === 'error' && (
        <>
          <span>{notice.message}</span> — or answer in the <TerminalLink runId={runId} />
        </>
      )}
    </div>
  )
  const locked = busy || isSent

  if (!state.tabbed) {
    const q = questions[0]
    const stage = stages[0] ?? emptyStage()
    return (
      <>
        <PickerBlock
          q={q}
          stage={stage}
          disabled={locked}
          onOption={(o) => editStage(0, (s) => toggleOption(s, o, q.multiSelect))}
          onOther={() => editStage(0, (s) => toggleOther(s, q.multiSelect))}
          onText={(t) => editStage(0, (s) => setText(s, t))}
        />
        <div className="chat-question-send">
          <button
            type="button"
            className="chat-question-send-btn"
            disabled={locked || !isStageComplete(stage)}
            onClick={() => void act('Step 1', () => answerStep(runId, call.toolCallId, 0, toAnswer(stage)))}
          >
            Send answer
          </button>
          {statusLine}
        </div>
      </>
    )
  }

  const chips = (
    <ol className="chat-question-wizard-chips" aria-label="Questions">
      {questions.map((q, i) => (
        <li
          key={i}
          className={`chat-question-wizard-chip${i === state.step ? ' current' : ''}`}
          aria-current={i === state.step ? 'step' : undefined}
        >
          {q.header ?? `Q${i + 1}`}
        </li>
      ))}
    </ol>
  )

  if (state.step >= state.count) {
    const review = state.review ?? []
    return (
      <div className="chat-question-wizard">
        <div className="chat-question-wizard-top">
          <span className="chat-question-wizard-progress">Review your answers</span>
          {chips}
        </div>
        <ul className="chat-question-wizard-review">
          {questions.map((q, i) => (
            <li key={i}>
              <button type="button" className="chat-question-wizard-review-row" disabled={locked} onClick={() => void jumpTo(i)}>
                <span className="chat-question-wizard-review-q">{q.header ?? q.question}</span>
                <span className="chat-question-wizard-review-a">{review[i] ?? ''}</span>
              </button>
            </li>
          ))}
        </ul>
        {statusLine}
        <div className="chat-question-wizard-nav">
          <button
            type="button"
            className="chat-question-wizard-next"
            disabled={locked || review.length !== state.count}
            onClick={() => void act('Submit', () => answerSubmit(runId, call.toolCallId, review))}
          >
            Submit
          </button>
        </div>
      </div>
    )
  }

  const step = state.step
  const q = questions[step]
  const stage = stages[step] ?? emptyStage()
  const last = step === state.count - 1
  return (
    <div className="chat-question-wizard">
      <div className="chat-question-wizard-top">
        <span className="chat-question-wizard-progress">{step + 1} of {state.count}</span>
        {chips}
      </div>
      <PickerBlock
        q={q}
        stage={stage}
        disabled={locked}
        showHeader={false}
        onOption={(o) => editStage(step, (s) => toggleOption(s, o, q.multiSelect))}
        onOther={() => editStage(step, (s) => toggleOther(s, q.multiSelect))}
        onText={(t) => editStage(step, (s) => setText(s, t))}
      />
      {statusLine}
      <div className="chat-question-wizard-nav">
        {step > 0 && (
          <button
            type="button"
            className="chat-question-wizard-back"
            disabled={locked}
            onClick={() => void act(`Step ${step + 1}`, () => answerBack(runId, call.toolCallId, step))}
          >
            Back
          </button>
        )}
        <button
          type="button"
          className="chat-question-wizard-next"
          disabled={locked || !isStageComplete(stage)}
          onClick={() => void act(`Step ${step + 1}`, () => answerStep(runId, call.toolCallId, step, toAnswer(stage)))}
        >
          {last ? 'Review' : 'Next'}
        </button>
      </div>
    </div>
  )
}
