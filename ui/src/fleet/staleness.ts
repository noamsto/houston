import type { Run } from '../api/runs'

/**
 * How recently a run must have reported to count as fresh.
 *
 * Tuned against real data: a live server carried 22 finished-or-detached runs
 * alongside 10 live ones, six of them still reporting `blocked` from up to five
 * hours earlier. Move this if an hour proves wrong in practice — it is the only
 * number that decides what the badge counts.
 */
export const FRESH_MS = 60 * 60 * 1000

export function isFresh(run: Run, now: number): boolean {
  if (!run.updated_at) return false
  return now - run.updated_at * 1000 <= FRESH_MS
}

/**
 * needsYou drives the badge and the sort. A run that asked for input long ago
 * is almost always a session that ended while waiting, and counting it would
 * leave the badge permanently lit — but see isHistory: it is still shown.
 */
export function needsYou(run: Run, now: number): boolean {
  return run.state === 'blocked' && isFresh(run, now)
}

/**
 * isHistory marks a run as foldable behind the "All" filter. Only terminal
 * states qualify, and only once stale: a run that is blocked, running or
 * thinking is never history however old it looks, because hiding something
 * that is waiting on you is the worse failure.
 */
export function isHistory(run: Run, now: number): boolean {
  if (run.state !== 'done' && run.state !== 'failed') return false
  return !isFresh(run, now)
}
