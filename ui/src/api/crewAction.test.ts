import { afterEach, describe, expect, it, vi } from 'vitest'
import { runCrewAction } from './crewAction'

function response(status: number, body: string): Response {
  return { ok: status >= 200 && status < 300, status, text: () => Promise.resolve(body) } as Response
}

function stubFetch(res: Response) {
  const fetchMock = vi.fn<(url: string, init?: RequestInit) => Promise<Response>>(() => Promise.resolve(res))
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('runCrewAction', () => {
  it('posts the item and returns the output', async () => {
    const fetchMock = stubFetch(response(200, '{"output":"ok"}'))
    const out = await runCrewAction('pane-1', 'stall:c1:feat/e')
    expect(out).toEqual({ ok: true, output: 'ok' })
    const [url, init] = fetchMock.mock.calls[0]
    expect(url).toBe('/api/runs/pane-1/crew/action')
    expect(init?.method).toBe('POST')
    expect(init?.body).toBe('{"item":"stall:c1:feat/e"}')
  })

  it('includes text and confirm when given', async () => {
    const fetchMock = stubFetch(response(200, '{"output":"ok"}'))
    await runCrewAction('pane-1', 'i', { text: 'yes' })
    await runCrewAction('pane-1', 'i', { confirm: true })
    expect(JSON.parse(fetchMock.mock.calls[0][1]?.body as string)).toEqual({ item: 'i', text: 'yes' })
    expect(JSON.parse(fetchMock.mock.calls[1][1]?.body as string)).toEqual({ item: 'i', confirm: true })
  })

  it('surfaces the 428 confirm prompt', async () => {
    stubFetch(response(428, '{"confirm":"Squash-merge PR #12?"}'))
    expect(await runCrewAction('pane-1', 'i')).toEqual({
      ok: false,
      status: 428,
      message: 'Squash-merge PR #12?',
      confirm: 'Squash-merge PR #12?',
    })
  })

  it('returns plain-text errors trimmed', async () => {
    stubFetch(response(409, 'nope\n'))
    expect(await runCrewAction('pane-1', 'i')).toEqual({ ok: false, status: 409, message: 'nope' })
  })

  it('url-encodes the run id', async () => {
    const fetchMock = stubFetch(response(200, '{"output":""}'))
    await runCrewAction('peer-a.b/c', 'i')
    expect(fetchMock.mock.calls[0][0]).toBe('/api/runs/peer-a.b%2Fc/crew/action')
  })

  it('maps a network failure to status 0', async () => {
    vi.stubGlobal('fetch', vi.fn(() => Promise.reject(new TypeError('offline'))))
    expect(await runCrewAction('pane-1', 'i')).toEqual({ ok: false, status: 0, message: 'offline' })
  })

  it('sends pr with confirm', async () => {
    const fetchMock = stubFetch(response(200, '{"output":"ok"}'))
    await runCrewAction('pane-1', 'i', { confirm: true, pr: 12 })
    expect(JSON.parse(fetchMock.mock.calls[0][1]?.body as string)).toEqual({ item: 'i', confirm: true, pr: 12 })
  })

  it('carries pr on a 428 prompt', async () => {
    stubFetch(response(428, '{"confirm":"Squash-merge PR #12?","pr":12}'))
    expect(await runCrewAction('pane-1', 'i')).toEqual({
      ok: false, status: 428, message: 'Squash-merge PR #12?', confirm: 'Squash-merge PR #12?', pr: 12,
    })
  })

  it('treats a JSON 409 with confirm and pr as a re-confirm', async () => {
    stubFetch(response(409, '{"confirm":"Squash-merge PR #13?","pr":13}'))
    expect(await runCrewAction('pane-1', 'i', { confirm: true, pr: 12 })).toEqual({
      ok: false, status: 409, message: 'Squash-merge PR #13?', confirm: 'Squash-merge PR #13?', pr: 13,
    })
  })

  it('keeps a JSON 409 without confirm as plain text', async () => {
    stubFetch(response(409, '{"error":"x"}'))
    expect(await runCrewAction('pane-1', 'i')).toEqual({ ok: false, status: 409, message: '{"error":"x"}' })
  })

  it('reports a non-JSON 200 as malformed', async () => {
    stubFetch(response(200, '<html>proxy</html>\n'))
    expect(await runCrewAction('pane-1', 'i')).toEqual({
      ok: false, status: 200, message: 'malformed response: <html>proxy</html>',
    })
  })

  it('reports a non-JSON 428 as malformed', async () => {
    stubFetch(response(428, 'nope'))
    expect(await runCrewAction('pane-1', 'i')).toEqual({ ok: false, status: 428, message: 'malformed response: nope' })
  })

  it('maps a body read failure to a network error', async () => {
    stubFetch({ ok: true, status: 200, text: () => Promise.reject(new TypeError('dropped')) } as Response)
    expect(await runCrewAction('pane-1', 'i')).toEqual({ ok: false, status: 0, message: 'network error' })
  })
})
