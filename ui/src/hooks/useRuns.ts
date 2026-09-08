import { useEffect, useState } from 'react'
import type { Run } from '../api/runs'

/**
 * applyEvent folds one streamed Run into the map, returning a new map.
 *
 * A removal arrives as an ordinary update carrying only `id` and `removed`, so
 * it must be checked before any other field is read.
 */
export function applyEvent(runs: Map<string, Run>, r: Run): Map<string, Run> {
  const next = new Map(runs)
  if (r.removed) {
    next.delete(r.id)
    return next
  }
  next.set(r.id, r)
  return next
}

/**
 * Subscribes to /api/runs/stream. The server sends one `snapshot` event on
 * connect and one `update` per composed change; it also re-sends a full
 * `snapshot` when it detects this subscriber missed an update, so a snapshot
 * arriving mid-stream is a resync and replaces the map wholesale.
 */
export function useRuns() {
  const [runs, setRuns] = useState<Map<string, Run>>(new Map())
  const [connected, setConnected] = useState(false)

  useEffect(() => {
    const es = new EventSource('/api/runs/stream')

    es.addEventListener('open', () => setConnected(true))

    es.addEventListener('snapshot', (ev: MessageEvent<string>) => {
      try {
        const arr = JSON.parse(ev.data) as Run[]
        setRuns(new Map(arr.map((r) => [r.id, r])))
        setConnected(true)
      } catch (e) {
        console.error('runs snapshot parse failed', e)
      }
    })

    es.addEventListener('update', (ev: MessageEvent<string>) => {
      try {
        const r = JSON.parse(ev.data) as Run
        setRuns((prev) => applyEvent(prev, r))
      } catch (e) {
        console.error('runs update parse failed', e)
      }
    })

    es.onerror = () => setConnected(false) // EventSource retries on its own

    return () => es.close()
  }, [])

  return { runs: Array.from(runs.values()), connected }
}
