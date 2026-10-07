import { useEffect, useState } from 'react'
import { fetchTool } from '../api/chat'
import type { ChatToolDetail } from '../api/chat'
import type { QuestionItem, ToolCall } from './chatModel'

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

function FallbackRow({ call }: { call: ToolCall }) {
  return (
    <div className="chat-tool-call-row" data-testid="question-fallback">
      <span className="chat-tool-name">{call.tool}</span>
      {call.title && <span className="chat-tool-title">{call.title}</span>}
    </div>
  )
}

export function QuestionCard({ item, runId }: { item: QuestionItem; runId: string }) {
  const { call } = item
  const [detail, setDetail] = useState<ChatToolDetail | 'error' | null>(null)
  const done = call.status === 'completed' || call.status === 'failed'

  useEffect(() => {
    let cancelled = false
    fetchTool(runId, call.toolCallId).then(
      (data) => { if (!cancelled) setDetail(data) },
      () => { if (!cancelled) setDetail((prev) => prev ?? 'error') },
    )
    return () => { cancelled = true }
  }, [runId, call.toolCallId, call.status])

  if (!detail || detail === 'error' || detail.inputOmitted) return <FallbackRow call={call} />
  const questions = parseQuestions(detail.input)
  if (questions.length === 0) return <FallbackRow call={call} />

  const output = done ? (detail.output ?? '') : ''
  const answers = call.status === 'completed' ? parseAnswers(questions, output) : null

  return (
    <div className="chat-question" data-seq={item.seq} data-id={item.id} data-status={call.status}>
      {questions.map((q) => (
        <QuestionBlock key={q.question} q={q} chosen={answers?.get(q.question)} />
      ))}
      {!done && <div className="chat-question-pending">Waiting for an answer — answer in the Terminal tab</div>}
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
