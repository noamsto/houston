import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import {
  answerBack,
  answerChoice,
  answerStep,
  answerSubmit,
  fetchPrompt,
  readQuestionState,
  type QuestionState,
} from './answer'

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

describe('answerChoice', () => {
  it('POSTs the ordinal and frame to /api/runs/:id/answer', async () => {
    expect(await answerChoice('a/b', 2, 'ab12')).toEqual({ ok: true })
    const { url, init } = lastRequest()
    expect(url).toBe('/api/runs/a%2Fb/answer')
    expect(init.method).toBe('POST')
    expect(init.headers).toEqual({ 'Content-Type': 'application/json' })
    expect(JSON.parse(String(init.body))).toEqual({ kind: 'choice', ordinal: 2, frame: 'ab12' })
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

const state: QuestionState = {
  tabbed: true,
  step: 1,
  count: 2,
  questions: [{ question: 'Pick', multiSelect: false, preview: false, options: [{ label: 'A' }, { label: 'B' }] }],
  options: [1],
}

describe('readQuestionState', () => {
  it('POSTs the state kind and returns the parsed state on 200', async () => {
    vi.mocked(fetch).mockResolvedValue(respond(200, JSON.stringify(state)))
    expect(await readQuestionState('a/b', 't1')).toEqual(state)
    const { url, init } = lastRequest()
    expect(url).toBe('/api/runs/a%2Fb/answer')
    expect(init.method).toBe('POST')
    expect(JSON.parse(String(init.body))).toEqual({ kind: 'state', toolCallId: 't1' })
    expect(init.signal).toBeInstanceOf(AbortSignal)
  })

  it('returns null on 404', async () => {
    vi.mocked(fetch).mockResolvedValue(respond(404, 'no question'))
    expect(await readQuestionState('r1', 't1')).toBeNull()
  })

  it('throws on any other status', async () => {
    vi.mocked(fetch).mockResolvedValue(respond(500, 'boom'))
    await expect(readQuestionState('r1', 't1')).rejects.toThrow('500')
  })

  it('throws when the request fails', async () => {
    vi.mocked(fetch).mockRejectedValue(new TypeError('Failed to fetch'))
    await expect(readQuestionState('r1', 't1')).rejects.toThrow()
  })
})

describe('answerStep', () => {
  it('POSTs the step body and returns the state of a 200', async () => {
    vi.mocked(fetch).mockResolvedValue(respond(200, JSON.stringify(state)))
    const answer = { options: [0, 2], text: 'x' }
    expect(await answerStep('r1', 't1', 1, answer)).toEqual({ ok: true, state })
    expect(JSON.parse(String(lastRequest().init.body))).toEqual({ kind: 'step', toolCallId: 't1', index: 1, answer })
  })

  it('a 204 is ok without state', async () => {
    expect(await answerStep('r1', 't1', 0, { options: [1] })).toEqual({ ok: true })
  })

  it('a 200 whose body is not a state is an error, not a bare ok', async () => {
    const unreadable = { error: 'could not read the terminal state' }
    vi.mocked(fetch).mockResolvedValue(respond(200, 'not json'))
    expect(await answerStep('r1', 't1', 0, { options: [1] })).toEqual(unreadable)
    vi.mocked(fetch).mockResolvedValue(respond(200, 'null'))
    expect(await answerBack('r1', 't1', 1)).toEqual(unreadable)
  })

  it('a 409 with {"partial":true} is moved and partial', async () => {
    vi.mocked(fetch).mockResolvedValue(respond(409, '{"partial":true}'))
    expect(await answerStep('r1', 't1', 0, { options: [1] })).toEqual({ moved: true, partial: true })
  })

  it('a 409 with a text body is moved, not partial', async () => {
    vi.mocked(fetch).mockResolvedValue(respond(409, 'prompt changed'))
    expect(await answerStep('r1', 't1', 0, { options: [1] })).toEqual({ moved: true, partial: false })
  })

  it('a copy-mode 409 asks to exit copy mode', async () => {
    vi.mocked(fetch).mockResolvedValue(respond(409, 'pane is in copy mode'))
    expect(await answerStep('r1', 't1', 0, { options: [1] })).toEqual({
      error: 'The terminal is scrolled back (copy mode) — exit it, then retry',
    })
  })

  it('a timeout warns the answer may have been partly sent', async () => {
    vi.mocked(fetch).mockRejectedValue(new DOMException('timed out', 'TimeoutError'))
    expect(await answerStep('r1', 't1', 0, { options: [1] })).toEqual({
      error: expect.stringContaining('may have been partly sent'),
    })
  })
})

describe('answerBack', () => {
  it('POSTs the back body and returns the state', async () => {
    vi.mocked(fetch).mockResolvedValue(respond(200, JSON.stringify(state)))
    expect(await answerBack('r1', 't1', 2)).toEqual({ ok: true, state })
    expect(JSON.parse(String(lastRequest().init.body))).toEqual({ kind: 'back', toolCallId: 't1', index: 2 })
  })
})

describe('answerSubmit', () => {
  it('POSTs the review strings; a 204 is ok', async () => {
    expect(await answerSubmit('r1', 't1', ['Blue', 'Basil'])).toEqual({ ok: true })
    expect(JSON.parse(String(lastRequest().init.body))).toEqual({
      kind: 'submit',
      toolCallId: 't1',
      review: ['Blue', 'Basil'],
    })
  })
})
