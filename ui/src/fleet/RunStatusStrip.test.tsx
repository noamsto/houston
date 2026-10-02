import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { RunQuestion, RunStatusStrip } from './RunStatusStrip'
import { agoLabel } from './format'
import type { Run } from '../api/runs'

vi.mock('../api/runs', () => ({ replyRun: vi.fn() }))

const now = 1_800_000_000_000 // fixed ms

function run(p: Partial<Run> = {}): Run {
  return {
    id: 'pane-1', agent: 'claude', state: 'running',
    activity: {}, tokens: { input: 0, output: 0 },
    caps: { terminal: true, reply: true, kill: true },
    updated_at: Math.floor(now / 1000),
    ...p,
  } as Run
}

afterEach(cleanup)

describe('RunStatusStrip line 1', () => {
  it('shows tool, hint, turn and age for a running run', () => {
    const r = run({ updated_at: Math.floor(now / 1000) - 120, activity: { tool: 'Edit', hint: 'a.ts', turn: 3 } })
    render(<RunStatusStrip run={r} now={now} />)
    expect(screen.getByText('Edit')).toBeTruthy()
    expect(screen.getByText(/a\.ts/)).toBeTruthy()
    expect(screen.getByText('Turn 3')).toBeTruthy()
    expect(screen.getByText(agoLabel(r.updated_at, now))).toBeTruthy()
  })

  it('marks a fresh blocked run as needs-you and a stale one as muted', () => {
    const { container, rerender } = render(<RunStatusStrip run={run({ state: 'blocked' })} now={now} />)
    const label = screen.getByText('needs you')
    expect(label.className).toContain('needs-you')
    expect(label.className).not.toContain('muted')

    rerender(<RunStatusStrip run={run({ state: 'blocked', updated_at: Math.floor(now / 1000) - 7200 })} now={now} />)
    expect(container.querySelector('.run-status-state')?.className).toContain('needs-you muted')
  })

  it('shows no tool or hint on a blocked run', () => {
    const r = run({ state: 'blocked', activity: { tool: 'Edit', hint: 'a.ts' } })
    render(<RunStatusStrip run={r} now={now} />)
    expect(screen.queryByText('Edit')).toBeNull()
    expect(screen.queryByText(/a\.ts/)).toBeNull()
  })
})

describe('RunStatusStrip crew line', () => {
  const crew = { name: 'c', codename: 'fern', tier: 'standard', model: 'opus', detail: 'plan' }

  it('shows codename, tier · agent · model and detail for a worker', () => {
    render(<RunStatusStrip run={run({ crew, role: 'worker' })} now={now} />)
    expect(screen.getByText('fern')).toBeTruthy()
    expect(screen.getByText('standard · claude · opus')).toBeTruthy()
    expect(screen.getByText('plan')).toBeTruthy()
  })

  it('renders no crew line for a solo run', () => {
    const { container } = render(<RunStatusStrip run={run()} now={now} />)
    expect(container.querySelector('.run-status-crew')).toBeNull()
  })

  it('hides the codename for a dispatcher', () => {
    render(<RunStatusStrip run={run({ crew, role: 'dispatcher' })} now={now} />)
    expect(screen.queryByText('fern')).toBeNull()
    expect(screen.getByText('standard · claude · opus')).toBeTruthy()
  })

  it('omits crew.detail when it repeats the question', () => {
    const r = run({ state: 'blocked', crew: { ...crew, detail: 'pick one' }, question: { text: 'pick one', via: 'crew' } })
    render(<RunStatusStrip run={r} now={now} />)
    expect(screen.queryByText('pick one')).toBeNull()
  })
})

describe('RunQuestion', () => {
  it('renders nothing without a question', () => {
    const { container } = render(<RunQuestion run={run()} />)
    expect(container.firstChild).toBeNull()
  })

  it('shows the text and a reply composer for a blocked crew question', () => {
    render(<RunQuestion run={run({ state: 'blocked', question: { text: 'which db?', via: 'crew' } })} />)
    expect(screen.getByText('which db?')).toBeTruthy()
    expect(screen.getByLabelText(/reply to the crew/i)).toBeTruthy()
  })

  it('offers Reply in Terminal for a blocked pane question and jumps to the terminal tab', () => {
    render(<RunQuestion run={run({ state: 'blocked', question: { text: 'ok?', via: 'pane' } })} />)
    fireEvent.click(screen.getByRole('button', { name: 'Reply in Terminal' }))
    expect(window.location.hash).toBe('#/fleet/pane-1/terminal')
  })

  it('offers no reply button for a pane question without a terminal', () => {
    const r = run({ state: 'blocked', caps: { terminal: false, reply: false, kill: false }, question: { text: 'ok?', via: 'pane' } })
    render(<RunQuestion run={r} />)
    expect(screen.getByText('ok?')).toBeTruthy()
    expect(screen.queryByRole('button')).toBeNull()
  })

  it('offers no reply affordance for a watchdog question', () => {
    render(<RunQuestion run={run({ state: 'blocked', question: { text: 'stalled', via: 'watchdog' } })} />)
    expect(screen.getByText('stalled')).toBeTruthy()
    expect(screen.queryByRole('button')).toBeNull()
    expect(screen.queryByRole('textbox')).toBeNull()
  })

  it('shows only the text when the run is not blocked', () => {
    render(<RunQuestion run={run({ question: { text: 'was asked', via: 'crew' } })} />)
    expect(screen.getByText('was asked')).toBeTruthy()
    expect(screen.queryByRole('button')).toBeNull()
    expect(screen.queryByRole('textbox')).toBeNull()
  })
})

describe('RunStatusStrip background tasks', () => {
  it('lists each task with its hint and age', () => {
    const since = Math.floor(now / 1000) - 180
    render(
      <RunStatusStrip
        run={run({ background: [{ id: 'a', kind: 'shell', hint: 'Sleep five minutes', since }, { id: 'b', kind: 'monitor', hint: 'CI checks' }] })}
        now={now}
      />,
    )
    const items = screen.getByLabelText('Background tasks').querySelectorAll('li')
    expect(items).toHaveLength(2)
    expect(items[0].textContent).toContain('shell')
    expect(items[0].textContent).toContain('Sleep five minutes')
    expect(items[0].textContent).toContain(agoLabel(since, now))
    expect(items[1].textContent).toContain('CI checks')
  })

  it('renders no list without background tasks', () => {
    render(<RunStatusStrip run={run()} now={now} />)
    expect(screen.queryByLabelText('Background tasks')).toBeNull()
  })
})
