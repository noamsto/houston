import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { ChatTab } from './ChatTab'
import type { Run } from '../api/runs'
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
}

function installFetch(opts: FetchOpts): ReturnType<typeof vi.fn> {
  const mock = vi.fn((url: string) => {
    if (url.includes('/chat/tool/')) return Promise.resolve(jsonResponse(opts.tool ?? {}))
    if (url.includes('before=')) return Promise.resolve(jsonResponse(opts.earlier ?? opts.page))
    if (url.includes('/chat')) return Promise.resolve(jsonResponse(opts.page))
    if (url.includes('/input')) return Promise.resolve(okResponse())
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
    expect(bubble.querySelector('ul')).toBeTruthy()
    expect(bubble.querySelectorAll('li')).toHaveLength(2)
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
      await startSendThenEsc()
      expect((screen.getByRole('button', { name: 'Esc' }) as HTMLButtonElement).disabled).toBe(false)
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
})
