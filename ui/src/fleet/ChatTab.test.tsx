import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { ChatTab } from './ChatTab'
import type { Run } from '../api/runs'
import type { ChatPage, ChatToolDetail, ChatUpdate } from '../api/chat'
import { installFakeEventSource } from '../testing/fakeEventSource'
import type { FakeEventSourceHandle } from '../testing/fakeEventSource'

const now = 1_800_000_000_000 // fixed ms

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

    const textarea = screen.getByPlaceholderText('Message…')
    fireEvent.change(textarea, { target: { value: 'ping the server' } })
    fireEvent.click(screen.getByRole('button', { name: 'Send' }))

    expect(screen.getByText('ping the server')).toBeTruthy()
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
