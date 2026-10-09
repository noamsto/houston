import { afterEach, describe, expect, it } from 'vitest'
import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { FleetView } from './FleetView'
import type { Run } from '../api/runs'

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

const runs = [
  run({ id: 'w1', project: 'houston', branch: 'w-one', role: 'worker', state: 'blocked' }),
  run({ id: 'o1', since: 3, project: 'other', branch: 'o-one' }),
  run({ id: 'd', since: 2, project: 'houston', branch: 'main', role: 'dispatcher' }),
  run({ id: 'w2', since: 1, project: 'houston', branch: 'w-two', role: 'worker' }),
]

afterEach(() => {
  cleanup()
  localStorage.clear()
})

function cardOrder(container: HTMLElement): string[] {
  return Array.from(container.querySelectorAll('.run-name')).map((e) => e.textContent ?? '')
}

describe('FleetView project grouping', () => {
  it('is flat by default with the project chip on every card and no headers', () => {
    const { container } = render(<FleetView runs={runs} connected now={now} />)
    expect(container.querySelectorAll('.fleet-project-group').length).toBe(0)
    expect(Array.from(container.querySelectorAll('.run-project')).map((e) => e.textContent))
      .toEqual(['houston', 'other', 'houston', 'houston'])
    expect(cardOrder(container)).toEqual(['w-one', 'o-one', 'main', 'w-two'])
    expect(screen.getByRole('button', { name: 'Group by project' }).getAttribute('aria-pressed')).toBe('false')
  })

  it('groups by project with counts, attention and the dispatcher first', () => {
    const { container } = render(<FleetView runs={runs} connected now={now} />)
    fireEvent.click(screen.getByRole('button', { name: 'Group by project' }))

    expect(screen.getByRole('button', { name: 'Group by project' }).getAttribute('aria-pressed')).toBe('true')
    const headers = Array.from(container.querySelectorAll('.fleet-project-group'))
    expect(headers.map((h) => h.querySelector('.fleet-project-name')?.textContent)).toEqual(['houston', 'other'])
    expect(headers[0].textContent).toContain('3')
    expect(headers[0].textContent).toContain('1 need you')
    expect(headers[1].textContent).not.toContain('need you')
    expect(cardOrder(container)).toEqual(['main', 'w-one', 'w-two', 'o-one'])
  })

  it('shows the crew summary on the dispatcher card from unfiltered runs', () => {
    const { container } = render(<FleetView runs={runs} connected now={now} />)
    expect(container.querySelector('.run-crew')?.textContent).toBe('2 workers · 1 blocked')
  })

  it('remembers the choice across a re-mount', () => {
    const first = render(<FleetView runs={runs} connected now={now} />)
    fireEvent.click(screen.getByRole('button', { name: 'Group by project' }))
    first.unmount()

    const { container } = render(<FleetView runs={runs} connected now={now} />)
    expect(container.querySelectorAll('.fleet-project-group').length).toBe(2)
  })
})

describe('FleetView attention filters', () => {
  const mixed = [
    run({ id: 'b-fresh', branch: 'blocked-fresh', state: 'blocked' }),
    run({ id: 'b-stale', branch: 'blocked-stale', state: 'blocked', updated_at: nowSec - 2 * 3600 }),
    run({ id: 's', branch: 'stuck-one', state: 'idle', attention: 'stuck', attention_note: 'no output for 20 min' }),
    run({ id: 'e', branch: 'ended-one', state: 'done' }),
  ]

  it('counts only fresh blocked runs in the header badge', () => {
    render(<FleetView runs={mixed} connected now={now} />)
    expect(screen.getByRole('button', { name: '1 needs you' })).toBeTruthy()
  })

  it('offers Active, Needs you, Stuck, Done and All', () => {
    const { container } = render(<FleetView runs={mixed} connected now={now} />)
    const nav = container.querySelector('.fleet-filters') as HTMLElement
    expect(Array.from(nav.querySelectorAll('button')).map((b) => b.textContent))
      .toEqual(['Active', 'Needs you', 'Stuck', 'Done', 'All'])
  })

  it('Stuck shows only the stuck card', () => {
    const { container } = render(<FleetView runs={mixed} connected now={now} />)
    fireEvent.click(screen.getByRole('button', { name: 'Stuck' }))
    expect(cardOrder(container)).toEqual(['stuck-one'])
  })

  it('keeps a fresh ended run out of Active and in All', () => {
    const { container } = render(<FleetView runs={mixed} connected now={now} />)
    expect(cardOrder(container)).not.toContain('ended-one')
    fireEvent.click(screen.getByRole('button', { name: 'All' }))
    expect(cardOrder(container)).toContain('ended-one')
  })
})
