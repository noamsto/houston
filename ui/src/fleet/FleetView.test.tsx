import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, cleanup, fireEvent, render, screen, within } from '@testing-library/react'
import { FleetView } from './FleetView'
import { resetCrewReposCache } from './useCrewRepos'
import { fetchDispatchOptions } from '../api/dispatch'
import { COLLAPSE_OVER, MEMBER_CAP } from './fleetEntries'
import { needsYou } from './staleness'
import type { Run } from '../api/runs'

vi.mock('../api/dispatch', () => ({ fetchDispatchOptions: vi.fn() }))

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

beforeEach(() => {
  resetCrewReposCache()
  vi.mocked(fetchDispatchOptions).mockReset()
  vi.mocked(fetchDispatchOptions).mockResolvedValue({ repos: [] } as never)
})

afterEach(() => {
  cleanup()
  localStorage.clear()
})

function cardOrder(container: HTMLElement): string[] {
  return Array.from(container.querySelectorAll('.run-name')).map((e) => e.textContent ?? '')
}

describe('FleetView project grouping', () => {
  it('is flat by default with the project chip on every card and no headers', () => {
    const { container } = render(<FleetView runs={runs} connected now={now} mode="dispatcher" />)
    expect(container.querySelectorAll('.fleet-project-group').length).toBe(0)
    expect(Array.from(container.querySelectorAll('.run-project')).map((e) => e.textContent))
      .toEqual(['houston', 'other', 'houston', 'houston'])
    expect(cardOrder(container)).toEqual(['w-one', 'o-one', 'main', 'w-two'])
    expect(screen.getByRole('button', { name: 'Group by project' }).getAttribute('aria-pressed')).toBe('false')
  })

  it('groups plain runs by project, ranked within the group as in the flat list', () => {
    const { container } = render(<FleetView runs={runs} connected now={now} mode="dispatcher" />)
    fireEvent.click(screen.getByRole('button', { name: 'Group by project' }))

    const headers = Array.from(container.querySelectorAll('.fleet-project-group'))
    expect(headers[0].textContent).toContain('3')
    expect(headers[0].textContent).toContain('1 need you')
    expect(cardOrder(container)).toEqual(['w-one', 'main', 'w-two', 'o-one'])
  })

  it('groups by project with counts, attention and the dispatcher first', () => {
    const crew = { name: '1700000000-11' }
    const joined = [
      run({ id: 'w1', project: 'houston', branch: 'w-one', role: 'worker', state: 'blocked', crew }),
      run({ id: 'o1', since: 3, project: 'other', branch: 'o-one' }),
      run({ id: 'd', since: 2, project: 'houston', branch: 'main', role: 'dispatcher', crew }),
      run({ id: 'w2', since: 1, project: 'houston', branch: 'w-two', role: 'worker', crew }),
    ]
    const { container } = render(<FleetView runs={joined} connected now={now} mode="dispatcher" />)
    fireEvent.click(screen.getByRole('button', { name: 'Group by project' }))

    expect(screen.getByRole('button', { name: 'Group by project' }).getAttribute('aria-pressed')).toBe('true')
    const headers = Array.from(container.querySelectorAll('.fleet-project-group'))
    expect(headers.map((h) => h.querySelector('.fleet-project-name')?.textContent)).toEqual(['houston', 'other'])
    expect(headers[0].textContent).toContain('3')
    expect(headers[0].textContent).toContain('1 need you')
    expect(headers[1].textContent).not.toContain('need you')
    expect(cardOrder(container)).toEqual(['main', 'o-one'])
  })

  it('remembers the choice across a re-mount', () => {
    const first = render(<FleetView runs={runs} connected now={now} mode="dispatcher" />)
    fireEvent.click(screen.getByRole('button', { name: 'Group by project' }))
    first.unmount()

    const { container } = render(<FleetView runs={runs} connected now={now} mode="dispatcher" />)
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
    render(<FleetView runs={mixed} connected now={now} mode="dispatcher" />)
    expect(screen.getByRole('button', { name: '1 needs you' })).toBeTruthy()
  })

  it('offers Active, Needs you, Stuck, Done and All', () => {
    const { container } = render(<FleetView runs={mixed} connected now={now} mode="dispatcher" />)
    const nav = container.querySelector('.fleet-filters') as HTMLElement
    expect(Array.from(nav.querySelectorAll('button')).map((b) => b.textContent))
      .toEqual(['Active', 'Needs you', 'Stuck', 'Done', 'All'])
  })

  it('Stuck shows only the stuck card', () => {
    const { container } = render(<FleetView runs={mixed} connected now={now} mode="dispatcher" />)
    fireEvent.click(screen.getByRole('button', { name: 'Stuck' }))
    expect(cardOrder(container)).toEqual(['stuck-one'])
  })

  it('keeps a fresh ended run out of Active and in All', () => {
    const { container } = render(<FleetView runs={mixed} connected now={now} mode="dispatcher" />)
    expect(cardOrder(container)).not.toContain('ended-one')
    fireEvent.click(screen.getByRole('button', { name: 'All' }))
    expect(cardOrder(container)).toContain('ended-one')
  })
})

describe('FleetView dispatcher mode', () => {
  const crew = { name: '1700000000-11' }
  const group = [
    run({ id: 'd', since: 2, project: 'houston', branch: 'main', role: 'dispatcher', state: 'idle', crew: { name: crew.name, codename: 'dispatcher' } }),
    run({ id: 'w1', since: 5, project: 'houston', branch: 'w-one', role: 'worker', state: 'blocked', crew: { ...crew, codename: 'blush', title: 'Fix the thing', tier: 'standard', model: 'sonnet' } }),
    run({ id: 'w2', since: 4, project: 'houston', branch: 'w-two', role: 'worker', crew: { ...crew, codename: 'amber' } }),
    run({ id: 'solo', since: 1, project: 'other', branch: 'o-one' }),
  ]

  function names(container: HTMLElement): string[] {
    return Array.from(container.querySelectorAll('.run-name')).map((e) => e.textContent ?? '')
  }

  it('nests the workers under the dispatcher card with the exact crew line and a need-you badge', () => {
    const { container } = render(<FleetView runs={group} connected now={now} mode="dispatcher" />)

    const card = container.querySelector('.fleet-group-card') as HTMLElement
    expect(card.classList.contains('needs-you')).toBe(true)
    expect(card.querySelector('.run-crew')?.textContent).toBe('2 workers · 1 need you')
    expect(card.querySelector('.fleet-group-badge')?.textContent).toBe('1 need you')
    expect(Array.from(card.querySelectorAll('.worker-row-codename')).map((e) => e.textContent)).toEqual(['blush', 'amber'])
    // Workers are rows, not top-level cards.
    expect(names(container)).toEqual(['main', 'o-one'])
    expect(card.querySelector('.run-card .worker-row')).toBeNull()
  })

  it('leaves the header badge at the flat needs-you count', () => {
    render(<FleetView runs={group} connected now={now} mode="dispatcher" />)
    expect(group.filter((r) => needsYou(r, now)).length).toBe(1)
    expect(screen.getByRole('button', { name: '1 needs you' })).toBeTruthy()
  })

  it('shows no badge and no need-you count while no worker needs you', () => {
    const quiet = group.map((r) => (r.id === 'w1' ? { ...r, state: 'running' as const } : r))
    const { container } = render(<FleetView runs={quiet} connected now={now} mode="dispatcher" />)
    expect(container.querySelector('.fleet-group-badge')).toBeNull()
    expect(container.querySelector('.fleet-group-card')?.classList.contains('needs-you')).toBe(false)
    expect(container.querySelector('.run-crew')?.textContent).toBe('2 workers')
  })

  it('counts the host header and project groups as head plus shown members', () => {
    const { container } = render(<FleetView runs={group} connected now={now} mode="dispatcher" />)
    expect(container.querySelector('.fleet-group')?.textContent).toContain('4')

    fireEvent.click(screen.getByRole('button', { name: 'Group by project' }))
    const headers = Array.from(container.querySelectorAll('.fleet-project-group'))
    expect(headers.map((h) => h.querySelector('.fleet-project-name')?.textContent)).toEqual(['houston', 'other'])
    expect(headers[0].textContent).toContain('3')
    expect(headers[0].textContent).toContain('1 need you')
    expect(headers[1].textContent).toContain('1')
  })

  it('counts only the members a filter shows, but keeps the head', () => {
    const { container } = render(<FleetView runs={group} connected now={now} mode="dispatcher" />)
    fireEvent.click(screen.getByRole('button', { name: 'Needs you' }))
    expect(container.querySelector('.fleet-group')?.textContent).toContain('2')
    expect(container.querySelectorAll('.worker-row').length).toBe(1)
    expect(names(container)).toEqual(['main'])
  })

  it('opens a worker from its row and not from its PR link', () => {
    const onOpen = vi.fn()
    const withPR = group.map((r) => (r.id === 'w2' ? { ...r, pr: { number: '7', url: 'https://example.com/pull/7' } } : r))
    const { container } = render(<FleetView runs={withPR} connected now={now} mode="dispatcher" onOpen={onOpen} />)

    const link = container.querySelector('a.run-chip.pr') as HTMLAnchorElement
    expect(link.closest('button')).toBeNull()
    expect(link.href).toBe('https://example.com/pull/7')
    link.addEventListener('click', (e) => e.preventDefault())
    fireEvent.click(link)
    expect(onOpen).not.toHaveBeenCalled()

    fireEvent.click(within(container).getByText('amber').closest('button') as HTMLElement)
    expect(onOpen).toHaveBeenCalledTimes(1)
    expect(onOpen.mock.calls[0][0].id).toBe('w2')
  })

  describe('collapse', () => {
    const many = (n: number) => [
      run({ id: 'd', since: 100, role: 'dispatcher', project: 'p', branch: 'main', crew: { name: crew.name } }),
      ...Array.from({ length: n }, (_, i) =>
        run({ id: `w${i}`, since: 50 - i, role: 'worker', project: 'p', branch: `b${i}`, crew: { ...crew, codename: `cn${i}` } }),
      ),
    ]

    it('renders every row up to COLLAPSE_OVER', () => {
      const { container } = render(<FleetView runs={many(COLLAPSE_OVER)} connected now={now} mode="dispatcher" />)
      expect(container.querySelectorAll('.worker-row').length).toBe(COLLAPSE_OVER)
      expect(screen.queryByRole('button', { name: /^Show/ })).toBeNull()
    })

    it('renders only MEMBER_CAP rows past it, and toggles the rest', () => {
      const n = COLLAPSE_OVER + 3
      const { container } = render(<FleetView runs={many(n)} connected now={now} mode="dispatcher" />)
      expect(container.querySelectorAll('.worker-row').length).toBe(MEMBER_CAP)
      expect(screen.queryByText('cn' + MEMBER_CAP)).toBeNull()

      fireEvent.click(screen.getByRole('button', { name: `Show ${n - MEMBER_CAP} more` }))
      expect(container.querySelectorAll('.worker-row').length).toBe(n)
      expect(screen.getByText('cn' + MEMBER_CAP)).toBeTruthy()

      fireEvent.click(screen.getByRole('button', { name: 'Show less' }))
      expect(container.querySelectorAll('.worker-row').length).toBe(MEMBER_CAP)
    })
  })

  describe('crew without a dispatcher', () => {
    const orphans = [
      run({ id: 'a', project: 'repo', role: 'worker', branch: 'wa', state: 'blocked', attention: 'needs-you', crew: { name: '1700000000-11-extra-long-id', codename: 'one' } }),
      run({ id: 'b', project: 'repo', role: 'worker', branch: 'wb', crew: { name: '1700000000-11-extra-long-id', codename: 'two' } }),
    ]

    it('renders a header with the short id, counts and a Dispatch here link for a resolvable repo', async () => {
      vi.mocked(fetchDispatchOptions).mockResolvedValue({
        repos: [{ name: 'repo', path: '/g/repo', home: ['1700000000-11-extra-long-id'] }],
      } as never)
      const { container } = render(<FleetView runs={orphans} connected now={now} mode="dispatcher" />)
      await act(async () => {})

      const head = container.querySelector('.fleet-group-head') as HTMLElement
      const id = head.querySelector('.fleet-group-id') as HTMLElement
      expect(id.textContent).toBe('1700000000…')
      expect(id.title).toBe('1700000000-11-extra-long-id')
      expect(head.querySelector('.fleet-group-repo')?.textContent).toBe('repo')
      expect(head.querySelector('.fleet-group-counts')?.textContent).toBe('1 working · 1 needs you')
      const link = within(head).getByRole('link', { name: 'Dispatch here' }) as HTMLAnchorElement
      expect(link.getAttribute('href')).toBe('#/dispatch?repo=%2Fg%2Frepo&crew=1700000000-11-extra-long-id')
      expect(container.querySelectorAll('.worker-row').length).toBe(2)
    })

    it('omits the link when the repo does not resolve', async () => {
      const { container } = render(<FleetView runs={orphans} connected now={now} mode="dispatcher" />)
      await act(async () => {})

      expect(container.querySelector('.fleet-group-repo')?.textContent).toBe('repo')
      expect(screen.queryByRole('link', { name: 'Dispatch here' })).toBeNull()
    })
  })
})

describe('FleetView tmux mode', () => {
  const crew = { name: '1700000000-11' }
  const crewed = [
    run({ id: 'd', since: 2, project: 'houston', branch: 'main', role: 'dispatcher', crew: { name: crew.name } }),
    run({ id: 'w1', since: 5, project: 'houston', branch: 'w-one', role: 'worker', state: 'blocked', crew }),
  ]

  it.each(['tmux', null] as const)('renders the flat list with workers as top-level cards in mode %s', (mode) => {
    const { container } = render(<FleetView runs={crewed} connected now={now} mode={mode} />)

    expect(container.querySelector('.fleet-group-card')).toBeNull()
    expect(container.querySelector('.worker-row')).toBeNull()
    expect(container.querySelector('.run-crew')).toBeNull()
    expect(Array.from(container.querySelectorAll('.run-name')).map((e) => e.textContent)).toEqual(['w-one', 'main'])
    expect(container.querySelectorAll('.run-card').length).toBe(2)
    expect(fetchDispatchOptions).not.toHaveBeenCalled()
  })
})
