// A run houston just started (a dispatcher restart) is not listed until its
// engine reports, so its detail shows a starting placeholder instead of "no
// longer available" while the id is pending.
//
// A failure is recorded only when the server attests that it already ended the
// old session; a refusal before that leaves the old run alive, so nothing is
// recorded for it.
export const PENDING_RUN_TTL_MS = 60_000
export const RESTART_FAILURE_TTL_MS = 10 * 60_000

export interface RestartFailure {
  error: string
  output?: string
  crew?: string
  status: number
}

const pending = new Map<string, number>()
const failures = new Map<string, { failure: RestartFailure; expiry: number }>()

export function markPendingRun(id: string, now: number): void {
  pending.set(id, now + PENDING_RUN_TTL_MS)
}

export function clearPendingRun(id: string): void {
  pending.delete(id)
}

export function isPendingRun(id: string, now: number): boolean {
  const expiry = pending.get(id)
  if (expiry === undefined) return false
  if (now >= expiry) {
    pending.delete(id)
    return false
  }
  return true
}

// A restart failure can arrive after the old run left the list and its
// control unmounted, so the old run's route reads it from here.
export function recordRestartFailure(id: string, failure: RestartFailure, now: number): void {
  failures.set(id, { failure, expiry: now + RESTART_FAILURE_TTL_MS })
}

export function restartFailure(id: string, now: number): RestartFailure | null {
  const entry = failures.get(id)
  if (!entry) return null
  if (now >= entry.expiry) {
    failures.delete(id)
    return null
  }
  return entry.failure
}

export function clearRestartFailure(id: string): void {
  failures.delete(id)
}

export function clearPendingRuns(): void {
  pending.clear()
  failures.clear()
}

// Only these two failures prove the new launcher is not running, so only they
// make a manual relaunch safe.
function launcherNotRunning(f: RestartFailure): boolean {
  return f.status === 422 || (f.status === 502 && f.error.startsWith('the old dispatcher process is still running'))
}

export function relaunchAdvice(f: RestartFailure, crew: string): string {
  return launcherNotRunning(f)
    ? `Relaunch it from a terminal in the repo with CREW_ID=${crew} dispatcher.`
    : 'Check Fleet for the new dispatcher before relaunching.'
}
