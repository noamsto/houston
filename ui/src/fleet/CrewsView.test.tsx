import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { CrewsView } from './CrewsView'
import type { Run } from '../api/runs'
import { fetchDispatchOptions } from '../api/dispatch'
import { replyRun } from '../api/runs'

vi.mock('../api/dispatch', () => ({
  fetchDispatchOptions: vi.fn(),
  submitDispatch: vi.fn(),
}))

vi.mock('../api/runs', async (importOriginal) => ({
  ...(await importOriginal<typeof import('../api/runs')>()),
  replyRun: vi.fn(),
}))

const now = 1_800_000_000_000 // fixed ms
const nowSec = Math.floor(now / 1000)
const old = nowSec - 10 * 3600

function run(p: Partial<Run> = {}): Run {
  return {
    id: 'pane-1', agent: 'claude', state: 'running',
    activity: {}, tokens: { input: 0, output: 0 },
    caps: { terminal: true, reply: true, kill: true },
    updated_at: nowSec,
    ...p,
  } as Run
}

function options(repos: { path: string; name: string; crews: string[] }[]) {
  return { repos, tiers: [], efforts: [], plans: [], engines: {}, engine_order: [], tier_models: {} }
}

const fetchOptions = vi.mocked(fetchDispatchOptions)
const replyRunMock = vi.mocked(replyRun)

beforeEach(() => {
  fetchOptions.mockResolvedValue(options([]))
})

afterEach(() => {
  cleanup()
  vi.clearAllMocks()
})

describe('CrewsView', () => {
  it('shows the empty state when no run carries a crew id', () => {
    const runs = [run({ id: 'no-crew' }), run({ id: 'blank-crew', crew: { name: '' } })]
    const { container } = render(<CrewsView runs={runs} now={now} />)

    expect(container.querySelectorAll('.crews-crew').length).toBe(0)
    expect(screen.getByText(/no crews/i)).toBeTruthy()
  })

  it('renders live crews and hides finished ones behind a collapsed toggle', () => {
    const runs = [
      run({ id: 'a', crew: { name: 'crew-live' } }),
      run({ id: 'b', crew: { name: 'crew-done' }, state: 'done', updated_at: old }),
    ]
    const { container } = render(<CrewsView runs={runs} now={now} />)

    expect(container.querySelectorAll('.crews-crew').length).toBe(1)
    expect(container.querySelector('.crews-id')?.getAttribute('title')).toBe('crew-live')

    const toggle = screen.getByRole('button', { name: /finished \(1\)/i })
    expect(toggle.getAttribute('aria-expanded')).toBe('false')

    fireEvent.click(toggle)
    expect(toggle.getAttribute('aria-expanded')).toBe('true')
    const titles = Array.from(container.querySelectorAll('.crews-id')).map((n) => n.getAttribute('title'))
    expect(titles).toEqual(['crew-live', 'crew-done'])

    fireEvent.click(toggle)
    expect(container.querySelectorAll('.crews-crew').length).toBe(1)
  })

  it('omits the Finished toggle when nothing has finished', () => {
    render(<CrewsView runs={[run({ crew: { name: 'c' } })]} now={now} />)
    expect(screen.queryByRole('button', { name: /finished/i })).toBeNull()
  })

  it('says there are no live crews when only finished ones exist', () => {
    const runs = [run({ crew: { name: 'c' }, state: 'done', updated_at: old })]
    render(<CrewsView runs={runs} now={now} />)
    expect(screen.getByText('No live crews.')).toBeTruthy()
    expect(screen.getByRole('button', { name: /finished \(1\)/i })).toBeTruthy()
  })

  it('does not treat a stale blocked run as keeping its crew live', () => {
    const runs = [run({ crew: { name: 'c' }, state: 'blocked', updated_at: old })]
    render(<CrewsView runs={runs} now={now} />)
    expect(screen.getByText('No live crews.')).toBeTruthy()
  })

  it('shows the counts label in the crew header', () => {
    const runs = [
      run({ id: 'a', crew: { name: 'c' }, state: 'running' }),
      run({ id: 'b', crew: { name: 'c' }, state: 'blocked' }),
      run({ id: 'd', crew: { name: 'c' }, state: 'done', updated_at: old }),
    ]
    const { container } = render(<CrewsView runs={runs} now={now} />)
    expect(container.querySelector('.crews-counts')?.textContent).toBe('1 blocked · 1 running · 1 done')
  })

  describe('roster row', () => {
    it('shows codename, branch as title, tier and engine chips, phase and age', () => {
      const runs = [
        run({
          id: 'a',
          crew: { name: 'c', codename: 'Apollo', tier: 'deep', color: '#112233' },
          agent: 'codex',
          branch: 'feat/thing',
          issue: { id: 'HOU-9' },
          activity: { tool: 'Edit', hint: 'main.go', task: 'garbage first prompt' },
          updated_at: nowSec - 120,
        }),
      ]
      const { container } = render(<CrewsView runs={runs} now={now} />)

      expect(container.querySelector('.crews-codename')?.textContent).toBe('Apollo')
      expect(container.querySelector('.crews-title')?.textContent).toBe('feat/thing')
      expect(container.querySelector('.run-chip.tier')?.textContent).toBe('deep')
      expect(screen.getByText('codex')).toBeTruthy()
      expect(container.querySelector('.run-chip.issue')?.textContent).toBe('HOU-9')
      expect(container.querySelector('.crews-phase')?.textContent).toBe('Edit · main.go')
      expect(container.querySelector('.run-age')?.textContent).toBe('2m')
      expect(container.textContent).not.toContain('garbage first prompt')
      expect((container.querySelector('.crews-swatch') as HTMLElement).style.background).toBe('#112233')
    })

    it('prefers the issue title for the title line and labels a codename-less worker generically', () => {
      const runs = [
        run({ id: 'a', crew: { name: 'c' }, branch: 'feat/68-x', issue: { id: 'H-1', title: 'Do the thing' }, updated_at: nowSec - 1 }),
        run({ id: 'bare', crew: { name: 'c' }, updated_at: nowSec - 2 }),
      ]
      const { container } = render(<CrewsView runs={runs} now={now} />)

      expect(Array.from(container.querySelectorAll('.crews-codename')).map((n) => n.textContent)).toEqual(['worker', 'worker'])
      expect(container.querySelector('.crews-title')?.textContent).toBe('Do the thing')
    })

    it('shows the crew title, then the issue title, then the branch, and a model chip', () => {
      const runs = [
        run({ id: 'a', crew: { name: 'c', title: 'Crew title', model: 'sonnet' }, branch: 'b', issue: { id: 'H-1', title: 'Issue title' }, updated_at: nowSec - 1 }),
        run({ id: 'b', crew: { name: 'c' }, branch: 'br', issue: { id: 'H-2', title: 'Issue two' }, updated_at: nowSec - 2 }),
        run({ id: 'd', crew: { name: 'c' }, branch: 'only-branch', updated_at: nowSec - 3 }),
      ]
      const { container } = render(<CrewsView runs={runs} now={now} />)

      expect(Array.from(container.querySelectorAll('.crews-title')).map((n) => n.textContent))
        .toEqual(['Crew title', 'Issue two', 'only-branch'])
      const models = container.querySelectorAll('.run-chip.model')
      expect(models.length).toBe(1)
      expect(models[0].textContent).toBe('sonnet')
    })

    it('shows the state label when the run has no tool', () => {
      const { container } = render(<CrewsView runs={[run({ crew: { name: 'c' }, state: 'thinking' })]} now={now} />)
      expect(container.querySelector('.crews-phase')?.textContent).toBe('thinking')
    })

    it('exposes the run state as a text alternative on the state dot', () => {
      const { container } = render(<CrewsView runs={[run({ crew: { name: 'c' }, state: 'running' })]} now={now} />)
      expect(container.querySelector('.run-dot')?.getAttribute('aria-label')).toBe('running')
      expect(screen.getByRole('img', { name: 'running' })).toBeTruthy()
    })

    it('marks a run with a down control connection as stale', () => {
      const { container } = render(<CrewsView runs={[run({ crew: { name: 'c' }, stale: true })]} now={now} />)
      expect(container.querySelector('.run-chip.stale')).toBeTruthy()
    })

    it('calls onOpen with the run when its main button is tapped', () => {
      const onOpen = vi.fn()
      const r = run({ id: 'a', crew: { name: 'c', codename: 'Apollo' } })
      render(<CrewsView runs={[r]} now={now} onOpen={onOpen} />)

      fireEvent.click(screen.getByRole('button', { name: /apollo/i }))
      expect(onOpen).toHaveBeenCalledWith(r)
    })
  })

  describe('blocked members', () => {
    it('lists blocked members first and renders a composer only for question.via "crew"', () => {
      const runs = [
        run({ id: 'run', crew: { name: 'c', codename: 'Runner' }, updated_at: nowSec }),
        run({ id: 'pane-q', crew: { name: 'c', codename: 'Pane' }, state: 'blocked', question: { text: 'y/n?', via: 'pane' }, updated_at: nowSec - 5 }),
        run({ id: 'crew-q', crew: { name: 'c', codename: 'Crewq' }, state: 'blocked', question: { text: 'proceed?', via: 'crew' }, updated_at: nowSec - 9 }),
      ]
      const { container } = render(<CrewsView runs={runs} now={now} />)

      const order = Array.from(container.querySelectorAll('.crews-codename')).map((n) => n.textContent)
      expect(order).toEqual(['Pane', 'Crewq', 'Runner'])
      expect(container.querySelectorAll('.crew-reply').length).toBe(1)
      expect(screen.getByText('proceed?')).toBeTruthy()
      expect(screen.getByText('y/n?')).toBeTruthy()
    })

    it('sends a reply to the member whose composer was used', async () => {
      replyRunMock.mockResolvedValue({ kind: 'delivered' })
      const runs = [
        run({ id: 'first', crew: { name: 'c', codename: 'First' }, state: 'blocked', question: { text: 'one?', via: 'crew' }, updated_at: nowSec }),
        run({ id: 'second', crew: { name: 'c', codename: 'Second' }, state: 'blocked', question: { text: 'two?', via: 'crew' }, updated_at: nowSec - 5 }),
      ]
      const { container } = render(<CrewsView runs={runs} now={now} />)

      const row = container.querySelectorAll('.crews-member')[1] as HTMLElement
      expect(row.textContent).toContain('Second')
      fireEvent.change(within(row).getByLabelText(/reply to the crew/i), { target: { value: 'go ahead' } })
      fireEvent.click(within(row).getByRole('button', { name: /send/i }))

      await waitFor(() => expect(replyRunMock).toHaveBeenCalledTimes(1))
      expect(replyRunMock).toHaveBeenCalledWith('second', 'go ahead')
    })

    it('hints to answer in the terminal for a pane question', () => {
      const runs = [run({ crew: { name: 'c' }, state: 'blocked', question: { text: 'y/n?', via: 'pane' } })]
      render(<CrewsView runs={runs} now={now} />)
      expect(screen.getByText('Answer in the terminal — tap to open.')).toBeTruthy()
    })

    it('does not show the terminal hint for a crew question', () => {
      const runs = [run({ crew: { name: 'c' }, state: 'blocked', question: { text: 'ok?', via: 'crew' } })]
      render(<CrewsView runs={runs} now={now} />)
      expect(screen.queryByText(/answer in the terminal/i)).toBeNull()
    })

    it('falls back to the activity message, then "waiting on you", when there is no question', () => {
      const runs = [
        run({ id: 'msg', crew: { name: 'c' }, state: 'blocked', activity: { message: 'needs approval' }, updated_at: nowSec }),
        run({ id: 'none', crew: { name: 'c' }, state: 'blocked', updated_at: nowSec - 1 }),
      ]
      const { container } = render(<CrewsView runs={runs} now={now} />)
      expect(Array.from(container.querySelectorAll('.crews-phase')).map((n) => n.textContent))
        .toEqual(['needs approval', 'waiting on you'])
    })
  })

  describe('pull request chip', () => {
    it('is a link that opens in a new tab when the PR has a url, outside the main button', () => {
      const runs = [run({ crew: { name: 'c' }, pr: { number: '42', url: 'https://example.com/pull/42' } })]
      const { container } = render(<CrewsView runs={runs} now={now} />)

      const a = container.querySelector('a.run-chip.pr') as HTMLAnchorElement
      expect(a.textContent).toBe('#42')
      expect(a.getAttribute('href')).toBe('https://example.com/pull/42')
      expect(a.getAttribute('target')).toBe('_blank')
      expect(a.closest('button')).toBeNull()
    })

    it('sits in the same chip row as the tier, engine and model chips', () => {
      const runs = [
        run({ crew: { name: 'c', tier: 'deep', model: 'opus' }, pr: { number: '42', url: 'https://example.com/pull/42' } }),
      ]
      const { container } = render(<CrewsView runs={runs} now={now} />)

      const a = container.querySelector('a.run-chip.pr') as HTMLAnchorElement
      const row = container.querySelector('.crews-chips') as HTMLElement
      expect(a.parentElement).toBe(row)
      expect(row.querySelector('.run-chip.tier')).toBeTruthy()
      expect(row.querySelector('.run-chip.model')).toBeTruthy()
      expect(container.querySelector('.crews-member-main')?.contains(row)).toBe(false)
    })

    it('is a plain chip when the PR has no url', () => {
      const runs = [run({ crew: { name: 'c' }, pr: { number: '7' } })]
      const { container } = render(<CrewsView runs={runs} now={now} />)

      expect(container.querySelector('a.run-chip.pr')).toBeNull()
      expect(container.querySelector('span.run-chip.pr')?.textContent).toBe('#7')
    })
  })

  describe('dispatch enrichment', () => {
    it('labels the repo unknown and renders no Dispatch link when nothing resolves the repo', () => {
      const { container } = render(<CrewsView runs={[run({ crew: { name: 'crew 1' } })]} now={now} />)

      expect(screen.getByText('unknown repo')).toBeTruthy()
      expect(container.querySelector('.crews-dispatch')).toBeNull()
    })

    it('titles a bus-only crew by run.project and links it via the repo of that name', async () => {
      fetchOptions.mockResolvedValue(options([{ path: '/home/me/qa-repo', name: 'qa-repo', crews: [] }]))
      const runs = [run({ crew: { name: 'crew-9' }, project: 'qa-repo' })]
      const { container } = render(<CrewsView runs={runs} now={now} />)

      expect(screen.getByText('qa-repo')).toBeTruthy()
      expect(container.querySelector('.crews-dispatch')).toBeNull()
      await waitFor(() => expect(container.querySelector('.crews-dispatch')).toBeTruthy())
      expect(container.querySelector('.crews-dispatch')?.getAttribute('href'))
        .toBe('#/dispatch?repo=%2Fhome%2Fme%2Fqa-repo&crew=crew-9')
    })

    it('omits the Dispatch link when two known repos share the project name', async () => {
      fetchOptions.mockResolvedValue(
        options([
          { path: '/a/app', name: 'app', crews: [] },
          { path: '/b/app', name: 'app', crews: [] },
        ]),
      )
      const { container } = render(<CrewsView runs={[run({ crew: { name: 'c' }, project: 'app' })]} now={now} />)

      await waitFor(() => expect(fetchOptions).toHaveBeenCalled())
      expect(screen.getByText('app')).toBeTruthy()
      expect(container.querySelector('.crews-dispatch')).toBeNull()
    })

    it('shows the repo name and includes the repo path once options load', async () => {
      fetchOptions.mockResolvedValue(options([{ path: '/home/me/houston', name: 'houston', crews: ['crew-1'] }]))
      const { container } = render(<CrewsView runs={[run({ crew: { name: 'crew-1' } })]} now={now} />)

      await waitFor(() => expect(screen.getByText('houston')).toBeTruthy())
      expect(container.querySelector('.crews-dispatch')?.getAttribute('href'))
        .toBe('#/dispatch?repo=%2Fhome%2Fme%2Fhouston&crew=crew-1')
      expect(fetchOptions).toHaveBeenCalledTimes(1)
    })

    it('still renders when the options fetch rejects', async () => {
      fetchOptions.mockRejectedValue(new Error('boom'))
      render(<CrewsView runs={[run({ crew: { name: 'c', codename: 'Apollo' } })]} now={now} />)

      await waitFor(() => expect(fetchOptions).toHaveBeenCalled())
      expect(screen.getByText('Apollo')).toBeTruthy()
      expect(screen.getByText('unknown repo')).toBeTruthy()
    })
  })
})
