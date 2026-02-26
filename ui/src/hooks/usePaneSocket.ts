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

  // Keep callbacks ref up-to-date without triggering reconnect
  callbacksRef.current = callbacks

  const sendInput = useCallback((data: string) => {
    if (wsRef.current?.readyState === WebSocket.OPEN) {
      wsRef.current.send(JSON.stringify({
        type: 'input',
        data: JSON.stringify({ data }),
      }))
    }
  }, [])

  const sendResize = useCallback((cols: number, rows: number) => {
    if (wsRef.current?.readyState === WebSocket.OPEN) {
      wsRef.current.send(JSON.stringify({
        type: 'resize',
        data: JSON.stringify({ cols, rows }),
      }))
    }
  }, [])

  useEffect(() => {
    if (!target) return

    const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
    const wsUrl = `${protocol}//${window.location.host}/api/pane/${target}/ws`

    const ws = new WebSocket(wsUrl)
    wsRef.current = ws

    ws.onopen = () => setConnected(true)
    ws.onclose = () => setConnected(false)

    ws.onmessage = (event) => {
      try {
        const msg = JSON.parse(event.data as string)
        switch (msg.type) {
          case 'output': {
            const output: WSOutput = JSON.parse(msg.data as string)
            callbacksRef.current.onOutput(output.data)
            break
          }
          case 'meta': {
            const meta: WSMeta = JSON.parse(msg.data as string)
            callbacksRef.current.onMeta(meta)
            break
          }
        }
      } catch (e) {
        console.error('Failed to parse WS message:', e)
      }
    }

    return () => {
      ws.close()
      wsRef.current = null
    }
  }, [target]) // Reconnect when target changes

  return { connected, sendInput, sendResize }
}
