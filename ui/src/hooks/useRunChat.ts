import { useCallback, useEffect, useRef, useState } from 'react'
import { ChatUnavailable, chatStreamURL, fetchChatPage } from '../api/chat'
import type { ChatUpdate } from '../api/chat'
import { mergeUpdates } from '../fleet/chatModel'
import { watchLiveness } from './streamLiveness'

export type ChatStatus = 'loading' | 'ready' | 'unavailable' | 'error'

export interface UseRunChatResult {
  status: ChatStatus
  updates: ChatUpdate[]
  liveIds: Set<string>
  more: boolean
  loadEarlier: () => Promise<void>
  retry: () => void
}

// A closed EventSource never reconnects on its own (native reconnect only
// applies to a still-open one whose connection dropped), so the hook has to
// notice and restart the whole catch-up-then-stream sequence itself.
const EVENTSOURCE_CLOSED = 2

function lastSeqOf(updates: ChatUpdate[]): number {
  return updates.length ? updates[updates.length - 1].seq : 0
}

/**
 * Loads the newest chat page for a run, then follows it live over SSE.
 * `enabled` gates everything — flipping it off (or unmounting) closes the
 * stream and drops in-flight fetches on the floor.
 */
export function useRunChat(runId: string, enabled: boolean): UseRunChatResult {
  const [status, setStatus] = useState<ChatStatus>('loading')
  const [updates, setUpdates] = useState<ChatUpdate[]>([])
  const [liveIds, setLiveIds] = useState<Set<string>>(new Set())
  const [more, setMore] = useState(false)
  const [retryToken, setRetryToken] = useState(0)

  const epochRef = useRef<string | null>(null)
  const esRef = useRef<EventSource | null>(null)
  // Bumped by every fresh load (mount, retry, SSE reset/reconnect) and by
  // teardown, so a loadEarlier started under an older generation drops its
  // result instead of overwriting newer state.
  const generationRef = useRef(0)
  const earlierRef = useRef<AbortController | null>(null)
  const updatesRef = useRef<ChatUpdate[]>([])
  const connRef = useRef<{ openStream: (epoch: string, seq: number) => void } | null>(null)

  updatesRef.current = updates

  const retry = useCallback(() => setRetryToken((t) => t + 1), [])

  useEffect(() => {
    if (!enabled) return

    let cancelled = false
    const controller = new AbortController()
    let retryTimer: ReturnType<typeof setTimeout> | undefined

    setStatus('loading')
    setUpdates([])
    setLiveIds(new Set())
    setMore(false)
    epochRef.current = null

    function newGeneration() {
      generationRef.current++
      earlierRef.current?.abort()
      earlierRef.current = null
    }

    const liveness = watchLiveness(() => {
      // No stream means a load, retry or terminal state is in charge. Resuming
      // from the cursor keeps loaded history; the server answers `reset` when
      // it can't serve the cursor.
      const epoch = epochRef.current
      if (cancelled || esRef.current === null || epoch === null) return
      openStream(epoch, lastSeqOf(updatesRef.current))
    })

    function closeStream() {
      esRef.current?.close()
      esRef.current = null
    }

    function openStream(epoch: string, seq: number) {
      closeStream()
      const es = new EventSource(chatStreamURL(runId, epoch, seq))
      esRef.current = es
      let highWaterSeq = seq
      // The server answers each (re)connect with one event holding everything
      // after the cursor; later events are live ticks that may batch several
      // updates of one message, so they all animate.
      let catchUp = true

      es.addEventListener('open', () => {
        liveness.seen()
        catchUp = true
      })
      es.addEventListener('ping', () => liveness.seen())

      es.addEventListener('updates', (ev: MessageEvent<string>) => {
        if (cancelled) return
        liveness.seen()
        let incoming: ChatUpdate[]
        try {
          incoming = JSON.parse(ev.data) as ChatUpdate[]
        } catch (e) {
          console.error('chat updates parse failed', e)
          return
        }
        setUpdates((prev) => mergeUpdates(prev, incoming))
        const fresh = incoming.filter((u) => u.seq > highWaterSeq)
        const isCatchUp = catchUp
        catchUp = false
        if (fresh.length) {
          const live = isCatchUp ? [fresh.reduce((a, b) => (b.seq > a.seq ? b : a))] : fresh
          setLiveIds((prev) => {
            const next = new Set(prev)
            for (const u of live) next.add(u.id)
            return next
          })
          highWaterSeq = Math.max(highWaterSeq, ...fresh.map((u) => u.seq))
        }
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
      try {
        const page = await fetchChatPage(runId, { limit: 50, signal: controller.signal })
        if (cancelled) return
        epochRef.current = page.epoch
        setUpdates(page.updates)
        setLiveIds(new Set())
        setMore(page.more)
        setStatus('ready')
        openStream(page.epoch, lastSeqOf(page.updates))
      } catch (e) {
        if (cancelled) return
        if (e instanceof ChatUnavailable) {
          closeStream()
          setStatus('unavailable')
          return
        }
        setStatus('error')
      }
    }

    connRef.current = { openStream }
    void loadNewest()

    return () => {
      cancelled = true
      controller.abort()
      liveness.stop()
      newGeneration()
      clearTimeout(retryTimer)
      closeStream()
      connRef.current = null
    }
  }, [runId, enabled, retryToken])

  const loadEarlier = useCallback(async () => {
    if (!enabled || earlierRef.current || !more) return
    const oldest = updatesRef.current[0]?.seq
    if (oldest === undefined) return

    const generation = generationRef.current
    const controller = new AbortController()
    earlierRef.current = controller
    try {
      const page = await fetchChatPage(runId, { before: oldest, limit: 50, signal: controller.signal })
      if (generation !== generationRef.current) return
      if (page.epoch !== epochRef.current) {
        epochRef.current = page.epoch
        setUpdates(page.updates)
        setLiveIds(new Set())
        setMore(page.more)
        connRef.current?.openStream(page.epoch, lastSeqOf(page.updates))
      } else {
        setUpdates((prev) => mergeUpdates(prev, page.updates))
        setMore(page.more)
      }
    } catch (e) {
      if (generation !== generationRef.current) return
      if (e instanceof ChatUnavailable) setStatus('unavailable')
    } finally {
      if (earlierRef.current === controller) earlierRef.current = null
    }
  }, [runId, enabled, more])

  return { status, updates, liveIds, more, loadEarlier, retry }
}
