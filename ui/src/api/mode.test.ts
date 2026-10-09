import { afterEach, describe, expect, it, vi } from 'vitest'
import { fetchMode } from './mode'

function response(status: number, body: unknown): Response {
  return { ok: status >= 200 && status < 300, status, json: () => Promise.resolve(body) } as Response
}

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('fetchMode', () => {
  it('returns the mode the server names', async () => {
    const fetchMock = vi.fn(() => Promise.resolve(response(200, { mode: 'tmux' })))
    vi.stubGlobal('fetch', fetchMock)

    await expect(fetchMode()).resolves.toBe('tmux')
    expect(fetchMock).toHaveBeenCalledWith('/api/mode')
  })

  it('rejects on a non-ok response', async () => {
    vi.stubGlobal('fetch', vi.fn(() => Promise.resolve(response(404, {}))))

    await expect(fetchMode()).rejects.toThrow()
  })

  it('rejects on an unknown mode', async () => {
    vi.stubGlobal('fetch', vi.fn(() => Promise.resolve(response(200, { mode: 'x' }))))

    await expect(fetchMode()).rejects.toThrow()
  })
})
