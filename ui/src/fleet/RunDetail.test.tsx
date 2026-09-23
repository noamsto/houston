import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, cleanup, fireEvent, render, screen, within } from '@testing-library/react'
import { RunDetail } from './RunDetail'
import type { Run } from '../api/runs'
import type { Terminal } from '@xterm/xterm'

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
    render(<RunDetail runs={[]} hasSnapshot={false} streamConnected now={now} id="pane-1" tab="activity" />)
    expect(screen.getByText(/loading run/i)).toBeTruthy()
    expect(screen.queryByText(/no longer available/i)).toBeNull()
  })

  it('shows "not found" once a snapshot has arrived and the id is not in it', () => {
    render(<RunDetail runs={[]} hasSnapshot streamConnected now={now} id="pane-1" tab="activity" />)
    expect(screen.getByText(/no longer available/i)).toBeTruthy()
  })

  it('falls back to the Activity tab when the deep-linked run has no terminal capability', () => {
    const r = run({ caps: { terminal: false, reply: true, kill: true }, activity: { tool: 'edit', hint: 'RunDetail.tsx' } })
    render(<RunDetail runs={[r]} hasSnapshot streamConnected now={now} id={r.id} tab="terminal" />)

    // Activity content renders...
    expect(screen.getByText('edit · RunDetail.tsx')).toBeTruthy()
    // ...and no Terminal tab/placeholder is offered.
    expect(screen.queryByText(/terminal/i)).toBeNull()
  })

  it('renders the Terminal tab button and placeholder when the run has terminal capability', () => {
    const r = run({ caps: { terminal: true, reply: true, kill: true } })
    render(<RunDetail runs={[r]} hasSnapshot streamConnected now={now} id={r.id} tab="terminal" />)
    expect(screen.getByText('Terminal')).toBeTruthy()
    expect(screen.getByText(/coming soon/i)).toBeTruthy()
  })

  it('shows project and branch separately in the header, not the worktree-dir repo slug twice', () => {
    const r = run({ project: 'houston', repo: 'feat-97-dogfood-houston-as-a-phone-user-and-file', branch: 'feat/97-dogfood-houston-as-a-phone-user-and-file' })
    render(<RunDetail runs={[r]} hasSnapshot streamConnected now={now} id={r.id} tab="activity" />)
    expect(screen.getByText('houston')).toBeTruthy()
    expect(screen.getByText('feat/97-dogfood-houston-as-a-phone-user-and-file')).toBeTruthy()
    expect(screen.queryByText(/feat-97-dogfood-houston-as-a-phone-user-and-file\/feat/)).toBeNull()
  })

  it('shows the agent chip in the header', () => {
    const r = run({ agent: 'pi' })
    render(<RunDetail runs={[r]} hasSnapshot streamConnected now={now} id={r.id} tab="activity" />)
    expect(screen.getByText('pi')).toBeTruthy()
  })
})

describe('RunDetail terminal lifecycle', () => {
  it('renders a live TerminalPane when the run has terminal capability and tmux data', () => {
    const r = liveRun()
    render(<RunDetail runs={[r]} hasSnapshot streamConnected now={now} id={r.id} tab="terminal" />)
    expect(screen.queryByText(/coming soon/i)).toBeNull()
    expect(screen.queryByText(/session ended/i)).toBeNull()
  })

  it('addresses the terminal socket at the run, not the pane', () => {
    const r = liveRun()
    render(<RunDetail runs={[r]} hasSnapshot streamConnected now={now} id={r.id} tab="terminal" />)
    expect(lastSocketPath).toBe(`/api/runs/${r.id}/terminal`)
  })

  it('case 1: shows "session ended" (not a silent Activity fallback) when caps.terminal drops after going live, and unmounts the terminal', () => {
    const r = liveRun()
    const { rerender } = render(<RunDetail runs={[r]} hasSnapshot streamConnected now={now} id={r.id} tab="terminal" />)
    expect(screen.queryByText(/session ended/i)).toBeNull()

    const dead = liveRun({ caps: { terminal: false, reply: true, kill: true } })
    rerender(<RunDetail runs={[dead]} hasSnapshot streamConnected now={now} id={dead.id} tab="terminal" />)

    expect(screen.getByText(/session ended/i)).toBeTruthy()
    expect(screen.getByRole('button', { name: /reconnect/i })).toBeTruthy()
    // Not a silent fallback to Activity: the run's own activity content isn't shown here.
    expect(screen.queryByText(/no longer available/i)).toBeNull()
  })

  it('case 2: shows "no longer available" when a run with a live terminal is evicted', () => {
    const r = liveRun()
    const { rerender } = render(<RunDetail runs={[r]} hasSnapshot streamConnected now={now} id={r.id} tab="terminal" />)
    expect(screen.queryByText(/session ended/i)).toBeNull()

    rerender(<RunDetail runs={[]} hasSnapshot streamConnected now={now} id={r.id} tab="terminal" />)

    expect(screen.getByText(/no longer available/i)).toBeTruthy()
  })

  it('case 3: a single brief pane-socket disconnect does not end the session', () => {
    vi.useFakeTimers()
    try {
      const r = liveRun()
      mockConnected = false
      const { rerender } = render(<RunDetail runs={[r]} hasSnapshot streamConnected now={now} id={r.id} tab="terminal" />)

      act(() => { vi.advanceTimersByTime(3000) })
      mockConnected = true
      rerender(<RunDetail runs={[r]} hasSnapshot streamConnected now={now} id={r.id} tab="terminal" />)

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
      render(<RunDetail runs={[r]} hasSnapshot streamConnected now={now} id={r.id} tab="terminal" />)

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
    render(<RunDetail runs={[r]} hasSnapshot streamConnected now={now} id={r.id} tab="terminal" />)

    expect(screen.getByText(/session ended/i)).toBeTruthy()
    expect(screen.getByText('tmux server changed')).toBeTruthy()
  })

  it('case 4: a stale SSE stream shows a reconnecting indicator but keeps the terminal mounted', () => {
    const r = liveRun()
    render(<RunDetail runs={[r]} hasSnapshot streamConnected={false} now={now} id={r.id} tab="terminal" />)

    expect(screen.getByRole('status')).toBeTruthy()
    expect(screen.getByText(/reconnecting/i)).toBeTruthy()
    expect(screen.queryByText(/session ended/i)).toBeNull()
  })

  it('recovers to a live terminal once caps.terminal flips back true after ending', () => {
    const r = liveRun()
    const { rerender } = render(<RunDetail runs={[r]} hasSnapshot streamConnected now={now} id={r.id} tab="terminal" />)

    const dead = liveRun({ caps: { terminal: false, reply: true, kill: true } })
    rerender(<RunDetail runs={[dead]} hasSnapshot streamConnected now={now} id={dead.id} tab="terminal" />)
    expect(screen.getByText(/session ended/i)).toBeTruthy()

    rerender(<RunDetail runs={[r]} hasSnapshot streamConnected now={now} id={r.id} tab="terminal" />)
    expect(screen.queryByText(/session ended/i)).toBeNull()
  })

  it('unmounts TerminalPane (rather than hiding it) when the tab switches away from terminal', () => {
    const r = liveRun()
    const { rerender } = render(<RunDetail runs={[r]} hasSnapshot streamConnected now={now} id={r.id} tab="terminal" />)
    expect(screen.queryByText(/coming soon/i)).toBeNull()

    rerender(<RunDetail runs={[r]} hasSnapshot streamConnected now={now} id={r.id} tab="activity" />)
    // Activity content is shown in place of the terminal.
    expect(screen.getByText(new RegExp(r.state))).toBeTruthy()

    // Switching back doesn't read as "ended" — the tab switch alone must not
    // have flagged the pane as dead.
    rerender(<RunDetail runs={[r]} hasSnapshot streamConnected now={now} id={r.id} tab="terminal" />)
    expect(screen.queryByText(/session ended/i)).toBeNull()
  })

  it('desktop: hides the classic PaneHeader (RunDetail supplies its own header/tabs)', () => {
    const r = liveRun()
    const { container } = render(<RunDetail runs={[r]} hasSnapshot streamConnected now={now} id={r.id} tab="terminal" />)

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
    render(<RunDetail runs={[r]} hasSnapshot streamConnected now={now} id={r.id} tab="terminal" />)

    const term = await lastTerminalInstance()
    act(() => {
      term.input('a')
    })
    expect(sendInput).toHaveBeenCalledWith('a')
  })

  it('mobile: MobileInputBar renders and sends via fetch, not sendInput', async () => {
    desktop = false
    const r = liveRun()
    render(<RunDetail runs={[r]} hasSnapshot streamConnected now={now} id={r.id} tab="terminal" />)

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
      <RunDetail runs={[r]} hasSnapshot streamConnected now={now} id={r.id} tab="terminal" onBack={onBack} />,
    )

    fireEvent.click(
      within(container.querySelector<HTMLElement>('.run-detail-tabs')!).getByRole('button', { name: /back to fleet/i }),
    )

    expect(onBack).toHaveBeenCalled()
  })
})

describe('RunDetail activity timeline', () => {
  const chips = (n: number) => Array.from({ length: n }, (_, i) => ({ tool: `tool${i}`, hint: `hint${i}`, done: true }))
  const renderActivity = (p: Partial<Run>) => {
    const r = run(p)
    return render(<RunDetail runs={[r]} hasSnapshot streamConnected now={now} id={r.id} tab="activity" />)
  }
  const rowTools = (c: HTMLElement) => Array.from(c.querySelectorAll('.activity-tool')).map((e) => e.textContent)

  it('renders the trail oldest first with the newest last', () => {
    const { container } = renderActivity({ activity: { trail: chips(3) } })
    expect(rowTools(container)).toEqual(['tool0', 'tool1', 'tool2'])
  })

  it('shows the last 10 rows and reveals earlier ones on demand', () => {
    const { container } = renderActivity({ activity: { trail: chips(15) } })
    expect(rowTools(container)).toEqual(chips(15).slice(5).map((t) => t.tool))

    fireEvent.click(screen.getByRole('button', { name: /show earlier \(5\)/i }))
    expect(rowTools(container)).toHaveLength(15)
    expect(screen.queryByRole('button', { name: /show earlier/i })).toBeNull()
  })

  it('collapses consecutive repeats of a tool into one row with a count', () => {
    const { container } = renderActivity({
      activity: { trail: [
        { tool: 'read', hint: 'a', done: true },
        { tool: 'read', hint: 'b', done: true },
        { tool: 'read', hint: 'c', done: true },
        { tool: 'edit', hint: 'd', done: true },
      ] },
    })
    expect(rowTools(container)).toEqual(['read', 'edit'])
    expect(screen.getByText('×3')).toBeTruthy()
    expect(screen.getByText('c')).toBeTruthy()
    expect(screen.queryByText('a')).toBeNull()
  })

  it('highlights an error row and does not merge it into its neighbours', () => {
    const { container } = renderActivity({
      activity: { trail: [
        { tool: 'bash', hint: 'ok', done: true },
        { tool: 'bash', hint: 'boom', done: true, error: true },
        { tool: 'bash', hint: 'ok2', done: true },
      ] },
    })
    const rows = container.querySelectorAll('.activity-row')
    expect(rows).toHaveLength(3)
    expect(rows[1].classList.contains('error')).toBe(true)
    expect(rows[0].classList.contains('error')).toBe(false)
  })

  it('pins the question after the timeline, outside the scroller', () => {
    const { container } = renderActivity({
      activity: { trail: chips(2) },
      question: { text: 'Deploy to prod?', via: 'pane' },
    })
    const question = screen.getByText('Deploy to prod?')
    const timeline = container.querySelector('.activity-timeline') as HTMLElement
    expect(timeline.contains(question)).toBe(false)
    expect(timeline.compareDocumentPosition(question) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
  })

  it('clicking Reply in Terminal navigates to the terminal tab for a pane-sourced question', () => {
    renderActivity({
      state: 'blocked',
      activity: { message: 'Deploy to prod?' },
      question: { text: 'Deploy to prod?', via: 'pane' },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Reply in Terminal' }))
    expect(window.location.hash).toBe('#/fleet/pane-1/terminal')
  })

  it('shows the crew reply composer for a crew-sourced question', () => {
    renderActivity({
      state: 'blocked',
      activity: { message: 'Deploy to prod?' },
      question: { text: 'Deploy to prod?', via: 'crew' },
    })
    expect(screen.getByLabelText('Reply to the crew')).toBeTruthy()
  })

  it('shows no reply affordance for a watchdog-sourced question', () => {
    renderActivity({
      state: 'blocked',
      activity: { message: 'Checking in — no reply needed.' },
      question: { text: 'Checking in — no reply needed.', via: 'watchdog' },
    })
    expect(screen.getByText('Checking in — no reply needed.')).toBeTruthy()
    expect(screen.queryByRole('button', { name: 'Reply in Terminal' })).toBeNull()
    expect(screen.queryByLabelText('Reply to the crew')).toBeNull()
  })

  it('renders a duplicated blocked message exactly once in the Activity tab', () => {
    const { container } = renderActivity({
      state: 'blocked',
      activity: { message: 'Deploy to prod?' },
      question: { text: 'Deploy to prod?', via: 'pane' },
    })
    expect(container.querySelector('.activity-message')).toBeNull()
    expect(screen.getAllByText('Deploy to prod?')).toHaveLength(1)
  })

  it('keeps the terminal excerpt collapsed until toggled', () => {
    renderActivity({ activity: { preview: 'raw terminal text' } })
    expect(screen.queryByText('raw terminal text')).toBeNull()

    const toggle = screen.getByRole('button', { name: /terminal excerpt/i })
    expect(toggle.getAttribute('aria-expanded')).toBe('false')
    fireEvent.click(toggle)
    expect(screen.getByText('raw terminal text')).toBeTruthy()
    expect(toggle.getAttribute('aria-expanded')).toBe('true')
  })

  it('omits the terminal excerpt toggle when there is no preview', () => {
    renderActivity({ activity: { message: 'hi' } })
    expect(screen.queryByRole('button', { name: /terminal excerpt/i })).toBeNull()
  })

  it('clamps the last message and expands it on tap', () => {
    renderActivity({ activity: { message: 'a long assistant message' } })
    const message = screen.getByRole('button', { name: 'a long assistant message' })
    expect(message.getAttribute('aria-expanded')).toBe('false')
    fireEvent.click(message)
    expect(message.getAttribute('aria-expanded')).toBe('true')
    expect(message.classList.contains('open')).toBe(true)
  })

  it('shows the turn and age in the glance row', () => {
    renderActivity({ activity: { turn: 7 }, updated_at: Math.floor(now / 1000) - 120 })
    expect(screen.getByText('Turn 7')).toBeTruthy()
    const glance = document.querySelector('.activity-glance') as HTMLElement
    expect(glance.textContent).toContain('2m')
  })

  describe('latest pill', () => {
    const proto = HTMLElement.prototype
    const saved = {
      scrollHeight: Object.getOwnPropertyDescriptor(proto, 'scrollHeight'),
      clientHeight: Object.getOwnPropertyDescriptor(proto, 'clientHeight'),
    }
    beforeEach(() => {
      Object.defineProperty(proto, 'scrollHeight', { configurable: true, get: () => 1000 })
      Object.defineProperty(proto, 'clientHeight', { configurable: true, get: () => 200 })
    })
    afterEach(() => {
      for (const key of ['scrollHeight', 'clientHeight'] as const) {
        const d = saved[key]
        if (d) Object.defineProperty(proto, key, d)
        else delete (proto as unknown as Record<string, unknown>)[key]
      }
    })

    it('appears after scrolling up and scrolls back to the bottom on tap', () => {
      const { container } = renderActivity({ activity: { trail: chips(3) } })
      const timeline = container.querySelector('.activity-timeline') as HTMLElement
      expect(screen.queryByRole('button', { name: /latest/i })).toBeNull()

      timeline.scrollTop = 100
      fireEvent.scroll(timeline)
      const pill = screen.getByRole('button', { name: /latest/i })
      expect(timeline.contains(pill)).toBe(false)

      fireEvent.click(pill)
      expect(timeline.scrollTop).toBe(1000)
      expect(screen.queryByRole('button', { name: /latest/i })).toBeNull()
    })

    it('does not yank the view down on new events once the user scrolled up', () => {
      const r = run({ activity: { trail: chips(3) } })
      const { container, rerender } = render(<RunDetail runs={[r]} hasSnapshot streamConnected now={now} id={r.id} tab="activity" />)
      const timeline = container.querySelector('.activity-timeline') as HTMLElement
      timeline.scrollTop = 100
      fireEvent.scroll(timeline)

      const next = run({ activity: { trail: chips(4) } })
      rerender(<RunDetail runs={[next]} hasSnapshot streamConnected now={now} id={next.id} tab="activity" />)
      expect(timeline.scrollTop).toBe(100)
    })

    it('resets to the bottom when the shown run changes', () => {
      const a = run({ id: 'a', activity: { trail: chips(3) } })
      const b = run({ id: 'b', activity: { trail: chips(3) } })
      const { container, rerender } = render(<RunDetail runs={[a, b]} hasSnapshot streamConnected now={now} id="a" tab="activity" />)
      const timeline = container.querySelector('.activity-timeline') as HTMLElement
      timeline.scrollTop = 100
      fireEvent.scroll(timeline)
      expect(screen.getByRole('button', { name: /latest/i })).toBeTruthy()

      rerender(<RunDetail runs={[a, b]} hasSnapshot streamConnected now={now} id="b" tab="activity" />)
      expect(screen.queryByRole('button', { name: /latest/i })).toBeNull()
    })
  })
})
