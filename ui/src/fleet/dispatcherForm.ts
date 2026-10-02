import type { DispatcherRequest } from '../api/dispatch'

export const MAX_TASKS = 20
const MAX_TASK_CHARS = 2000
const MAX_PROMPT_BYTES = 8 << 10

// Go's unicode.IsSpace, exactly — not JS \s, which also matches U+FEFF.
const GO_SPACE_RUN = /[\t\n\v\f\r \u0085\u00a0\u1680\u2000-\u200a\u2028\u2029\u202f\u205f\u3000]+/u

// Cc after normalization; the whitespace members of Cc are already collapsed.
function hasControlChar(s: string): boolean {
  for (let i = 0; i < s.length; i++) {
    const c = s.charCodeAt(i)
    if (c <= 0x1f || (c >= 0x7f && c <= 0x9f)) return true
  }
  return false
}

/** Mirrors server normalizeTask: every whitespace run becomes one space. */
export function normalizeTask(s: string): string {
  return s.split(GO_SPACE_RUN).filter((p) => p !== '').join(' ')
}

function composePrompt(tasks: string[]): string {
  if (tasks.length === 0) return ''
  if (tasks.length === 1) return tasks[0]
  return `${tasks.length} tasks:` + tasks.map((t, i) => ` (${i + 1}) ${t}`).join('')
}

/** A short reason the task rows can't be sent, or null when they're fine. */
export function taskRowsProblem(rows: string[]): string | null {
  const tasks: string[] = []
  for (const [i, raw] of rows.entries()) {
    const t = normalizeTask(raw)
    if (t === '') continue
    tasks.push(t)
    const n = `Task ${i + 1}`
    if ([...t].length > MAX_TASK_CHARS) return `${n} is limited to ${MAX_TASK_CHARS} characters`
    if (hasControlChar(t)) return `${n} contains a control character`
    if (t.startsWith('-')) return `${n} cannot start with "-"`
  }
  if (tasks.length > MAX_TASKS) return `At most ${MAX_TASKS} tasks`
  if (new TextEncoder().encode(composePrompt(tasks)).length > MAX_PROMPT_BYTES) {
    return `Tasks are too long together (${MAX_PROMPT_BYTES >> 10} KB max)`
  }
  return null
}

export function buildDispatcherRequest(f: {
  repo: string
  rows: string[]
  engine: string
  model: string
  effort: string
}): DispatcherRequest {
  const tasks = f.rows.map(normalizeTask).filter((r) => r !== '')
  const req: DispatcherRequest = { repo: f.repo, tasks, engine: f.engine }
  if (f.model) req.model = f.model
  if (f.effort) req.effort = f.effort
  return req
}
