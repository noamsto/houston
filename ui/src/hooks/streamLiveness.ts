// A phone that slept leaves EventSource and WebSocket connections half-open:
// no error ever fires, so the stream looks alive and never delivers again.
// Streams are therefore reopened after a long hidden spell, and when a
// visible page hears nothing (the server sends a `ping` every 25 s).
export const RESYNC_AFTER_HIDDEN_MS = 10_000
export const STALE_AFTER_MS = 60_000
const CHECK_EVERY_MS = 5_000

/**
 * Tracks how long the page was hidden. `onVisible` receives the hidden
 * duration (ms) each time the page becomes visible after being hidden.
 */
export function trackHidden(onVisible: (hiddenMs: number) => void): () => void {
  let hiddenAt: number | null = null
  const onChange = () => {
    if (document.visibilityState === 'hidden') {
      hiddenAt ??= Date.now()
      return
    }
    const hiddenMs = hiddenAt === null ? 0 : Date.now() - hiddenAt
    hiddenAt = null
    onVisible(hiddenMs)
  }
  document.addEventListener('visibilitychange', onChange)
  return () => document.removeEventListener('visibilitychange', onChange)
}

export interface Liveness {
  seen: () => void
  stop: () => void
}

/**
 * Calls `onStale` when a visible page has heard nothing for STALE_AFTER_MS, or
 * becomes visible after RESYNC_AFTER_HIDDEN_MS hidden. The owner calls `seen`
 * on every sign of life from the stream (open, messages, pings).
 */
export function watchLiveness(onStale: () => void): Liveness {
  let lastSeen = Date.now()
  const fire = () => {
    lastSeen = Date.now()
    onStale()
  }
  const timer = setInterval(() => {
    if (document.visibilityState !== 'visible') return
    if (Date.now() - lastSeen > STALE_AFTER_MS) fire()
  }, CHECK_EVERY_MS)
  const untrack = trackHidden((hiddenMs) => {
    if (hiddenMs >= RESYNC_AFTER_HIDDEN_MS) fire()
  })
  return {
    seen: () => { lastSeen = Date.now() },
    stop: () => {
      clearInterval(timer)
      untrack()
    },
  }
}
