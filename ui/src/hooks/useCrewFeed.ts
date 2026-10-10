import { useCallback, useEffect, useRef, useState } from 'react'
import { CrewFeedReset, CrewFeedUnavailable, crewFeedStreamFromStart, crewFeedStreamURL, fetchCrewFeed } from '../api/crewFeed'
import type { FeedEntry } from '../api/crewFeed'
import { watchLiveness } from './streamLiveness'

export type CrewFeedStatus = 'loading' | 'ready' | 'unavailable' | 'error'

export interface UseCrewFeedResult {
  status: CrewFeedStatus
  entries: FeedEntry[]
  more: boolean
  loadOlder: () => Promise<void>
  retry: () => void
}

// A closed EventSource never reconnects on its own, so the hook restarts the
// whole catch-up-then-stream sequence itself.
const EVENTSOURCE_CLOSED = 2

function dropKnown(incoming: FeedEntry[], known: FeedEntry[]): FeedEntry[] {
  const ids = new Set(known.map((e) => e.id))
  return incoming.filter((e) => !ids.has(e.id))
}

/**
 * Loads the newest page of a dispatcher run's crew feed, then follows it live
 * over SSE. `enabled` gates everything — flipping it off (or unmounting)
 * closes the stream and drops in-flight fetches on the floor.
 */
export function useCrewFeed(runId: string, enabled: boolean): UseCrewFeedResult {
  const [status, setStatus] = useState<CrewFeedStatus>('loading')
  const [entries, setEntries] = useState<FeedEntry[]>([])
  const [more, setMore] = useState(false)
  const [retryToken, setRetryToken] = useState(0)

  const epochRef = useRef<string | null>(null)
  const esRef = useRef<EventSource | null>(null)
  // Bumped by every fresh load and by teardown, so a loadOlder started under
  // an older generation drops its result instead of overwriting newer state.
  const generationRef = useRef(0)
  const olderRef = useRef<AbortController | null>(null)
  const entriesRef = useRef<FeedEntry[]>([])
  const reloadRef = useRef<(() => void) | null>(null)

  entriesRef.current = entries

  const retry = useCallback(() => setRetryToken((t) => t + 1), [])

  useEffect(() => {
    if (!enabled) return

    let cancelled = false
    const controller = new AbortController()
    let retryTimer: ReturnType<typeof setTimeout> | undefined

    setStatus('loading')
    setEntries([])
    setMore(false)
    epochRef.current = null

    function newGeneration() {
      generationRef.current++
      olderRef.current?.abort()
      olderRef.current = null
    }

    // An empty feed has no entry id to resume after, so it streams from the
    // start of its epoch.
    function streamURL(epoch: string, list: FeedEntry[]): string {
      return list.length
        ? crewFeedStreamURL(runId, list[list.length - 1].id)
        : crewFeedStreamFromStart(runId, epoch)
    }

    const liveness = watchLiveness(() => {
      // No stream means a load, retry or terminal state is in charge. Resuming
      // from the cursor keeps loaded history; the server answers `reset` when
      // it can't serve the cursor.
      const epoch = epochRef.current
      if (cancelled || esRef.current === null || epoch === null) return
      openStream(streamURL(epoch, entriesRef.current))
    })

    function closeStream() {
      esRef.current?.close()
      esRef.current = null
    }

    function openStream(url: string) {
      closeStream()
      const es = new EventSource(url)
      esRef.current = es

      es.addEventListener('open', () => liveness.seen())
      es.addEventListener('ping', () => liveness.seen())

      es.addEventListener('entries', (ev: MessageEvent<string>) => {
        if (cancelled) return
        liveness.seen()
        let incoming: FeedEntry[]
        try {
          incoming = JSON.parse(ev.data) as FeedEntry[]
        } catch (e) {
          console.error('crew feed entries parse failed', e)
          return
        }
        setEntries((prev) => {
          const fresh = dropKnown(incoming, prev)
          return fresh.length ? [...prev, ...fresh] : prev
        })
      })

      es.addEventListener('reset', () => {
        if (cancelled) return
        liveness.seen()
        closeStream()
        void loadNewest()
      })

      es.onerror = () => {
        if (cancelled || es.readyState !== EVENTSOURCE_CLOSED) return
        closeStream()
        retryTimer = setTimeout(() => {
          if (!cancelled) void loadNewest()
        }, 3000)
      }
    }

    async function loadNewest() {
      newGeneration()
      // Until the page lands, loadOlder would anchor on history from before the gap.
      epochRef.current = null
      setMore(false)
      try {
        const page = await fetchCrewFeed(runId, { limit: 50, signal: controller.signal })
        if (cancelled) return
        epochRef.current = page.epoch
        setEntries(page.entries)
        setMore(page.more)
        setStatus('ready')
        openStream(streamURL(page.epoch, page.entries))
      } catch (e) {
        if (cancelled) return
        if (e instanceof CrewFeedUnavailable) {
          closeStream()
          setStatus('unavailable')
          return
        }
        setStatus('error')
      }
    }

    reloadRef.current = () => {
      closeStream()
      void loadNewest()
    }
    void loadNewest()

    return () => {
      cancelled = true
      controller.abort()
      liveness.stop()
      newGeneration()
      clearTimeout(retryTimer)
      closeStream()
      reloadRef.current = null
    }
  }, [runId, enabled, retryToken])

  const loadOlder = useCallback(async () => {
    if (!enabled || epochRef.current === null || olderRef.current || !more) return
    const oldest = entriesRef.current[0]?.id
    if (oldest === undefined) return

    const generation = generationRef.current
    const controller = new AbortController()
    olderRef.current = controller
    try {
      const page = await fetchCrewFeed(runId, { before: oldest, limit: 50, signal: controller.signal })
      if (generation !== generationRef.current) return
      setEntries((prev) => [...dropKnown(page.entries, prev), ...prev])
      setMore(page.more)
    } catch (e) {
      if (generation !== generationRef.current) return
      if (e instanceof CrewFeedUnavailable) {
        setStatus('unavailable')
        setMore(false)
      } else if (e instanceof CrewFeedReset) reloadRef.current?.()
    } finally {
      if (olderRef.current === controller) olderRef.current = null
    }
  }, [runId, enabled, more])

  return { status, entries, more, loadOlder, retry }
}
