import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, cleanup, renderHook } from '@testing-library/react'
import { usePaneSocket } from './usePaneSocket'

const noopCallbacks: Parameters<typeof usePaneSocket>[1] = {
  onDims: () => {},
  onSeed: () => {},
  onReseed: () => {},
  onOutput: () => {},
  onMeta: () => {},
}

class FakeWebSocket {
  static instances: FakeWebSocket[] = []
  static readonly CONNECTING = 0
  static readonly OPEN = 1
  static readonly CLOSING = 2
  static readonly CLOSED = 3
  readyState = FakeWebSocket.CONNECTING
  onopen: (() => void) | null = null
  onclose: ((e: { code: number; reason: string }) => void) | null = null
  onmessage: ((e: { data: string }) => void) | null = null
  onerror: (() => void) | null = null
  url: string

  constructor(url: string) {
    this.url = url
    FakeWebSocket.instances.push(this)
  }

  send() {}

  close() {
    this.readyState = FakeWebSocket.CLOSED
  }
}

function setVisibility(state: DocumentVisibilityState) {
  Object.defineProperty(document, 'visibilityState', { value: state, configurable: true })
}

beforeEach(() => {
  FakeWebSocket.instances.length = 0
  vi.stubGlobal('WebSocket', FakeWebSocket)
  vi.useFakeTimers()
})

afterEach(() => {
  cleanup()
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

describe('usePaneSocket', () => {
  it('4409 sets ended and does not reconnect after the backoff window', () => {
    const { result } = renderHook(() => usePaneSocket('/api/runs/r1/terminal', noopCallbacks))
    const ws = FakeWebSocket.instances[0]

    act(() => { ws.onclose?.({ code: 4409, reason: 'tmux server changed' }) })
    expect(result.current.ended).toBe('tmux server changed')

    act(() => { vi.advanceTimersByTime(10_000) })
    expect(FakeWebSocket.instances).toHaveLength(1)
  })

  it('1006 reconnects after the backoff', () => {
    renderHook(() => usePaneSocket('/api/runs/r1/terminal', noopCallbacks))
    const ws = FakeWebSocket.instances[0]

    act(() => { ws.onclose?.({ code: 1006, reason: '' }) })
    expect(FakeWebSocket.instances).toHaveLength(1)

    act(() => { vi.advanceTimersByTime(500) })
    expect(FakeWebSocket.instances).toHaveLength(2)
  })

  it('does not reconnect on visibilitychange after 4409', () => {
    renderHook(() => usePaneSocket('/api/runs/r1/terminal', noopCallbacks))
    const ws = FakeWebSocket.instances[0]

    act(() => { ws.onclose?.({ code: 4409, reason: 'tmux server changed' }) })

    setVisibility('visible')
    act(() => { document.dispatchEvent(new Event('visibilitychange')) })

    expect(FakeWebSocket.instances).toHaveLength(1)
  })

  it('a path change after 4409 opens a new socket and clears ended', () => {
    const { result, rerender } = renderHook(
      ({ path }) => usePaneSocket(path, noopCallbacks),
      { initialProps: { path: '/api/runs/r1/terminal' } },
    )
    const ws = FakeWebSocket.instances[0]

    act(() => { ws.onclose?.({ code: 4409, reason: 'tmux server changed' }) })
    expect(result.current.ended).toBe('tmux server changed')

    rerender({ path: '/api/runs/r2/terminal' })

    expect(FakeWebSocket.instances).toHaveLength(2)
    expect(result.current.ended).toBeNull()
  })

  describe('zombie sockets', () => {
    afterEach(() => { setVisibility('visible') })

    function hide() {
      setVisibility('hidden')
      document.dispatchEvent(new Event('visibilitychange'))
    }
    function show() {
      setVisibility('visible')
      document.dispatchEvent(new Event('visibilitychange'))
    }

    it('replaces an OPEN socket at once after the page was hidden 15 s, ignoring the old one later', () => {
      renderHook(() => usePaneSocket('/api/runs/r1/terminal', noopCallbacks))
      const zombie = FakeWebSocket.instances[0]
      const staleOnClose = zombie.onclose
      act(() => { zombie.readyState = FakeWebSocket.OPEN; zombie.onopen?.() })

      act(() => { hide() })
      act(() => { vi.advanceTimersByTime(15_000) })
      act(() => { show() })

      expect(FakeWebSocket.instances).toHaveLength(2)
      expect(zombie.onclose).toBeNull()

      act(() => { staleOnClose?.({ code: 1006, reason: '' }) })
      act(() => { vi.advanceTimersByTime(10_000) })
      expect(FakeWebSocket.instances).toHaveLength(2)
    })

    it('detaches and closes a CONNECTING socket on a short hidden spell, leaving exactly one live socket', () => {
      renderHook(() => usePaneSocket('/api/runs/r1/terminal', noopCallbacks))
      const connecting = FakeWebSocket.instances[0]
      expect(connecting.readyState).toBe(FakeWebSocket.CONNECTING)

      act(() => { hide() })
      act(() => { vi.advanceTimersByTime(3_000) })
      act(() => { show() })

      expect(FakeWebSocket.instances).toHaveLength(2)
      expect(connecting.readyState).toBe(FakeWebSocket.CLOSED)
      expect(connecting.onopen).toBeNull()
      expect(connecting.onclose).toBeNull()
      expect(connecting.onmessage).toBeNull()
      expect(connecting.onerror).toBeNull()
    })

    it('keeps an OPEN socket after the page was hidden only 5 s', () => {
      renderHook(() => usePaneSocket('/api/runs/r1/terminal', noopCallbacks))
      const ws = FakeWebSocket.instances[0]
      act(() => { ws.readyState = FakeWebSocket.OPEN; ws.onopen?.() })

      act(() => { hide() })
      act(() => { vi.advanceTimersByTime(5_000) })
      act(() => { show() })

      expect(FakeWebSocket.instances).toHaveLength(1)
    })
  })
})
