import { afterEach, describe, expect, it, vi } from 'vitest'
import { submitDispatch, submitDispatcher, type DispatchRequest, type DispatcherRequest } from './dispatch'

function jsonResponse(status: number, body: string): Response {
  return { ok: status >= 200 && status < 300, status, text: () => Promise.resolve(body) } as Response
}

const req: DispatchRequest = {
  repo: '/repo',
  title: 'add widget',
  tier: 'standard',
  engine: 'claude',
  model: 'sonnet',
  effort: 'high',
  crew: '1700000000-123',
}

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('submitDispatch', () => {
  it('200 JSON body returns started', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(() => Promise.resolve(jsonResponse(200, '{"worker_id":"worker:feat/1-x#s1","branch":"feat/1-x"}'))),
    )

    const outcome = await submitDispatch(req)
    expect(outcome).toEqual({
      kind: 'started',
      workerId: 'worker:feat/1-x#s1',
      branch: 'feat/1-x',
      issueUrl: undefined,
      crew: undefined,
    })
  })

  it('200 JSON body with a minted crew carries it on the outcome', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(() =>
        Promise.resolve(
          jsonResponse(200, '{"worker_id":"worker:feat/1-x#s1","branch":"feat/1-x","crew":"1700000000-4242"}'),
        ),
      ),
    )

    const outcome = await submitDispatch(req)
    expect(outcome).toEqual({
      kind: 'started',
      workerId: 'worker:feat/1-x#s1',
      branch: 'feat/1-x',
      issueUrl: undefined,
      crew: '1700000000-4242',
    })
  })

  it('200 non-JSON body returns failed with malformed response', async () => {
    vi.stubGlobal('fetch', vi.fn(() => Promise.resolve(jsonResponse(200, '<html>not json</html>'))))

    const outcome = await submitDispatch(req)
    expect(outcome).toEqual({ kind: 'failed', status: 200, error: 'malformed response: <html>not json</html>' })
  })

  it('422 JSON error returns failed with the error text', async () => {
    vi.stubGlobal('fetch', vi.fn(() => Promise.resolve(jsonResponse(422, '{"error":"dispatch failed","output":"log"}'))))

    const outcome = await submitDispatch(req)
    expect(outcome).toEqual({
      kind: 'failed',
      status: 422,
      error: 'dispatch failed',
      output: 'log',
      workerId: undefined,
      crew: undefined,
    })
  })

  it('422 JSON error with a minted crew carries it on the outcome', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(() =>
        Promise.resolve(jsonResponse(422, '{"error":"dispatch failed","output":"log","crew":"1700000000-4242"}')),
      ),
    )

    const outcome = await submitDispatch(req)
    expect(outcome).toEqual({
      kind: 'failed',
      status: 422,
      error: 'dispatch failed',
      output: 'log',
      workerId: undefined,
      crew: '1700000000-4242',
    })
  })

  it('network rejection returns failed with status 0', async () => {
    vi.stubGlobal('fetch', vi.fn(() => Promise.reject(new Error('network down'))))

    const outcome = await submitDispatch(req)
    expect(outcome).toEqual({ kind: 'failed', status: 0, error: 'network error' })
  })
})

describe('submitDispatcher', () => {
  const dreq: DispatcherRequest = { repo: '/repo', tasks: ['fix the bug'], engine: 'claude' }

  it('posts JSON to /api/dispatch/dispatcher and maps 200 to started', async () => {
    const fetchMock = vi.fn<typeof fetch>(() =>
      Promise.resolve(
        jsonResponse(200, '{"crew":"1700000000-42","session":"repo","window":"@3","pane":"%7","run_id":"abc123"}'),
      ),
    )
    vi.stubGlobal('fetch', fetchMock)

    const outcome = await submitDispatcher(dreq)
    expect(outcome).toEqual({
      kind: 'started',
      crew: '1700000000-42',
      session: 'repo',
      window: '@3',
      pane: '%7',
      runId: 'abc123',
    })
    const [url, init] = fetchMock.mock.calls[0]
    expect(url).toBe('/api/dispatch/dispatcher')
    expect(init?.method).toBe('POST')
    expect(JSON.parse(init?.body as string)).toEqual(dreq)
  })

  it('200 non-JSON body returns failed with malformed response', async () => {
    vi.stubGlobal('fetch', vi.fn(() => Promise.resolve(jsonResponse(200, 'nope'))))
    expect(await submitDispatcher(dreq)).toEqual({ kind: 'failed', status: 200, error: 'malformed response: nope' })
  })

  it('non-200 JSON returns failed with error and output', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(() => Promise.resolve(jsonResponse(422, '{"error":"died at startup","output":"pane text"}'))),
    )
    expect(await submitDispatcher(dreq)).toEqual({
      kind: 'failed',
      status: 422,
      error: 'died at startup',
      output: 'pane text',
    })
  })

  it('non-200 non-JSON body is returned as the error text', async () => {
    vi.stubGlobal('fetch', vi.fn(() => Promise.resolve(jsonResponse(502, 'bad gateway\n'))))
    expect(await submitDispatcher(dreq)).toEqual({ kind: 'failed', status: 502, error: 'bad gateway' })
  })

  it('network rejection returns failed with status 0', async () => {
    vi.stubGlobal('fetch', vi.fn(() => Promise.reject(new Error('network down'))))
    expect(await submitDispatcher(dreq)).toEqual({ kind: 'failed', status: 0, error: 'network error' })
  })
})
