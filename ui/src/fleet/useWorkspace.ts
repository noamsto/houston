import { useCallback, useEffect, useRef, useState } from 'react'
import { fetchWorkspace } from '../api/workspace'
import type { Workspace } from '../api/workspace'

const POLL_INTERVAL_MS = 3000

/**
 * Polls /api/workspace on a fixed interval. A failed poll sets `error` but
 * keeps the last-known `workspace` rather than blanking the view.
 *
 * Polling pauses while the tab is hidden (a phone in a pocket shouldn't keep
 * shelling out to tmux and git every 3 s) and resumes with an immediate fetch
 * when it becomes visible again. `refresh` fetches on demand, for Retry; the
 * newest request always wins so a slow, older response can't overwrite it, and
 * `refreshing` is true while any request is in flight.
 */
export function useWorkspace() {
  const [workspace, setWorkspace] = useState<Workspace | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [loading, setLoading] = useState(true)
  const [refreshing, setRefreshing] = useState(false)
  const alive = useRef(true)
  const latest = useRef(0)

  const load = useCallback(() => {
    const seq = ++latest.current
    fetchWorkspace()
      .then((w) => {
        if (!alive.current || seq !== latest.current) return
        setWorkspace(w)
        setError(null)
        setLoading(false)
        setRefreshing(false)
      })
      .catch((e: unknown) => {
        if (!alive.current || seq !== latest.current) return
        setError(e instanceof Error ? e.message : String(e))
        setLoading(false)
        setRefreshing(false)
      })
  }, [])

  const refresh = useCallback(() => {
    setRefreshing(true)
    load()
  }, [load])

  useEffect(() => {
    alive.current = true

    load()
    const id = setInterval(() => {
      if (!document.hidden) load()
    }, POLL_INTERVAL_MS)
    const onVisible = () => {
      if (!document.hidden) load()
    }
    document.addEventListener('visibilitychange', onVisible)

    return () => {
      alive.current = false
      clearInterval(id)
      document.removeEventListener('visibilitychange', onVisible)
    }
  }, [load])

  return { workspace, error, loading, refreshing, refresh }
}
