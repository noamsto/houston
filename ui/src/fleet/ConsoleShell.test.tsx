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
    expect(within(detail).getByText('repo-b/branch-b')).toBeTruthy()

    const list = screen.getByLabelText('fleet list')
    expect(cardFor(list, 'repo-b/branch-b').getAttribute('aria-current')).toBe('true')
  })

  it('clicking a card writes the hash and the detail follows on hashchange', () => {
    const runs = [
      run({ id: 'a', repo: 'repo-a', branch: 'branch-a' }),
      run({ id: 'b', repo: 'repo-b', branch: 'branch-b' }),
    ]
    render(<ConsoleShell runs={runs} connected hasSnapshot now={now} />)

    const list = screen.getByLabelText('fleet list')
    fireEvent.click(cardFor(list, 'repo-a/branch-a'))
    expect(window.location.hash).toBe('#/fleet/a/activity')

    act(() => { window.dispatchEvent(new HashChangeEvent('hashchange')) })

    const detail = screen.getByLabelText('detail')
    expect(within(detail).getByText('repo-a/branch-a')).toBeTruthy()
    expect(cardFor(list, 'repo-a/branch-a').getAttribute('aria-current')).toBe('true')
    expect(cardFor(list, 'repo-b/branch-b').getAttribute('aria-current')).toBeNull()
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
    expect(within(rail).getByRole('button', { name: /^Active/ }).textContent).toContain('3')
    expect(within(rail).getByRole('button', { name: /^Needs you/ }).textContent).toContain('2')
    expect(within(rail).getByRole('button', { name: /^All/ }).textContent).toContain('4')
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
    expect(within(list).getByText('r-x1/b-x1')).toBeTruthy()
    expect(within(list).queryByText('r-y1/b-y1')).toBeNull()
    expect(within(list).queryByText('r-y2/b-y2')).toBeNull()
    expect(within(list).queryByText('r-n1/b-n1')).toBeNull()
    expect(within(list).getByText(/1 of 4/)).toBeTruthy()
    expect(within(list).getByRole('button', { name: 'Clear crew filter' })).toBeTruthy()
    expect(within(rail).getByRole('button', { name: /^Active/ }).textContent).toContain('4')

    fireEvent.click(within(list).getByRole('button', { name: 'Clear crew filter' }))
    expect(within(list).getByText('r-x1/b-x1')).toBeTruthy()
    expect(within(list).getByText('r-y1/b-y1')).toBeTruthy()
    expect(within(list).getByText('r-y2/b-y2')).toBeTruthy()
    expect(within(list).getByText('r-n1/b-n1')).toBeTruthy()
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

  it('switches the list column between Fleet, Crews, Workspace and Dispatch', () => {
    const runs = [run({ id: 'a' })]
    render(<ConsoleShell runs={runs} connected hasSnapshot now={now} />)

    const rail = screen.getByLabelText('rail')

    fireEvent.click(within(rail).getByRole('button', { name: 'Crews' }))
    expect(screen.getByRole('heading', { level: 1, name: 'Crews' })).toBeTruthy()
    expect(screen.queryByRole('heading', { level: 1, name: 'Active' })).toBeNull()

    fireEvent.click(within(rail).getByRole('button', { name: 'Workspace' }))
    expect(screen.getByRole('heading', { level: 1, name: 'Workspace' })).toBeTruthy()

    fireEvent.click(within(rail).getByRole('button', { name: 'Dispatch' }))
    expect(screen.getByText(/dispatcher milestone/)).toBeTruthy()

    fireEvent.click(within(rail).getByRole('button', { name: /^Active/ }))
    expect(screen.getByRole('heading', { level: 1, name: 'Active' })).toBeTruthy()
  })
})
