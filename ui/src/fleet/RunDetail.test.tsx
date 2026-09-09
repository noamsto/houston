import { afterEach, describe, expect, it, vi } from 'vitest'
import { act, cleanup, render, screen } from '@testing-library/react'
import { RunDetail } from './RunDetail'
import type { Run } from '../api/runs'

const now = 1_800_000_000_000 // fixed ms

let mockConnected = true
vi.mock('../hooks/usePaneSocket', () => ({
  usePaneSocket: () => ({ connected: mockConnected, sendInput: vi.fn(), sendResize: vi.fn() }),
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

afterEach(() => {
  cleanup()
  mockConnected = true
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
})

describe('RunDetail terminal lifecycle', () => {
  it('renders a live TerminalPane when the run has terminal capability and tmux data', () => {
    const r = liveRun()
    render(<RunDetail runs={[r]} hasSnapshot streamConnected now={now} id={r.id} tab="terminal" />)
    expect(screen.queryByText(/coming soon/i)).toBeNull()
    expect(screen.queryByText(/session ended/i)).toBeNull()
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
})
