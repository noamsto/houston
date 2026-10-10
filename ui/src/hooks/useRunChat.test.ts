import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, cleanup, renderHook, waitFor } from '@testing-library/react'
import { useRunChat } from './useRunChat'
import { installFakeEventSource } from '../testing/fakeEventSource'
import type { FakeEventSourceHandle } from '../testing/fakeEventSource'
import type { ChatPage, ChatUpdate } from '../api/chat'

function upd(seq: number): ChatUpdate {
  return { id: `u${seq}`, seq, ts: seq, sessionUpdate: 'agent_message_chunk' }
}

function page(epoch: string, updates: ChatUpdate[], more = false): ChatPage {
  return { epoch, updates, more }
}

function jsonResponse(body: unknown): Response {
  return { status: 200, ok: true, json: async () => body, text: async () => JSON.stringify(body) } as Response
}

function notFoundResponse(): Response {
  return { status: 404, ok: false, json: async () => ({}), text: async () => 'not found' } as Response
}

let fake: FakeEventSourceHandle
let fetchMock: ReturnType<typeof vi.fn>

beforeEach(() => {
  fake = installFakeEventSource()
  fetchMock = vi.fn()
  vi.stubGlobal('fetch', fetchMock)
})

afterEach(() => {
  cleanup()
  fake.uninstall()
  vi.unstubAllGlobals()
})

describe('useRunChat', () => {
  it('loads the newest page then opens the stream at after=<epoch>.<last seq>', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(page('e1', [upd(1), upd(2)])))
    const { result } = renderHook(() => useRunChat('r1', true))

    await waitFor(() => expect(result.current.status).toBe('ready'))
    expect(result.current.updates.map((u) => u.seq)).toEqual([1, 2])
    expect(fake.instances.length).toBe(1)
    expect(fake.instances[0].url).toBe('/api/runs/r1/chat/stream?after=e1.2')
  })

  it('appends stream updates and ignores duplicates', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(page('e1', [upd(1)])))
    const { result } = renderHook(() => useRunChat('r1', true))
    await waitFor(() => expect(result.current.status).toBe('ready'))
    const es = fake.instances[0]

    act(() => { es.emit('updates', [upd(2)]) })
    expect(result.current.updates.map((u) => u.seq)).toEqual([1, 2])

    act(() => { es.emit('updates', [upd(2)]) }) // duplicate, already merged above
    expect(result.current.updates.map((u) => u.seq)).toEqual([1, 2])
  })

  it('marks live only the ids that arrive over the stream after it opened', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(page('e1', [upd(1), upd(2)])))
    const { result } = renderHook(() => useRunChat('r1', true))
    await waitFor(() => expect(result.current.status).toBe('ready'))
    const es = fake.instances[0]

    // A replayed duplicate of a page item must not be marked live.
    act(() => { es.emit('updates', [upd(2)]) })
    expect(result.current.liveIds.size).toBe(0)

    act(() => { es.emit('updates', [upd(3)]) })
    expect(Array.from(result.current.liveIds)).toEqual(['u3'])
  })

  it('marks only the newest update of a catch-up batch live, after a resume too', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(page('e1', [upd(1)])))
    const { result } = renderHook(() => useRunChat('r1', true))
    await waitFor(() => expect(result.current.status).toBe('ready'))

    act(() => { fake.instances[0].emit('updates', [upd(2), upd(3), upd(4)]) })
    expect(result.current.updates.map((u) => u.seq)).toEqual([1, 2, 3, 4])
    expect(Array.from(result.current.liveIds)).toEqual(['u4'])

    // A later multi-update event is a live tick: all of it animates.
    act(() => { fake.instances[0].emit('updates', [upd(5), upd(6)]) })
    expect(Array.from(result.current.liveIds)).toEqual(['u4', 'u5', 'u6'])
  })

  it('treats the first event of a reopened stream as a catch-up', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(page('e1', [upd(1)])))
    const { result } = renderHook(() => useRunChat('r1', true))
    await waitFor(() => expect(result.current.status).toBe('ready'))
    const es = fake.instances[0]
    act(() => { es.emit('updates', [upd(2)]) })

    act(() => { es.open() })
    act(() => { es.emit('updates', [upd(3), upd(4), upd(5)]) })
    expect(Array.from(result.current.liveIds)).toEqual(['u2', 'u5'])
  })

  it('an empty catch-up event leaves the next multi-update tick fully live', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(page('e1', [upd(1)])))
    const { result } = renderHook(() => useRunChat('r1', true))
    await waitFor(() => expect(result.current.status).toBe('ready'))
    const es = fake.instances[0]

    act(() => { es.emit('updates', []) })
    act(() => { es.emit('updates', [upd(2), upd(3)]) })
    expect(Array.from(result.current.liveIds)).toEqual(['u2', 'u3'])
  })

  it('reset: closes the source, refetches the newest page, and reopens with the new epoch', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(page('e1', [upd(1), upd(2)])))
    const { result } = renderHook(() => useRunChat('r1', true))
    await waitFor(() => expect(result.current.status).toBe('ready'))
    const first = fake.instances[0]

    fetchMock.mockResolvedValueOnce(jsonResponse(page('e2', [upd(1)])))
    act(() => { first.emit('reset', null) })

    expect(first.closed).toBe(true)
    await waitFor(() => expect(fake.instances.length).toBe(2))
    expect(fake.instances[1].url).toBe('/api/runs/r1/chat/stream?after=e2.1')
    expect(result.current.updates.map((u) => u.seq)).toEqual([1])
  })

  it('loadEarlier prepends older updates and updates `more`', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(page('e1', [upd(5), upd(6)], true)))
    const { result } = renderHook(() => useRunChat('r1', true))
    await waitFor(() => expect(result.current.status).toBe('ready'))

    fetchMock.mockResolvedValueOnce(jsonResponse(page('e1', [upd(3), upd(4)], false)))
    await act(async () => { await result.current.loadEarlier() })

    expect(result.current.updates.map((u) => u.seq)).toEqual([3, 4, 5, 6])
    expect(result.current.more).toBe(false)
  })

  it('a loadEarlier page with a different epoch takes the reset path', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(page('e1', [upd(5), upd(6)], true)))
    const { result } = renderHook(() => useRunChat('r1', true))
    await waitFor(() => expect(result.current.status).toBe('ready'))

    fetchMock.mockResolvedValueOnce(jsonResponse(page('e2', [upd(1)], false)))
    await act(async () => { await result.current.loadEarlier() })

    expect(result.current.updates.map((u) => u.seq)).toEqual([1])
    await waitFor(() => expect(fake.instances.length).toBe(2))
    expect(fake.instances[1].url).toBe('/api/runs/r1/chat/stream?after=e2.1')
  })

  it('drops a loadEarlier page that resolves after an SSE reset, and aborts its fetch', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(page('e1', [upd(5), upd(6)], true)))
    const { result } = renderHook(() => useRunChat('r1', true))
    await waitFor(() => expect(result.current.status).toBe('ready'))
    const first = fake.instances[0]

    let resolveEarlier!: (r: Response) => void
    fetchMock.mockReturnValueOnce(new Promise<Response>((resolve) => { resolveEarlier = resolve }))
    let earlier!: Promise<void>
    act(() => { earlier = result.current.loadEarlier() })
    const earlierInit = fetchMock.mock.calls[1][1] as RequestInit

    fetchMock.mockResolvedValueOnce(jsonResponse(page('e2', [upd(1)], false)))
    act(() => { first.emit('reset', null) })
    await waitFor(() => expect(fake.instances.length).toBe(2))
    expect(fake.instances[1].url).toBe('/api/runs/r1/chat/stream?after=e2.1')

    await act(async () => {
      resolveEarlier(jsonResponse(page('e1', [upd(3), upd(4)], false)))
      await earlier
    })

    expect(earlierInit.signal?.aborted).toBe(true)
    expect(result.current.updates.map((u) => u.seq)).toEqual([1])
    expect(result.current.more).toBe(false)
    expect(fake.instances.length).toBe(2)
    expect(fake.instances[1].closed).toBe(false)
  })

  it('drops a loadEarlier page that resolves after retry()', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(page('e1', [upd(5), upd(6)], true)))
    const { result } = renderHook(() => useRunChat('r1', true))
    await waitFor(() => expect(result.current.status).toBe('ready'))

    let resolveEarlier!: (r: Response) => void
    fetchMock.mockReturnValueOnce(new Promise<Response>((resolve) => { resolveEarlier = resolve }))
    let earlier!: Promise<void>
    act(() => { earlier = result.current.loadEarlier() })

    fetchMock.mockResolvedValueOnce(jsonResponse(page('e2', [upd(9)], true)))
    act(() => { result.current.retry() })
    await waitFor(() => expect(fake.instances.length).toBe(2))

    await act(async () => {
      resolveEarlier(jsonResponse(page('e1', [upd(3), upd(4)], false)))
      await earlier
    })

    expect(result.current.updates.map((u) => u.seq)).toEqual([9])
    expect(result.current.more).toBe(true)
    expect(fake.instances.length).toBe(2)
  })

  it('a 404 on the initial load sets status unavailable and opens no stream', async () => {
    fetchMock.mockResolvedValueOnce(notFoundResponse())
    const { result } = renderHook(() => useRunChat('r1', true))

    await waitFor(() => expect(result.current.status).toBe('unavailable'))
    expect(fake.instances.length).toBe(0)
  })

  it('closes the stream on unmount', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(page('e1', [upd(1)])))
    const { result, unmount } = renderHook(() => useRunChat('r1', true))
    await waitFor(() => expect(result.current.status).toBe('ready'))
    const es = fake.instances[0]

    unmount()
    expect(es.closed).toBe(true)
  })

  it('never fetches while disabled', async () => {
    renderHook(() => useRunChat('r1', false))
    await Promise.resolve()

    expect(fetchMock).not.toHaveBeenCalled()
    expect(fake.instances.length).toBe(0)
  })

  describe('liveness', () => {
    function setVisibility(state: DocumentVisibilityState) {
      Object.defineProperty(document, 'visibilityState', { value: state, configurable: true })
      document.dispatchEvent(new Event('visibilitychange'))
    }

    beforeEach(() => {
      vi.useFakeTimers()
    })

    afterEach(() => {
      setVisibility('visible')
      vi.useRealTimers()
    })

    async function advance(ms: number) {
      await act(async () => { await vi.advanceTimersByTimeAsync(ms) })
    }

    it('hidden 15 s then visible: resumes the stream from the cursor without refetching or dropping history', async () => {
      fetchMock.mockResolvedValue(jsonResponse(page('e1', [upd(1)])))
      const { result } = renderHook(() => useRunChat('r1', true))
      await advance(0)
      act(() => { fake.instances[0].emit('updates', [upd(2)]) })
      expect(fake.instances.length).toBe(1)
      expect(fetchMock).toHaveBeenCalledTimes(1)

      setVisibility('hidden')
      await advance(15_000)
      setVisibility('visible')
      await advance(0)

      expect(fake.instances[0].closed).toBe(true)
      expect(fake.instances.length).toBe(2)
      expect(fake.instances[1].closed).toBe(false)
      expect(fake.instances[1].url).toBe('/api/runs/r1/chat/stream?after=e1.2')
      expect(fetchMock).toHaveBeenCalledTimes(1)
      expect(result.current.status).toBe('ready')
      expect(result.current.updates.map((u) => u.seq)).toEqual([1, 2])
    })

    it('keeps the status ready when the network is still down on return', async () => {
      fetchMock.mockResolvedValue(jsonResponse(page('e1', [upd(1)])))
      const { result } = renderHook(() => useRunChat('r1', true))
      await advance(0)
      fetchMock.mockRejectedValue(new TypeError('network down'))

      setVisibility('hidden')
      await advance(15_000)
      setVisibility('visible')
      await advance(10_000)

      expect(fake.instances.length).toBe(2)
      expect(fake.instances[1].closed).toBe(false)
      expect(result.current.status).toBe('ready')
    })

    it('a quiet open stream reconnects once per stale episode, not every check', async () => {
      fetchMock.mockResolvedValue(jsonResponse(page('e1', [upd(1)])))
      renderHook(() => useRunChat('r1', true))
      await advance(0)
      act(() => { fake.instances[0].open() })

      await advance(125_000)
      expect(fake.instances.length).toBe(2)
      await advance(10_000)
      expect(fake.instances.length).toBe(3)
      expect(fetchMock).toHaveBeenCalledTimes(1)
    })

    it('pings keep a quiet stream alive', async () => {
      fetchMock.mockResolvedValue(jsonResponse(page('e1', [upd(1)])))
      renderHook(() => useRunChat('r1', true))
      await advance(0)

      for (let i = 0; i < 6; i++) {
        await advance(25_000)
        act(() => { fake.instances[0].emit('ping', null) })
      }
      expect(fake.instances.length).toBe(1)
      expect(fetchMock).toHaveBeenCalledTimes(1)
    })

    it('does not reconnect while the first page fetch is pending', async () => {
      fetchMock.mockReturnValue(new Promise<Response>(() => {}))
      renderHook(() => useRunChat('r1', true))

      await advance(130_000)
      setVisibility('hidden')
      await advance(15_000)
      setVisibility('visible')
      await advance(0)

      expect(fetchMock).toHaveBeenCalledTimes(1)
      expect(fake.instances.length).toBe(0)
    })
  })
})
