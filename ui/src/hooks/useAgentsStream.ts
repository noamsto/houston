import { useEffect, useRef, useState, useCallback } from 'react'
import type { SessionView } from '../api/types'

type State = {
  sessions: Map<string, SessionView>
  connected: boolean
  error: string | null
}

const initial: State = { sessions: new Map(), connected: false, error: null }

/**
 * Subscribes to /api/agents/stream and keeps a live map of agent SessionViews.
 *
 * Emits an initial `snapshot` event on connect (list of SessionView), then one
 * `update` event per session state change thereafter. Removed sessions drop
 * out of the map when their state file is deleted (hub doesn't broadcast that
 * today — rely on `updated_at` staleness in the caller if needed).
 */
export function useAgentsStream() {
  const [state, setState] = useState<State>(initial)
  const esRef = useRef<EventSource | null>(null)

  useEffect(() => {
    const es = new EventSource('/api/agents/stream')
    esRef.current = es

    es.addEventListener('open', () => {
      setState((s) => ({ ...s, connected: true, error: null }))
    })

    es.addEventListener('snapshot', (ev: MessageEvent<string>) => {
      try {
        const arr = JSON.parse(ev.data) as SessionView[]
        const next = new Map<string, SessionView>()
        for (const v of arr) next.set(v.session_id, v)
        setState((s) => ({ ...s, sessions: next, connected: true }))
      } catch (e) {
        console.error('snapshot parse failed', e)
      }
    })

    es.addEventListener('update', (ev: MessageEvent<string>) => {
      try {
        const v = JSON.parse(ev.data) as SessionView
        setState((s) => {
          const next = new Map(s.sessions)
          next.set(v.session_id, v)
          return { ...s, sessions: next }
        })
      } catch (e) {
        console.error('update parse failed', e)
      }
    })

    es.onerror = () => {
      setState((s) => ({ ...s, connected: false, error: 'disconnected' }))
      // EventSource auto-reconnects with backoff.
    }

    return () => {
      es.close()
      esRef.current = null
    }
  }, [])

  const list = useCallback((): SessionView[] => {
    return Array.from(state.sessions.values()).sort((a, b) => b.updated_at - a.updated_at)
  }, [state.sessions])

  return { list: list(), connected: state.connected, error: state.error }
}
