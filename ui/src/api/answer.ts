export interface QuestionAnswer {
  question: number
  options: number[]
  text?: string
}

export type AnswerResult =
  | { ok: true }
  // The pane no longer shows the prompt that was answered. `partial`: some keys
  // were typed before the mismatch, so a retry would fail its first-step check.
  | { moved: true; partial: boolean }
  | { error: string }

export interface Prompt {
  question: string
  choices: string[]
  detail: string
  frame: string
}

const timedOut = 'timed out — the answer may have been partly sent; check the agent before resending'

function isPartial(body: string): boolean {
  try {
    const parsed: unknown = JSON.parse(body)
    return parsed !== null && typeof parsed === 'object' && 'partial' in parsed && parsed.partial === true
  } catch {
    return false
  }
}

async function post(runId: string, body: unknown): Promise<AnswerResult> {
  try {
    const res = await fetch(`/api/runs/${encodeURIComponent(runId)}/answer`, {
      method: 'POST',
      signal: AbortSignal.timeout(15000),
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    })
    if (res.ok) return { ok: true }
    if (res.status === 401) return { error: 'session expired — reload' }
    const text = (await res.text().catch(() => '')).trim()
    if (res.status === 409 && text === 'pane is in copy mode') {
      return { error: 'The terminal is scrolled back (copy mode) — exit it, then retry' }
    }
    if (res.status === 409) return { moved: true, partial: isPartial(text) }
    if ((res.status === 502 || res.status === 503) && isPartial(text)) {
      return { error: 'may have been partly sent — check the agent before resending' }
    }
    return { error: text && !isPartial(text) ? text : `HTTP ${res.status}` }
  } catch (e) {
    return { error: e instanceof DOMException && e.name === 'TimeoutError' ? timedOut : 'offline' }
  }
}

export function answerQuestion(runId: string, toolCallId: string, answers: QuestionAnswer[]): Promise<AnswerResult> {
  return post(runId, { kind: 'question', toolCallId, answers })
}

export function answerChoice(runId: string, ordinal: number, frame: string): Promise<AnswerResult> {
  return post(runId, { kind: 'choice', ordinal, frame })
}

/** The pane's current permission dialog, or null when it shows none (404).
 *  Any other failure throws, so a poller can keep its last value. */
export async function fetchPrompt(runId: string): Promise<Prompt | null> {
  const res = await fetch(`/api/runs/${encodeURIComponent(runId)}/prompt`, { signal: AbortSignal.timeout(10000) })
  if (res.status === 404) return null
  if (!res.ok) throw new Error(`fetchPrompt: ${res.status} ${await res.text()}`)
  return (await res.json()) as Prompt
}
