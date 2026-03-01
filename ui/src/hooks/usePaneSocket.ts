import { useCallback, useEffect, useRef, useState } from 'react'
import type { WSMeta, WSOutput } from '../api/types'

interface PaneSocketCallbacks {
  onOutput: (data: string) => void
  onMeta: (meta: WSMeta) => void
}

export function usePaneSocket(target: string | null, callbacks: PaneSocketCallbacks) {
  const wsRef = useRef<WebSocket | null>(null)
  const callbacksRef = useRef(callbacks)
  const [connected, setConnected] = useState(false)
  const retriesRef = useRef(0)

  // Keep callbacks ref up-to-date without triggering reconnect
  useEffect(() => {
    callbacksRef.current = callbacks
  })

  const sendInput = useCallback((data: string) => {
    const ws = wsRef.current
    if (ws?.readyState === WebSocket.OPEN) {
      ws.send(JSON.stringify({ type: 'input', data: { data } }))
      console.debug('[input] sent via WS:', JSON.stringify(data))
    } else {
      console.warn('[input] DROPPED — WS not open, readyState:', ws?.readyState, 'data:', JSON.stringify(data))
    }
  }, [])

  const sendResize = useCallback((cols: number, rows: number) => {
    if (wsRef.current?.readyState === WebSocket.OPEN) {
      wsRef.current.send(JSON.stringify({ type: 'resize', data: { cols, rows } }))
    }
  }, [])

  useEffect(() => {
    if (!target) return

    let cancelled = false
    let reconnectTimer: ReturnType<typeof setTimeout>

    function connect() {
      if (cancelled) return

      const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
      const wsUrl = `${protocol}//${window.location.host}/api/pane/${target}/ws`

      const ws = new WebSocket(wsUrl)
      wsRef.current = ws

      ws.onopen = () => {
        console.debug('[input] WS connected to', target)
        setConnected(true)
        retriesRef.current = 0
      }

      ws.onclose = (e) => {
        console.debug('[input] WS closed — target:', target, 'code:', e.code, 'reason:', e.reason, 'cancelled:', cancelled)
        // Only clear ref if it still points to THIS WebSocket instance.
        // When switching targets, the new effect sets wsRef.current to a new WS
        // before this old onclose fires — clearing it would null the new connection.
        if (wsRef.current === ws) {
          setConnected(false)
          wsRef.current = null
        }
        if (cancelled) return
        // Exponential backoff: 500ms, 1s, 2s, 4s, capped at 5s
        const delay = Math.min(500 * 2 ** retriesRef.current, 5000)
        retriesRef.current++
        reconnectTimer = setTimeout(connect, delay)
      }

      ws.onmessage = (event) => {
        try {
          const msg = JSON.parse(event.data as string)
          switch (msg.type) {
            case 'output': {
              const output = msg.data as WSOutput
              callbacksRef.current.onOutput(output.data)
              break
            }
            case 'meta': {
              const meta = msg.data as WSMeta
              callbacksRef.current.onMeta(meta)
              break
            }
          }
        } catch (e) {
          console.error('Failed to parse WS message:', e)
        }
      }
    }

    connect()

    // Reconnect immediately when tab becomes visible again
    const onVisibility = () => {
      if (document.visibilityState === 'visible' && wsRef.current?.readyState !== WebSocket.OPEN) {
        clearTimeout(reconnectTimer)
        retriesRef.current = 0
        connect()
      }
    }
    document.addEventListener('visibilitychange', onVisibility)

    return () => {
      cancelled = true
      clearTimeout(reconnectTimer)
      document.removeEventListener('visibilitychange', onVisibility)
      wsRef.current?.close()
      wsRef.current = null
    }
  }, [target])

  return { connected, sendInput, sendResize }
}
