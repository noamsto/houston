import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, cleanup, fireEvent, render, screen } from '@testing-library/react'
import { MobileInputBar } from './MobileInputBar'
import type { TerminalAddress } from '../api/terminal'
import { composerMaxHeight } from './composerMaxHeight'

const target = 'sess:0.0'
const paneAddress: TerminalAddress = { kind: 'pane', target }
const runAddress: TerminalAddress = { kind: 'run', id: 'run-1' }

function lastRequest(): { url: string; params: URLSearchParams } {
  const calls = vi.mocked(fetch).mock.calls
  const [url, init] = calls[calls.length - 1]
  return { url: String(url), params: new URLSearchParams(String(init?.body)) }
}

function lastJSONRequest(): { url: string; body: unknown } {
  const calls = vi.mocked(fetch).mock.calls
  const [url, init] = calls[calls.length - 1]
  return { url: String(url), body: JSON.parse(String(init?.body)) }
}

beforeEach(() => {
  vi.stubGlobal('fetch', vi.fn(() => Promise.resolve({ ok: true } as Response)))
})

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

async function click(el: HTMLElement) {
  await act(async () => {
    fireEvent.click(el)
  })
}

describe('MobileInputBar composer', () => {
  it('Send posts the typed text (server appends Enter) and clears the field', async () => {
    render(<MobileInputBar address={paneAddress} />)
    const field = screen.getByPlaceholderText('Send a message...') as HTMLTextAreaElement
    fireEvent.change(field, { target: { value: 'echo hi' } })

    await click(screen.getByRole('button', { name: 'Send' }))

    const { url, params } = lastRequest()
    expect(url).toBe(`/api/pane/${target}/send`)
    expect(params.get('input')).toBe('echo hi')
    expect(params.get('special')).toBeNull()
    expect(params.get('noenter')).toBeNull()
    expect(field.value).toBe('')
  })

  it('Send with an empty field presses Enter', async () => {
    render(<MobileInputBar address={paneAddress} />)
    await click(screen.getByRole('button', { name: 'Send' }))
    const { params } = lastRequest()
    expect(params.get('input')).toBe('Enter')
    expect(params.get('special')).toBe('true')
  })

  it('plain Enter inserts a newline; Ctrl+Enter sends', async () => {
    render(<MobileInputBar address={paneAddress} />)
    const field = screen.getByPlaceholderText('Send a message...')
    fireEvent.change(field, { target: { value: 'line1\nline2' } })

    fireEvent.keyDown(field, { key: 'Enter' })
    expect(fetch).not.toHaveBeenCalled()

    await act(async () => {
      fireEvent.keyDown(field, { key: 'Enter', ctrlKey: true })
    })
    expect(lastRequest().params.get('input')).toBe('line1\nline2')
  })

  it('uses the placeholder from the agent input text when given', () => {
    render(<MobileInputBar address={paneAddress} inputText="Type a reply" />)
    expect(screen.getByPlaceholderText('Type a reply')).toBeTruthy()
  })
})

describe('MobileInputBar quick keys', () => {
  it.each([
    ['Esc', 'Escape'],
    ['Ctrl+C', 'C-c'],
    ['Enter', 'Enter'],
    ['Tab', 'Tab'],
    ['Shift+Tab', 'BTab'],
    ['Up', 'Up'],
    ['Down', 'Down'],
    ['1', '1'],
    ['2', '2'],
    ['3', '3'],
    ['4', '4'],
    ['5', '5'],
    ['Y', 'y'],
    ['N', 'n'],
    ['Alt+P', 'M-p'],
    ['Ctrl+O', 'C-o'],
    ['Ctrl+Z', 'C-z'],
  ])('%s sends the %s key via the special route', async (label, key) => {
    render(<MobileInputBar address={paneAddress} />)
    await click(screen.getByRole('button', { name: label }))
    const { url, params } = lastRequest()
    expect(url).toBe(`/api/pane/${target}/send`)
    expect(params.get('input')).toBe(key)
    expect(params.get('special')).toBe('true')
  })
})

describe('MobileInputBar choices', () => {
  it('/copy is sent as text (with Enter), not a keystroke', async () => {
    render(<MobileInputBar address={paneAddress} />)
    await click(screen.getByRole('button', { name: '/copy' }))
    const { params } = lastRequest()
    expect(params.get('input')).toBe('/copy')
    expect(params.get('special')).toBeNull()
  })

  it('renders no choice group when the agent shows no prompt', () => {
    render(<MobileInputBar address={paneAddress} />)
    expect(screen.queryByTestId('choices')).toBeNull()
    render(<MobileInputBar address={paneAddress} choices={[]} />)
    expect(screen.queryByTestId('choices')).toBeNull()
  })

  it('renders each choice as a numbered button and answers with its ordinal key', async () => {
    render(<MobileInputBar address={paneAddress} agent="claude-code" choices={['Yes', 'Yes, always', 'No']} />)
    const group = screen.getByTestId('choices')
    expect(group.querySelectorAll('button')).toHaveLength(3)

    await click(screen.getByRole('button', { name: '2. Yes, always' }))
    const { params } = lastRequest()
    expect(params.get('input')).toBe('2')
    expect(params.get('special')).toBe('true')
  })

  it('non-Claude agents keep label + Enter (ordinals would be wrong once Amp reorders)', async () => {
    render(<MobileInputBar address={paneAddress} agent="amp" choices={['Allow All', 'Yes', 'No']} />)
    await click(screen.getByRole('button', { name: 'Yes' }))
    const { params } = lastRequest()
    expect(params.get('input')).toBe('Yes')
    expect(params.get('special')).toBeNull()
  })
})

describe('MobileInputBar send failures', () => {
  function typeAndSend(text: string) {
    render(<MobileInputBar address={paneAddress} />)
    const field = screen.getByPlaceholderText('Send a message...') as HTMLTextAreaElement
    fireEvent.change(field, { target: { value: text } })
    return field
  }

  it('keeps the text and shows the status on a non-ok response', async () => {
    vi.mocked(fetch).mockResolvedValue({ ok: false, status: 503 } as Response)
    const field = typeAndSend('echo hi')
    await click(screen.getByRole('button', { name: 'Send' }))
    expect(field.value).toBe('echo hi')
    expect(screen.getByTestId('send-error').textContent).toContain('HTTP 503')
  })

  it('keeps the text and shows offline when fetch rejects; Send again delivers', async () => {
    vi.mocked(fetch).mockRejectedValueOnce(new TypeError('Failed to fetch'))
    const field = typeAndSend('echo hi')
    await click(screen.getByRole('button', { name: 'Send' }))
    expect(field.value).toBe('echo hi')
    expect(screen.getByTestId('send-error').textContent).toContain('offline')

    await click(screen.getByRole('button', { name: 'Send' }))
    expect(field.value).toBe('')
    expect(screen.queryByTestId('send-error')).toBeNull()
  })

  it('401 says the session expired', async () => {
    vi.mocked(fetch).mockResolvedValue({ ok: false, status: 401 } as Response)
    typeAndSend('x')
    await click(screen.getByRole('button', { name: 'Send' }))
    expect(screen.getByTestId('send-error').textContent).toContain('session expired — reload')
  })

  it('disables Send while in flight, keeps the textarea editable, and sends once', async () => {
    let resolve!: (r: Response) => void
    vi.mocked(fetch).mockReturnValue(new Promise<Response>((r) => { resolve = r }))
    const field = typeAndSend('echo hi')
    const send = screen.getByRole('button', { name: 'Send' }) as HTMLButtonElement
    await click(send)
    expect(send.disabled).toBe(true)
    expect(field.disabled).toBe(false)
    fireEvent.keyDown(field, { key: 'Enter', ctrlKey: true })
    expect(fetch).toHaveBeenCalledTimes(1)

    await act(async () => resolve({ ok: true } as Response))
    expect(send.disabled).toBe(false)
    expect(field.value).toBe('')
  })

  it('quick-key failure surfaces a transient indicator', async () => {
    vi.mocked(fetch).mockResolvedValue({ ok: false, status: 500 } as Response)
    render(<MobileInputBar address={paneAddress} />)
    await click(screen.getByRole('button', { name: 'Ctrl+C' }))
    expect(screen.getByTestId('key-error').textContent).toContain('HTTP 500')
  })

  it('choice failure surfaces the indicator', async () => {
    vi.mocked(fetch).mockRejectedValue(new TypeError('x'))
    render(<MobileInputBar address={paneAddress} agent="claude-code" choices={['Yes']} />)
    await click(screen.getByRole('button', { name: '1. Yes' }))
    expect(screen.getByTestId('key-error').textContent).toContain('offline')
  })

  describe('file attach', () => {
    async function attach() {
      const input = document.querySelector('input[type="file"]') as HTMLInputElement
      const file = new File(['x'], 'a.png', { type: 'image/png' })
      await act(async () => {
        fireEvent.change(input, { target: { files: [file] } })
        await new Promise((r) => setTimeout(r, 20))
      })
    }

    it('keeps the text and shows the error when the upload fails', async () => {
      vi.mocked(fetch).mockResolvedValue({ ok: false, status: 500 } as Response)
      const field = typeAndSend('look at this')
      await attach()
      expect(field.value).toBe('look at this')
      expect(screen.getByTestId('send-error').textContent).toContain('HTTP 500')
    })

    it('keeps the text and shows offline when fetch rejects', async () => {
      vi.mocked(fetch).mockRejectedValue(new TypeError('x'))
      const field = typeAndSend('look at this')
      await attach()
      expect(field.value).toBe('look at this')
      expect(screen.getByTestId('send-error').textContent).toContain('offline')
    })

    it('keeps text typed during the upload and blocks a concurrent Send', async () => {
      let resolve!: (r: Response) => void
      vi.mocked(fetch).mockReturnValue(new Promise<Response>((r) => { resolve = r }))
      const field = typeAndSend('look at this')
      await attach()
      const send = screen.getByRole('button', { name: 'Send' }) as HTMLButtonElement
      expect(send.disabled).toBe(true)
      fireEvent.change(field, { target: { value: 'next message' } })
      await act(async () => resolve({ ok: true } as Response))
      expect(field.value).toBe('next message')
      expect(fetch).toHaveBeenCalledTimes(1)
    })

    it('clears the text on success', async () => {
      const field = typeAndSend('look at this')
      await attach()
      expect(field.value).toBe('')
      expect(screen.queryByTestId('send-error')).toBeNull()
    })
  })
})

// #59: same composer, addressed at a run instead of a raw pane target. Every
// request must land on /api/runs/:id/input — never a /api/pane/ URL.
describe('run address', () => {
  function typeAndSend(text: string) {
    render(<MobileInputBar address={runAddress} />)
    const field = screen.getByPlaceholderText('Send a message...') as HTMLTextAreaElement
    fireEvent.change(field, { target: { value: text } })
    return field
  }

  it('Send posts {type: text} to /api/runs/:id/input', async () => {
    const field = typeAndSend('echo hi')
    await click(screen.getByRole('button', { name: 'Send' }))
    const { url, body } = lastJSONRequest()
    expect(url).toBe('/api/runs/run-1/input')
    expect(body).toEqual({ type: 'text', text: 'echo hi' })
    expect(field.value).toBe('')
  })

  it('Send with an empty field posts {type: key, key: Enter}', async () => {
    render(<MobileInputBar address={runAddress} />)
    await click(screen.getByRole('button', { name: 'Send' }))
    const { url, body } = lastJSONRequest()
    expect(url).toBe('/api/runs/run-1/input')
    expect(body).toEqual({ type: 'key', key: 'Enter' })
  })

  it('Esc posts {type: key, key: Escape}', async () => {
    render(<MobileInputBar address={runAddress} />)
    await click(screen.getByRole('button', { name: 'Esc' }))
    expect(lastJSONRequest().body).toEqual({ type: 'key', key: 'Escape' })
  })

  it('a claude-code choice tap posts its ordinal as a key', async () => {
    render(<MobileInputBar address={runAddress} agent="claude-code" choices={['Yes', 'No']} />)
    await click(screen.getByRole('button', { name: '2. No' }))
    expect(lastJSONRequest().body).toEqual({ type: 'key', key: '2' })
  })

  it('a non-claude choice tap posts its label as text', async () => {
    render(<MobileInputBar address={runAddress} agent="amp" choices={['Allow All']} />)
    await click(screen.getByRole('button', { name: 'Allow All' }))
    expect(lastJSONRequest().body).toEqual({ type: 'text', text: 'Allow All' })
  })

  it('attach posts {type: image} with the caption and encoded image', async () => {
    const field = typeAndSend('look at this')
    const input = document.querySelector('input[type="file"]') as HTMLInputElement
    const file = new File(['x'], 'a.png', { type: 'image/png' })
    await act(async () => {
      fireEvent.change(input, { target: { files: [file] } })
      await new Promise((r) => setTimeout(r, 20))
    })
    const { url, body } = lastJSONRequest()
    expect(url).toBe('/api/runs/run-1/input')
    expect(body).toMatchObject({ type: 'image', text: 'look at this' })
    expect(field.value).toBe('')
  })

  it('offline then retry: keeps the text, shows send-error, then succeeds', async () => {
    vi.mocked(fetch).mockRejectedValueOnce(new TypeError('Failed to fetch'))
    const field = typeAndSend('echo hi')
    await click(screen.getByRole('button', { name: 'Send' }))
    expect(field.value).toBe('echo hi')
    expect(screen.getByTestId('send-error').textContent).toContain('offline')

    await click(screen.getByRole('button', { name: 'Send' }))
    expect(field.value).toBe('')
    expect(screen.queryByTestId('send-error')).toBeNull()
  })

  it('never builds a /api/pane/ URL for a run address', async () => {
    render(<MobileInputBar address={runAddress} agent="claude-code" choices={['Yes']} />)
    const field = screen.getByPlaceholderText('Send a message...')
    fireEvent.change(field, { target: { value: 'echo hi' } })
    await click(screen.getByRole('button', { name: 'Send' }))
    await click(screen.getByRole('button', { name: 'Esc' }))
    await click(screen.getByRole('button', { name: '1. Yes' }))

    for (const [url] of vi.mocked(fetch).mock.calls) {
      expect(String(url)).not.toContain('/api/pane/')
    }
  })
})

describe('composer auto-grow', () => {
  it('caps at 40% of the viewport', () => {
    expect(composerMaxHeight(800)).toBe(320)
    expect(composerMaxHeight(400)).toBe(160)
  })

  it('grows to the cap then scrolls internally', () => {
    vi.stubGlobal('visualViewport', { height: 500 })
    render(<MobileInputBar address={paneAddress} />)
    const field = screen.getByPlaceholderText('Send a message...') as HTMLTextAreaElement
    expect(field.style.touchAction).toBe('pan-y')

    Object.defineProperty(field, 'scrollHeight', { configurable: true, value: 120 })
    fireEvent.change(field, { target: { value: 'a\nb\nc' } })
    expect(field.style.height).toBe('120px')
    expect(field.style.overflowY).toBe('hidden')

    Object.defineProperty(field, 'scrollHeight', { configurable: true, value: 900 })
    fireEvent.change(field, { target: { value: 'x\n'.repeat(30) } })
    expect(field.style.height).toBe('200px')
    expect(field.style.overflowY).toBe('auto')
  })
})
