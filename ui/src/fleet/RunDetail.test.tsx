import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, cleanup, fireEvent, render, screen, within } from '@testing-library/react'
import { waitFor } from '@testing-library/react'
import { RunDetail } from './RunDetail'
import { parseDetailRoute } from './routes'
import { resetCrewReposCache } from './useCrewRepos'
import type { Run } from '../api/runs'
import type { Terminal } from '@xterm/xterm'
import { installFakeEventSource } from '../testing/fakeEventSource'
import type { FakeEventSourceHandle } from '../testing/fakeEventSource'

// Subclass the real Terminal so xterm's real DOM/buffer behavior keeps
// working, while letting tests inspect the instance TerminalPane actually
// constructed (options, term.input(), ...). Duplicated from
// TerminalPane.test.tsx — vi.mock is file-scoped, so it can't be shared.
vi.mock('@xterm/xterm', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@xterm/xterm')>()
  const instances: InstanceType<typeof actual.Terminal>[] = []
  class T extends actual.Terminal {
    constructor(o?: ConstructorParameters<typeof actual.Terminal>[0]) {
      super(o)
      instances.push(this)
    }
  }
  return { ...actual, Terminal: T, __instances: instances }
})

async function lastTerminalInstance(): Promise<Terminal> {
  const { __instances } = (await import('@xterm/xterm')) as unknown as { __instances: Terminal[] }
  return __instances[__instances.length - 1]
}

const now = 1_800_000_000_000 // fixed ms

let mockConnected = true
let mockEnded: string | null = null
let lastSocketPath: string | null = null
const sendInput = vi.fn()
vi.mock('../hooks/usePaneSocket', () => ({
  usePaneSocket: (path: string | null) => {
    lastSocketPath = path
    return { connected: mockConnected, ended: mockEnded, sendInput, sendResize: vi.fn() }
  },
}))

let desktop = true
vi.mock('../hooks/useMediaQuery', () => ({
  useIsDesktop: () => desktop,
}))

function run(p: Partial<Run> = {}): Run {
  return {
    id: 'pane-1', agent: 'claude', state: 'running',
    activity: {}, tokens: { input: 0, output: 0 },
    caps: { terminal: true, reply: true, kill: true },
    updated_at: Math.floor(now / 1000),
    ...p,
  } as Run
}

function liveRun(p: Partial<Run> = {}): Run {
  return run({ tmux: { session: 'sess', window: 0, pane_id: '%1' }, ...p })
}

beforeEach(() => {
  // MobileInputBar sends via raw fetch; an unstubbed real relative-URL fetch
  // under happy-dom would reject/flake, so stub it globally for every test.
  vi.stubGlobal('fetch', vi.fn(() => Promise.resolve({ ok: true } as Response)))
})

afterEach(async () => {
  cleanup()
  mockConnected = true
  mockEnded = null
  lastSocketPath = null
  desktop = true
  sendInput.mockClear()
  vi.unstubAllGlobals()
  const { __instances } = (await import('@xterm/xterm')) as unknown as { __instances: Terminal[] }
  __instances.length = 0
})

describe('RunDetail', () => {
  it('shows a loading state before the first snapshot arrives, even for an id not yet known', () => {
    render(<RunDetail mode={null} runs={[]} hasSnapshot={false} streamConnected now={now} id="pane-1" tab="chat" />)
    expect(screen.getByText(/loading run/i)).toBeTruthy()
    expect(screen.queryByText(/no longer available/i)).toBeNull()
  })

  it('shows "not found" once a snapshot has arrived and the id is not in it', () => {
    render(<RunDetail mode={null} runs={[]} hasSnapshot streamConnected now={now} id="pane-1" tab="chat" />)
    expect(screen.getByText(/no longer available/i)).toBeTruthy()
  })

  it('falls back to the status card when the deep-linked run has no terminal capability', () => {
    const r = run({ caps: { terminal: false, reply: true, kill: true }, activity: { tool: 'edit', hint: 'RunDetail.tsx' } })
    render(<RunDetail mode={null} runs={[r]} hasSnapshot streamConnected now={now} id={r.id} tab="terminal" />)

    // The status card renders...
    const card = document.querySelector<HTMLElement>('.run-status-card')!
    expect(within(card).getByText('edit')).toBeTruthy()
    expect(within(card).getByText(/RunDetail\.tsx/)).toBeTruthy()
    // ...and no Terminal tab/placeholder is offered.
    expect(screen.queryByText(/terminal/i)).toBeNull()
  })

  it('renders the Terminal tab button and placeholder when the run has terminal capability', () => {
    const r = run({ caps: { terminal: true, reply: true, kill: true } })
    render(<RunDetail mode={null} runs={[r]} hasSnapshot streamConnected now={now} id={r.id} tab="terminal" />)
    expect(screen.getByText('Terminal')).toBeTruthy()
    expect(screen.getByText(/coming soon/i)).toBeTruthy()
  })

  it('shows project and branch separately in the header, not the worktree-dir repo slug twice', () => {
    const r = run({ project: 'houston', repo: 'feat-97-dogfood-houston-as-a-phone-user-and-file', branch: 'feat/97-dogfood-houston-as-a-phone-user-and-file' })
    render(<RunDetail mode={null} runs={[r]} hasSnapshot streamConnected now={now} id={r.id} tab="chat" />)
    expect(screen.getByText('houston')).toBeTruthy()
    expect(screen.getByText('feat/97-dogfood-houston-as-a-phone-user-and-file')).toBeTruthy()
    expect(screen.queryByText(/feat-97-dogfood-houston-as-a-phone-user-and-file\/feat/)).toBeNull()
  })

  it('shows the agent chip in the header', () => {
    const r = run({ agent: 'pi' })
    render(<RunDetail mode={null} runs={[r]} hasSnapshot streamConnected now={now} id={r.id} tab="chat" />)
    expect(screen.getByText('pi')).toBeTruthy()
  })
})

describe('RunDetail terminal lifecycle', () => {
  it('renders a live TerminalPane when the run has terminal capability and tmux data', () => {
    const r = liveRun()
    render(<RunDetail mode={null} runs={[r]} hasSnapshot streamConnected now={now} id={r.id} tab="terminal" />)
    expect(screen.queryByText(/coming soon/i)).toBeNull()
    expect(screen.queryByText(/session ended/i)).toBeNull()
  })

  it('addresses the terminal socket at the run, not the pane', () => {
    const r = liveRun()
    render(<RunDetail mode={null} runs={[r]} hasSnapshot streamConnected now={now} id={r.id} tab="terminal" />)
    expect(lastSocketPath).toBe(`/api/runs/${r.id}/terminal`)
  })

  it('case 1: shows "session ended" when caps.terminal drops after going live, and unmounts the terminal', () => {
    const r = liveRun()
    const { rerender } = render(<RunDetail mode={null} runs={[r]} hasSnapshot streamConnected now={now} id={r.id} tab="terminal" />)
    expect(screen.queryByText(/session ended/i)).toBeNull()

    const dead = liveRun({ caps: { terminal: false, reply: true, kill: true } })
    rerender(<RunDetail mode={null} runs={[dead]} hasSnapshot streamConnected now={now} id={dead.id} tab="terminal" />)

    expect(screen.getByText(/session ended/i)).toBeTruthy()
    expect(screen.getByRole('button', { name: /reconnect/i })).toBeTruthy()
    expect(document.querySelector('.xterm')).toBeNull()
    expect(screen.queryByText(/no longer available/i)).toBeNull()
  })

  it('case 2: shows "no longer available" when a run with a live terminal is evicted', () => {
    const r = liveRun()
    const { rerender } = render(<RunDetail mode={null} runs={[r]} hasSnapshot streamConnected now={now} id={r.id} tab="terminal" />)
    expect(screen.queryByText(/session ended/i)).toBeNull()

    rerender(<RunDetail mode={null} runs={[]} hasSnapshot streamConnected now={now} id={r.id} tab="terminal" />)

    expect(screen.getByText(/no longer available/i)).toBeTruthy()
  })

  it('case 3: a single brief pane-socket disconnect does not end the session', () => {
    vi.useFakeTimers()
    try {
      const r = liveRun()
      mockConnected = false
      const { rerender } = render(<RunDetail mode={null} runs={[r]} hasSnapshot streamConnected now={now} id={r.id} tab="terminal" />)

      act(() => { vi.advanceTimersByTime(3000) })
      mockConnected = true
      rerender(<RunDetail mode={null} runs={[r]} hasSnapshot streamConnected now={now} id={r.id} tab="terminal" />)

      act(() => { vi.advanceTimersByTime(15000) })
      expect(screen.queryByText(/session ended/i)).toBeNull()
    } finally {
      vi.useRealTimers()
    }
  })

  it('case 3: a pane-socket disconnect that stays down for ~10s ends the session', () => {
    vi.useFakeTimers()
    try {
      const r = liveRun()
      mockConnected = false
      render(<RunDetail mode={null} runs={[r]} hasSnapshot streamConnected now={now} id={r.id} tab="terminal" />)

      act(() => { vi.advanceTimersByTime(9000) })
      expect(screen.queryByText(/session ended/i)).toBeNull()

      act(() => { vi.advanceTimersByTime(1500) })
      expect(screen.getByText(/session ended/i)).toBeTruthy()
    } finally {
      vi.useRealTimers()
    }
  })

  it('ends the session immediately (no 10s grace) and shows the reason when the terminal socket reports tmux server changed', () => {
    const r = liveRun()
    mockEnded = 'tmux server changed'
    render(<RunDetail mode={null} runs={[r]} hasSnapshot streamConnected now={now} id={r.id} tab="terminal" />)

    expect(screen.getByText(/session ended/i)).toBeTruthy()
    expect(screen.getByText('tmux server changed')).toBeTruthy()
  })

  it('case 4: a stale SSE stream shows a reconnecting indicator but keeps the terminal mounted', () => {
    const r = liveRun()
    render(<RunDetail mode={null} runs={[r]} hasSnapshot streamConnected={false} now={now} id={r.id} tab="terminal" />)

    expect(screen.getByRole('status')).toBeTruthy()
    expect(screen.getByText(/reconnecting/i)).toBeTruthy()
    expect(screen.queryByText(/session ended/i)).toBeNull()
  })

  it('recovers to a live terminal once caps.terminal flips back true after ending', () => {
    const r = liveRun()
    const { rerender } = render(<RunDetail mode={null} runs={[r]} hasSnapshot streamConnected now={now} id={r.id} tab="terminal" />)

    const dead = liveRun({ caps: { terminal: false, reply: true, kill: true } })
    rerender(<RunDetail mode={null} runs={[dead]} hasSnapshot streamConnected now={now} id={dead.id} tab="terminal" />)
    expect(screen.getByText(/session ended/i)).toBeTruthy()

    rerender(<RunDetail mode={null} runs={[r]} hasSnapshot streamConnected now={now} id={r.id} tab="terminal" />)
    expect(screen.queryByText(/session ended/i)).toBeNull()
  })

  it('unmounts TerminalPane (rather than hiding it) when the tab switches away from terminal', () => {
    const r = liveRun()
    const { rerender } = render(<RunDetail mode={null} runs={[r]} hasSnapshot streamConnected now={now} id={r.id} tab="terminal" />)
    expect(screen.queryByText(/coming soon/i)).toBeNull()

    rerender(<RunDetail mode={null} runs={[r]} hasSnapshot streamConnected now={now} id={r.id} tab="chat" />)
    expect(document.querySelector('.run-status-card')).toBeTruthy()
    expect(document.querySelector('.xterm')).toBeNull()

    // Switching back doesn't read as "ended" — the tab switch alone must not
    // have flagged the pane as dead.
    rerender(<RunDetail mode={null} runs={[r]} hasSnapshot streamConnected now={now} id={r.id} tab="terminal" />)
    expect(screen.queryByText(/session ended/i)).toBeNull()
  })

  it('desktop: hides the classic PaneHeader (RunDetail supplies its own header/tabs)', () => {
    const r = liveRun()
    const { container } = render(<RunDetail mode={null} runs={[r]} hasSnapshot streamConnected now={now} id={r.id} tab="terminal" />)

    // PaneHeader would render the pane id as text — neither it nor any
    // /api/pane reference must appear here (RunDetail addresses the run).
    expect(screen.queryByText(r.tmux!.pane_id)).toBeNull()
    expect(container.textContent).not.toContain('/api/pane')
    // RunDetail's own header/tabs are still present.
    expect(within(container.querySelector<HTMLElement>('.run-detail-header')!).getByLabelText('Back to Fleet')).toBeTruthy()
    expect(screen.getByText('Terminal')).toBeTruthy()
  })

  it('desktop: input is wired — typing into the mounted terminal calls sendInput', async () => {
    const r = liveRun()
    render(<RunDetail mode={null} runs={[r]} hasSnapshot streamConnected now={now} id={r.id} tab="terminal" />)

    const term = await lastTerminalInstance()
    act(() => {
      term.input('a')
    })
    expect(sendInput).toHaveBeenCalledWith('a')
  })

  it('mobile: MobileInputBar renders and sends via fetch, not sendInput', async () => {
    desktop = false
    const r = liveRun()
    render(<RunDetail mode={null} runs={[r]} hasSnapshot streamConnected now={now} id={r.id} tab="terminal" />)

    expect(screen.getByPlaceholderText('Send a message...')).toBeTruthy()
    // Simplest reliable path through MobileInputBar: a quick-action button, not
    // a real IME-driven text entry (happy-dom's textarea typing is flaky).
    const yButton = screen.getByRole('button', { name: 'Y' })
    await act(async () => {
      yButton.click()
    })

    const fetchMock = vi.mocked(fetch)
    expect(fetchMock).toHaveBeenCalled()
    const [url, init] = fetchMock.mock.calls[0]
    expect(url).toBe(`/api/runs/${r.id}/input`)
    expect(JSON.parse(String(init?.body))).toEqual({ type: 'key', key: 'y' })
    expect(sendInput).not.toHaveBeenCalled()
  })

  it('the tabs-row back button returns to Fleet (landscape one-step back)', () => {
    const r = liveRun()
    const onBack = vi.fn()
    const { container } = render(
      <RunDetail mode={null} runs={[r]} hasSnapshot streamConnected now={now} id={r.id} tab="terminal" onBack={onBack} />,
    )

    fireEvent.click(
      within(container.querySelector<HTMLElement>('.run-detail-tabs')!).getByRole('button', { name: /back to fleet/i }),
    )

    expect(onBack).toHaveBeenCalled()
  })
})

describe('RunDetail status card (no chat)', () => {
  const renderCard = (p: Partial<Run>) => {
    const r = run(p)
    return render(<RunDetail mode={null} runs={[r]} hasSnapshot streamConnected now={now} id={r.id} tab="chat" />)
  }

  it('shows no reply affordance for a watchdog-sourced question', () => {
    renderCard({
      state: 'blocked',
      activity: { message: 'Checking in — no reply needed.' },
      question: { text: 'Checking in — no reply needed.', via: 'watchdog' },
    })
    expect(screen.getByText('Checking in — no reply needed.')).toBeTruthy()
    expect(screen.queryByRole('button', { name: 'Reply in Terminal' })).toBeNull()
    expect(screen.queryByLabelText('Reply to the crew')).toBeNull()
  })

  it('renders a duplicated blocked message exactly once', () => {
    const { container } = renderCard({
      state: 'blocked',
      activity: { message: 'Deploy to prod?' },
      question: { text: 'Deploy to prod?', via: 'pane' },
    })
    expect(container.querySelector('.run-status-message')).toBeNull()
    expect(screen.getAllByText('Deploy to prod?')).toHaveLength(1)
  })

  it('offers Reply in Terminal for a blocked pane question on the default tab', () => {
    renderCard({ state: 'blocked', question: { text: 'ok?', via: 'pane' }, caps: { terminal: true, reply: true, kill: true } })
    expect(screen.getByRole('button', { name: 'Reply in Terminal' })).toBeTruthy()
  })

  it('omits Reply in Terminal for the card above the terminal', () => {
    const r = liveRun({ state: 'blocked', question: { text: 'ok?', via: 'pane' } })
    render(<RunDetail mode={null} runs={[r]} hasSnapshot streamConnected now={now} id={r.id} tab="terminal" />)
    expect(screen.getByText('ok?')).toBeTruthy()
    expect(screen.queryByRole('button', { name: 'Reply in Terminal' })).toBeNull()
  })

  it('clamps the last message and expands it on tap', () => {
    renderCard({ activity: { message: 'a long assistant message' } })
    const message = screen.getByRole('button', { name: 'a long assistant message' })
    expect(message.getAttribute('aria-expanded')).toBe('false')
    fireEvent.click(message)
    expect(message.getAttribute('aria-expanded')).toBe('true')
    expect(message.classList.contains('open')).toBe(true)
  })
})

describe('RunDetail chat tab', () => {
  let fake: FakeEventSourceHandle

  beforeEach(() => {
    fake = installFakeEventSource()
  })

  afterEach(() => {
    fake.uninstall()
  })

  function chatRun(p: Partial<Run> = {}): Run {
    return run({ caps: { terminal: true, reply: true, kill: true, chat: true }, ...p })
  }

  function stubChatFetch(body: unknown, status = 200) {
    vi.mocked(fetch).mockImplementation((input: string | URL | Request) => {
      const url = String(input)
      if (url.includes('/chat')) {
        return Promise.resolve({
          ok: status < 300,
          status,
          json: async () => body,
          text: async () => JSON.stringify(body),
        } as Response)
      }
      return Promise.resolve({ ok: true, status: 200, json: async () => ({}), text: async () => '' } as Response)
    })
  }

  const emptyPage = { epoch: 'e1', updates: [], more: false }

  it('offers and selects the Chat tab by default when caps.chat is true and no tab is routed', async () => {
    stubChatFetch(emptyPage)
    const r = chatRun()
    render(<RunDetail mode={null} runs={[r]} hasSnapshot streamConnected now={now} id={r.id} />)

    const chatBtn = screen.getByRole('button', { name: 'Chat' })
    expect(chatBtn.getAttribute('aria-pressed')).toBe('true')
    await waitFor(() => expect(screen.queryByText(/loading chat/i)).toBeNull())
  })

  it('the tabs row holds exactly Chat and Terminal', async () => {
    stubChatFetch(emptyPage)
    const r = chatRun()
    const { container } = render(<RunDetail mode={null} runs={[r]} hasSnapshot streamConnected now={now} id={r.id} tab="chat" />)

    const names = within(container.querySelector<HTMLElement>('.run-detail-tabs')!)
      .getAllByRole('button')
      .filter((b) => !b.classList.contains('run-detail-tabs-back'))
      .map((b) => b.textContent)
    expect(names).toEqual(['Chat', 'Terminal'])
    await waitFor(() => expect(screen.queryByText(/loading chat/i)).toBeNull())
  })

  it('a legacy #/fleet/<id>/activity deep link opens Chat', async () => {
    stubChatFetch(emptyPage)
    const r = chatRun()
    render(<RunDetail mode={null} runs={[r]} hasSnapshot streamConnected now={now} id={r.id} tab={parseDetailRoute('#/fleet/pane-1/activity')!.tab} />)

    expect(screen.getByRole('button', { name: 'Chat' }).getAttribute('aria-pressed')).toBe('true')
    await waitFor(() => expect(vi.mocked(fetch).mock.calls.some((c) => String(c[0]).includes('/chat'))).toBe(true))
  })

  it('a legacy #/fleet/<id>/activity deep link shows the status card for a no-chat run', () => {
    const r = run()
    render(<RunDetail mode={null} runs={[r]} hasSnapshot streamConnected now={now} id={r.id} tab={parseDetailRoute('#/fleet/pane-1/activity')!.tab} />)

    expect(document.querySelector('.run-status-card')).toBeTruthy()
  })

  it('without caps.chat shows the status card with question and last message', () => {
    const r = run({
      state: 'blocked',
      activity: { message: 'Still working on the migration' },
      question: { text: 'Deploy to prod?', via: 'pane' },
    })
    const { container } = render(<RunDetail mode={null} runs={[r]} hasSnapshot streamConnected now={now} id={r.id} />)

    const card = container.querySelector<HTMLElement>('.run-status-card')!
    expect(card.querySelector('.run-status')).toBeTruthy()
    expect(within(card).getByText('Deploy to prod?')).toBeTruthy()
    expect(within(card).getByText('Still working on the migration')).toBeTruthy()
    expect(screen.queryByRole('button', { name: 'Chat' })).toBeNull()
    expect(screen.queryByRole('button', { name: 'Activity' })).toBeNull()
  })

  it('offers no Activity tab for any run', () => {
    stubChatFetch(emptyPage)
    const chat = chatRun()
    const { unmount } = render(<RunDetail mode={null} runs={[chat]} hasSnapshot streamConnected now={now} id={chat.id} />)
    expect(screen.queryByRole('button', { name: 'Activity' })).toBeNull()
    unmount()

    const plain = run()
    render(<RunDetail mode={null} runs={[plain]} hasSnapshot streamConnected now={now} id={plain.id} />)
    expect(screen.queryByRole('button', { name: 'Activity' })).toBeNull()
  })

  it('a no-chat run with no terminal renders the card as the body', () => {
    const r = run({ caps: { terminal: false, reply: true, kill: true }, activity: { message: 'hello there' } })
    const { container } = render(<RunDetail mode={null} runs={[r]} hasSnapshot streamConnected now={now} id={r.id} />)

    const body = container.querySelector<HTMLElement>('.run-detail-body')!
    expect(body.querySelector('.run-status-card')).toBeTruthy()
    expect(within(body).getByText('hello there')).toBeTruthy()
  })

  it('a no-chat run on the terminal tab shows the card above the terminal', () => {
    const r = liveRun()
    const { container } = render(<RunDetail mode={null} runs={[r]} hasSnapshot streamConnected now={now} id={r.id} tab="terminal" />)

    const card = container.querySelector<HTMLElement>('.run-status-card')!
    const terminal = container.querySelector<HTMLElement>('.xterm')!
    expect(card).toBeTruthy()
    expect(terminal).toBeTruthy()
    expect(card.compareDocumentPosition(terminal) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
  })

  it('shows the Chat tab once a rerender flips caps.chat to true', () => {
    const r = run()
    const { rerender } = render(<RunDetail mode={null} runs={[r]} hasSnapshot streamConnected now={now} id={r.id} />)
    expect(screen.queryByRole('button', { name: 'Chat' })).toBeNull()

    stubChatFetch(emptyPage)
    const withChat = chatRun()
    rerender(<RunDetail mode={null} runs={[withChat]} hasSnapshot streamConnected now={now} id={withChat.id} />)
    expect(screen.getByRole('button', { name: 'Chat' })).toBeTruthy()
  })

  it('a deep link to tab="chat" without caps.chat degrades to the status card', () => {
    const r = run()
    render(<RunDetail mode={null} runs={[r]} hasSnapshot streamConnected now={now} id={r.id} tab="chat" />)

    expect(screen.queryByRole('button', { name: 'Chat' })).toBeNull()
    expect(document.querySelector('.run-status-card')).toBeTruthy()
    expect(vi.mocked(fetch).mock.calls.some((c) => String(c[0]).includes('/chat'))).toBe(false)
  })

  it('does not carry one run\'s suggestion onto a run without chat', async () => {
    desktop = false
    const fenced = { epoch: 'e1', more: false, updates: [{
      id: 'a1', seq: 1, ts: 1000, sessionUpdate: 'agent_message_chunk',
      content: [{ type: 'content', content: { type: 'text', text: 'Try:\n\n```\n/compact keep\n```\n' } }],
    }] }
    stubChatFetch(fenced)
    const a = liveRun({ id: 'pane-a', state: 'idle', caps: { terminal: true, reply: true, kill: true, chat: true } })
    const b = liveRun({ id: 'pane-b', state: 'idle' })
    const { rerender } = render(<RunDetail mode={null} runs={[a, b]} hasSnapshot streamConnected now={now} id="pane-a" tab="terminal" />)
    await waitFor(() => expect(screen.getByRole('button', { name: '↳ suggested' })).toBeTruthy())

    rerender(<RunDetail mode={null} runs={[a, b]} hasSnapshot streamConnected now={now} id="pane-b" tab="terminal" />)
    expect(screen.queryByRole('button', { name: '↳ suggested' })).toBeNull()
  })

  it('shows "Chat unavailable" with a Retry when the chat page fetch 404s', async () => {
    stubChatFetch({}, 404)
    const r = chatRun()
    render(<RunDetail mode={null} runs={[r]} hasSnapshot streamConnected now={now} id={r.id} />)

    await waitFor(() => expect(screen.getByText(/chat unavailable/i)).toBeTruthy())
    expect(screen.getByRole('button', { name: 'Retry' })).toBeTruthy()
  })
})

describe('RunDetail crew tab', () => {
  let fake: FakeEventSourceHandle

  beforeEach(() => {
    fake = installFakeEventSource()
    vi.mocked(fetch).mockImplementation((input: string | URL | Request) => {
      if (String(input).includes('/crew/feed')) {
        const page = {
          epoch: 'e1',
          more: false,
          entries: [{ id: 'e1.10', ts: now, kind: 'dispatch', text: 'dispatched a worker', branch: 'w-one', codename: 'blush' }],
        }
        return Promise.resolve({ ok: true, status: 200, json: async () => page, text: async () => '' } as Response)
      }
      return Promise.resolve({ ok: true, status: 200, json: async () => ({}), text: async () => '' } as Response)
    })
  })

  afterEach(() => {
    fake.uninstall()
    resetCrewReposCache()
  })

  const dispatcher = (p: Partial<Run> = {}) =>
    run({ id: 'd', role: 'dispatcher', branch: 'main', crew: { name: '1-1' }, caps: { terminal: true, reply: true, kill: true, chat: true }, ...p })
  const worker = (p: Partial<Run> = {}) =>
    run({ id: 'w', role: 'worker', branch: 'w-one', crew: { name: '1-1', codename: 'blush' }, ...p })

  const tabNames = (container: HTMLElement) =>
    within(container.querySelector<HTMLElement>('.run-detail-tabs')!)
      .getAllByRole('button')
      .filter((b) => !b.classList.contains('run-detail-tabs-back'))
      .map((b) => b.textContent)

  it('a dispatcher with a crew opens on the Crew tab in dispatcher mode and shows the feed', async () => {
    const d = dispatcher()
    const { container } = render(<RunDetail mode="dispatcher" runs={[d, worker()]} hasSnapshot streamConnected now={now} id={d.id} />)

    expect(tabNames(container)).toEqual(['Crew', 'Chat', 'Terminal'])
    expect(screen.getByRole('button', { name: 'Crew' }).getAttribute('aria-pressed')).toBe('true')
    await waitFor(() => expect(screen.getByText('dispatched a worker')).toBeTruthy())
    expect(container.querySelector('.crew-tab')).toBeTruthy()
  })

  it('a crew route opens the Crew tab too', async () => {
    const d = dispatcher()
    render(<RunDetail mode="dispatcher" runs={[d]} hasSnapshot streamConnected now={now} id={d.id} tab="crew" />)

    expect(screen.getByRole('button', { name: 'Crew' }).getAttribute('aria-pressed')).toBe('true')
    await waitFor(() => expect(screen.getByText('dispatched a worker')).toBeTruthy())
  })

  it('an explicit chat or terminal route still wins on a dispatcher', () => {
    const d = dispatcher({ tmux: { session: 's', window: 0, pane_id: '%1' } })
    const { rerender, container } = render(
      <RunDetail mode="dispatcher" runs={[d]} hasSnapshot streamConnected now={now} id={d.id} tab="chat" />,
    )
    expect(screen.getByRole('button', { name: 'Chat' }).getAttribute('aria-pressed')).toBe('true')
    expect(container.querySelector('.crew-tab')).toBeNull()

    rerender(<RunDetail mode="dispatcher" runs={[d]} hasSnapshot streamConnected now={now} id={d.id} tab="terminal" />)
    expect(screen.getByRole('button', { name: 'Terminal' }).getAttribute('aria-pressed')).toBe('true')
    expect(container.querySelector('.crew-tab')).toBeNull()
  })

  it('has no Crew tab for a worker, a solo run, a dispatcher without a crew, or outside dispatcher mode', () => {
    const cases: [Run, 'dispatcher' | 'tmux' | null][] = [
      [worker(), 'dispatcher'],
      [run({ id: 's' }), 'dispatcher'],
      [dispatcher({ crew: undefined }), 'dispatcher'],
      [dispatcher({ crew: { name: '' } }), 'dispatcher'],
      [dispatcher(), 'tmux'],
      [dispatcher(), null],
    ]
    for (const [r, mode] of cases) {
      const { unmount } = render(<RunDetail mode={mode} runs={[r]} hasSnapshot streamConnected now={now} id={r.id} />)
      expect(screen.queryByRole('button', { name: 'Crew' })).toBeNull()
      unmount()
    }
    expect(vi.mocked(fetch).mock.calls.some((c) => String(c[0]).includes('/crew/feed'))).toBe(false)
  })

  it('a crew route on a run that does not qualify degrades to the default tab', () => {
    const w = worker()
    const { container } = render(<RunDetail mode="dispatcher" runs={[w]} hasSnapshot streamConnected now={now} id={w.id} tab="crew" />)

    expect(container.querySelector('.crew-tab')).toBeNull()
    expect(container.querySelector('.run-status-card')).toBeTruthy()
  })
})
