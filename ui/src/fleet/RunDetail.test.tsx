import { afterEach, describe, expect, it } from 'vitest'
import { cleanup, render, screen } from '@testing-library/react'
import { RunDetail } from './RunDetail'
import type { Run } from '../api/runs'

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

afterEach(() => {
  cleanup()
})

describe('RunDetail', () => {
  it('shows a loading state before the first snapshot arrives, even for an id not yet known', () => {
    render(<RunDetail runs={[]} hasSnapshot={false} now={now} id="pane-1" tab="activity" />)
    expect(screen.getByText(/loading run/i)).toBeTruthy()
    expect(screen.queryByText(/no longer available/i)).toBeNull()
  })

  it('shows "not found" once a snapshot has arrived and the id is not in it', () => {
    render(<RunDetail runs={[]} hasSnapshot now={now} id="pane-1" tab="activity" />)
    expect(screen.getByText(/no longer available/i)).toBeTruthy()
  })

  it('falls back to the Activity tab when the deep-linked run has no terminal capability', () => {
    const r = run({ caps: { terminal: false, reply: true, kill: true }, activity: { tool: 'edit', hint: 'RunDetail.tsx' } })
    render(<RunDetail runs={[r]} hasSnapshot now={now} id={r.id} tab="terminal" />)

    // Activity content renders...
    expect(screen.getByText('edit · RunDetail.tsx')).toBeTruthy()
    // ...and no Terminal tab/placeholder is offered.
    expect(screen.queryByText(/terminal/i)).toBeNull()
  })

  it('renders the Terminal tab button and placeholder when the run has terminal capability', () => {
    const r = run({ caps: { terminal: true, reply: true, kill: true } })
    render(<RunDetail runs={[r]} hasSnapshot now={now} id={r.id} tab="terminal" />)
    expect(screen.getByText('Terminal')).toBeTruthy()
    expect(screen.getByText(/coming soon/i)).toBeTruthy()
  })
})
