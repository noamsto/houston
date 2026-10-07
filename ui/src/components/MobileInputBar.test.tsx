import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, cleanup, fireEvent, render, screen } from '@testing-library/react'
import { MobileInputBar } from './MobileInputBar'
import type { TerminalAddress } from '../api/terminal'
import { composerMaxHeight } from './composerMaxHeight'

const runAddress: TerminalAddress = { kind: 'run', id: 'run-1' }

function lastJSONRequest(): { url: string; body: unknown } {
  const calls = vi.mocked(fetch).mock.calls
  const [url, init] = calls[calls.length - 1]
  return { url: String(url), body: JSON.parse(String(init?.body)) }
}

beforeEach(() => {
  localStorage.clear()
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
  it('keeps a draft per session: the same session restores it, another on the same run id does not', () => {
    const first = render(<MobileInputBar address={runAddress} draftKey="sess-a" />)
    fireEvent.change(screen.getByPlaceholderText('Send a message...'), { target: { value: 'for agent A' } })
    first.unmount()

    const other = render(<MobileInputBar address={runAddress} draftKey="sess-b" />)
    expect((screen.getByPlaceholderText('Send a message...') as HTMLTextAreaElement).value).toBe('')
    other.unmount()

    render(<MobileInputBar address={runAddress} draftKey="sess-a" />)
    expect((screen.getByPlaceholderText('Send a message...') as HTMLTextAreaElement).value).toBe('for agent A')
  })

  it('Send posts the typed text (server appends Enter) and clears the field', async () => {
    render(<MobileInputBar address={runAddress} />)
    const field = screen.getByPlaceholderText('Send a message...') as HTMLTextAreaElement
    fireEvent.change(field, { target: { value: 'echo hi' } })

    await click(screen.getByRole('button', { name: 'Send' }))

    const { url, body } = lastJSONRequest()
    expect(url).toBe('/api/runs/run-1/input')
    expect(body).toEqual({ type: 'text', text: 'echo hi' })
    expect(field.value).toBe('')
  })

  it('Send with an empty field presses Enter', async () => {
    render(<MobileInputBar address={runAddress} />)
    await click(screen.getByRole('button', { name: 'Send' }))
    expect(lastJSONRequest().body).toEqual({ type: 'key', key: 'Enter' })
  })

  it('plain Enter inserts a newline; Ctrl+Enter sends', async () => {
    render(<MobileInputBar address={runAddress} />)
    const field = screen.getByPlaceholderText('Send a message...')
    fireEvent.change(field, { target: { value: 'line1\nline2' } })

    fireEvent.keyDown(field, { key: 'Enter' })
    expect(fetch).not.toHaveBeenCalled()

    await act(async () => {
      fireEvent.keyDown(field, { key: 'Enter', ctrlKey: true })
    })
    expect(lastJSONRequest().body).toEqual({ type: 'text', text: 'line1\nline2' })
  })

  it('uses the placeholder from the agent input text when given', () => {
    render(<MobileInputBar address={runAddress} inputText="Type a reply" />)
    expect(screen.getByPlaceholderText('Type a reply')).toBeTruthy()
  })
})

describe('MobileInputBar quick keys', () => {
  it.each([
    ['Esc', 'Escape'],
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
  ])('%s sends the %s key', async (label, key) => {
    render(<MobileInputBar address={runAddress} />)
    await click(screen.getByRole('button', { name: label }))
    const { url, body } = lastJSONRequest()
    expect(url).toBe('/api/runs/run-1/input')
    expect(body).toEqual({ type: 'key', key })
  })
})

describe('MobileInputBar ^C', () => {
  it('needs a confirm before sending C-c', async () => {
    render(<MobileInputBar address={runAddress} />)
    await click(screen.getByRole('button', { name: 'Ctrl+C' }))
    expect(fetch).not.toHaveBeenCalled()
    await click(screen.getByRole('button', { name: 'Confirm' }))
    expect(lastJSONRequest().body).toEqual({ type: 'key', key: 'C-c' })
  })

  it('Cancel sends nothing', async () => {
    render(<MobileInputBar address={runAddress} />)
    await click(screen.getByRole('button', { name: 'Ctrl+C' }))
    await click(screen.getByRole('button', { name: 'Cancel' }))
    expect(fetch).not.toHaveBeenCalled()
    expect(screen.queryByText('Send ^C?')).toBeNull()
  })

  it('shows quick commands for a claude run and prefills /compact', async () => {
    render(<MobileInputBar address={runAddress} runAgent="claude" runState="idle" />)
    await click(screen.getByRole('button', { name: 'Commands' }))
    await click(screen.getByRole('button', { name: '/compact…' }))
    expect((screen.getByPlaceholderText('Send a message...') as HTMLTextAreaElement).value).toBe('/compact ')
  })

  it('does not overwrite a draft with /compact…', async () => {
    render(<MobileInputBar address={runAddress} runAgent="claude" runState="idle" />)
    const field = screen.getByPlaceholderText('Send a message...') as HTMLTextAreaElement
    fireEvent.change(field, { target: { value: 'my draft' } })
    await click(screen.getByRole('button', { name: 'Commands' }))
    await click(screen.getByRole('button', { name: '/compact…' }))
    expect(field.value).toBe('my draft')
    expect(screen.getByText('Clear the draft and attachment first, then tap /compact…')).toBeTruthy()
  })
})

describe('MobileInputBar choices', () => {
  it('/copy is sent as text (with Enter), not a keystroke', async () => {
    render(<MobileInputBar address={runAddress} />)
    await click(screen.getByRole('button', { name: '/copy' }))
    expect(lastJSONRequest().body).toEqual({ type: 'text', text: '/copy' })
  })

  it('renders no choice group when the agent shows no prompt', () => {
    render(<MobileInputBar address={runAddress} />)
    expect(screen.queryByTestId('choices')).toBeNull()
    render(<MobileInputBar address={runAddress} choices={[]} />)
    expect(screen.queryByTestId('choices')).toBeNull()
  })

  it('renders each choice as a numbered button and answers with its ordinal key', async () => {
    render(<MobileInputBar address={runAddress} agent="claude-code" choices={['Yes', 'Yes, always', 'No']} />)
    const group = screen.getByTestId('choices')
    expect(group.querySelectorAll('button')).toHaveLength(3)

    await click(screen.getByRole('button', { name: '2. Yes, always' }))
    expect(lastJSONRequest().body).toEqual({ type: 'key', key: '2' })
  })

  it('non-Claude agents keep label + Enter (ordinals would be wrong once Amp reorders)', async () => {
    render(<MobileInputBar address={runAddress} agent="amp" choices={['Allow All', 'Yes', 'No']} />)
    await click(screen.getByRole('button', { name: 'Yes' }))
    expect(lastJSONRequest().body).toEqual({ type: 'text', text: 'Yes' })
  })
})

describe('MobileInputBar send failures', () => {
  function typeAndSend(text: string) {
    render(<MobileInputBar address={runAddress} />)
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
    render(<MobileInputBar address={runAddress} />)
    await click(screen.getByRole('button', { name: 'Ctrl+C' }))
    await click(screen.getByRole('button', { name: 'Confirm' }))
    expect(screen.getByTestId('key-error').textContent).toContain('HTTP 500')
  })

  it('choice failure surfaces the indicator', async () => {
    vi.mocked(fetch).mockRejectedValue(new TypeError('x'))
    render(<MobileInputBar address={runAddress} agent="claude-code" choices={['Yes']} />)
    await click(screen.getByRole('button', { name: '1. Yes' }))
    expect(screen.getByTestId('key-error').textContent).toContain('offline')
  })
})

const FIELD = 'Send a message...'
const field = () => screen.getByPlaceholderText(FIELD) as HTMLTextAreaElement
const sendBtn = () => screen.getByRole('button', { name: 'Send' })

describe('MobileInputBar durable draft', () => {
  it('restores the draft after a remount with the same address', () => {
    const { unmount } = render(<MobileInputBar address={runAddress} />)
    fireEvent.change(field(), { target: { value: 'half-typed\nthought' } })
    unmount()
    render(<MobileInputBar address={runAddress} />)
    expect(field().value).toBe('half-typed\nthought')
  })

  it('keeps drafts isolated per run', () => {
    const { unmount } = render(<MobileInputBar address={runAddress} />)
    fireEvent.change(field(), { target: { value: 'for run-1' } })
    unmount()
    render(<MobileInputBar address={{ kind: 'run', id: 'run-2' }} />)
    expect(field().value).toBe('')
  })

  it('clears the draft after a successful send, including across a remount', async () => {
    const { unmount } = render(<MobileInputBar address={runAddress} />)
    fireEvent.change(field(), { target: { value: 'echo hi' } })
    await click(sendBtn())
    expect(field().value).toBe('')
    unmount()
    render(<MobileInputBar address={runAddress} />)
    expect(field().value).toBe('')
  })
})

describe('MobileInputBar send in flight', () => {
  let resolve!: (r: Response) => void
  const storedDraft = () => {
    const raw = localStorage.getItem('houston-draft:run:run-1')
    return raw === null ? null : (JSON.parse(raw) as { t: string }).t
  }
  beforeEach(() => {
    vi.mocked(fetch).mockReturnValue(new Promise<Response>((r) => { resolve = r }))
  })

  it('a send that succeeds after the composer remounted leaves the new one empty', async () => {
    const first = render(<MobileInputBar address={runAddress} />)
    fireEvent.change(field(), { target: { value: 'deploy prod' } })
    await click(sendBtn())
    first.unmount()

    render(<MobileInputBar address={runAddress} />)
    expect(field().value).toBe('deploy prod')

    await act(async () => resolve({ ok: true } as Response))
    expect(field().value).toBe('')
    expect(localStorage.getItem('houston-draft:run:run-1')).toBeNull()
  })

  it('keeps text typed while the send was in flight, in the field and in storage', async () => {
    render(<MobileInputBar address={runAddress} />)
    fireEvent.change(field(), { target: { value: 'hello' } })
    await click(sendBtn())
    fireEvent.change(field(), { target: { value: 'hello again' } })

    await act(async () => resolve({ ok: true } as Response))
    expect(field().value).toBe('hello again')
    expect(storedDraft()).toBe('hello again')
  })

  it('picking a file while a send is in flight reports busy and stages nothing', async () => {
    render(<MobileInputBar address={runAddress} />)
    fireEvent.change(field(), { target: { value: 'hello' } })
    await click(sendBtn())
    const input = document.querySelector('input[type="file"]') as HTMLInputElement
    fireEvent.change(input, { target: { files: [new File(['x'], 'a.png', { type: 'image/png' })] } })

    expect(screen.getByTestId('send-error').textContent).toContain('busy — pick the file again')
    expect(screen.queryByTestId('staged-image')).toBeNull()
    await act(async () => resolve({ ok: true } as Response))
  })
})

describe('MobileInputBar failed sends keep the draft', () => {
  const multi = 'line one\nline two\nline three'

  it.each([
    ['a non-ok response', () => vi.mocked(fetch).mockResolvedValue({ ok: false, status: 500 } as Response), 'HTTP 500'],
    ['a timeout', () => vi.mocked(fetch).mockRejectedValue(new DOMException('x', 'TimeoutError')), 'timed out'],
  ])('%s shows the alert and keeps the full text across a remount', async (_name, arrange, message) => {
    arrange()
    const { unmount } = render(<MobileInputBar address={runAddress} />)
    fireEvent.change(field(), { target: { value: multi } })
    await click(sendBtn())
    expect(screen.getByTestId('send-error').textContent).toContain(message)
    expect(field().value).toBe(multi)
    unmount()
    render(<MobileInputBar address={runAddress} />)
    expect(field().value).toBe(multi)
  })
})

describe('MobileInputBar staged attachment', () => {
  const pick = (name = 'a.png') => {
    const input = document.querySelector('input[type="file"]') as HTMLInputElement
    fireEvent.change(input, { target: { files: [new File(['x'], name, { type: 'image/png' })] } })
  }
  const sendWithImage = async () => {
    await act(async () => {
      fireEvent.click(sendBtn())
      await new Promise((r) => setTimeout(r, 20))
    })
  }

  it('refuses the /compact… prefill while an image is staged', async () => {
    render(<MobileInputBar address={runAddress} runAgent="claude" runState="idle" />)
    pick()
    await click(screen.getByRole('button', { name: 'Commands' }))
    await click(screen.getByRole('button', { name: '/compact…' }))
    expect((field() as HTMLTextAreaElement).value).toBe('')
    expect(screen.getByText('Clear the draft and attachment first, then tap /compact…')).toBeTruthy()
  })

  it('picking a file stages a chip and does not call fetch', () => {
    render(<MobileInputBar address={runAddress} />)
    pick()
    expect(fetch).not.toHaveBeenCalled()
    expect(screen.getByTestId('staged-image').textContent).toContain('a.png')
  })

  it('x removes the chip and a following Send is Enter-only', async () => {
    render(<MobileInputBar address={runAddress} />)
    pick()
    fireEvent.click(screen.getByRole('button', { name: 'Remove attachment' }))
    expect(screen.queryByTestId('staged-image')).toBeNull()
    await click(sendBtn())
    expect(fetch).toHaveBeenCalledTimes(1)
    expect(lastJSONRequest().body).toEqual({ type: 'key', key: 'Enter' })
  })

  it('Send posts text and image once, then clears the chip and field', async () => {
    render(<MobileInputBar address={runAddress} />)
    fireEvent.change(field(), { target: { value: 'look at this' } })
    pick()
    await sendWithImage()
    expect(fetch).toHaveBeenCalledTimes(1)
    expect(lastJSONRequest().body).toEqual({
      type: 'image',
      text: 'look at this',
      images: [{ name: 'a.png', type: 'image/png', data: 'eA==' }],
    })
    expect(screen.queryByTestId('staged-image')).toBeNull()
    expect(field().value).toBe('')
  })

  it('a failed image send keeps the chip and the text, and retry delivers', async () => {
    vi.mocked(fetch).mockResolvedValueOnce({ ok: false, status: 500 } as Response)
    render(<MobileInputBar address={runAddress} />)
    fireEvent.change(field(), { target: { value: 'look at this' } })
    pick()
    await sendWithImage()
    expect(screen.getByTestId('send-error').textContent).toContain('HTTP 500')
    expect(screen.getByTestId('staged-image')).toBeTruthy()
    expect(field().value).toBe('look at this')

    await sendWithImage()
    expect(fetch).toHaveBeenCalledTimes(2)
    expect(screen.queryByTestId('staged-image')).toBeNull()
    expect(field().value).toBe('')
  })
})

describe('composer auto-grow', () => {
  it('caps at 40% of the viewport', () => {
    expect(composerMaxHeight(800)).toBe(320)
    expect(composerMaxHeight(400)).toBe(160)
  })

  it('grows to the cap then scrolls internally', () => {
    vi.stubGlobal('visualViewport', { height: 500 })
    render(<MobileInputBar address={runAddress} />)
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
