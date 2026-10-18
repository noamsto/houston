import { afterEach, describe, expect, it, vi } from 'vitest'
import { addRepo, fetchRepoCandidates, fetchRepos, removeRepo } from './repos'

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

describe('fetchRepos', () => {
  it('GETs /api/repos and returns the body', async () => {
    const fetchMock = vi.fn(() =>
      Promise.resolve(response(200, '{"roots":["/r"],"repos":[{"path":"/r/a","name":"a","valid":true}]}')),
    )
    vi.stubGlobal('fetch', fetchMock)

    const out = await fetchRepos()
    expect(fetchMock).toHaveBeenCalledWith('/api/repos')
    expect(out).toEqual({ roots: ['/r'], repos: [{ path: '/r/a', name: 'a', valid: true }] })
  })

  it('throws on a non-OK status', async () => {
    vi.stubGlobal('fetch', vi.fn(() => Promise.resolve(response(500, 'boom'))))
    await expect(fetchRepos()).rejects.toThrow('500 boom')
  })

  it('GETs the host-addressed route for a peer and the plain one for local', async () => {
    const fetchMock = vi.fn(() => Promise.resolve(response(200, '{"roots":[],"repos":[]}')))
    vi.stubGlobal('fetch', fetchMock)

    await fetchRepos('b')
    expect(fetchMock).toHaveBeenLastCalledWith('/api/hosts/b/repos')
    await fetchRepos('local')
    expect(fetchMock).toHaveBeenLastCalledWith('/api/repos')
  })
})

describe('fetchRepoCandidates', () => {
  it('GETs the candidates URL with the query encoded', async () => {
    const fetchMock = vi.fn(() =>
      Promise.resolve(
        response(
          200,
          '{"roots":["/r"],"candidates":[{"path":"/r/a b","name":"a b","registered":false}],"truncated":true}',
        ),
      ),
    )
    vi.stubGlobal('fetch', fetchMock)

    const out = await fetchRepoCandidates('a b&c')
    expect(fetchMock).toHaveBeenCalledWith('/api/repos/candidates?q=a%20b%26c')
    expect(out.truncated).toBe(true)
    expect(out.candidates).toEqual([{ path: '/r/a b', name: 'a b', registered: false }])
  })

  it('throws on a non-OK status', async () => {
    vi.stubGlobal('fetch', vi.fn(() => Promise.resolve(response(503, 'down'))))
    await expect(fetchRepoCandidates('x')).rejects.toThrow('503 down')
  })

  it('GETs the host-addressed route for a peer and the plain one for local', async () => {
    const fetchMock = vi.fn(() => Promise.resolve(response(200, '{"roots":[],"candidates":[],"truncated":false}')))
    vi.stubGlobal('fetch', fetchMock)

    await fetchRepoCandidates('a b', 'b')
    expect(fetchMock).toHaveBeenLastCalledWith('/api/hosts/b/repos/candidates?q=a%20b')
    await fetchRepoCandidates('a b', 'local')
    expect(fetchMock).toHaveBeenLastCalledWith('/api/repos/candidates?q=a%20b')
  })
})

describe('addRepo', () => {
  it('POSTs the path as JSON and returns the resolved path', async () => {
    const fetchMock = vi.fn<typeof fetch>(() =>
      Promise.resolve(response(200, '{"path":"/real/a","name":"a"}')),
    )
    vi.stubGlobal('fetch', fetchMock)

    const out = await addRepo('/link/a')
    expect(out).toEqual({ ok: true, path: '/real/a' })
    const [url, init] = fetchMock.mock.calls[0]
    expect(url).toBe('/api/repos')
    expect(init?.method).toBe('POST')
    expect(JSON.parse(init?.body as string)).toEqual({ path: '/link/a' })
  })

  it('POSTs to the host-addressed route for a peer and the plain one for local', async () => {
    const fetchMock = vi.fn<typeof fetch>(() => Promise.resolve(response(200, '{"path":"/real/a","name":"a"}')))
    vi.stubGlobal('fetch', fetchMock)

    await addRepo('/link/a', 'b')
    await addRepo('/link/a', 'local')
    const calls = fetchMock.mock.calls
    expect(calls.map(([url]) => url)).toEqual(['/api/hosts/b/repos', '/api/repos'])
    for (const [, init] of calls) {
      expect(init?.method).toBe('POST')
      expect(init?.headers).toEqual({ 'Content-Type': 'application/json' })
      expect(JSON.parse(init?.body as string)).toEqual({ path: '/link/a' })
    }
  })

  it('maps a JSON error body to the error text', async () => {
    vi.stubGlobal('fetch', vi.fn(() => Promise.resolve(response(422, '{"error":"not a git main checkout"}'))))
    expect(await addRepo('/x')).toEqual({ ok: false, error: 'not a git main checkout' })
  })

  it('falls back to the raw text for a non-JSON error', async () => {
    vi.stubGlobal('fetch', vi.fn(() => Promise.resolve(response(502, 'bad gateway\n'))))
    expect(await addRepo('/x')).toEqual({ ok: false, error: 'bad gateway' })
  })

  it('network rejection returns an error', async () => {
    vi.stubGlobal('fetch', vi.fn(() => Promise.reject(new Error('down'))))
    expect(await addRepo('/x')).toEqual({ ok: false, error: 'network error' })
  })
})

describe('removeRepo', () => {
  it('DELETEs with a JSON body and maps 204 to ok', async () => {
    const fetchMock = vi.fn<typeof fetch>(() => Promise.resolve(response(204, '')))
    vi.stubGlobal('fetch', fetchMock)

    expect(await removeRepo('/real/a')).toEqual({ ok: true })
    const [url, init] = fetchMock.mock.calls[0]
    expect(url).toBe('/api/repos')
    expect(init?.method).toBe('DELETE')
    expect(JSON.parse(init?.body as string)).toEqual({ path: '/real/a' })
  })

  it('DELETEs on the host-addressed route for a peer and the plain one for local', async () => {
    const fetchMock = vi.fn<typeof fetch>(() => Promise.resolve(response(204, '')))
    vi.stubGlobal('fetch', fetchMock)

    expect(await removeRepo('/real/a', 'b')).toEqual({ ok: true })
    await removeRepo('/real/a', 'local')
    const calls = fetchMock.mock.calls
    expect(calls.map(([url]) => url)).toEqual(['/api/hosts/b/repos', '/api/repos'])
    for (const [, init] of calls) {
      expect(init?.method).toBe('DELETE')
      expect(init?.headers).toEqual({ 'Content-Type': 'application/json' })
      expect(JSON.parse(init?.body as string)).toEqual({ path: '/real/a' })
    }
  })

  it('maps a 404 JSON error', async () => {
    vi.stubGlobal('fetch', vi.fn(() => Promise.resolve(response(404, '{"error":"not registered"}'))))
    expect(await removeRepo('/x')).toEqual({ ok: false, error: 'not registered' })
  })

  it('network rejection returns an error', async () => {
    vi.stubGlobal('fetch', vi.fn(() => Promise.reject(new Error('down'))))
    expect(await removeRepo('/x')).toEqual({ ok: false, error: 'network error' })
  })
})
