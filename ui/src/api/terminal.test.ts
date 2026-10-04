import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { sendImage, sendKey, sendText, terminalKey, terminalSocketPath, type TerminalAddress } from './terminal'

const runAddr: TerminalAddress = { kind: 'run', id: 'run-1' }

function lastRequest(): { url: string; init: RequestInit } {
  const calls = vi.mocked(fetch).mock.calls
  const [url, init] = calls[calls.length - 1]
  return { url: String(url), init: init as RequestInit }
}

beforeEach(() => {
  vi.stubGlobal('fetch', vi.fn(() => Promise.resolve({ ok: true } as Response)))
})

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('terminalKey', () => {
  it('formats a run address', () => {
    expect(terminalKey(runAddr)).toBe('run:run-1')
  })
})

describe('terminalSocketPath', () => {
  it('points at the run terminal route, encoding the id', () => {
    expect(terminalSocketPath({ kind: 'run', id: 'a/b' })).toBe('/api/runs/a%2Fb/terminal')
  })
})

describe('sendText', () => {
  it('JSON POST to /api/runs/:id/input', async () => {
    await sendText(runAddr, 'echo hi')
    const { url, init } = lastRequest()
    expect(url).toBe('/api/runs/run-1/input')
    expect(init.headers).toEqual({ 'Content-Type': 'application/json' })
    expect(JSON.parse(String(init.body))).toEqual({ type: 'text', text: 'echo hi' })
  })

  it('encodes a run id containing a slash', async () => {
    await sendText({ kind: 'run', id: 'a/b' }, 'x')
    expect(lastRequest().url).toBe('/api/runs/a%2Fb/input')
  })
})

describe('sendKey', () => {
  it('JSON POST with type key, no implicit Enter', async () => {
    await sendKey(runAddr, 'Escape')
    const { url, init } = lastRequest()
    expect(url).toBe('/api/runs/run-1/input')
    expect(JSON.parse(String(init.body))).toEqual({ type: 'key', key: 'Escape' })
  })
})

describe('sendImage', () => {
  const image = { name: 'a.png', type: 'image/png', data: 'ZGF0YQ==' }

  it('JSON POST with type image, text, and a one-element images array', async () => {
    await sendImage(runAddr, 'look at this', image)
    const { url, init } = lastRequest()
    expect(url).toBe('/api/runs/run-1/input')
    expect(JSON.parse(String(init.body))).toEqual({ type: 'image', text: 'look at this', images: [image] })
  })
})

describe('error mapping', () => {
  it('401 reads as a session expiry', async () => {
    vi.mocked(fetch).mockResolvedValue({ ok: false, status: 401 } as Response)
    expect(await sendText(runAddr, 'x')).toBe('session expired — reload')
  })

  it('other non-ok statuses read as HTTP <status>', async () => {
    vi.mocked(fetch).mockResolvedValue({ ok: false, status: 409 } as Response)
    expect(await sendText(runAddr, 'x')).toBe('HTTP 409')
  })

  it('a rejected fetch reads as offline', async () => {
    vi.mocked(fetch).mockRejectedValue(new TypeError('Failed to fetch'))
    expect(await sendKey(runAddr, 'Escape')).toBe('offline')
  })

  it('a timeout DOMException reads as timed out', async () => {
    vi.mocked(fetch).mockRejectedValue(new DOMException('The operation timed out.', 'TimeoutError'))
    expect(await sendKey(runAddr, 'Escape')).toBe('timed out')
  })
})
