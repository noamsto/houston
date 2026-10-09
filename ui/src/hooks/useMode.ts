import { useEffect, useState } from 'react'
import { fetchMode, type Mode } from '../api/mode'

const RETRY_MS = 5000

/** Null until the server's mode is known; a failed fetch is retried until it succeeds. */
export function useMode(): Mode | null {
  const [mode, setMode] = useState<Mode | null>(null)
  useEffect(() => {
    let cancelled = false
    let timer: ReturnType<typeof setTimeout> | undefined
    const load = () => {
      fetchMode().then(
        (m) => { if (!cancelled) setMode(m) },
        () => { if (!cancelled) timer = setTimeout(load, RETRY_MS) },
      )
    }
    load()
    return () => {
      cancelled = true
      clearTimeout(timer)
    }
  }, [])
  return mode
}
