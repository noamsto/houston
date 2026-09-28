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
})
