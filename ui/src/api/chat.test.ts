import { afterEach, describe, expect, it, vi } from 'vitest'
import { ChatUnavailable, chatStreamURL, fetchChatPage, fetchTool, formatCursor, parseCursor } from './chat'

function jsonResponse(status: number, body: string): Response {
  return { ok: status >= 200 && status < 300, status, text: () => Promise.resolve(body), json: () => Promise.resolve(JSON.parse(body)) } as unknown as Response
}

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('formatCursor / parseCursor', () => {
  it('round-trips an epoch and seq', () => {
    expect(formatCursor('a1b2c3d4e5f6', 42)).toBe('a1b2c3d4e5f6.42')
    expect(parseCursor('a1b2c3d4e5f6.42')).toEqual({ epoch: 'a1b2c3d4e5f6', seq: 42 })
  })

  it('accepts seq 0', () => {
    expect(parseCursor('deadbeef.0')).toEqual({ epoch: 'deadbeef', seq: 0 })
  })

  it('rejects a string with no dot', () => {
    expect(parseCursor('deadbeef')).toBeNull()
  })

  it('rejects an empty string', () => {
    expect(parseCursor('')).toBeNull()
  })

  it('rejects a trailing dot with no seq', () => {
    expect(parseCursor('deadbeef.')).toBeNull()
  })

  it('rejects a leading dot with no epoch', () => {
    expect(parseCursor('.42')).toBeNull()
  })

  it('rejects a non-numeric seq', () => {
    expect(parseCursor('deadbeef.abc')).toBeNull()
  })
})

describe('chatStreamURL', () => {
  it('builds the stream URL from a run id, epoch and seq', () => {
    expect(chatStreamURL('run-1', 'deadbeef', 7)).toBe('/api/runs/run-1/chat/stream?after=deadbeef.7')
  })

  it('encodes a run id containing special characters', () => {
    expect(chatStreamURL('a/b', 'deadbeef', 0)).toBe('/api/runs/a%2Fb/chat/stream?after=deadbeef.0')
  })
})

describe('fetchChatPage', () => {
  it('fetches with no query string when neither before nor limit is given', async () => {
    const fetchMock = vi.fn<(url: string) => Promise<Response>>(() => Promise.resolve(jsonResponse(200, '{"epoch":"e1","updates":[],"more":false}')))
    vi.stubGlobal('fetch', fetchMock)

    const page = await fetchChatPage('run-1')

    expect(fetchMock.mock.calls[0][0]).toBe('/api/runs/run-1/chat')
    expect(page).toEqual({ epoch: 'e1', updates: [], more: false })
  })

  it('adds only the params that were given', async () => {
    const fetchMock = vi.fn<(url: string) => Promise<Response>>(() => Promise.resolve(jsonResponse(200, '{"epoch":"e1","updates":[],"more":false}')))
    vi.stubGlobal('fetch', fetchMock)

    await fetchChatPage('run-1', { before: 10 })
    expect(fetchMock.mock.calls[0][0]).toBe('/api/runs/run-1/chat?before=10')

    await fetchChatPage('run-1', { limit: 25 })
    expect(fetchMock.mock.calls[1][0]).toBe('/api/runs/run-1/chat?limit=25')

    await fetchChatPage('run-1', { before: 10, limit: 25 })
    expect(fetchMock.mock.calls[2][0]).toBe('/api/runs/run-1/chat?before=10&limit=25')
  })

  it('encodes the run id', async () => {
    const fetchMock = vi.fn<(url: string) => Promise<Response>>(() => Promise.resolve(jsonResponse(200, '{"epoch":"e1","updates":[],"more":false}')))
    vi.stubGlobal('fetch', fetchMock)

    await fetchChatPage('a/b')
    expect(fetchMock.mock.calls[0][0]).toBe('/api/runs/a%2Fb/chat')
  })

  it('throws ChatUnavailable on 404', async () => {
    vi.stubGlobal('fetch', vi.fn(() => Promise.resolve(jsonResponse(404, 'no chat'))))

    await expect(fetchChatPage('run-1')).rejects.toBeInstanceOf(ChatUnavailable)
  })

  it('throws a plain Error with the status on other non-ok responses', async () => {
    vi.stubGlobal('fetch', vi.fn(() => Promise.resolve(jsonResponse(503, 'run registry not started'))))

    await expect(fetchChatPage('run-1')).rejects.toThrow('503')
  })
})

describe('fetchTool', () => {
  it('fetches the tool detail route with an encoded call id', async () => {
    const body = '{"toolCallId":"call-1","name":"Read","output":"hi"}'
    const fetchMock = vi.fn<(url: string) => Promise<Response>>(() => Promise.resolve(jsonResponse(200, body)))
    vi.stubGlobal('fetch', fetchMock)

    const detail = await fetchTool('run-1', 'call/1')

    expect(fetchMock.mock.calls[0][0]).toBe('/api/runs/run-1/chat/tool/call%2F1')
    expect(detail).toEqual({ toolCallId: 'call-1', name: 'Read', output: 'hi' })
  })

  it('throws on a non-ok response', async () => {
    vi.stubGlobal('fetch', vi.fn(() => Promise.resolve(jsonResponse(404, 'unknown call'))))

    await expect(fetchTool('run-1', 'call-1')).rejects.toThrow('404')
  })
})
