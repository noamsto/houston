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
      // data field must be an embedded JSON object (json.RawMessage on the Go side)
      wsRef.current.send(JSON.stringify({ type: 'input', data: { data } }))
    }
  }, [])

  const sendResize = useCallback((cols: number, rows: number) => {
    if (wsRef.current?.readyState === WebSocket.OPEN) {
      wsRef.current.send(JSON.stringify({ type: 'resize', data: { cols, rows } }))
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
        // msg.data is already a parsed object (json.RawMessage embeds as JSON, not a string)
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

    return () => {
      ws.close()
      wsRef.current = null
    }
  }, [target]) // Reconnect when target changes

  return { connected, sendInput, sendResize }
}
