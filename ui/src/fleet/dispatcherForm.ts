import type { DispatcherRequest } from '../api/dispatch'

export const MAX_TASKS = 20
const MAX_TASK_CHARS = 2000

/** Mirrors server normalizeTask: every whitespace run becomes one space. */
export function normalizeTask(s: string): string {
  return s.replace(/\s+/gu, ' ').trim()
}

function normalizedRows(rows: string[]): string[] {
  return rows.map(normalizeTask).filter((r) => r !== '')
}

/** A short reason the task rows can't be sent, or null when they're fine. */
export function taskRowsProblem(rows: string[]): string | null {
  const tasks = normalizedRows(rows)
  if (tasks.length > MAX_TASKS) return `At most ${MAX_TASKS} tasks`
  if (tasks.some((t) => t.startsWith('-'))) return 'A task cannot start with "-"'
  if (tasks.some((t) => [...t].length > MAX_TASK_CHARS)) return `A task is limited to ${MAX_TASK_CHARS} characters`
  return null
}

export function buildDispatcherRequest(f: {
  repo: string
  rows: string[]
  engine: string
  model: string
  effort: string
}): DispatcherRequest {
  const req: DispatcherRequest = { repo: f.repo, tasks: normalizedRows(f.rows), engine: f.engine }
  if (f.model) req.model = f.model
  if (f.effort) req.effort = f.effort
  return req
}
