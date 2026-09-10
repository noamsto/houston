import { useEffect, useState } from 'react'
import { fetchWorkspace } from '../api/workspace'
import type { Workspace } from '../api/workspace'

const POLL_INTERVAL_MS = 3000

/**
 * Polls /api/workspace on a fixed interval. A failed poll sets `error` but
 * keeps the last-known `workspace` rather than blanking the view.
 */
export function useWorkspace() {
  const [workspace, setWorkspace] = useState<Workspace | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [loading, setLoading] = useState(true)

  useEffect(() => {
    let cancelled = false

    const poll = () => {
      fetchWorkspace()
        .then((w) => {
          if (cancelled) return
          setWorkspace(w)
          setError(null)
          setLoading(false)
        })
        .catch((e: unknown) => {
          if (cancelled) return
          setError(e instanceof Error ? e.message : String(e))
          setLoading(false)
        })
    }

    poll()
    const id = setInterval(poll, POLL_INTERVAL_MS)

    return () => {
      cancelled = true
      clearInterval(id)
    }
  }, [])

  return { workspace, error, loading }
}
