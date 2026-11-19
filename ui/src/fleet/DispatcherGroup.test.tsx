import { afterEach, describe, expect, it } from 'vitest'
import { cleanup, render } from '@testing-library/react'
import { CrewGroup, DispatcherGroup } from './DispatcherGroup'
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

  it('chips a member working in another repo than the dispatcher', () => {
    const cross = run({ id: 'w2', role: 'worker', project: 'nix-config', branch: 'w-two' })
    const same = run({ id: 'w3', role: 'worker', project: 'houston', branch: 'w-three' })
    const { container } = render(
      <DispatcherGroup entry={entry({ shown: [cross, same], members: [cross, same] })} now={now} />,
    )
    const chips = [...container.querySelectorAll('.worker-row-project')]
    expect(chips.map((c) => c.textContent)).toEqual(['nix-config'])
  })

  it('chips each differing member of an orphan crew against the crew project', () => {
    const a = run({ id: 'a', role: 'worker', project: 'dispatcher', branch: 'a' })
    const b = run({ id: 'b', role: 'worker', project: 'nix-config', branch: 'b' })
    // A peer host keeps CrewGroupHeader from fetching the local repo list.
    const crew: Extract<FleetEntry, { kind: 'crew' }> = {
      kind: 'crew', key: 'crew:peer-a:1-1', crew: '1-1', host: 'peer-a', project: 'dispatcher', shown: [a, b],
      members: [a, b], needsYou: 0, counts,
    }
    const { container } = render(<CrewGroup entry={crew} now={now} />)
    expect([...container.querySelectorAll('.worker-row-project')].map((c) => c.textContent)).toEqual(['nix-config'])
  })
})
