import { afterEach, describe, expect, it } from 'vitest'
import { cleanup, render } from '@testing-library/react'
import { DispatcherGroup } from './DispatcherGroup'
import { RunCard } from './RunCard'
import type { FleetEntry } from './fleetEntries'
import type { Run } from '../api/runs'

type DispatcherEntry = Extract<FleetEntry, { kind: 'dispatcher' }>

const now = 1_800_000_000_000 // fixed ms
const nowSec = Math.floor(now / 1000)

function run(p: Partial<Run> = {}): Run {
  return {
    id: 'pane-1', agent: 'claude', state: 'running',
    activity: {}, tokens: { input: 0, output: 0 },
    caps: { terminal: true, reply: true, kill: true },
    updated_at: nowSec,
    ...p,
  } as Run
}

const counts = { working: 0, needsYou: 0, stuck: 0, done: 0, idle: 0, ended: 0 }
const head = run({ id: 'd', role: 'dispatcher', project: 'houston', branch: 'main' })
const worker = run({ id: 'w1', role: 'worker', branch: 'w-one' })

const entry = (p: Partial<DispatcherEntry> = {}): DispatcherEntry => ({
  kind: 'dispatcher', key: head.id, head, headShown: true,
  shown: [], members: [], needsYou: 0, counts, ...p,
})

afterEach(cleanup)

describe('DispatcherGroup', () => {
  it('renders a plain RunCard when no member is shown and none needs you', () => {
    const grouped = render(<DispatcherGroup entry={entry()} now={now} />)
    expect(grouped.container.querySelector('.fleet-group-card')).toBeNull()
    const groupedHtml = grouped.container.innerHTML
    cleanup()
    const plain = render(<RunCard run={head} now={now} selected={false} crewLine={null} />)
    expect(groupedHtml).toBe(plain.container.innerHTML)
  })

  it('keeps the container and badge when zero are shown but a worker needs you', () => {
    const { container } = render(<DispatcherGroup entry={entry({ needsYou: 2 })} now={now} />)
    expect(container.querySelector('.fleet-group-card.needs-you')).not.toBeNull()
    expect(container.querySelector('.fleet-group-badge')?.textContent).toBe('2 need you')
  })

  it('keeps the container with one shown member', () => {
    const { container } = render(
      <DispatcherGroup entry={entry({ shown: [worker], members: [worker] })} now={now} />,
    )
    expect(container.querySelector('.fleet-group-card')).not.toBeNull()
  })
})
