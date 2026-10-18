import { afterEach, describe, expect, it, vi } from 'vitest'
import { fetchHosts, hostAPI } from './hosts'

function response(status: number, body: string): Response {
  return {
    ok: status >= 200 && status < 300,
    status,
    text: () => Promise.resolve(body),
    json: () => Promise.resolve(JSON.parse(body)),
  } as Response
}

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('hostAPI', () => {
  it('uses the plain route for the local host', () => {
    expect(hostAPI(undefined, 'repos')).toBe('/api/repos')
    expect(hostAPI('', 'repos')).toBe('/api/repos')
    expect(hostAPI('local', 'repos')).toBe('/api/repos')
  })

  it('addresses a peer through /api/hosts and encodes the name', () => {
    expect(hostAPI('b', 'dispatch/options')).toBe('/api/hosts/b/dispatch/options')
    expect(hostAPI('a b', 'repos')).toBe('/api/hosts/a%20b/repos')
  })
})

describe('fetchHosts', () => {
  it('returns the parsed host list', async () => {
    const fetchMock = vi.fn(() =>
      Promise.resolve(
        response(
          200,
          '{"hosts":[{"name":"local","local":true,"health":"connected"},{"name":"b","health":"unreachable","error":"timeout","last_seen":1700000000}]}',
        ),
      ),
    )
    vi.stubGlobal('fetch', fetchMock)

    const hosts = await fetchHosts()
    expect(fetchMock).toHaveBeenCalledWith('/api/hosts')
    expect(hosts).toEqual([
      { name: 'local', local: true, health: 'connected' },
      { name: 'b', health: 'unreachable', error: 'timeout', last_seen: 1700000000 },
    ])
  })

  it('throws on a non-OK status', async () => {
    vi.stubGlobal('fetch', vi.fn(() => Promise.resolve(response(500, 'boom'))))
    await expect(fetchHosts()).rejects.toThrow('fetchHosts: 500 boom')
  })
})
