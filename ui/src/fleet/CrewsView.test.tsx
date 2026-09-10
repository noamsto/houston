import { afterEach, describe, expect, it } from 'vitest'
import { cleanup, render, screen } from '@testing-library/react'
import { CrewsView } from './CrewsView'
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

afterEach(cleanup)

describe('CrewsView grouping', () => {
  it('groups two runs with different crew.name into two headers with the right member and blocked counts', () => {
    const runs = [
      run({ id: 'a', crew: { name: 'crew-a' }, state: 'running' }),
      run({ id: 'b', crew: { name: 'crew-b' }, state: 'blocked' }),
    ]
    const { container } = render(<CrewsView runs={runs} now={now} />)

    const headers = container.querySelectorAll('.crews-group')
    expect(headers.length).toBe(2)

    const crewA = Array.from(headers).find((h) => h.getAttribute('title') === 'crew-a')!
    expect(crewA.textContent).toContain('1 member')
    expect(crewA.textContent).not.toContain('blocked')

    const crewB = Array.from(headers).find((h) => h.getAttribute('title') === 'crew-b')!
    expect(crewB.textContent).toContain('1 member')
    expect(crewB.textContent).toContain('1 blocked')
  })

  it('excludes a run with no crew and one with crew.name === "", showing the empty state when that is all there is', () => {
    const runs = [
      run({ id: 'no-crew' }),
      run({ id: 'blank-crew', crew: { name: '' } }),
    ]
    const { container } = render(<CrewsView runs={runs} now={now} />)

    expect(container.querySelectorAll('.crews-group').length).toBe(0)
    expect(screen.getByText(/no crews/i)).toBeTruthy()
  })

  it('renders the tier chip for a member with crew.tier and the PR chip for a member with a pr', () => {
    const runs = [
      run({ id: 'tiered', crew: { name: 'crew-c', tier: 'lead' } }),
      run({ id: 'prd', crew: { name: 'crew-c' }, pr: { number: '42' } }),
    ]
    const { container } = render(<CrewsView runs={runs} now={now} />)

    expect(container.querySelector('.run-chip.tier')?.textContent).toBe('lead')
    expect(container.querySelector('.run-chip.pr')?.textContent).toBe('#42')
  })

  it('renders a composer only for a member whose question.via is "crew"', () => {
    const runs = [
      run({ id: 'crew-q', crew: { name: 'crew-d' }, state: 'blocked', question: { text: 'proceed?', via: 'crew' } }),
      run({ id: 'pane-q', crew: { name: 'crew-d' }, state: 'blocked', question: { text: 'y/n?', via: 'pane' } }),
    ]
    const { container } = render(<CrewsView runs={runs} now={now} />)

    const composers = container.querySelectorAll('.crew-reply')
    expect(composers.length).toBe(1)
  })

  it('orders crew groups: a group with a blocked member first, then by most recent member activity', () => {
    const nowSec = Math.floor(now / 1000)
    const runs = [
      run({ id: 'old', crew: { name: 'crew-old' }, state: 'running', updated_at: nowSec - 100_000 }),
      run({ id: 'recent', crew: { name: 'crew-recent' }, state: 'running', updated_at: nowSec - 10 }),
      run({ id: 'blocked', crew: { name: 'crew-blocked' }, state: 'blocked', updated_at: nowSec }),
    ]
    const { container } = render(<CrewsView runs={runs} now={now} />)

    const order = Array.from(container.querySelectorAll('.crews-group')).map((h) => h.getAttribute('title'))
    expect(order).toEqual(['crew-blocked', 'crew-recent', 'crew-old'])
  })

  it('orders members within a group: blocked first, then by most recently updated', () => {
    const nowSec = Math.floor(now / 1000)
    const runs = [
      run({ id: 'old', crew: { name: 'crew-e' }, state: 'running', updated_at: nowSec - 1_000 }),
      run({ id: 'new', crew: { name: 'crew-e' }, state: 'running', updated_at: nowSec }),
      run({ id: 'blocked', crew: { name: 'crew-e' }, state: 'blocked', updated_at: nowSec - 5 }),
    ]
    const { container } = render(<CrewsView runs={runs} now={now} />)

    const section = container.querySelector('section')!
    const order = Array.from(section.querySelectorAll('.run-name')).map((n) => n.textContent)
    expect(order).toEqual(['blocked', 'new', 'old'])
  })
})
