import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, cleanup, fireEvent, render, screen } from '@testing-library/react'
import { MobileInputBar } from './MobileInputBar'

const target = 'sess:0.0'

function lastRequest(): { url: string; params: URLSearchParams } {
  const calls = vi.mocked(fetch).mock.calls
  const [url, init] = calls[calls.length - 1]
  return { url: String(url), params: new URLSearchParams(String(init?.body)) }
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
    render(<MobileInputBar target={target} />)
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
    render(<MobileInputBar target={target} />)
    await click(screen.getByRole('button', { name: 'Send' }))
    const { params } = lastRequest()
    expect(params.get('input')).toBe('Enter')
    expect(params.get('special')).toBe('true')
  })

  it('plain Enter inserts a newline; Ctrl+Enter sends', async () => {
    render(<MobileInputBar target={target} />)
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
    render(<MobileInputBar target={target} inputText="Type a reply" />)
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
    render(<MobileInputBar target={target} />)
    await click(screen.getByRole('button', { name: label }))
    const { url, params } = lastRequest()
    expect(url).toBe(`/api/pane/${target}/send`)
    expect(params.get('input')).toBe(key)
    expect(params.get('special')).toBe('true')
  })
})

describe('MobileInputBar choices', () => {
  it('/copy is sent as text (with Enter), not a keystroke', async () => {
    render(<MobileInputBar target={target} />)
    await click(screen.getByRole('button', { name: '/copy' }))
    const { params } = lastRequest()
    expect(params.get('input')).toBe('/copy')
    expect(params.get('special')).toBeNull()
  })

  it('renders no choice group when the agent shows no prompt', () => {
    render(<MobileInputBar target={target} />)
    expect(screen.queryByTestId('choices')).toBeNull()
    render(<MobileInputBar target={target} choices={[]} />)
    expect(screen.queryByTestId('choices')).toBeNull()
  })

  it('renders each choice as a numbered button and answers with its ordinal key', async () => {
    render(<MobileInputBar target={target} agent="claude-code" choices={['Yes', 'Yes, always', 'No']} />)
    const group = screen.getByTestId('choices')
    expect(group.querySelectorAll('button')).toHaveLength(3)

    await click(screen.getByRole('button', { name: '2. Yes, always' }))
    const { params } = lastRequest()
    expect(params.get('input')).toBe('2')
    expect(params.get('special')).toBe('true')
  })

  it('non-Claude agents keep label + Enter (ordinals would be wrong once Amp reorders)', async () => {
    render(<MobileInputBar target={target} agent="amp" choices={['Allow All', 'Yes', 'No']} />)
    await click(screen.getByRole('button', { name: 'Yes' }))
    const { params } = lastRequest()
    expect(params.get('input')).toBe('Yes')
    expect(params.get('special')).toBeNull()
  })
})

describe('MobileInputBar send failures', () => {
  function typeAndSend(text: string) {
    render(<MobileInputBar target={target} />)
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
    render(<MobileInputBar target={target} />)
    await click(screen.getByRole('button', { name: 'Ctrl+C' }))
    expect(screen.getByTestId('key-error').textContent).toContain('HTTP 500')
  })

  it('choice failure surfaces the indicator', async () => {
    vi.mocked(fetch).mockRejectedValue(new TypeError('x'))
    render(<MobileInputBar target={target} agent="claude-code" choices={['Yes']} />)
    await click(screen.getByRole('button', { name: '1. Yes' }))
    expect(screen.getByTestId('key-error').textContent).toContain('offline')
  })
})
