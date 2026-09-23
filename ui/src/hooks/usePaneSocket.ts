import { useCallback, useEffect, useRef, useState } from 'react'
import type { WSMeta, WSOutput } from '../api/types'
import { WS_CLOSE_SERVER_CHANGED } from '../api/terminal'

interface WSDims {
  cols: number
  rows: number
}

interface PaneSocketCallbacks {
  onDims: (dims: WSDims) => void    // pane dimensions — resize xterm.js to match
  onSeed: (data: string) => void    // full snapshot — write via writeSnapshot (clears scrollback)
  onReseed: (data: string) => void  // post-resize snapshot — write without clearing scrollback
  onOutput: (data: string) => void  // incremental terminal data — write via term.write
  onMeta: (meta: WSMeta) => void
}

export function usePaneSocket(path: string | null, callbacks: PaneSocketCallbacks) {
  const wsRef = useRef<WebSocket | null>(null)
  const callbacksRef = useRef(callbacks)
  const [connected, setConnected] = useState(false)
  const retriesRef = useRef(0)
  // Keyed by path rather than cleared in an effect, so a path change resets
  // `ended` to null on its own — react-hooks flags setState-in-effect.
  const [endedState, setEndedState] = useState<{ path: string; reason: string } | null>(null)
  const ended = path && endedState?.path === path ? endedState.reason : null
  const endedRef = useRef(false)

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
    if (!path) return
    const socketPath = path // narrow to string for the closures below

    let cancelled = false
    let reconnectTimer: ReturnType<typeof setTimeout>
    endedRef.current = false

    function connect() {
      if (cancelled) return

      const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
      const wsUrl = `${protocol}//${window.location.host}${socketPath}`

      const ws = new WebSocket(wsUrl)
      wsRef.current = ws

      ws.onopen = () => {
        console.debug('[input] WS connected to', socketPath)
        setConnected(true)
        retriesRef.current = 0
      }

      ws.onclose = (e) => {
        console.debug('[input] WS closed — path:', socketPath, 'code:', e.code, 'reason:', e.reason, 'cancelled:', cancelled)
        // Only clear ref if it still points to THIS WebSocket instance.
        // When switching targets, the new effect sets wsRef.current to a new WS
        // before this old onclose fires — clearing it would null the new connection.
        const isCurrent = wsRef.current === ws
        if (isCurrent) {
          setConnected(false)
          wsRef.current = null
        }
        if (isCurrent && !cancelled && e.code === WS_CLOSE_SERVER_CHANGED) {
          setEndedState({ path: socketPath, reason: e.reason || 'tmux server changed' })
          endedRef.current = true
          return
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
            case 'dims': {
              const dims = msg.data as WSDims
              callbacksRef.current.onDims(dims)
              break
            }
            case 'seed': {
              const output = msg.data as WSOutput
              callbacksRef.current.onSeed(output.data)
              break
            }
            case 'reseed': {
              const output = msg.data as WSOutput
              callbacksRef.current.onReseed(output.data)
              break
            }
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
      if (endedRef.current) return
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
  }, [path])

  return { connected, ended, sendInput, sendResize }
}
