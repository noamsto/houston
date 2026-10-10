import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { ChatTab } from './ChatTab'
import { QuestionCard } from './QuestionCard'
import type { Run } from '../api/runs'
import type { Prompt } from '../api/answer'
import type { ChatPage, ChatToolDetail, ChatUpdate } from '../api/chat'
import { installFakeEventSource } from '../testing/fakeEventSource'
import type { FakeEventSourceHandle } from '../testing/fakeEventSource'

const now = 1_800_000_000_000 // fixed ms
const REVEAL_TICK_MS = 1000 / 16

function run(p: Partial<Run> = {}): Run {
  return {
    id: 'r1', agent: 'claude', state: 'running',
    activity: {}, tokens: { input: 0, output: 0 },
    caps: { terminal: true, reply: true, kill: true, chat: true },
    updated_at: Math.floor(now / 1000),
    ...p,
  } as Run
}

function textUpdate(id: string, seq: number, text: string, extra: Partial<ChatUpdate> = {}): ChatUpdate {
  return {
    id, seq, ts: seq * 1000, sessionUpdate: 'agent_message_chunk',
    content: [{ type: 'content', content: { type: 'text', text } }],
    ...extra,
  }
}

function userChunk(id: string, seq: number, text: string, extra: Partial<ChatUpdate> = {}): ChatUpdate {
  return { ...textUpdate(id, seq, text, extra), sessionUpdate: 'user_message_chunk' }
}

function toolCall(id: string, seq: number, tool: string, extra: Partial<ChatUpdate> = {}): ChatUpdate {
  return { id, seq, ts: seq * 1000, sessionUpdate: 'tool_call', toolCallId: id, status: 'completed', _meta: { tool }, ...extra }
}

function page(epoch: string, updates: ChatUpdate[], more = false): ChatPage {
  return { epoch, updates, more }
}

function jsonResponse(body: unknown): Response {
  return { status: 200, ok: true, json: async () => body, text: async () => JSON.stringify(body) } as Response
}

function okResponse(): Response {
  return { status: 204, ok: true, json: async () => ({}), text: async () => '' } as Response
}

interface FetchOpts {
  page: ChatPage
  earlier?: ChatPage
  tool?: ChatToolDetail
  toolStatus?: number
  answer?: { status: number; body?: string }
  prompt?: Prompt
}

function installFetch(opts: FetchOpts): ReturnType<typeof vi.fn> {
  const mock = vi.fn((url: string) => {
    if (url.includes('/chat/tool/')) {
      if (opts.toolStatus) return Promise.resolve({ status: opts.toolStatus, ok: false, text: async () => 'boom' } as Response)
      return Promise.resolve(jsonResponse(opts.tool ?? {}))
    }
    if (url.includes('before=')) return Promise.resolve(jsonResponse(opts.earlier ?? opts.page))
    if (url.includes('/chat')) return Promise.resolve(jsonResponse(opts.page))
    if (url.includes('/input')) return Promise.resolve(okResponse())
    if (url.includes('/prompt')) {
      return Promise.resolve(opts.prompt ? jsonResponse(opts.prompt) : ({ status: 404, ok: false, text: async () => '' } as Response))
    }
    if (url.includes('/answer')) {
      const { status, body = '' } = opts.answer ?? { status: 204 }
      return Promise.resolve({
        status, ok: status < 300, text: async () => body, json: async () => JSON.parse(body),
      } as Response)
    }
    throw new Error(`unexpected fetch: ${url}`)
  })
  vi.stubGlobal('fetch', mock)
  return mock
}

function mockMatchMedia(matches: boolean) {
  window.matchMedia = vi.fn().mockImplementation((query: string) => ({
    matches,
    media: query,
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
  })) as unknown as typeof window.matchMedia
}

let fake: FakeEventSourceHandle

beforeEach(() => {
  localStorage.clear()
  fake = installFakeEventSource()
  mockMatchMedia(false)
})

afterEach(() => {
  cleanup()
  fake.uninstall()
  vi.unstubAllGlobals()
  vi.useRealTimers()
})

async function renderReady(p: Partial<Run>, opts: FetchOpts) {
  const fetchMock = installFetch(opts)
  const r = run(p)
  const view = render(<ChatTab run={r} now={now} />)
  await waitFor(() => expect(screen.queryByText(/loading chat/i)).toBeNull())
  return { ...view, fetchMock, run: r }
}

describe('ChatTab', () => {
  it('renders a user bubble', async () => {
    const { container } = await renderReady({}, { page: page('e1', [userChunk('u1', 1, 'hello there')]) })
    const bubble = container.querySelector('[data-id="u1"]')!
    expect(bubble.className).toContain('chat-user')
    expect(bubble.textContent).toBe('hello there')
  })

  it('renders assistant text as markdown', async () => {
    const { container } = await renderReady({}, { page: page('e1', [textUpdate('a1', 1, 'a **note**\n\n- one\n- two')]) })
    const bubble = container.querySelector('[data-id="a1"]')!
    await waitFor(() => expect(bubble.querySelectorAll('li')).toHaveLength(2))
  })

  it('applies the commentary class to a dimmer assistant chunk', async () => {
    const { container } = await renderReady({}, {
      page: page('e1', [textUpdate('a1', 1, 'about to look', { _meta: { phase: 'commentary' } })]),
    })
    expect(container.querySelector('[data-id="a1"]')!.className).toContain('commentary')
  })

  it('renders a task-notification chunk as a centred divider', async () => {
    const { container } = await renderReady({}, {
      page: page('e1', [userChunk('d1', 1, 'ignored', { _meta: { origin: 'task-notification' } })]),
    })
    const el = container.querySelector('[data-id="d1"]')!
    expect(el.className).toContain('chat-divider')
    expect(el.textContent).toMatch(/background task finished/i)
  })

  it('renders a pi-compaction divider label', async () => {
    const { container } = await renderReady({}, {
      page: page('e1', [userChunk('d1', 1, '', { _meta: { origin: 'pi-compaction' } })]),
    })
    expect(container.querySelector('[data-id="d1"]')!.textContent).toMatch(/context compacted/i)
    expect(screen.queryByRole('button', { name: /context compacted/i })).toBeNull()
  })

  it('toggles a pi-branch divider summary', async () => {
    await renderReady({}, {
      page: page('e1', [userChunk('d1', 1, 'abandoned branch gist', { _meta: { origin: 'pi-branch' } })]),
    })
    const toggle = screen.getByRole('button', { name: /switched branch/i })
    expect(toggle.getAttribute('aria-expanded')).toBe('false')
    expect(screen.queryByText('abandoned branch gist')).toBeNull()

    fireEvent.click(toggle)
    expect(toggle.getAttribute('aria-expanded')).toBe('true')
    expect(await screen.findByText('abandoned branch gist')).toBeTruthy()

    fireEvent.click(toggle)
    expect(toggle.getAttribute('aria-expanded')).toBe('false')
    expect(screen.queryByText('abandoned branch gist')).toBeNull()
  })

  it('collapses consecutive tool calls into one row and expands to per-call rows', async () => {
    const { container } = await renderReady({}, {
      page: page('e1', [
        toolCall('t1', 1, 'Read'),
        toolCall('t2', 2, 'Read'),
        toolCall('t3', 3, 'Edit'),
      ]),
    })

    const toggle = screen.getByRole('button', { name: 'Read ×2 · Edit' })
    expect(toggle.getAttribute('aria-expanded')).toBe('false')

    fireEvent.click(toggle)
    expect(toggle.getAttribute('aria-expanded')).toBe('true')

    const rows = container.querySelectorAll('.chat-tool-call-row')
    expect(rows).toHaveLength(3)
    expect(Array.from(rows).map((r) => r.querySelector('.chat-tool-name')?.textContent)).toEqual(['Read', 'Read', 'Edit'])
  })

  it('prefixes tool rows with aria-hidden kind glyphs and marks a failed call with ✗', async () => {
    const { container } = await renderReady({}, {
      page: page('e1', [
        toolCall('t1', 1, 'Read', { kind: 'read' }),
        toolCall('t2', 2, 'Read', { kind: 'read' }),
        toolCall('t3', 3, 'Bash', { kind: 'execute', status: 'failed' }),
        toolCall('t4', 4, 'Mystery', { kind: 'switch_mode', status: 'in_progress' }),
      ]),
    })

    const toggle = screen.getByRole('button', { name: 'Read ×2 · Bash · Mystery' })
    expect(toggle.textContent).toBe('▤Read ×2 · ❯Bash✗ · ◇Mystery')
    const hidden = Array.from(toggle.querySelectorAll('[aria-hidden="true"]')).map((el) => el.textContent)
    expect(hidden).toEqual(['▤', '❯', '✗', '◇'])

    fireEvent.click(toggle)
    const calls = within(container.querySelector('.chat-tools-calls')!)
    const failedRow = calls.getByRole('button', { name: 'Bash failed' })
    expect(failedRow.querySelector('.chat-tool-kind')?.textContent).toBe('❯')
    const status = failedRow.querySelector('.chat-tool-status')!
    expect(status.getAttribute('data-status')).toBe('failed')
    expect(status.querySelector('[aria-hidden="true"]')?.textContent).toBe('✗')
    expect(calls.getAllByRole('button', { name: 'Read completed' })[0].querySelector('.chat-tool-status [aria-hidden="true"]')?.textContent).toBe('✓')
    expect(calls.getByRole('button', { name: 'Mystery in_progress' }).querySelector('.chat-tool-status [aria-hidden="true"]')?.textContent).toBe('◌')
  })

  it('gives the subagent tag, divider and working line aria-hidden glyphs', async () => {
    const { container } = await renderReady({ state: 'running' }, {
      page: page('e1', [
        userChunk('d1', 1, 'ignored', { _meta: { origin: 'task-notification' } }),
        toolCall('t1', 2, 'Task', { kind: 'think', _meta: { tool: 'Task', subagent: 'explorer' } }),
      ]),
    })

    const divider = container.querySelector('[data-id="d1"]')!
    expect(divider.querySelector('[aria-hidden="true"]')?.textContent).toBe('✦')

    fireEvent.click(screen.getByRole('button', { name: 'Task' }))
    const row = within(container.querySelector('.chat-tools-calls')!).getByRole('button', { name: 'Task completed subagent' })
    expect(row.querySelector('.chat-tool-subagent [aria-hidden="true"]')?.textContent).toBe('⑂')

    const working = container.querySelector('.chat-working')!
    expect(working.querySelector('.chat-working-dot')?.getAttribute('aria-hidden')).toBe('true')
    expect(working.textContent).toBe('●working…')
  })

  it('fetches and shows a diff when an edit call is tapped', async () => {
    const { container, fetchMock } = await renderReady({}, {
      page: page('e1', [toolCall('t1', 1, 'Edit')]),
      tool: {
        toolCallId: 't1', name: 'edit',
        diff: { path: 'a.ts', oldText: 'foo\nbar', newText: 'foo\nbaz' },
      },
    })

    fireEvent.click(screen.getByRole('button', { name: 'Edit' }))
    const row = within(container.querySelector('.chat-tools-calls')!).getByRole('button', { name: /Edit/ })
    fireEvent.click(row)

    await waitFor(() => expect(container.querySelector('.chat-diff')).toBeTruthy())
    expect(container.querySelector('.chat-diff')!.textContent).toBe('- foo\n- bar\n+ foo\n+ baz')
    expect(fetchMock).toHaveBeenCalledWith(expect.stringContaining('/chat/tool/t1'))
  })

  async function expandTool(container: HTMLElement, tool: string) {
    fireEvent.click(screen.getByRole('button', { name: tool }))
    fireEvent.click(within(container.querySelector('.chat-tools-calls')!).getByRole('button', { name: `${tool} completed` }))
  }

  it('labels a truncated output but not a kept input', async () => {
    const { container } = await renderReady({}, {
      page: page('e1', [toolCall('t1', 1, 'Write')]),
      tool: { toolCallId: 't1', name: 'write', output: 'cut here', truncated: true },
    })
    await expandTool(container, 'Write')

    await waitFor(() => expect(container.querySelector('.chat-tool-truncated')).toBeTruthy())
    expect(container.querySelector('.chat-tool-truncated')!.textContent).toBe('(truncated)')
    expect(container.querySelector('.chat-tool-input-omitted')).toBeNull()
  })

  it('notes an omitted input but does not label it truncated', async () => {
    const { container } = await renderReady({}, {
      page: page('e1', [toolCall('t1', 1, 'Write')]),
      tool: { toolCallId: 't1', name: 'write', output: 'short', inputOmitted: true },
    })
    await expandTool(container, 'Write')

    await waitFor(() => expect(container.querySelector('.chat-tool-input-omitted')).toBeTruthy())
    expect(container.querySelector('.chat-tool-input-omitted')!.textContent).toBe('input omitted (over 16 KiB)')
    expect(container.querySelector('.chat-tool-truncated')).toBeNull()
  })

  it('shows both labels, each in its own place, when both flags are set', async () => {
    const { container } = await renderReady({}, {
      page: page('e1', [toolCall('t1', 1, 'Write')]),
      tool: { toolCallId: 't1', name: 'write', output: 'cut here', truncated: true, inputOmitted: true },
    })
    await expandTool(container, 'Write')

    await waitFor(() => expect(container.querySelector('.chat-tool-truncated')).toBeTruthy())
    const detail = container.querySelector('.chat-tool-detail')!
    expect(Array.from(detail.children).map((el) => el.className)).toEqual([
      'chat-tool-input-omitted', 'chat-tool-output', 'chat-tool-truncated',
    ])
  })

  it('shows "Couldn\'t load tool detail" when the tool fetch fails', async () => {
    const fetchMock = installFetch({ page: page('e1', [toolCall('t1', 1, 'Edit')]) })
    fetchMock.mockImplementation((url: string) => {
      if (url.includes('/chat/tool/')) return Promise.resolve({ status: 404, ok: false, json: async () => ({}), text: async () => 'no' } as Response)
      if (url.includes('/chat')) return Promise.resolve(jsonResponse(page('e1', [toolCall('t1', 1, 'Edit')])))
      return Promise.resolve(okResponse())
    })
    const r = run()
    const { container } = render(<ChatTab run={r} now={now} />)
    await waitFor(() => expect(screen.queryByText(/loading chat/i)).toBeNull())

    fireEvent.click(screen.getByRole('button', { name: 'Edit' }))
    fireEvent.click(within(container.querySelector('.chat-tools-calls')!).getByRole('button', { name: /Edit/ }))

    await waitFor(() => expect(screen.getByText(/Couldn't load tool detail/i)).toBeTruthy())
  })

  it('Show earlier loads the previous page with before=<oldest seq>', async () => {
    const { fetchMock } = await renderReady({}, {
      page: page('e1', [userChunk('u5', 5, 'five'), userChunk('u6', 6, 'six')], true),
      earlier: page('e1', [userChunk('u3', 3, 'three'), userChunk('u4', 4, 'four')], false),
    })

    fireEvent.click(screen.getByRole('button', { name: /show earlier/i }))

    await waitFor(() => expect(screen.getByText('three')).toBeTruthy())
    const calledWithBefore = fetchMock.mock.calls.some((call: unknown[]) => typeof call[0] === 'string' && call[0].includes('before=5'))
    expect(calledWithBefore).toBe(true)
  })

  it('composer Send posts input and shows an optimistic bubble that reconciles on the matching chunk', async () => {
    const { fetchMock } = await renderReady({}, { page: page('e1', []) })

    const textarea = screen.getByPlaceholderText('Message…') as HTMLTextAreaElement
    fireEvent.change(textarea, { target: { value: 'ping the server' } })
    fireEvent.click(screen.getByRole('button', { name: 'Send' }))

    expect(document.querySelector('.chat-optimistic')?.textContent).toBe('ping the server')
    await waitFor(() => expect(textarea.value).toBe(''))
    const [url, init] = fetchMock.mock.calls[fetchMock.mock.calls.length - 1] as [string, RequestInit?]
    expect(url).toBe('/api/runs/r1/input')
    expect(JSON.parse(String(init?.body))).toEqual({ type: 'text', text: 'ping the server' })

    const es = fake.instances[0]
    act(() => {
      es.emit('updates', [userChunk('confirmed', 100, 'ping the server', { ts: Date.now() })])
    })

    await waitFor(() => expect(document.querySelector('.chat-optimistic')).toBeNull())
    expect(screen.getByText('ping the server')).toBeTruthy()
  })

  it('a message queued behind a long turn is flagged not confirmed, then reconciles when its chunk lands late', async () => {
    await renderReady({}, { page: page('e1', []) })
    const es = fake.instances[0]

    vi.useFakeTimers()
    try {
      const textarea = screen.getByPlaceholderText('Message…') as HTMLTextAreaElement
      fireEvent.change(textarea, { target: { value: 'queued message' } })
      fireEvent.click(screen.getByRole('button', { name: 'Send' }))
      await act(async () => {})
      expect(document.querySelector('.chat-optimistic')?.textContent).toBe('queued message')

      act(() => { vi.advanceTimersByTime(35_000) })
      expect(document.querySelector('.chat-optimistic-flag')?.textContent).toBe('not confirmed')

      act(() => { vi.advanceTimersByTime(25_000) })
      act(() => {
        es.emit('updates', [userChunk('late', 100, 'queued message', { ts: Date.now() })])
      })

      expect(document.querySelector('.chat-optimistic')).toBeNull()
      expect(screen.getByText('queued message')).toBeTruthy()
    } finally {
      vi.useRealTimers()
    }
  })

  it('a failed Send keeps the text, shows the error, and leaves no optimistic bubble', async () => {
    const { fetchMock } = await renderReady({}, { page: page('e1', []) })
    fetchMock.mockImplementation((url: string) => {
      if (url.includes('/input')) return Promise.resolve({ status: 500, ok: false, json: async () => ({}), text: async () => 'boom' } as Response)
      return Promise.resolve(jsonResponse(page('e1', [])))
    })

    const textarea = screen.getByPlaceholderText('Message…') as HTMLTextAreaElement
    fireEvent.change(textarea, { target: { value: 'ping the server' } })
    fireEvent.click(screen.getByRole('button', { name: 'Send' }))

    await waitFor(() => expect(screen.getByRole('alert').textContent).toBe('HTTP 500'))
    expect(textarea.value).toBe('ping the server')
    expect(document.querySelector('.chat-optimistic')).toBeNull()
  })

  describe('durable drafts and staged attachments', () => {
    const textarea = () => screen.getByPlaceholderText('Message…') as HTMLTextAreaElement
    const input = () => document.querySelector('input[type=file]') as HTMLInputElement
    const png = () => new File(['x'], 'shot.png', { type: 'image/png' })
    const pick = () => fireEvent.change(input(), { target: { files: [png()] } })
    const storedDraft = () => {
      const raw = localStorage.getItem('houston-draft:chat:r1')
      return raw === null ? null : (JSON.parse(raw) as { t: string }).t
    }
    const inputCalls = (fetchMock: ReturnType<typeof vi.fn>) =>
      fetchMock.mock.calls.filter((c: unknown[]) => String(c[0]).includes('/input')) as [string, RequestInit][]

    it('restores the typed draft after the tab remounts for the same run, and not for another run', async () => {
      const first = await renderReady({}, { page: page('e1', []) })
      fireEvent.change(textarea(), { target: { value: 'half-typed' } })
      first.unmount()

      render(<ChatTab run={run()} now={now} />)
      await waitFor(() => expect(textarea().value).toBe('half-typed'))
      cleanup()

      render(<ChatTab run={run({ id: 'r2' })} now={now} />)
      await waitFor(() => expect(textarea().value).toBe(''))
    })

    it('does not restore a draft written for another session on the same run id', async () => {
      const first = await renderReady({ draft_key: 'sess-a' }, { page: page('e1', []) })
      fireEvent.change(textarea(), { target: { value: 'for agent A' } })
      first.unmount()

      render(<ChatTab run={run({ draft_key: 'sess-b' })} now={now} />)
      await waitFor(() => expect(textarea().value).toBe(''))
      cleanup()

      render(<ChatTab run={run({ draft_key: 'sess-a' })} now={now} />)
      await waitFor(() => expect(textarea().value).toBe('for agent A'))
    })

    it('a draft_key change on a mounted chat tab does not carry the text over', async () => {
      const view = await renderReady({ draft_key: 'sess-a' }, { page: page('e1', []) })
      fireEvent.change(textarea(), { target: { value: 'for agent A' } })

      view.rerender(<ChatTab run={run({ draft_key: 'sess-b' })} now={now} />)
      await waitFor(() => expect(textarea().value).toBe(''))
      fireEvent.change(textarea(), { target: { value: 'x' } })
      expect(JSON.parse(localStorage.getItem('houston-draft:chat:sess-b') ?? '{}').t).toBe('x')
      expect(JSON.parse(localStorage.getItem('houston-draft:chat:sess-a') ?? '{}').t).toBe('for agent A')
    })

    it('drops the stored draft after a successful send', async () => {
      await renderReady({}, { page: page('e1', []) })
      fireEvent.change(textarea(), { target: { value: 'ping the server' } })
      expect(storedDraft()).toBe('ping the server')
      fireEvent.click(screen.getByRole('button', { name: 'Send' }))
      await waitFor(() => expect(textarea().value).toBe(''))
      expect(storedDraft()).toBeNull()
    })

    it('a timed-out send keeps the full multi-line text, stored draft and no optimistic bubble', async () => {
      const { fetchMock } = await renderReady({}, { page: page('e1', []) })
      fetchMock.mockImplementation((url: string) => {
        if (url.includes('/input')) return Promise.reject(new DOMException('x', 'TimeoutError'))
        return Promise.resolve(jsonResponse(page('e1', [])))
      })
      const body = 'line one\nline two\n\nline four'
      fireEvent.change(textarea(), { target: { value: body } })
      fireEvent.click(screen.getByRole('button', { name: 'Send' }))

      await waitFor(() =>
        expect(screen.getByRole('alert').textContent).toBe(
          'timed out — the text may have been partly sent; check the agent before resending',
        ),
      )
      expect(textarea().value).toBe(body)
      expect(document.querySelector('.chat-optimistic')).toBeNull()
      expect(storedDraft()).toBe(body)
    })

    it('picking a file only stages it: no /input request, chip shown, × removes it', async () => {
      const { fetchMock } = await renderReady({}, { page: page('e1', []) })
      pick()
      await waitFor(() => expect(screen.getByTestId('staged-image').textContent).toContain('shot.png'))
      expect(inputCalls(fetchMock)).toHaveLength(0)

      fireEvent.click(screen.getByRole('button', { name: 'Remove attachment' }))
      expect(screen.queryByTestId('staged-image')).toBeNull()
    })

    it('Send with a chip and text posts the image, then clears the chip and the field', async () => {
      const { fetchMock } = await renderReady({}, { page: page('e1', []) })
      fireEvent.change(textarea(), { target: { value: 'look at this' } })
      pick()
      fireEvent.click(screen.getByRole('button', { name: 'Send' }))

      await waitFor(() => expect(screen.queryByTestId('staged-image')).toBeNull())
      expect(textarea().value).toBe('')
      const calls = inputCalls(fetchMock)
      expect(calls).toHaveLength(1)
      expect(JSON.parse(String(calls[0][1].body))).toEqual({
        type: 'image',
        text: 'look at this',
        images: [{ name: 'shot.png', type: 'image/png', data: 'eA==' }],
      })
      expect(document.querySelector('.chat-optimistic')).toBeNull()
    })

    it('Send with only a chip (no text) is allowed', async () => {
      const { fetchMock } = await renderReady({}, { page: page('e1', []) })
      pick()
      fireEvent.click(screen.getByRole('button', { name: 'Send' }))
      await waitFor(() => expect(screen.queryByTestId('staged-image')).toBeNull())
      expect(JSON.parse(String(inputCalls(fetchMock)[0][1].body))).toMatchObject({ type: 'image', text: '' })
    })

    it('a failed image send keeps the chip and the text', async () => {
      const { fetchMock } = await renderReady({}, { page: page('e1', []) })
      fetchMock.mockImplementation((url: string) => {
        if (url.includes('/input')) return Promise.resolve({ status: 500, ok: false, json: async () => ({}), text: async () => '' } as Response)
        return Promise.resolve(jsonResponse(page('e1', [])))
      })
      fireEvent.change(textarea(), { target: { value: 'look at this' } })
      pick()
      fireEvent.click(screen.getByRole('button', { name: 'Send' }))

      await waitFor(() => expect(screen.getByRole('alert').textContent).toBe('HTTP 500'))
      expect(screen.getByTestId('staged-image')).toBeTruthy()
      expect(textarea().value).toBe('look at this')
    })
  })

  it('a failed Esc shows the error inline', async () => {
    const { fetchMock } = await renderReady({}, { page: page('e1', []) })
    fetchMock.mockImplementation((url: string) => {
      if (url.includes('/input')) return Promise.resolve({ status: 401, ok: false, json: async () => ({}), text: async () => '' } as Response)
      return Promise.resolve(jsonResponse(page('e1', [])))
    })

    fireEvent.click(screen.getByRole('button', { name: 'Esc' }))
    await waitFor(() => expect(screen.getByRole('alert').textContent).toBe('session expired — reload'))
  })

  describe('overlapping composer actions', () => {
    function deferredInput() {
      const pending: { body: { type: string }; resolve: (r: Response) => void }[] = []
      return {
        pending,
        impl: (url: string, init?: RequestInit) => {
          if (!url.includes('/input')) return Promise.resolve(jsonResponse(page('e1', [])))
          return new Promise<Response>((resolve) => pending.push({ body: JSON.parse(String(init?.body)), resolve }))
        },
      }
    }
    const fail = (status: number) => ({ status, ok: false, json: async () => ({}), text: async () => '' }) as Response
    const sendBtn = () => screen.getByRole('button', { name: 'Send' }) as HTMLButtonElement
    const attachBtn = () => screen.getByRole('button', { name: 'Attach image' }) as HTMLButtonElement

    async function startSendThenEsc() {
      const { fetchMock } = await renderReady({}, { page: page('e1', []) })
      const d = deferredInput()
      fetchMock.mockImplementation(d.impl)
      fireEvent.change(screen.getByPlaceholderText('Message…'), { target: { value: 'hello' } })
      fireEvent.click(sendBtn())
      fireEvent.click(screen.getByRole('button', { name: 'Esc' }))
      await waitFor(() => expect(d.pending).toHaveLength(2))
      return d
    }

    it('keeps Send and Attach disabled when Esc settles before the Send does', async () => {
      const d = await startSendThenEsc()
      expect(sendBtn().disabled).toBe(true)
      expect(attachBtn().disabled).toBe(true)

      await act(async () => d.pending[1].resolve(okResponse()))
      expect(sendBtn().disabled).toBe(true)
      expect(attachBtn().disabled).toBe(true)

      await act(async () => d.pending[0].resolve(okResponse()))
      expect(sendBtn().disabled).toBe(false)
      expect(attachBtn().disabled).toBe(false)
    })

    it('does not block Esc while a Send is in flight', async () => {
      const d = await startSendThenEsc()
      expect((screen.getByRole('button', { name: 'Esc' }) as HTMLButtonElement).disabled).toBe(false)
      await act(async () => d.pending.forEach((p) => p.resolve(okResponse())))
    })

    it('two synchronous Sends post only once', async () => {
      const { fetchMock } = await renderReady({}, { page: page('e1', []) })
      const d = deferredInput()
      fetchMock.mockImplementation(d.impl)
      fireEvent.change(screen.getByPlaceholderText('Message…'), { target: { value: 'hello' } })
      fireEvent.click(sendBtn())
      fireEvent.keyDown(screen.getByPlaceholderText('Message…'), { key: 'Enter', ctrlKey: true })
      fireEvent.keyDown(screen.getByPlaceholderText('Message…'), { key: 'Enter', ctrlKey: true })
      await waitFor(() => expect(d.pending).toHaveLength(1))
      await act(async () => d.pending[0].resolve(okResponse()))
      expect(d.pending).toHaveLength(1)
    })

    function stubFileReader(mode: 'ok' | 'error') {
      let finish: () => void = () => {}
      const spy = vi.spyOn(FileReader.prototype, 'readAsDataURL').mockImplementation(function (this: FileReader) {
        finish = () => {
          if (mode === 'error') {
            this.onerror?.(new ProgressEvent('error') as ProgressEvent<FileReader>)
            return
          }
          Object.defineProperty(this, 'result', { value: 'data:image/png;base64,AAAA' })
          this.onload?.(new ProgressEvent('load') as ProgressEvent<FileReader>)
        }
      })
      return { finish: () => finish(), restore: () => spy.mockRestore() }
    }
    const pickFile = () => {
      const input = document.querySelector('input[type=file]') as HTMLInputElement
      fireEvent.change(input, { target: { files: [new File(['x'], 'a.png', { type: 'image/png' })] } })
    }

    it('a Send with a staged image holds the gate while the file is read, so another Send cannot overlap or drop it', async () => {
      const { fetchMock } = await renderReady({}, { page: page('e1', []) })
      const d = deferredInput()
      fetchMock.mockImplementation(d.impl)
      const reader = stubFileReader('ok')
      try {
        const textarea = screen.getByPlaceholderText('Message…') as HTMLTextAreaElement
        fireEvent.change(textarea, { target: { value: 'hello' } })
        pickFile()
        fireEvent.click(sendBtn())
        await waitFor(() => expect(sendBtn().disabled).toBe(true))
        fireEvent.keyDown(textarea, { key: 'Enter', ctrlKey: true })
        expect(d.pending).toHaveLength(0)
        await act(async () => reader.finish())
        await waitFor(() => expect(d.pending).toHaveLength(1))
        expect(d.pending[0].body).toMatchObject({ type: 'image', text: 'hello' })
        await act(async () => d.pending[0].resolve(okResponse()))
        expect(textarea.value).toBe('')
      } finally {
        reader.restore()
      }
    })

    it('a failed file read shows an error, keeps the chip and releases the gate', async () => {
      await renderReady({}, { page: page('e1', []) })
      const reader = stubFileReader('error')
      try {
        pickFile()
        fireEvent.click(sendBtn())
        await waitFor(() => expect(sendBtn().disabled).toBe(true))
        await act(async () => reader.finish())
        expect(screen.getByRole('alert').textContent).toBe('could not read file')
        expect(screen.getByTestId('staged-image')).toBeTruthy()
        expect(sendBtn().disabled).toBe(false)
      } finally {
        reader.restore()
      }
    })

    it('keeps text typed while a Send was in flight', async () => {
      const { fetchMock } = await renderReady({}, { page: page('e1', []) })
      const d = deferredInput()
      fetchMock.mockImplementation(d.impl)
      const textarea = screen.getByPlaceholderText('Message…') as HTMLTextAreaElement
      fireEvent.change(textarea, { target: { value: 'hello' } })
      fireEvent.click(sendBtn())
      await waitFor(() => expect(d.pending).toHaveLength(1))
      fireEvent.change(textarea, { target: { value: 'hello again' } })
      await act(async () => d.pending[0].resolve(okResponse()))
      expect(textarea.value).toBe('hello again')
    })

    it('a send that succeeds after the composer remounted does not leave the sent text in the new one', async () => {
      const first = await renderReady({}, { page: page('e1', []) })
      const d = deferredInput()
      first.fetchMock.mockImplementation(d.impl)
      fireEvent.change(screen.getByPlaceholderText('Message…'), { target: { value: 'deploy prod' } })
      fireEvent.click(sendBtn())
      await waitFor(() => expect(d.pending).toHaveLength(1))
      first.unmount()

      render(<ChatTab run={run()} now={now} />)
      const textarea = await screen.findByPlaceholderText('Message…') as HTMLTextAreaElement
      expect(textarea.value).toBe('deploy prod')

      await act(async () => d.pending[0].resolve(okResponse()))
      expect(textarea.value).toBe('')
      expect(localStorage.getItem('houston-draft:chat:r1')).toBeNull()
    })

    it('picking a file after a Send started (before the button disabled) reports busy', async () => {
      const { fetchMock } = await renderReady({}, { page: page('e1', []) })
      const d = deferredInput()
      fetchMock.mockImplementation(d.impl)
      fireEvent.change(screen.getByPlaceholderText('Message…'), { target: { value: 'hello' } })
      fireEvent.click(sendBtn())
      await waitFor(() => expect(d.pending).toHaveLength(1))
      pickFile()
      expect(screen.getByRole('alert').textContent).toBe('busy — pick the file again')
      expect(d.pending).toHaveLength(1)
      await act(async () => d.pending[0].resolve(okResponse()))
    })

    it('a later successful settle does not clear an earlier-settling failure', async () => {
      const d = await startSendThenEsc()
      await act(async () => d.pending[1].resolve(fail(500)))
      expect(screen.getByRole('alert').textContent).toBe('HTTP 500')
      await act(async () => d.pending[0].resolve(okResponse()))
      expect(screen.getByRole('alert').textContent).toBe('HTTP 500')
    })

    it('the last failure to settle wins', async () => {
      const d = await startSendThenEsc()
      await act(async () => d.pending[1].resolve(fail(500)))
      await act(async () => d.pending[0].resolve(fail(401)))
      expect(screen.getByRole('alert').textContent).toBe('session expired — reload')
    })
  })

  it('Esc chip sends the Escape key', async () => {
    const { fetchMock } = await renderReady({}, { page: page('e1', []) })
    fireEvent.click(screen.getByRole('button', { name: 'Esc' }))

    const [url, init] = fetchMock.mock.calls[fetchMock.mock.calls.length - 1] as [string, RequestInit?]
    expect(url).toBe('/api/runs/r1/input')
    expect(JSON.parse(String(init?.body))).toEqual({ type: 'key', key: 'Escape' })
  })

  it('disables the composer with a reason when caps.terminal is false', async () => {
    await renderReady({ caps: { terminal: false, reply: true, kill: true, chat: true } }, { page: page('e1', []) })

    expect(screen.getByText('No live terminal for this run')).toBeTruthy()
    expect((screen.getByPlaceholderText('Message…') as HTMLTextAreaElement).disabled).toBe(true)
    expect((screen.getByRole('button', { name: 'Esc' }) as HTMLButtonElement).disabled).toBe(true)
    expect((screen.getByRole('button', { name: 'Send' }) as HTMLButtonElement).disabled).toBe(true)
  })

  it('shows the status strip and a crew question both while loading and once loaded', async () => {
    installFetch({ page: page('e1', []) })
    const r = run({ state: 'blocked', question: { text: 'Ship it?', via: 'crew' } })
    render(<ChatTab run={r} now={now} />)

    expect(screen.getByText(/loading chat/i)).toBeTruthy()
    expect(screen.getByText('needs you')).toBeTruthy()
    expect(screen.getByText('Ship it?')).toBeTruthy()

    await waitFor(() => expect(screen.queryByText(/loading chat/i)).toBeNull())
    expect(screen.getByText('needs you')).toBeTruthy()
    expect(screen.getByText('Ship it?')).toBeTruthy()
  })

  it('shows a provisional tool row from run.activity while running, gone once the matching tool_call arrives', async () => {
    const { container } = await renderReady(
      { state: 'running', activity: { tool: 'Edit', hint: 'foo.ts' } },
      { page: page('e1', []) },
    )

    expect(container.querySelector('.chat-provisional')).toBeTruthy()
    expect(container.querySelector('.chat-provisional')!.textContent).toContain('Edit')

    const es = fake.instances[0]
    act(() => {
      es.emit('updates', [toolCall('t1', 1, 'Edit')])
    })

    await waitFor(() => expect(container.querySelector('.chat-provisional')).toBeNull())
  })

  it('typewriter: a live chunk is partially revealed mid-way and complete by 1s', async () => {
    const { container } = await renderReady({}, { page: page('e1', []) })
    const es = fake.instances[0]
    const full = 'a fairly long streamed sentence being revealed live'

    vi.useFakeTimers()
    try {
      act(() => {
        es.emit('updates', [textUpdate('live1', 1, full)])
      })

      act(() => { vi.advanceTimersByTime(500) })
      const mid = container.querySelector('[data-id="live1"]')!.textContent!
      expect(mid.length).toBeGreaterThan(0)
      expect(mid.length).toBeLessThan(full.length)

      act(() => { vi.advanceTimersByTime(600) })
      expect(container.querySelector('[data-id="live1"]')!.textContent).toBe(full)
    } finally {
      vi.useRealTimers()
    }
  })

  it('typewriter: a live item that grows mid-reveal keeps its progress and finishes within ~1s of the growth', async () => {
    const { container } = await renderReady({}, { page: page('e1', []) })
    const es = fake.instances[0]
    const first = 'the first streamed paragraph of this reply'
    const second = 'a second paragraph folded into the same message'

    vi.useFakeTimers()
    try {
      act(() => {
        es.emit('updates', [textUpdate('a1', 1, first, { _meta: { messageId: 'm1' } })])
      })
      act(() => { vi.advanceTimersByTime(500) })
      const mid = container.querySelector('[data-id="a1"]')!.textContent!
      expect(mid.length).toBeGreaterThan(0)

      act(() => {
        es.emit('updates', [textUpdate('a2', 2, second, { _meta: { messageId: 'm1' } })])
      })
      act(() => { vi.advanceTimersByTime(REVEAL_TICK_MS) })
      const afterGrowth = container.querySelector('[data-id="a1"]')!.textContent!
      expect(afterGrowth.startsWith(mid)).toBe(true)
      expect(afterGrowth.length).toBeGreaterThan(mid.length)

      act(() => { vi.advanceTimersByTime(1100) })
      vi.useRealTimers()
      await waitFor(() => expect(container.querySelectorAll('[data-id="a1"] p')).toHaveLength(2))
      const paragraphs = container.querySelectorAll('[data-id="a1"] p')
      expect(Array.from(paragraphs).map((p) => p.textContent)).toEqual([first, second])
    } finally {
      vi.useRealTimers()
    }
  })

  it('a page-loaded chunk renders complete immediately, no reveal', async () => {
    const full = 'a chunk that arrived on the initial page load'
    const { container } = await renderReady({}, { page: page('e1', [textUpdate('page1', 1, full)]) })
    expect(container.querySelector('[data-id="page1"]')!.textContent).toBe(full)
  })

  it('prefers-reduced-motion skips the reveal for a live chunk', async () => {
    mockMatchMedia(true)
    const { container } = await renderReady({}, { page: page('e1', []) })
    const es = fake.instances[0]
    const full = 'a live chunk that should render whole under reduced motion'

    act(() => {
      es.emit('updates', [textUpdate('live1', 1, full)])
    })

    expect(container.querySelector('[data-id="live1"]')!.textContent).toBe(full)
  })

  describe('AskUserQuestion', () => {
    const input = {
      questions: [
        {
          header: 'Scope', question: 'Which scope?', multiSelect: false,
          options: [{ label: 'A', description: 'Small' }, { label: 'Z', description: 'Large' }],
        },
        {
          header: 'Extras', question: 'Which extras?', multiSelect: true,
          options: [{ label: 'B', description: 'Bee' }, { label: 'C', description: 'Sea' }, { label: 'D' }],
        },
      ],
    }
    const askCall = (extra: Partial<ChatUpdate> = {}) =>
      toolCall('q1', 1, 'AskUserQuestion', { title: 'Scope', ...extra })

    it('renders the questions and highlights the chosen answers once completed', async () => {
      const { container } = await renderReady({}, {
        page: page('e1', [askCall()]),
        tool: {
          toolCallId: 'q1', name: 'AskUserQuestion', input,
          output: 'User has answered your questions: "Which scope?"="A", "Which extras?"="B, C". You can now continue with the user\'s answers in mind.',
        },
      })
      await waitFor(() => expect(container.querySelectorAll('.chat-question-block')).toHaveLength(2))
      expect(screen.getByText('Which scope?')).toBeTruthy()
      expect(screen.getByText('Select all that apply')).toBeTruthy()
      const chosen = Array.from(container.querySelectorAll('.chat-question-option.chosen .chat-question-option-label')).map((e) => e.textContent)
      expect(chosen).toEqual(['A', 'B', 'C'])
      expect(screen.queryByText(/answer in the terminal/i)).toBeNull()
    })

    it.each([
      ['no terminal', { caps: { terminal: false, reply: false, kill: false, chat: true } }],
      ['an ended run', { state: 'done' as const }],
      ['a failed run', { state: 'failed' as const }],
    ])('says not answered, with no terminal hint, for a pending question on %s', async (_name, over) => {
      const { container } = await renderReady(over, {
        page: page('e1', [askCall({ status: 'in_progress' })]),
        tool: { toolCallId: 'q1', name: 'AskUserQuestion', input },
      })
      await waitFor(() => expect(container.querySelectorAll('.chat-question-block')).toHaveLength(2))
      expect(screen.queryByText(/answer in the terminal/i)).toBeNull()
      expect(container.querySelector('.chat-question-declined')!.textContent).toContain('Not answered')
    })

    it('shows the terminal hint while pending for an agent houston cannot answer for', async () => {
      const { container } = await renderReady({ agent: 'pi' }, {
        page: page('e1', [askCall({ status: 'in_progress' })]),
        tool: { toolCallId: 'q1', name: 'AskUserQuestion', input },
      })
      await waitFor(() => expect(container.querySelectorAll('.chat-question-block')).toHaveLength(2))
      expect(screen.getByText(/answer in the terminal tab/i)).toBeTruthy()
      expect(container.querySelector('.chat-question-option.chosen')).toBeNull()
    })

    describe('answering', () => {
      const pending = (answer?: FetchOpts['answer']): FetchOpts => ({
        page: page('e1', [askCall({ status: 'in_progress' })]),
        tool: { toolCallId: 'q1', name: 'AskUserQuestion', input },
        answer,
      })
      const answerCalls = (fetchMock: ReturnType<typeof vi.fn>) =>
        fetchMock.mock.calls.filter(([url]) => String(url).includes('/answer')) as [string, RequestInit][]
      const sendBtn = () => screen.getByRole('button', { name: 'Send answer' }) as HTMLButtonElement
      const toolFetches = (fetchMock: ReturnType<typeof vi.fn>) =>
        fetchMock.mock.calls.filter(([url]) => String(url).includes('/chat/tool/')).length

      it('replaces the terminal hint with a picker whose options are radios and checkboxes', async () => {
        await renderReady({}, pending())
        await screen.findByText('Which scope?')
        expect(screen.queryByText(/answer in the terminal/i)).toBeNull()
        expect(screen.getAllByRole('radio').map((e) => e.getAttribute('aria-checked'))).toEqual(['false', 'false', 'false'])
        expect(screen.getAllByRole('checkbox')).toHaveLength(4)
      })

      it('keeps Send disabled until every question is answered, then posts the staged answers', async () => {
        const { fetchMock } = await renderReady({}, pending())
        await screen.findByText('Which scope?')
        expect(sendBtn().disabled).toBe(true)
        fireEvent.click(screen.getByRole('radio', { name: /Z/ }))
        expect(screen.getByRole('radio', { name: /Z/ }).getAttribute('aria-checked')).toBe('true')
        expect(sendBtn().disabled).toBe(true)
        expect(answerCalls(fetchMock)).toHaveLength(0)
        fireEvent.click(screen.getByRole('checkbox', { name: /^D/ }))
        expect(sendBtn().disabled).toBe(false)
        fireEvent.click(sendBtn())
        await screen.findByText('Sent — waiting for Claude')
        const calls = answerCalls(fetchMock)
        expect(calls).toHaveLength(1)
        expect(calls[0][0]).toBe('/api/runs/r1/answer')
        expect(JSON.parse(String(calls[0][1].body))).toEqual({
          kind: 'question', toolCallId: 'q1',
          answers: [{ question: 0, options: [1] }, { question: 1, options: [2] }],
        })
        expect(sendBtn().disabled).toBe(true)
        expect(screen.getAllByRole('radio').every((e) => (e as HTMLButtonElement).disabled)).toBe(true)
      })

      it('posts multi-select options together with trimmed Other text', async () => {
        const { fetchMock } = await renderReady({}, pending())
        await screen.findByText('Which scope?')
        fireEvent.click(screen.getByRole('radio', { name: /A/ }))
        fireEvent.click(screen.getByRole('checkbox', { name: /^B/ }))
        const others = screen.getAllByRole('checkbox', { name: 'Other' })
        fireEvent.click(others[0])
        expect(sendBtn().disabled).toBe(true) // Other on with blank text
        fireEvent.change(screen.getByLabelText('Other answer: Which extras?'), { target: { value: '  my own  ' } })
        expect(sendBtn().disabled).toBe(false)
        fireEvent.click(sendBtn())
        await screen.findByText('Sent — waiting for Claude')
        expect(JSON.parse(String(answerCalls(fetchMock)[0][1].body)).answers).toEqual([
          { question: 0, options: [0] },
          { question: 1, options: [0], text: 'my own' },
        ])
      })

      it('single-select Other clears the chosen option', async () => {
        const { fetchMock } = await renderReady({}, pending())
        await screen.findByText('Which scope?')
        fireEvent.click(screen.getByRole('radio', { name: /A/ }))
        fireEvent.click(screen.getByRole('radio', { name: 'Other' }))
        expect(screen.getByRole('radio', { name: /A/ }).getAttribute('aria-checked')).toBe('false')
        fireEvent.change(screen.getByLabelText('Other answer: Which scope?'), { target: { value: 'neither' } })
        fireEvent.click(screen.getByRole('checkbox', { name: /^B/ }))
        fireEvent.click(sendBtn())
        await screen.findByText('Sent — waiting for Claude')
        expect(JSON.parse(String(answerCalls(fetchMock)[0][1].body)).answers[0]).toEqual({ question: 0, options: [], text: 'neither' })
      })

      it('a 409 shows the moved message, re-fetches the tool and keeps the picker usable', async () => {
        const { fetchMock } = await renderReady({}, pending({ status: 409, body: 'prompt changed' }))
        await screen.findByText('Which scope?')
        fireEvent.click(screen.getByRole('radio', { name: /A/ }))
        fireEvent.click(screen.getByRole('checkbox', { name: /^B/ }))
        const before = toolFetches(fetchMock)
        fireEvent.click(sendBtn())
        await screen.findByText('The session moved on — refresh')
        await waitFor(() => expect(toolFetches(fetchMock)).toBe(before + 1))
        expect(screen.getByRole('link', { name: 'Terminal tab' }).getAttribute('href')).toBe('#/fleet/r1/terminal')
        expect(sendBtn().disabled).toBe(false)
      })

      it('a partial 409 offers the Terminal tab and locks the picker', async () => {
        await renderReady({}, pending({ status: 409, body: '{"partial":true}' }))
        await screen.findByText('Which scope?')
        fireEvent.click(screen.getByRole('radio', { name: /A/ }))
        fireEvent.click(screen.getByRole('checkbox', { name: /^B/ }))
        fireEvent.click(sendBtn())
        const link = await screen.findByRole('link', { name: 'Terminal tab' })
        expect(link.getAttribute('href')).toBe('#/fleet/r1/terminal')
        expect(screen.getByText(/part of the answer went in/i)).toBeTruthy()
        expect(sendBtn().disabled).toBe(true)
      })

      it('shows the server text on an error and keeps the staging', async () => {
        await renderReady({}, pending({ status: 429, body: 'busy' }))
        await screen.findByText('Which scope?')
        fireEvent.click(screen.getByRole('radio', { name: /A/ }))
        fireEvent.click(screen.getByRole('checkbox', { name: /^B/ }))
        fireEvent.click(sendBtn())
        await screen.findByText('busy')
        expect(sendBtn().disabled).toBe(false)
        expect(screen.getByRole('radio', { name: /A/ }).getAttribute('aria-checked')).toBe('true')
      })

      it('moves focus and selection with the arrow keys, wrapping through Other, with a roving tabindex', async () => {
        await renderReady({}, pending())
        await screen.findByText('Which scope?')
        const group = screen.getByRole('radiogroup', { name: 'Which scope?' })
        const radios = () => within(group).getAllByRole('radio') as HTMLButtonElement[]
        const tabStops = () => radios().map((r) => r.tabIndex)
        expect(tabStops()).toEqual([0, -1, -1])

        radios()[0].focus()
        fireEvent.keyDown(radios()[0], { key: 'ArrowDown' })
        expect(radios()[1].getAttribute('aria-checked')).toBe('true')
        expect(document.activeElement).toBe(radios()[1])
        expect(tabStops()).toEqual([-1, 0, -1])

        fireEvent.keyDown(radios()[1], { key: 'ArrowRight' })
        expect(radios()[2].getAttribute('aria-checked')).toBe('true')
        expect(document.activeElement).toBe(radios()[2])

        fireEvent.keyDown(radios()[2], { key: 'ArrowDown' })
        expect(radios()[0].getAttribute('aria-checked')).toBe('true')
        expect(document.activeElement).toBe(radios()[0])

        fireEvent.keyDown(radios()[0], { key: 'ArrowUp' })
        expect(radios()[2].getAttribute('aria-checked')).toBe('true')
        fireEvent.keyDown(radios()[2], { key: 'ArrowLeft' })
        expect(radios()[1].getAttribute('aria-checked')).toBe('true')
        expect(document.activeElement).toBe(radios()[1])
      })

      it('tapping Other focuses its input, and a viewport resize scrolls the focused input back into view', async () => {
        const viewport = new EventTarget()
        vi.stubGlobal('visualViewport', viewport)
        await renderReady({}, pending())
        await screen.findByText('Which scope?')
        fireEvent.click(screen.getByRole('radio', { name: 'Other' }))
        const input = screen.getByLabelText('Other answer: Which scope?') as HTMLInputElement
        expect(document.activeElement).toBe(input)
        const scroll = vi.fn()
        input.scrollIntoView = scroll
        act(() => { viewport.dispatchEvent(new Event('resize')) })
        expect(scroll).toHaveBeenCalledWith({ block: 'nearest' })
      })

      it('offers no picker for an agent houston cannot answer for', async () => {
        await renderReady({ agent: 'pi' }, pending())
        await screen.findByText('Which scope?')
        expect(screen.getByText(/answer in the terminal tab/i)).toBeTruthy()
        expect(screen.queryByRole('button', { name: 'Send answer' })).toBeNull()
        expect(screen.queryByRole('radio')).toBeNull()
      })
    })

    it('falls back to the raw output when a label contains a comma', async () => {
      const commaInput = { questions: [{ question: 'Pick?', options: [{ label: 'A, B' }, { label: 'C' }] }] }
      const { container } = await renderReady({}, {
        page: page('e1', [askCall()]),
        tool: { toolCallId: 'q1', name: 'AskUserQuestion', input: commaInput, output: 'User has answered: "Pick?"="A, B".' },
      })
      await waitFor(() => expect(container.querySelector('.chat-tool-output')).not.toBeNull())
      expect(container.querySelector('.chat-tool-output')!.textContent).toContain('"Pick?"="A, B"')
      expect(container.querySelector('.chat-question-option.chosen')).toBeNull()
    })

    it('falls back to the plain title row when the input is omitted', async () => {
      await renderReady({}, {
        page: page('e1', [askCall()]),
        tool: { toolCallId: 'q1', name: 'AskUserQuestion', inputOmitted: true },
      })
      const row = await screen.findByTestId('question-fallback')
      expect(row.textContent).toContain('Scope')
    })

    it('a pending question whose detail fails to load points to the Terminal tab', async () => {
      await renderReady({ state: 'blocked', question: { text: 'Which?', via: 'pane' } }, {
        page: page('e1', [askCall({ status: 'in_progress' })]),
        toolStatus: 500,
      })
      const link = await screen.findByRole('link', { name: 'Open Terminal' })
      expect(link.getAttribute('href')).toBe('#/fleet/r1/terminal')
      expect(screen.getByTestId('question-fallback').textContent).toMatch(/answer it in the terminal tab/i)
      expect(screen.queryByRole('button', { name: 'Reply in Terminal' })).toBeNull()
    })

    it('reports a layout change once the tool detail loads', async () => {
      installFetch({ page: page('e1', []), tool: { toolCallId: 'q1', name: 'AskUserQuestion', input } })
      const onLayout = vi.fn()
      const item = { kind: 'question' as const, id: 'q1', seq: 1, call: { toolCallId: 'q1', tool: 'AskUserQuestion', status: 'in_progress', seq: 1 } }
      render(<QuestionCard item={item} runId="r1" canAnswer answerable onLayout={onLayout} />)
      await screen.findByText('Which scope?')
      expect(onLayout).toHaveBeenCalled()
    })

    it('fetches the answers when the pending call completes live', async () => {
      const opts: FetchOpts = {
        page: page('e1', [askCall({ status: 'in_progress' })]),
        tool: { toolCallId: 'q1', name: 'AskUserQuestion', input },
      }
      const { container } = await renderReady({}, opts)
      await screen.findByRole('button', { name: 'Send answer' })
      opts.tool = {
        toolCallId: 'q1', name: 'AskUserQuestion', input,
        output: 'User has answered your questions: "Which scope?"="Z", "Which extras?"="D".',
      }
      act(() => {
        fake.instances[0].emit('updates', [{ id: 'q1u', seq: 2, ts: 2, sessionUpdate: 'tool_call_update', toolCallId: 'q1', status: 'completed' }])
      })
      await waitFor(() => expect(container.querySelectorAll('.chat-question-option.chosen')).toHaveLength(2))
      expect(screen.queryByText(/answer in the terminal tab/i)).toBeNull()
      expect(screen.queryByRole('button', { name: 'Send answer' })).toBeNull()
    })

    it('shows a failed call as not answered with its output', async () => {
      const { container } = await renderReady({}, {
        page: page('e1', [askCall({ status: 'failed' })]),
        tool: { toolCallId: 'q1', name: 'AskUserQuestion', input, output: 'The user dismissed the question' },
      })
      await waitFor(() => expect(container.querySelector('.chat-question-declined')).not.toBeNull())
      expect(container.querySelector('.chat-question-declined')!.textContent).toContain('Not answered')
      expect(container.querySelector('.chat-question-declined')!.textContent).toContain('dismissed')
    })

    it('falls back to the raw output when a free-text answer contains a quote', async () => {
      const { container } = await renderReady({}, {
        page: page('e1', [askCall()]),
        tool: { toolCallId: 'q1', name: 'AskUserQuestion', input: { questions: [{ question: 'Pick?', options: [{ label: 'A' }, { label: 'Z' }] }] }, output: 'User has answered: "Pick?"="say "hi"".' },
      })
      await waitFor(() => expect(container.querySelector('.chat-tool-output')).not.toBeNull())
      expect(container.querySelector('.chat-question-other')).toBeNull()
    })

    it('does not split a single-select free-text answer on commas', async () => {
      const { container } = await renderReady({}, {
        page: page('e1', [askCall()]),
        tool: { toolCallId: 'q1', name: 'AskUserQuestion', input: { questions: [{ question: 'Pick?', options: [{ label: 'A' }, { label: 'Z' }] }] }, output: 'User has answered: "Pick?"="A, but smaller".' },
      })
      await waitFor(() => expect(container.querySelector('.chat-question-other')).not.toBeNull())
      expect(container.querySelector('.chat-question-option.chosen')).toBeNull()
      expect(container.querySelector('.chat-question-other')!.textContent).toContain('A, but smaller')
    })
  })
})

describe('ChatTab quick commands', () => {
  const fenced = (cmd: string) => `Compact first:\n\n\`\`\`\n${cmd}\n\`\`\`\n`

  it('offers the suggestion when the latest assistant message has a fenced slash command', async () => {
    await renderReady({ state: 'idle' }, { page: page('e1', [textUpdate('a1', 1, fenced('/compact keep the plan'))]) })
    expect(screen.getByRole('button', { name: '↳ suggested' })).toBeTruthy()
  })

  it('offers none without a fenced command', async () => {
    await renderReady({ state: 'idle' }, { page: page('e1', [textUpdate('a1', 1, 'nothing to run')]) })
    expect(screen.queryByRole('button', { name: '↳ suggested' })).toBeNull()
  })

  it('/compact… prefills an empty draft but never overwrites one', async () => {
    await renderReady({ state: 'idle' }, { page: page('e1', []) })
    const textarea = screen.getByPlaceholderText('Message…') as HTMLTextAreaElement
    fireEvent.click(screen.getByRole('button', { name: 'Commands' }))
    fireEvent.click(screen.getByRole('button', { name: '/compact…' }))
    expect(textarea.value).toBe('/compact ')

    fireEvent.change(textarea, { target: { value: 'my draft' } })
    fireEvent.click(screen.getByRole('button', { name: '/compact…' }))
    expect(textarea.value).toBe('my draft')
    expect(screen.getByText('Clear the draft and attachment first, then tap /compact…')).toBeTruthy()
  })

  it('is absent for a non-claude run', async () => {
    await renderReady({ state: 'idle', agent: 'pi' }, { page: page('e1', [textUpdate('a1', 1, fenced('/compact'))]) })
    expect(screen.queryByRole('button', { name: 'Commands' })).toBeNull()
  })
})

describe('ChatTab permission bar', () => {
  const dialog: Prompt = {
    question: 'Do you want to proceed?',
    choices: ['Yes', 'Yes, and don\'t ask again', 'No'],
    detail: 'rm -rf build\nls',
    frame: 'f1',
  }
  const withPrompt = (extra: Partial<FetchOpts> = {}): FetchOpts => ({ page: page('e1', []), prompt: dialog, ...extra })
  const barText = 'Do you want to proceed?'
  const promptFetches = (m: ReturnType<typeof vi.fn>) => m.mock.calls.filter(([u]) => String(u).includes('/prompt')).length
  const answerCalls = (m: ReturnType<typeof vi.fn>) =>
    m.mock.calls.filter(([u]) => String(u).includes('/answer')) as [string, RequestInit][]

  it('shows the question, detail and one numbered button per choice when blocked', async () => {
    await renderReady({ state: 'blocked' }, withPrompt())
    await screen.findByText(barText)
    expect(screen.getByRole('button', { name: '1. Yes' })).toBeTruthy()
    expect(screen.getByRole('button', { name: "2. Yes, and don't ask again" })).toBeTruthy()
    expect(screen.getByRole('button', { name: '3. No' })).toBeTruthy()
    expect(screen.getByRole('button', { name: /rm -rf build/ }).getAttribute('aria-expanded')).toBe('false')
  })

  it('expands the detail on tap', async () => {
    await renderReady({ state: 'blocked' }, withPrompt())
    const detail = await screen.findByRole('button', { name: /rm -rf build/ })
    fireEvent.click(detail)
    expect(detail.getAttribute('aria-expanded')).toBe('true')
  })

  it('shows for an idle run with a prompt', async () => {
    await renderReady({ state: 'idle' }, withPrompt())
    await screen.findByText(barText)
  })

  it('is hidden when the prompt route says 404', async () => {
    const { fetchMock } = await renderReady({ state: 'blocked' }, { page: page('e1', []) })
    await waitFor(() => expect(promptFetches(fetchMock)).toBe(1))
    expect(screen.queryByText(barText)).toBeNull()
  })

  it.each([
    ['a non-claude agent', { state: 'blocked' as const, agent: 'pi' }],
    ['no terminal', { state: 'blocked' as const, caps: { terminal: false, reply: false, kill: false, chat: true } }],
    ['a running run', { state: 'running' as const }],
  ])('is hidden and never polls for %s', async (_name, over) => {
    const { fetchMock } = await renderReady(over, withPrompt())
    expect(screen.queryByText(barText)).toBeNull()
    expect(promptFetches(fetchMock)).toBe(0)
  })

  it('hides when the run leaves blocked and idle', async () => {
    const { rerender, run: r } = await renderReady({ state: 'blocked' }, withPrompt())
    await screen.findByText(barText)
    rerender(<ChatTab run={{ ...r, state: 'running' }} now={now} />)
    expect(screen.queryByText(barText)).toBeNull()
  })

  it('polls every 2 s while active and stops when the run leaves blocked', async () => {
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] })
    const { rerender, fetchMock, run: r } = await renderReady({ state: 'blocked' }, withPrompt())
    await waitFor(() => expect(promptFetches(fetchMock)).toBe(1))
    await act(async () => { vi.advanceTimersByTime(2000) })
    expect(promptFetches(fetchMock)).toBe(2)
    rerender(<ChatTab run={{ ...r, state: 'running' }} now={now} />)
    await act(async () => { vi.advanceTimersByTime(6000) })
    expect(promptFetches(fetchMock)).toBe(2)
  })

  it('posts the tapped ordinal with the frame, then hides until the frame changes', async () => {
    const { fetchMock } = await renderReady({ state: 'blocked' }, withPrompt({ answer: { status: 204 } }))
    fireEvent.click(await screen.findByRole('button', { name: '2. Yes, and don\'t ask again' }))
    await waitFor(() => expect(screen.queryByText(barText)).toBeNull())
    const calls = answerCalls(fetchMock)
    expect(calls).toHaveLength(1)
    expect(calls[0][0]).toBe('/api/runs/r1/answer')
    expect(JSON.parse(String(calls[0][1].body))).toEqual({ kind: 'choice', ordinal: 2, frame: 'f1' })
  })

  it('stays hidden while the answered frame is still on screen, and shows an identical dialog after it went away', async () => {
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] })
    await renderReady({ state: 'blocked' }, withPrompt({ answer: { status: 204 } }))
    let current: Prompt | null = dialog
    const base = globalThis.fetch
    vi.stubGlobal('fetch', vi.fn((url: string) => {
      if (!url.includes('/prompt')) return base(url)
      return Promise.resolve(current ? jsonResponse(current) : ({ status: 404, ok: false, text: async () => '' } as Response))
    }))
    fireEvent.click(await screen.findByRole('button', { name: '1. Yes' }))
    await waitFor(() => expect(screen.queryByText(barText)).toBeNull())

    await act(async () => { vi.advanceTimersByTime(2000) })
    expect(screen.queryByText(barText)).toBeNull()

    current = null
    await act(async () => { vi.advanceTimersByTime(2000) })
    expect(screen.queryByText(barText)).toBeNull()

    current = dialog
    await act(async () => { vi.advanceTimersByTime(2000) })
    expect(screen.getByText(barText)).toBeTruthy()
  })

  it('disables every choice while the answer is in flight', async () => {
    await renderReady({ state: 'blocked' }, withPrompt())
    let release: (r: Response) => void = () => {}
    const base = globalThis.fetch
    vi.stubGlobal('fetch', vi.fn((url: string) =>
      url.includes('/answer') ? new Promise<Response>((res) => { release = res }) : base(url)))
    fireEvent.click(await screen.findByRole('button', { name: '1. Yes' }))
    await waitFor(() => expect((screen.getByRole('button', { name: '3. No' }) as HTMLButtonElement).disabled).toBe(true))
    expect((screen.getByRole('button', { name: '1. Yes' }) as HTMLButtonElement).disabled).toBe(true)
    await act(async () => { release({ status: 204, ok: true, text: async () => '' } as Response) })
  })

  it('a 409 shows the moved message and re-fetches the prompt', async () => {
    const { fetchMock } = await renderReady({ state: 'blocked' }, withPrompt({ answer: { status: 409, body: 'moved' } }))
    fireEvent.click(await screen.findByRole('button', { name: '1. Yes' }))
    await screen.findByText('The session moved on — refresh')
    await waitFor(() => expect(promptFetches(fetchMock)).toBe(2))
    expect((screen.getByRole('button', { name: '1. Yes' }) as HTMLButtonElement).disabled).toBe(false)
  })

  describe('moved notice', () => {
    const moved = 'The session moved on — refresh'

    it('stays when the re-fetch finds no prompt, until dismissed', async () => {
      const opts = withPrompt({ answer: { status: 409, body: 'moved' } })
      const { fetchMock } = await renderReady({ state: 'blocked' }, opts)
      fireEvent.click(await screen.findByRole('button', { name: '1. Yes' }))
      opts.prompt = undefined
      await waitFor(() => expect(promptFetches(fetchMock)).toBe(2))
      await waitFor(() => expect(screen.queryByText(barText)).toBeNull())
      expect(screen.getByText(moved)).toBeTruthy()
      fireEvent.click(screen.getByRole('button', { name: 'Dismiss' }))
      expect(screen.queryByText(moved)).toBeNull()
    })

    it('stays above a new prompt from the re-fetch, until a later answer is sent', async () => {
      const opts = withPrompt({ answer: { status: 409, body: 'moved' } })
      await renderReady({ state: 'blocked' }, opts)
      fireEvent.click(await screen.findByRole('button', { name: '1. Yes' }))
      opts.prompt = { ...dialog, question: 'Run the tests?', frame: 'f2' }
      await screen.findByText('Run the tests?')
      expect(screen.getByText(moved)).toBeTruthy()
      opts.answer = { status: 204 }
      fireEvent.click(screen.getByRole('button', { name: '1. Yes' }))
      expect(screen.queryByText(moved)).toBeNull()
    })
  })

  it('shows the answered frame again once three polls after the answer still return it', async () => {
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] })
    await renderReady({ state: 'blocked' }, withPrompt({ answer: { status: 204 } }))
    fireEvent.click(await screen.findByRole('button', { name: '1. Yes' }))
    await waitFor(() => expect(screen.queryByText(barText)).toBeNull())
    await act(async () => { vi.advanceTimersByTime(2000) })
    await act(async () => { vi.advanceTimersByTime(2000) })
    expect(screen.queryByText(barText)).toBeNull()
    await act(async () => { vi.advanceTimersByTime(2000) })
    expect(screen.getByText(barText)).toBeTruthy()
  })

  it('skips a poll while the previous one is still in flight', async () => {
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] })
    installFetch(withPrompt())
    const base = globalThis.fetch
    let release: (r: Response) => void = () => {}
    const fetchMock = vi.fn((url: string) =>
      url.includes('/prompt') ? new Promise<Response>((res) => { release = res }) : base(url))
    vi.stubGlobal('fetch', fetchMock)
    render(<ChatTab run={run({ state: 'blocked' })} now={now} />)
    await waitFor(() => expect(promptFetches(fetchMock)).toBe(1))
    await act(async () => { vi.advanceTimersByTime(4000) })
    expect(promptFetches(fetchMock)).toBe(1)
    await act(async () => { release(jsonResponse(dialog)) })
    await screen.findByText(barText)
    await act(async () => { vi.advanceTimersByTime(2000) })
    expect(promptFetches(fetchMock)).toBe(2)
  })

  it('shows an answer error and re-enables the choices', async () => {
    await renderReady({ state: 'blocked' }, withPrompt({ answer: { status: 500, body: 'boom' } }))
    fireEvent.click(await screen.findByRole('button', { name: '1. Yes' }))
    await screen.findByText('boom')
    expect((screen.getByRole('button', { name: '1. Yes' }) as HTMLButtonElement).disabled).toBe(false)
  })

  it('keeps the composer usable while the bar shows', async () => {
    await renderReady({ state: 'blocked' }, withPrompt())
    await screen.findByText(barText)
    const textarea = screen.getByPlaceholderText('Message…') as HTMLTextAreaElement
    expect(textarea.disabled).toBe(false)
  })

  describe('header Reply in Terminal', () => {
    const blocked = { state: 'blocked' as const, question: { text: 'Allow it?', via: 'pane' as const } }
    const replyBtn = () => screen.queryByRole('button', { name: 'Reply in Terminal' })

    it('is hidden while the bar shows, the question text stays', async () => {
      await renderReady(blocked, withPrompt())
      await screen.findByText(barText)
      expect(replyBtn()).toBeNull()
      expect(screen.getByText('Allow it?')).toBeTruthy()
    })

    it('is present when there is no prompt', async () => {
      const { fetchMock } = await renderReady(blocked, { page: page('e1', []) })
      await waitFor(() => expect(promptFetches(fetchMock)).toBe(1))
      expect(replyBtn()).toBeTruthy()
    })

    it('is hidden while an answerable AskUserQuestion card is pending', async () => {
      const { container } = await renderReady(blocked, {
        page: page('e1', [toolCall('q1', 1, 'AskUserQuestion', { status: 'in_progress' })]),
        tool: {
          toolCallId: 'q1', name: 'AskUserQuestion',
          input: { questions: [{ question: 'Which?', options: [{ label: 'A' }] }] },
        },
      })
      await waitFor(() => expect(container.querySelector('.chat-question-block')).not.toBeNull())
      expect(replyBtn()).toBeNull()
    })

    it('stays for a pending question card on a run houston cannot answer for', async () => {
      const { container } = await renderReady({ ...blocked, agent: 'pi' }, {
        page: page('e1', [toolCall('q1', 1, 'AskUserQuestion', { status: 'in_progress' })]),
        tool: {
          toolCallId: 'q1', name: 'AskUserQuestion',
          input: { questions: [{ question: 'Which?', options: [{ label: 'A' }] }] },
        },
      })
      await waitFor(() => expect(container.querySelector('.chat-question-block')).not.toBeNull())
      expect(replyBtn()).toBeTruthy()
    })
  })
})
