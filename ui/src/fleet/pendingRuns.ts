// A run houston just started (a dispatcher restart) is not listed until its
// engine reports, so its detail shows a starting placeholder instead of "no
// longer available" while the id is pending.
export const PENDING_RUN_TTL_MS = 60_000

const pending = new Map<string, number>()

export function markPendingRun(id: string, now: number): void {
  pending.set(id, now + PENDING_RUN_TTL_MS)
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

export function clearPendingRuns(): void {
  pending.clear()
}
