import { useEffect, useRef, useState } from 'react'
import type { SessionsData } from '../api/types'

export function useSessionsStream() {
  const [sessions, setSessions] = useState<SessionsData | null>(null)
  const [connected, setConnected] = useState(false)
  const eventSourceRef = useRef<EventSource | null>(null)

  useEffect(() => {
    const es = new EventSource('/api/sessions?stream=1')
    eventSourceRef.current = es

    es.onopen = () => setConnected(true)

    es.onmessage = (event) => {
      try {
        const data: SessionsData = JSON.parse(event.data)
        setSessions(data)
      } catch (e) {
        console.error('Failed to parse sessions SSE:', e)
      }
    }

    es.onerror = () => {
      setConnected(false)
      // EventSource auto-reconnects
    }

    return () => {
      es.close()
      eventSourceRef.current = null
    }
  }, [])

  return { sessions, connected }
}
