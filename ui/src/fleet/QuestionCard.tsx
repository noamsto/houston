import { useEffect, useState } from 'react'
import { fetchTool } from '../api/chat'
import type { ChatToolDetail } from '../api/chat'
import type { QuestionItem } from './chatModel'
import { FallbackRow, QuestionBlock } from './questionParts'
import type { Question, QuestionOption } from './questionParts'
import { QuestionWizard } from './QuestionWizard'

const RETRY_MS = 3000

const str = (v: unknown): string | undefined => (typeof v === 'string' && v !== '' ? v : undefined)

function parseQuestions(input: unknown): Question[] {
  const raw = (input as { questions?: unknown } | null)?.questions
  if (!Array.isArray(raw)) return []
  const out: Question[] = []
  for (const q of raw as Record<string, unknown>[]) {
    const question = str(q?.question)
    if (!question) continue
    const options: QuestionOption[] = []
    let preview = false
    for (const o of Array.isArray(q.options) ? (q.options as Record<string, unknown>[]) : []) {
      const label = str(o?.label)
      if (label) options.push({ label, description: str(o.description) })
      if (typeof o?.preview === 'string' && o.preview.trim() !== '') preview = true
    }
    const multiSelect = q.multiSelect === true
    out.push({ question, header: str(q.header), multiSelect, options, preview: preview && !multiSelect })
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
  // `n` counts finished fetches so a failed retry schedules the next one.
  const [fetched, setFetched] = useState({ ok: true, n: 0 })
  const [reload, setReload] = useState(0)
  const done = call.status === 'completed' || call.status === 'failed'

  useEffect(() => {
    let cancelled = false
    fetchTool(runId, call.toolCallId).then(
      (data) => {
        if (cancelled) return
        setDetail(data)
        setFetched((f) => ({ ok: true, n: f.n + 1 }))
      },
      () => {
        if (cancelled) return
        setDetail((prev) => prev ?? 'error')
        setFetched((f) => ({ ok: false, n: f.n + 1 }))
      },
    )
    return () => { cancelled = true }
  }, [runId, call.toolCallId, call.status, reload])

  useEffect(() => {
    if (done || fetched.ok) return
    const timer = setTimeout(() => setReload((n) => n + 1), RETRY_MS)
    return () => clearTimeout(timer)
  }, [done, fetched])

  const picking = !done && canAnswer && answerable
  useEffect(() => {
    if (detail) onLayout()
  }, [detail, picking, onLayout])

  const parsed = detail && detail !== 'error' && !detail.inputOmitted ? parseQuestions(detail.input) : []

  // The wizard renders from the pane's own state, so it does not wait for
  // (or depend on) the capped tool detail.
  if (picking) {
    return (
      <div className="chat-question" data-seq={item.seq} data-id={item.id} data-status={call.status}>
        <QuestionWizard key={call.toolCallId} runId={runId} call={call} fallbackQuestions={parsed} onLayout={onLayout} />
      </div>
    )
  }

  if (!detail) return <FallbackRow call={call} />
  const terminalRunId = !done && canAnswer ? runId : undefined
  if (detail === 'error' || detail.inputOmitted) return <FallbackRow call={call} terminalRunId={terminalRunId} />
  const questions = parsed
  if (questions.length === 0) return <FallbackRow call={call} terminalRunId={terminalRunId} />

  const output = done ? (detail.output ?? '') : ''
  const answers = call.status === 'completed' ? parseAnswers(questions, output) : null

  return (
    <div className="chat-question" data-seq={item.seq} data-id={item.id} data-status={call.status}>
      {questions.map((q) => <QuestionBlock key={q.question} q={q} chosen={answers?.get(q.question)} />)}
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
