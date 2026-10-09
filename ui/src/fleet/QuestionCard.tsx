import { useEffect, useRef, useState } from 'react'
import { answerQuestion } from '../api/answer'
import { fetchTool } from '../api/chat'
import type { ChatToolDetail } from '../api/chat'
import type { QuestionItem, ToolCall } from './chatModel'
import { emptyStage, isComplete, setText, toggleOption, toggleOther, toWire } from './questionStage'
import type { Stage } from './questionStage'
import { runHash } from './routes'

interface QuestionOption {
  label: string
  description?: string
}

interface Question {
  question: string
  header?: string
  multiSelect: boolean
  options: QuestionOption[]
}

const str = (v: unknown): string | undefined => (typeof v === 'string' && v !== '' ? v : undefined)

function parseQuestions(input: unknown): Question[] {
  const raw = (input as { questions?: unknown } | null)?.questions
  if (!Array.isArray(raw)) return []
  const out: Question[] = []
  for (const q of raw as Record<string, unknown>[]) {
    const question = str(q?.question)
    if (!question) continue
    const options: QuestionOption[] = []
    for (const o of Array.isArray(q.options) ? (q.options as Record<string, unknown>[]) : []) {
      const label = str(o?.label)
      if (label) options.push({ label, description: str(o.description) })
    }
    out.push({ question, header: str(q.header), multiSelect: q.multiSelect === true, options })
  }
  return out
}

// The lookahead rejects a pair whose value holds a stray quote ("say "hi""),
// so such an output fails the every-question-answered check and falls back.
const ANSWER_PAIR = /"([^"]*)"="([^"]*)"(?=, "|\.|$)/g

/** Chosen answers by question text, or null when the output can't be read
 *  unambiguously (a comma or quote in a label or question, or a question
 *  with no answer pair) — the caller then shows the raw output. */
function parseAnswers(questions: Question[], output: string): Map<string, string[]> | null {
  const ambiguous = questions.some(
    (q) => q.question.includes('"') || q.options.some((o) => /[,"]/.test(o.label)),
  )
  if (ambiguous) return null
  const answers = new Map<string, string[]>()
  for (const m of output.matchAll(ANSWER_PAIR)) {
    const multi = questions.find((q) => q.question === m[1])?.multiSelect
    answers.set(m[1], (multi ? m[2].split(', ') : [m[2]]).filter((a) => a !== ''))
  }
  return questions.every((q) => answers.has(q.question)) ? answers : null
}

function QuestionBlock({ q, chosen }: { q: Question; chosen?: string[] }) {
  const labels = new Set(q.options.map((o) => o.label))
  const other = chosen?.filter((a) => !labels.has(a)) ?? []
  return (
    <div className="chat-question-block">
      {q.header && <span className="chat-question-header">{q.header}</span>}
      <div className="chat-question-text">{q.question}</div>
      {q.multiSelect && <div className="chat-question-hint">Select all that apply</div>}
      <ul className="chat-question-options">
        {q.options.map((o) => {
          const picked = chosen?.includes(o.label) ?? false
          return (
            <li key={o.label} className={`chat-question-option${picked ? ' chosen' : ''}`}>
              <span className="chat-question-marker" aria-hidden="true">
                {q.multiSelect ? (picked ? '☑' : '☐') : picked ? '●' : '○'}
              </span>
              <span className="chat-question-option-body">
                <span className="chat-question-option-label">{o.label}</span>
                {o.description && <span className="chat-question-option-desc">{o.description}</span>}
              </span>
              {picked && <span className="chat-question-sr">chosen</span>}
            </li>
          )
        })}
      </ul>
      {other.length > 0 && <div className="chat-question-other">Other: {other.join(', ')}</div>}
    </div>
  )
}

function PickerBlock({ q, stage, disabled, onOption, onOther, onText }: {
  q: Question
  stage: Stage
  disabled: boolean
  onOption: (index: number) => void
  onOther: () => void
  onText: (text: string) => void
}) {
  const role = q.multiSelect ? 'checkbox' : 'radio'
  const marker = (on: boolean) => (q.multiSelect ? (on ? '☑' : '☐') : on ? '●' : '○')
  const otherIndex = q.options.length
  const checked = stage.otherOn ? otherIndex : (stage.options[0] ?? 0)
  // Single-select is a radiogroup: one tab stop, arrows move it (WAI-ARIA).
  const tabIndex = (i: number) => (q.multiSelect || i === checked ? 0 : -1)
  const select = (i: number) => (i === otherIndex ? onOther() : onOption(i))

  const otherRef = useRef<HTMLInputElement>(null)
  // Only a tap or click on Other moves focus into its field; arrowing onto it
  // keeps focus on the radio.
  const focusOther = useRef(false)
  useEffect(() => {
    const input = otherRef.current
    if (!stage.otherOn || !input) return
    if (focusOther.current) {
      focusOther.current = false
      input.focus()
    }
    const viewport = window.visualViewport
    if (!viewport) return
    // The on-screen keyboard shrinks the viewport after focus already scrolled.
    const reveal = () => { if (document.activeElement === input) input.scrollIntoView?.({ block: 'nearest' }) }
    viewport.addEventListener('resize', reveal)
    return () => viewport.removeEventListener('resize', reveal)
  }, [stage.otherOn])

  const onKeyDown = (e: React.KeyboardEvent<HTMLDivElement>) => {
    if (q.multiSelect || (e.target as HTMLElement).getAttribute('role') !== 'radio') return
    const step = e.key === 'ArrowDown' || e.key === 'ArrowRight' ? 1 : e.key === 'ArrowUp' || e.key === 'ArrowLeft' ? -1 : 0
    if (!step) return
    e.preventDefault()
    const radios = e.currentTarget.querySelectorAll<HTMLButtonElement>('[role="radio"]')
    const next = (Array.from(radios).indexOf(e.target as HTMLButtonElement) + step + radios.length) % radios.length
    select(next)
    radios[next].focus()
  }

  return (
    <div className="chat-question-block">
      {q.header && <span className="chat-question-header">{q.header}</span>}
      <div className="chat-question-text">{q.question}</div>
      {q.multiSelect && <div className="chat-question-hint">Select all that apply</div>}
      <div className="chat-question-picks" role={q.multiSelect ? 'group' : 'radiogroup'} aria-label={q.question} onKeyDown={onKeyDown}>
        {q.options.map((o, i) => {
          const on = stage.options.includes(i)
          return (
            <button
              key={o.label}
              type="button"
              role={role}
              aria-checked={on}
              tabIndex={tabIndex(i)}
              disabled={disabled}
              className={`chat-question-pick${on ? ' on' : ''}`}
              onClick={() => onOption(i)}
            >
              <span className="chat-question-marker" aria-hidden="true">{marker(on)}</span>
              <span className="chat-question-option-body">
                <span className="chat-question-option-label">{o.label}</span>
                {o.description && <span className="chat-question-option-desc">{o.description}</span>}
              </span>
            </button>
          )
        })}
        <button
          type="button"
          role={role}
          aria-checked={stage.otherOn}
          tabIndex={tabIndex(otherIndex)}
          disabled={disabled}
          className={`chat-question-pick${stage.otherOn ? ' on' : ''}`}
          onClick={() => {
            focusOther.current = !stage.otherOn
            onOther()
          }}
        >
          <span className="chat-question-marker" aria-hidden="true">{marker(stage.otherOn)}</span>
          <span className="chat-question-option-body">
            <span className="chat-question-option-label">Other</span>
          </span>
        </button>
        {stage.otherOn && (
          <input
            ref={otherRef}
            type="text"
            className="chat-question-other-input"
            aria-label={`Other answer: ${q.question}`}
            maxLength={500}
            value={stage.text}
            disabled={disabled}
            onChange={(e) => onText(e.target.value)}
            onFocus={(e) => e.currentTarget.scrollIntoView?.({ block: 'nearest' })}
          />
        )}
      </div>
    </div>
  )
}

/** `terminalHref`: the question is still open and can't be shown here, so
 *  point to where it can be answered. */
function FallbackRow({ call, terminalHref }: { call: ToolCall; terminalHref?: string }) {
  return (
    <div className="chat-question-fallback" data-testid="question-fallback">
      <div className="chat-tool-call-row">
        <span className="chat-tool-name">{call.tool}</span>
        {call.title && <span className="chat-tool-title">{call.title}</span>}
      </div>
      {terminalHref && (
        <div className="chat-question-pending">
          Can't show this question here — answer it in the Terminal tab
          <a className="chat-question-terminal" href={terminalHref}>Open Terminal</a>
        </div>
      )}
    </div>
  )
}

type SendPhase =
  | { kind: 'idle' }
  | { kind: 'sending' }
  // Tied to the status it was sent under: a status change ends the wait.
  | { kind: 'sent'; status?: string }
  | { kind: 'moved' }
  | { kind: 'partial' }
  | { kind: 'error'; message: string }

/** `canAnswer`: the run is live and has a terminal to answer in. `answerable`:
 *  houston can also drive that terminal's prompt (a Claude run). `onLayout`
 *  fires when the card changes height on its own (detail loaded, picker shown
 *  or hidden), so a pinned timeline can stay pinned. */
export function QuestionCard({ item, runId, canAnswer, answerable, onLayout }: {
  item: QuestionItem
  runId: string
  canAnswer: boolean
  answerable: boolean
  onLayout: () => void
}) {
  const { call } = item
  const [detail, setDetail] = useState<ChatToolDetail | 'error' | null>(null)
  const [reload, setReload] = useState(0)
  const [stages, setStages] = useState<Stage[]>([])
  const [phase, setPhase] = useState<SendPhase>({ kind: 'idle' })
  const done = call.status === 'completed' || call.status === 'failed'

  useEffect(() => {
    let cancelled = false
    fetchTool(runId, call.toolCallId).then(
      (data) => { if (!cancelled) setDetail(data) },
      () => { if (!cancelled) setDetail((prev) => prev ?? 'error') },
    )
    return () => { cancelled = true }
  }, [runId, call.toolCallId, call.status, reload])

  const picking = !done && canAnswer && answerable
  useEffect(() => {
    if (detail) onLayout()
  }, [detail, picking, phase.kind, onLayout])

  if (!detail) return <FallbackRow call={call} />
  const terminalHref = !done && canAnswer ? runHash(runId, 'terminal') : undefined
  if (detail === 'error' || detail.inputOmitted) return <FallbackRow call={call} terminalHref={terminalHref} />
  const questions = parseQuestions(detail.input)
  if (questions.length === 0) return <FallbackRow call={call} terminalHref={terminalHref} />

  const output = done ? (detail.output ?? '') : ''
  const answers = call.status === 'completed' ? parseAnswers(questions, output) : null

  const stageOf = (i: number): Stage => stages[i] ?? emptyStage()
  const allStages = questions.map((_, i) => stageOf(i))
  const stage = (i: number, f: (s: Stage) => Stage) =>
    setStages(questions.map((_, j) => (j === i ? f(stageOf(j)) : stageOf(j))))
  const locked = phase.kind === 'sending' || phase.kind === 'partial' || (phase.kind === 'sent' && phase.status === call.status)

  const send = async () => {
    setPhase({ kind: 'sending' })
    const res = await answerQuestion(runId, call.toolCallId, toWire(allStages))
    if ('ok' in res) setPhase({ kind: 'sent', status: call.status })
    else if ('error' in res) setPhase({ kind: 'error', message: res.error })
    else if (res.partial) setPhase({ kind: 'partial' })
    else {
      setPhase({ kind: 'moved' })
      setReload((n) => n + 1)
    }
  }

  return (
    <div className="chat-question" data-seq={item.seq} data-id={item.id} data-status={call.status}>
      {picking
        ? questions.map((q, i) => (
            <PickerBlock
              key={i}
              q={q}
              stage={allStages[i]}
              disabled={locked}
              onOption={(o) => stage(i, (s) => toggleOption(s, o, q.multiSelect))}
              onOther={() => stage(i, (s) => toggleOther(s, q.multiSelect))}
              onText={(t) => stage(i, (s) => setText(s, t))}
            />
          ))
        : questions.map((q) => <QuestionBlock key={q.question} q={q} chosen={answers?.get(q.question)} />)}
      {picking && (
        <div className="chat-question-send">
          <button type="button" className="chat-question-send-btn" disabled={locked || !isComplete(allStages)} onClick={send}>
            Send answer
          </button>
          <div className="chat-question-status" role="status">
            {phase.kind === 'sent' && phase.status === call.status && 'Sent — waiting for Claude'}
            {phase.kind === 'moved' && 'The session moved on — refresh'}
            {phase.kind === 'partial' && (
              <>
                Part of the answer went in — finish in the{' '}
                <a className="chat-question-terminal" href={runHash(runId, 'terminal')}>Terminal tab</a>
              </>
            )}
            {phase.kind === 'error' && phase.message}
          </div>
        </div>
      )}
      {!done && canAnswer && !answerable && <div className="chat-question-pending">Waiting for an answer — answer in the Terminal tab</div>}
      {!done && !canAnswer && <div className="chat-question-declined">Not answered — the run ended or has no terminal</div>}
      {call.status === 'completed' && !answers && output && <pre className="chat-tool-output">{output}</pre>}
      {call.status === 'failed' && (
        <div className="chat-question-declined">
          Not answered
          {output && <pre className="chat-tool-output">{output}</pre>}
        </div>
      )}
    </div>
  )
}
