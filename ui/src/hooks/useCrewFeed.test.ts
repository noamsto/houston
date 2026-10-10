import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, cleanup, renderHook, waitFor } from '@testing-library/react'
import { useCrewFeed } from './useCrewFeed'
import { installFakeEventSource } from '../testing/fakeEventSource'
import type { FakeEventSourceHandle } from '../testing/fakeEventSource'
import type { FeedEntry, FeedPage } from '../api/crewFeed'

function ent(epoch: string, offset: number): FeedEntry {
  return { id: `${epoch}.${offset}`, ts: offset, kind: 'status', text: `entry ${offset}` }
}

function page(epoch: string, entries: FeedEntry[], more = false): FeedPage {
  return { epoch, entries, more }
}

function jsonResponse(body: unknown): Response {
  return { status: 200, ok: true, json: async () => body, text: async () => JSON.stringify(body) } as Response
}

function statusResponse(status: number): Response {
  return { status, ok: false, json: async () => ({}), text: async () => 'x' } as Response
}

const ids = (entries: FeedEntry[]) => entries.map((e) => e.id)

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

describe('useCrewFeed', () => {
  it('loads the newest page then opens the stream after the last entry id', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(page('e1', [ent('e1', 10), ent('e1', 20)])))
    const { result } = renderHook(() => useCrewFeed('r1', true))

    await waitFor(() => expect(result.current.status).toBe('ready'))
    expect(ids(result.current.entries)).toEqual(['e1.10', 'e1.20'])
    expect(fetchMock.mock.calls[0][0]).toBe('/api/runs/r1/crew/feed?limit=50')
    expect(fake.instances.length).toBe(1)
    expect(fake.instances[0].url).toBe('/api/runs/r1/crew/feed/stream?after=e1.20')
  })

  it('streams from the start of the epoch for an empty first page', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(page('e1', [])))
    const { result } = renderHook(() => useCrewFeed('r1', true))

    await waitFor(() => expect(result.current.status).toBe('ready'))
    expect(result.current.entries).toEqual([])
    expect(fake.instances[0].url).toBe('/api/runs/r1/crew/feed/stream?from=e1')
  })

  it('appends stream entries and ignores duplicates', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(page('e1', [ent('e1', 10)])))
    const { result } = renderHook(() => useCrewFeed('r1', true))
    await waitFor(() => expect(result.current.status).toBe('ready'))
    const es = fake.instances[0]

    act(() => { es.emit('entries', [ent('e1', 20)]) })
    expect(ids(result.current.entries)).toEqual(['e1.10', 'e1.20'])

    act(() => { es.emit('entries', [ent('e1', 10), ent('e1', 20), ent('e1', 30)]) })
    expect(ids(result.current.entries)).toEqual(['e1.10', 'e1.20', 'e1.30'])
  })

  it('reset: closes the source, refetches the newest page, and reopens with the new epoch', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(page('e1', [ent('e1', 10)])))
    const { result } = renderHook(() => useCrewFeed('r1', true))
    await waitFor(() => expect(result.current.status).toBe('ready'))
    const first = fake.instances[0]

    fetchMock.mockResolvedValueOnce(jsonResponse(page('e2', [ent('e2', 5)])))
    act(() => { first.emit('reset', null) })

    expect(first.closed).toBe(true)
    await waitFor(() => expect(fake.instances.length).toBe(2))
    expect(fake.instances[1].url).toBe('/api/runs/r1/crew/feed/stream?after=e2.5')
    expect(ids(result.current.entries)).toEqual(['e2.5'])
  })

  it('reset then 404 leaves no `more`, so no inert Load older', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(page('e1', [ent('e1', 10)], true)))
    const { result } = renderHook(() => useCrewFeed('r1', true))
    await waitFor(() => expect(result.current.more).toBe(true))

    fetchMock.mockResolvedValueOnce(statusResponse(404))
    act(() => { fake.instances[0].emit('reset', null) })

    await waitFor(() => expect(result.current.status).toBe('unavailable'))
    expect(result.current.more).toBe(false)
  })

  it('a 404 on loadOlder clears `more` so Load older is not left inert', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(page('e1', [ent('e1', 10)], true)))
    const { result } = renderHook(() => useCrewFeed('r1', true))
    await waitFor(() => expect(result.current.more).toBe(true))

    fetchMock.mockResolvedValueOnce(statusResponse(404))
    await act(async () => { await result.current.loadOlder() })

    expect(result.current.status).toBe('unavailable')
    expect(result.current.more).toBe(false)
  })

  it('loadOlder prepends older entries before the oldest id and updates `more`', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(page('e1', [ent('e1', 30), ent('e1', 40)], true)))
    const { result } = renderHook(() => useCrewFeed('r1', true))
    await waitFor(() => expect(result.current.status).toBe('ready'))

    fetchMock.mockResolvedValueOnce(jsonResponse(page('e1', [ent('e1', 10), ent('e1', 20), ent('e1', 30)], false)))
    await act(async () => { await result.current.loadOlder() })

    expect(fetchMock.mock.calls[1][0]).toBe('/api/runs/r1/crew/feed?before=e1.30&limit=50')
    expect(ids(result.current.entries)).toEqual(['e1.10', 'e1.20', 'e1.30', 'e1.40'])
    expect(result.current.more).toBe(false)
  })

  it('loadOlder does nothing when there is no more', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(page('e1', [ent('e1', 10)], false)))
    const { result } = renderHook(() => useCrewFeed('r1', true))
    await waitFor(() => expect(result.current.status).toBe('ready'))

    await act(async () => { await result.current.loadOlder() })
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })

  it('a 409 on loadOlder reloads the newest page and reopens the stream', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(page('e1', [ent('e1', 30)], true)))
    const { result } = renderHook(() => useCrewFeed('r1', true))
    await waitFor(() => expect(result.current.status).toBe('ready'))

    fetchMock.mockResolvedValueOnce(statusResponse(409))
    fetchMock.mockResolvedValueOnce(jsonResponse(page('e2', [ent('e2', 7)], false)))
    await act(async () => { await result.current.loadOlder() })

    await waitFor(() => expect(ids(result.current.entries)).toEqual(['e2.7']))
    expect(fake.instances[0].closed).toBe(true)
    await waitFor(() => expect(fake.instances.length).toBe(2))
    expect(fake.instances[1].url).toBe('/api/runs/r1/crew/feed/stream?after=e2.7')
  })

  it('drops a loadOlder page that resolves after an SSE reset, and aborts its fetch', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(page('e1', [ent('e1', 30)], true)))
    const { result } = renderHook(() => useCrewFeed('r1', true))
    await waitFor(() => expect(result.current.status).toBe('ready'))
    const first = fake.instances[0]

    let resolveOlder!: (r: Response) => void
    fetchMock.mockReturnValueOnce(new Promise<Response>((resolve) => { resolveOlder = resolve }))
    let older!: Promise<void>
    act(() => { older = result.current.loadOlder() })
    const olderInit = fetchMock.mock.calls[1][1] as RequestInit

    fetchMock.mockResolvedValueOnce(jsonResponse(page('e2', [ent('e2', 1)], false)))
    act(() => { first.emit('reset', null) })
    await waitFor(() => expect(fake.instances.length).toBe(2))

    await act(async () => {
      resolveOlder(jsonResponse(page('e1', [ent('e1', 10)], false)))
      await older
    })

    expect(olderInit.signal?.aborted).toBe(true)
    expect(ids(result.current.entries)).toEqual(['e2.1'])
  })

  it('ignores a loadOlder started while the newest page is reloading after a reset', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(page('e1', [ent('e1', 30)], true)))
    const { result } = renderHook(() => useCrewFeed('r1', true))
    await waitFor(() => expect(result.current.status).toBe('ready'))
    const first = fake.instances[0]

    let resolveNewest!: (r: Response) => void
    fetchMock.mockReturnValueOnce(new Promise<Response>((resolve) => { resolveNewest = resolve }))
    act(() => { first.emit('reset', null) })

    let resolveOlder: ((r: Response) => void) | undefined
    fetchMock.mockReturnValueOnce(new Promise<Response>((resolve) => { resolveOlder = resolve }))
    let older!: Promise<void>
    act(() => { older = result.current.loadOlder() })

    await act(async () => { resolveNewest(jsonResponse(page('e2', [ent('e2', 1)], false))) })
    await waitFor(() => expect(ids(result.current.entries)).toEqual(['e2.1']))
    await act(async () => {
      resolveOlder?.(jsonResponse(page('e1', [ent('e1', 10)], false)))
      await older
    })

    expect(ids(result.current.entries)).toEqual(['e2.1'])
    expect(result.current.more).toBe(false)
  })

  it('a 404 on the initial load sets status unavailable and opens no stream', async () => {
    fetchMock.mockResolvedValueOnce(statusResponse(404))
    const { result } = renderHook(() => useCrewFeed('r1', true))

    await waitFor(() => expect(result.current.status).toBe('unavailable'))
    expect(fake.instances.length).toBe(0)
  })

  it('another failure sets status error and retry reloads', async () => {
    fetchMock.mockResolvedValueOnce(statusResponse(500))
    const { result } = renderHook(() => useCrewFeed('r1', true))
    await waitFor(() => expect(result.current.status).toBe('error'))
    expect(fake.instances.length).toBe(0)

    fetchMock.mockResolvedValueOnce(jsonResponse(page('e1', [ent('e1', 10)])))
    act(() => { result.current.retry() })

    await waitFor(() => expect(result.current.status).toBe('ready'))
    expect(fake.instances.length).toBe(1)
  })

  it('closes the stream on unmount', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(page('e1', [ent('e1', 10)])))
    const { result, unmount } = renderHook(() => useCrewFeed('r1', true))
    await waitFor(() => expect(result.current.status).toBe('ready'))
    const es = fake.instances[0]

    unmount()
    expect(es.closed).toBe(true)
  })

  it('closes the stream when disabled after being enabled', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(page('e1', [ent('e1', 10)])))
    const { result, rerender } = renderHook(({ on }) => useCrewFeed('r1', on), { initialProps: { on: true } })
    await waitFor(() => expect(result.current.status).toBe('ready'))
    const es = fake.instances[0]

    rerender({ on: false })
    expect(es.closed).toBe(true)
  })

  it('never fetches while disabled', async () => {
    renderHook(() => useCrewFeed('r1', false))
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

    it('hidden 15 s then visible: resumes from the cursor without refetching or dropping history', async () => {
      fetchMock.mockResolvedValue(jsonResponse(page('e1', [ent('e1', 10)])))
      const { result } = renderHook(() => useCrewFeed('r1', true))
      await advance(0)
      act(() => { fake.instances[0].emit('entries', [ent('e1', 20)]) })

      setVisibility('hidden')
      await advance(15_000)
      setVisibility('visible')
      await advance(0)

      expect(fake.instances[0].closed).toBe(true)
      expect(fake.instances.length).toBe(2)
      expect(fake.instances[1].url).toBe('/api/runs/r1/crew/feed/stream?after=e1.20')
      expect(fetchMock).toHaveBeenCalledTimes(1)
      expect(result.current.status).toBe('ready')
      expect(ids(result.current.entries)).toEqual(['e1.10', 'e1.20'])
    })

    it('reopens an empty feed from the start of its epoch', async () => {
      fetchMock.mockResolvedValue(jsonResponse(page('e1', [])))
      renderHook(() => useCrewFeed('r1', true))
      await advance(0)

      setVisibility('hidden')
      await advance(15_000)
      setVisibility('visible')
      await advance(0)

      expect(fake.instances.length).toBe(2)
      expect(fake.instances[1].url).toBe('/api/runs/r1/crew/feed/stream?from=e1')
    })

    it('pings keep a quiet stream alive', async () => {
      fetchMock.mockResolvedValue(jsonResponse(page('e1', [ent('e1', 10)])))
      renderHook(() => useCrewFeed('r1', true))
      await advance(0)

      for (let i = 0; i < 6; i++) {
        await advance(25_000)
        act(() => { fake.instances[0].emit('ping', null) })
      }
      expect(fake.instances.length).toBe(1)
      expect(fetchMock).toHaveBeenCalledTimes(1)
    })

    it('a quiet stream with no ping reopens after the stale window', async () => {
      fetchMock.mockResolvedValue(jsonResponse(page('e1', [ent('e1', 10)])))
      renderHook(() => useCrewFeed('r1', true))
      await advance(0)

      await advance(70_000)
      expect(fake.instances.length).toBe(2)
      expect(fetchMock).toHaveBeenCalledTimes(1)
    })
  })
})
