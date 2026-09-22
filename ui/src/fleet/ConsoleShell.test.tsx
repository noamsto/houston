import { afterEach, describe, expect, it, vi } from 'vitest'
import { act, cleanup, fireEvent, render, screen, within } from '@testing-library/react'
import { ConsoleShell } from './ConsoleShell'
import type { Run } from '../api/runs'

const now = 1_800_000_000_000 // fixed ms
const nowSec = Math.floor(now / 1000)

vi.mock('./useWorkspace', () => ({
  useWorkspace: () => ({ workspace: null, error: null, loading: false }),
}))
vi.mock('../hooks/usePaneSocket', () => ({
  usePaneSocket: () => ({ connected: true, sendInput: vi.fn(), sendResize: vi.fn() }),
}))
// DispatchView is kept mounted like Crews/Workspace, so it fetches on every
// render of this shell — never a real fetch in tests.
vi.mock('../api/dispatch', () => ({
  NEW_CREW: 'new',
  fetchDispatchOptions: () => Promise.resolve({
    repos: [{ path: '/repo', name: 'repo', crews: ['1-1'] }],
    tiers: ['trivial', 'standard', 'deep'],
    efforts: ['low', 'medium', 'high', 'xhigh', 'max'],
    plans: ['required', 'provided'],
    engines: { claude: ['opus', 'sonnet', 'haiku', 'fable'] },
    engine_order: ['claude'],
    tier_models: { claude: { trivial: 'haiku', standard: 'sonnet', deep: 'opus' } },
  }),
  submitDispatch: vi.fn(),
}))

function run(p: Partial<Run> = {}): Run {
  return {
    id: 'pane-1', agent: 'claude', state: 'running',
    activity: {}, tokens: { input: 0, output: 0 },
    caps: { terminal: true, reply: true, kill: true },
    updated_at: nowSec,
    ...p,
  } as Run
}

function cardFor(container: HTMLElement, label: string): HTMLElement {
  return within(container).getByText(label).closest('.run-card') as HTMLElement
}

afterEach(() => {
  cleanup()
  localStorage.clear()
  window.location.hash = ''
})

describe('ConsoleShell layout', () => {
  it('renders the three regions and an empty detail state with an empty hash', () => {
    render(<ConsoleShell runs={[]} connected hasSnapshot now={now} />)
    expect(screen.getByLabelText('rail')).toBeTruthy()
    expect(screen.getByLabelText('list')).toBeTruthy()
    const detail = screen.getByLabelText('detail')
    expect(within(detail).getByText('Select a run')).toBeTruthy()
  })

  it('preselects a run from the hash and marks its card selected', () => {
    window.location.hash = '#/fleet/b'
    const runs = [
      run({ id: 'a', repo: 'repo-a', branch: 'branch-a' }),
      run({ id: 'b', repo: 'repo-b', branch: 'branch-b' }),
    ]
    render(<ConsoleShell runs={runs} connected hasSnapshot now={now} />)

    const detail = screen.getByLabelText('detail')
    expect(within(detail).getByText('branch-b')).toBeTruthy()

    const list = screen.getByLabelText('fleet list')
    expect(cardFor(list, 'branch-b').getAttribute('aria-current')).toBe('true')
  })

  it('clicking a card writes the hash and the detail follows on hashchange', () => {
    const runs = [
      run({ id: 'a', repo: 'repo-a', branch: 'branch-a' }),
      run({ id: 'b', repo: 'repo-b', branch: 'branch-b' }),
    ]
    render(<ConsoleShell runs={runs} connected hasSnapshot now={now} />)

    const list = screen.getByLabelText('fleet list')
    fireEvent.click(cardFor(list, 'branch-a'))
    expect(window.location.hash).toBe('#/fleet/a/activity')

    act(() => { window.dispatchEvent(new HashChangeEvent('hashchange')) })

    const detail = screen.getByLabelText('detail')
    expect(within(detail).getByText('branch-a')).toBeTruthy()
    expect(cardFor(list, 'branch-a').getAttribute('aria-current')).toBe('true')
    expect(cardFor(list, 'branch-b').getAttribute('aria-current')).toBeNull()
  })

  it('shows global rail counts over all runs', () => {
    const runs = [
      run({ id: 'fresh-blocked', state: 'blocked' }),
      run({ id: 'stale-blocked', state: 'blocked', updated_at: nowSec - 2 * 3600 }),
      run({ id: 'stale-done', state: 'done', updated_at: nowSec - 2 * 3600 }),
      run({ id: 'running', state: 'running' }),
    ]
    render(<ConsoleShell runs={runs} connected hasSnapshot now={now} />)

    const rail = screen.getByLabelText('rail')
    expect(within(rail).getByRole('button', { name: '1 needs you' })).toBeTruthy()
    expect(within(rail).getByRole('button', { name: /^Active/ }).querySelector('.console-count')?.textContent).toBe('3')
    expect(within(rail).getByRole('button', { name: /^Needs you/ }).querySelector('.console-count')?.textContent).toBe('2')
    expect(within(rail).getByRole('button', { name: /^All/ }).querySelector('.console-count')?.textContent).toBe('4')
  })

  it('drops a crew from the rail once its only run has aged into history', () => {
    const runs = [
      run({
        id: 'dead',
        crew: { name: 'DEAD' },
        state: 'review',
        updated_at: nowSec - 7 * 24 * 3600,
        caps: { terminal: false, reply: true, kill: false },
      }),
      run({ id: 'live', crew: { name: 'LIVE' } }),
    ]
    render(<ConsoleShell runs={runs} connected hasSnapshot now={now} />)

    const rail = screen.getByLabelText('rail')
    expect(within(rail).queryByTitle('DEAD')).toBeNull()
    expect(within(rail).getByTitle('LIVE')).toBeTruthy()
    expect(within(rail).getByRole('button', { name: /^Active/ }).querySelector('.console-count')?.textContent).toBe('1')
  })

  it('narrows the fleet list to one crew and shows a clearable "N of M" chip', () => {
    const runs = [
      run({ id: 'x1', crew: { name: 'X' }, repo: 'r-x1', branch: 'b-x1' }),
      run({ id: 'y1', crew: { name: 'Y' }, repo: 'r-y1', branch: 'b-y1' }),
      run({ id: 'y2', crew: { name: 'Y' }, repo: 'r-y2', branch: 'b-y2' }),
      run({ id: 'n1', repo: 'r-n1', branch: 'b-n1' }),
    ]
    render(<ConsoleShell runs={runs} connected hasSnapshot now={now} />)

    const rail = screen.getByLabelText('rail')
    fireEvent.click(within(rail).getByTitle('X'))

    const list = screen.getByLabelText('fleet list')
    expect(within(list).getByText('b-x1')).toBeTruthy()
    expect(within(list).queryByText('b-y1')).toBeNull()
    expect(within(list).queryByText('b-y2')).toBeNull()
    expect(within(list).queryByText('b-n1')).toBeNull()
    expect(within(list).getByText('1 of 4')).toBeTruthy()
    expect(within(list).getByRole('button', { name: 'Clear crew filter' })).toBeTruthy()
    expect(within(rail).getByRole('button', { name: /^Active/ }).querySelector('.console-count')?.textContent).toBe('4')

    fireEvent.click(within(list).getByRole('button', { name: 'Clear crew filter' }))
    expect(within(list).getByText('b-x1')).toBeTruthy()
    expect(within(list).getByText('b-y1')).toBeTruthy()
    expect(within(list).getByText('b-y2')).toBeTruthy()
    expect(within(list).getByText('b-n1')).toBeTruthy()
    expect(within(list).queryByText(/ of /)).toBeNull()
  })

  it('invariant: the needs-you badge always clears the crew filter, so stale-blocked runs are never hidden behind it', () => {
    const runs = [
      run({ id: 'x1', crew: { name: 'X' }, state: 'running' }),
      run({ id: 'y-fresh', crew: { name: 'Y' }, state: 'blocked' }),
      run({ id: 'y-stale', crew: { name: 'Y' }, state: 'blocked', updated_at: nowSec - 2 * 3600 }),
      run({ id: 'n-stale', state: 'blocked', updated_at: nowSec - 2 * 3600 }),
    ]
    render(<ConsoleShell runs={runs} connected hasSnapshot now={now} />)

    const rail = screen.getByLabelText('rail')
    const list = screen.getByLabelText('fleet list')

    fireEvent.click(within(rail).getByTitle('X'))
    // Proves the crew filter is on and would hide the blocked runs.
    expect(within(list).getByText('x1')).toBeTruthy()
    expect(within(list).queryByText('y-fresh')).toBeNull()
    expect(within(list).queryByText('y-stale')).toBeNull()
    expect(within(list).queryByText('n-stale')).toBeNull()

    fireEvent.click(within(rail).getByRole('button', { name: '1 needs you' }))
    expect(within(list).queryByText('x1')).toBeNull()
    expect(within(list).getByText('y-fresh')).toBeTruthy()
    expect(within(list).getByText('y-stale')).toBeTruthy()
    expect(within(list).getByText('n-stale')).toBeTruthy()
    expect(within(list).queryByText(/ of /)).toBeNull()
    expect(within(list).queryByRole('button', { name: 'Clear crew filter' })).toBeNull()
  })

  it('switches the list column between Fleet, Crews, Workspace and Dispatch', async () => {
    const runs = [run({ id: 'a' })]
    render(<ConsoleShell runs={runs} connected hasSnapshot now={now} />)

    const rail = screen.getByLabelText('rail')

    fireEvent.click(within(rail).getByRole('button', { name: 'Crews' }))
    expect(screen.getByRole('heading', { level: 1, name: 'Crews' })).toBeTruthy()
    expect(screen.queryByRole('heading', { level: 1, name: 'Active' })).toBeNull()

    fireEvent.click(within(rail).getByRole('button', { name: 'Workspace' }))
    expect(screen.getByRole('heading', { level: 1, name: 'Workspace' })).toBeTruthy()

    fireEvent.click(within(rail).getByRole('button', { name: 'Dispatch' }))
    expect(await screen.findByLabelText('Title')).toBeTruthy()

    fireEvent.click(within(rail).getByRole('button', { name: /^Active/ }))
    expect(screen.getByRole('heading', { level: 1, name: 'Active' })).toBeTruthy()
  })

  it('opens on the Dispatch section for a dispatch link', () => {
    window.location.hash = '#/dispatch?repo=%2Frepo&crew=new'
    render(<ConsoleShell runs={[]} connected hasSnapshot now={now} />)

    const rail = screen.getByLabelText('rail')
    expect(within(rail).getByRole('button', { name: 'Dispatch' }).getAttribute('aria-pressed')).toBe('true')
  })

  it('switches to Dispatch when the hash becomes a dispatch link', () => {
    render(<ConsoleShell runs={[]} connected hasSnapshot now={now} />)
    const rail = screen.getByLabelText('rail')
    expect(within(rail).getByRole('button', { name: 'Dispatch' }).getAttribute('aria-pressed')).toBe('false')

    // happy-dom fires hashchange itself on a changing hash assignment.
    act(() => { window.location.hash = '#/dispatch?repo=%2Frepo' })

    expect(within(rail).getByRole('button', { name: 'Dispatch' }).getAttribute('aria-pressed')).toBe('true')
  })
})

describe('ConsoleShell project grouping', () => {
  const runs = [
    run({ id: 'w1', project: 'houston', branch: 'w-one', role: 'worker', state: 'blocked', crew: { name: 'X' } }),
    run({ id: 'o1', project: 'other', branch: 'o-one' }),
    run({ id: 'd', project: 'houston', branch: 'main', role: 'dispatcher', crew: { name: 'X' } }),
    run({ id: 'w2', project: 'houston', branch: 'w-two', role: 'worker', crew: { name: 'Y' } }),
  ]

  it('shows the project chip in the default flat list', () => {
    render(<ConsoleShell runs={runs} connected hasSnapshot now={now} />)
    const list = screen.getByLabelText('fleet list')
    expect(list.querySelectorAll('.fleet-project-group').length).toBe(0)
    expect(cardFor(list, 'w-one').querySelector('.run-project')?.textContent).toBe('houston')
  })

  it('groups by project with counts and dispatcher before its workers', () => {
    render(<ConsoleShell runs={runs} connected hasSnapshot now={now} />)
    const list = screen.getByLabelText('fleet list')
    fireEvent.click(within(list).getByRole('button', { name: 'Group by project' }))

    const headers = Array.from(list.querySelectorAll('.fleet-project-group'))
    expect(headers.map((h) => h.querySelector('.fleet-project-name')?.textContent)).toEqual(['houston', 'other'])
    expect(headers[0].textContent).toContain('3')
    expect(headers[0].textContent).toContain('1 need you')
    const names = Array.from(list.querySelectorAll('.run-name')).map((e) => e.textContent)
    expect(names).toEqual(['main', 'w-one', 'w-two', 'o-one'])
  })

  it('keeps the dispatcher crew summary complete while a crew narrows the list', () => {
    render(<ConsoleShell runs={runs} connected hasSnapshot now={now} />)
    fireEvent.click(within(screen.getByLabelText('rail')).getByTitle('X'))
    const list = screen.getByLabelText('fleet list')
    expect(within(list).queryByText('w-two')).toBeNull()
    expect(cardFor(list, 'main').querySelector('.run-crew')?.textContent).toBe('2 workers · 1 blocked')
  })
})

describe('ConsoleShell tab routes', () => {
  const runs = [run({ id: 'b', repo: 'repo-b', branch: 'branch-b' })]

  it('keeps the detail pane when a rail filter is chosen', () => {
    window.location.hash = '#/fleet/b'
    render(<ConsoleShell runs={runs} connected hasSnapshot now={now} />)

    fireEvent.click(within(screen.getByLabelText('rail')).getByRole('button', { name: /^All/ }))

    expect(window.location.hash).toBe('#/fleet/b')
    expect(within(screen.getByLabelText('detail')).getByText('branch-b')).toBeTruthy()
  })

  it('switches the list to Crews while the detail stays open', () => {
    window.location.hash = '#/fleet/b'
    render(<ConsoleShell runs={runs} connected hasSnapshot now={now} />)

    const crewsButton = within(screen.getByLabelText('rail')).getByRole('button', { name: 'Crews' })
    fireEvent.click(crewsButton)

    expect(crewsButton.getAttribute('aria-pressed')).toBe('true')
    expect(within(screen.getByLabelText('detail')).getByText('branch-b')).toBeTruthy()
  })

  it('the detail back button returns to the current section', () => {
    window.location.hash = '#/fleet/b'
    render(<ConsoleShell runs={runs} connected hasSnapshot now={now} />)
    fireEvent.click(within(screen.getByLabelText('rail')).getByRole('button', { name: 'Crews' }))

    fireEvent.click(
      within(screen.getByLabelText('detail').querySelector<HTMLElement>('.run-detail-header')!).getByRole('button', {
        name: 'Back to Crews',
      }),
    )

    expect(window.location.hash).toBe('#/crews')
  })
})
