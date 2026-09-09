import { useCallback, useEffect, useRef, useState } from 'react'

export type TerminalLifecycle = 'live' | 'ended'

// How long the pane socket may sit disconnected before we treat the pane as
// gone. The tmux poll that flips `caps.terminal` lags the pane's actual
// death, so the common path is a bare-1006 close that recovers on its own
// (e.g. a phone screen lock, handled by usePaneSocket's visibilitychange
// reconnect) — a single close must not read as "ended".
const DISCONNECT_GRACE_MS = 10_000

interface TerminalLifecycleResult {
  /** Has the terminal ever actually gone live for the current run id? Distinguishes a
   *  deep link to a run that never had (or already lost) terminal capability — which
   *  degrades silently to Activity — from an in-session loss, which is shown explicitly. */
  everLive: boolean
  state: TerminalLifecycle
  /** SSE stream is stale while the terminal is otherwise live — "we don't know", not "it's gone". */
  reconnecting: boolean
  /** Bumped on every manual reconnect; part of TerminalPane's key so a reconnect forces a fresh mount. */
  attempt: number
  onConnectionChange: (connected: boolean) => void
  reconnect: () => void
}

/**
 * Tracks the Terminal tab's pane lifecycle across four cases the pane
 * WebSocket and `caps.terminal` don't cover on their own: capability loss
 * after going live, eviction (handled upstream by RunDetail's `!run` branch),
 * a pane socket that never comes back, and a stale SSE stream. See
 * RunDetail.tsx's terminal-tab render branch for how these states map to UI.
 *
 * `capable` (run.caps.terminal && has tmux data) drives the "ended"/recovery
 * transitions on its own, independent of `onTerminalTab` — switching to the
 * Activity tab and back must not, by itself, read as the pane dying.
 * `onTerminalTab` only feeds the "has this ever actually been shown" latch.
 *
 * Transitions are computed during render (React's documented pattern for
 * state derived from props — see "Adjusting state based on a prop change")
 * rather than in an effect, since a setState-in-effect here would just
 * trigger an extra cascading render for the same result.
 */
export function useTerminalLifecycle(
  runId: string,
  capable: boolean,
  onTerminalTab: boolean,
  streamConnected: boolean,
): TerminalLifecycleResult {
  const liveNow = onTerminalTab && capable

  const [state, setState] = useState<TerminalLifecycle>('live')
  const [attempt, setAttempt] = useState(0)
  const [everLive, setEverLive] = useState(liveNow)
  const [prevRunId, setPrevRunId] = useState(runId)
  const [prevCapable, setPrevCapable] = useState(capable)
  const disconnectTimerRef = useRef<ReturnType<typeof setTimeout> | undefined>(undefined)

  if (runId !== prevRunId) {
    setPrevRunId(runId)
    setPrevCapable(capable)
    setEverLive(liveNow)
    setState('live')
    setAttempt(0)
  } else {
    if (liveNow && !everLive) setEverLive(true)

    if (capable !== prevCapable) {
      setPrevCapable(capable)
      if (!capable && everLive) {
        setState('ended') // Case 1: caps.terminal dropped after going live.
      } else if (capable && everLive) {
        setState('live') // The pane respawned under the same run id.
      }
    }
  }

  // Cancel any pending disconnect timer when the run identity changes, or on unmount.
  useEffect(() => () => clearTimeout(disconnectTimerRef.current), [runId])

  const onConnectionChange = useCallback((connected: boolean) => {
    clearTimeout(disconnectTimerRef.current)
    if (connected) return
    disconnectTimerRef.current = setTimeout(() => {
      setState((s) => (s === 'live' ? 'ended' : s))
    }, DISCONNECT_GRACE_MS)
  }, [])

  const reconnect = useCallback(() => {
    clearTimeout(disconnectTimerRef.current)
    setState('live')
    setAttempt((a) => a + 1)
  }, [])

  return {
    everLive,
    state,
    reconnecting: state === 'live' && !streamConnected,
    attempt,
    onConnectionChange,
    reconnect,
  }
}
