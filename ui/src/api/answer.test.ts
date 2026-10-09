import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { answerChoice, answerQuestion, fetchPrompt } from './answer'

function respond(status: number, body = ''): Response {
  return {
    ok: status >= 200 && status < 300,
    status,
    text: async () => body,
    json: async () => JSON.parse(body),
  } as Response
}

function lastRequest(): { url: string; init: RequestInit } {
  const [url, init] = vi.mocked(fetch).mock.calls.at(-1)!
  return { url: String(url), init: (init ?? {}) as RequestInit }
}

beforeEach(() => {
  vi.stubGlobal('fetch', vi.fn(() => Promise.resolve(respond(204))))
})

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('answerQuestion', () => {
  it('POSTs the question body to /api/runs/:id/answer', async () => {
    const answers = [{ question: 0, options: [1] }, { question: 1, options: [], text: 'x' }]
    expect(await answerQuestion('a/b', 't1', answers)).toEqual({ ok: true })
    const { url, init } = lastRequest()
    expect(url).toBe('/api/runs/a%2Fb/answer')
    expect(init.method).toBe('POST')
    expect(init.headers).toEqual({ 'Content-Type': 'application/json' })
    expect(JSON.parse(String(init.body))).toEqual({ kind: 'question', toolCallId: 't1', answers })
  })
})

describe('answerChoice', () => {
  it('POSTs the ordinal and frame', async () => {
    expect(await answerChoice('r1', 2, 'ab12')).toEqual({ ok: true })
    expect(JSON.parse(String(lastRequest().init.body))).toEqual({ kind: 'choice', ordinal: 2, frame: 'ab12' })
  })
})

describe('result mapping', () => {
  it('409 with a text body is moved, not partial', async () => {
    vi.mocked(fetch).mockResolvedValue(respond(409, 'prompt changed'))
    expect(await answerChoice('r1', 1, 'f')).toEqual({ moved: true, partial: false })
  })

  it('409 with {"partial":true} is moved and partial', async () => {
    vi.mocked(fetch).mockResolvedValue(respond(409, '{"partial":true}'))
    expect(await answerChoice('r1', 1, 'f')).toEqual({ moved: true, partial: true })
  })

  it('401 reads as a session expiry', async () => {
    vi.mocked(fetch).mockResolvedValue(respond(401))
    expect(await answerChoice('r1', 1, 'f')).toEqual({ error: 'session expired — reload' })
  })

  it('502 with partial true warns the answer may have been partly sent', async () => {
    vi.mocked(fetch).mockResolvedValue(respond(502, '{"partial":true}'))
    expect(await answerChoice('r1', 1, 'f')).toEqual({
      error: 'may have been partly sent — check the agent before resending',
    })
  })

  it('503 with partial true warns the answer may have been partly sent', async () => {
    vi.mocked(fetch).mockResolvedValue(respond(503, '{"partial":true}'))
    expect(await answerChoice('r1', 1, 'f')).toEqual({
      error: 'may have been partly sent — check the agent before resending',
    })
  })

  it('a copy-mode 409 asks to exit copy mode instead of reading as moved', async () => {
    vi.mocked(fetch).mockResolvedValue(respond(409, 'pane is in copy mode\n'))
    expect(await answerChoice('r1', 1, 'f')).toEqual({
      error: 'The terminal is scrolled back (copy mode) — exit it, then retry',
    })
  })

  it('other statuses show the server text', async () => {
    vi.mocked(fetch).mockResolvedValue(respond(422, 'too many options\n'))
    expect(await answerChoice('r1', 1, 'f')).toEqual({ error: 'too many options' })
  })

  it('an empty body reads as HTTP <status>', async () => {
    vi.mocked(fetch).mockResolvedValue(respond(500))
    expect(await answerChoice('r1', 1, 'f')).toEqual({ error: 'HTTP 500' })
  })

  it('a rejected fetch reads as offline', async () => {
    vi.mocked(fetch).mockRejectedValue(new TypeError('Failed to fetch'))
    expect(await answerChoice('r1', 1, 'f')).toEqual({ error: 'offline' })
  })

  it('a timeout warns the answer may have been partly sent', async () => {
    vi.mocked(fetch).mockRejectedValue(new DOMException('timed out', 'TimeoutError'))
    expect(await answerChoice('r1', 1, 'f')).toEqual({ error: expect.stringContaining('may have been partly sent') })
  })
})

describe('fetchPrompt', () => {
  it('returns the prompt on 200', async () => {
    const prompt = { question: 'Proceed?', choices: ['Yes', 'No'], detail: 'ls', frame: 'f' }
    vi.mocked(fetch).mockResolvedValue(respond(200, JSON.stringify(prompt)))
    expect(await fetchPrompt('r1')).toEqual(prompt)
    expect(lastRequest().url).toBe('/api/runs/r1/prompt')
  })

  it('bounds the request with a timeout signal', async () => {
    vi.mocked(fetch).mockResolvedValue(respond(404))
    await fetchPrompt('r1')
    expect(lastRequest().init.signal).toBeInstanceOf(AbortSignal)
  })

  it('returns null on 404', async () => {
    vi.mocked(fetch).mockResolvedValue(respond(404, 'no prompt'))
    expect(await fetchPrompt('r1')).toBeNull()
  })

  it('throws on any other failure', async () => {
    vi.mocked(fetch).mockResolvedValue(respond(503, 'tmux unavailable'))
    await expect(fetchPrompt('r1')).rejects.toThrow('503')
  })
})
