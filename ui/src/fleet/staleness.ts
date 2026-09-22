import type { Run } from '../api/runs'

/** The slice of a run the freshness rules read; a Workspace pane carries it too. */
export type Freshness = { state: string; updated_at: number }

/**
 * How recently a run must have reported to count as fresh.
 *
 * Tuned against real data: a live server carried 22 finished-or-detached runs
 * alongside 10 live ones, six of them still reporting `blocked` from up to five
 * hours earlier. Move this if an hour proves wrong in practice — it is the only
 * number that decides what the badge counts.
 */
export const FRESH_MS = 60 * 60 * 1000

export function isFresh(run: Freshness, now: number): boolean {
  if (!run.updated_at) return false
  return now - run.updated_at * 1000 <= FRESH_MS
}

/**
 * needsYou drives the badge and the sort. A run that asked for input long ago
 * is almost always a session that ended while waiting, and counting it would
 * leave the badge permanently lit — but see isHistory: it is still shown.
 */
export function needsYou(run: Freshness, now: number): boolean {
  return run.state === 'blocked' && isFresh(run, now)
}

/**
 * How long a review-state run with no live pane stays out of history. A
 * crew-bus worker parked at pr_open has no session once it exits, so nothing
 * ever advances that state; the bus carries no merged/closed bit, so age is
 * the only signal houston has that the run is over. A day is long enough that
 * a session which just ended mid-shepherd stays visible, short enough that a
 * merged PR does not sit in Active forever.
 */
export const REVIEW_HISTORY_MS = 24 * 60 * 60 * 1000

/**
 * isHistory marks a run as foldable behind the "All" filter. Terminal states
 * qualify once stale, and a review run qualifies once it is a day old with no
 * live pane — the one non-terminal state houston publishes that nothing will
 * ever move on its own. A run that is blocked, running or thinking is never
 * history however old it looks, because hiding something that is waiting on
 * you is the worse failure.
 */
export function isHistory(run: Run, now: number): boolean {
  if (run.state === 'done' || run.state === 'failed') return !isFresh(run, now)
  if (run.state === 'review' && run.caps?.terminal !== true) {
    return run.updated_at > 0 && now - run.updated_at * 1000 > REVIEW_HISTORY_MS
  }
  return false
}
