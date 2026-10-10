import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { CrewTab } from './CrewTab'
import type { Run } from '../api/runs'
import { fetchDispatchOptions } from '../api/dispatch'
import { replyRun } from '../api/runs'
import { resetCrewReposCache } from './useCrewRepos'

vi.mock('../api/dispatch', () => ({
  fetchDispatchOptions: vi.fn(),
  submitDispatch: vi.fn(),
}))

vi.mock('../api/runs', async (importOriginal) => ({
  ...(await importOriginal<typeof import('../api/runs')>()),
  replyRun: vi.fn(),
}))

vi.mock('../hooks/useCrewFeed', () => ({
  useCrewFeed: () => ({ status: 'ready', entries: [], more: false, loadOlder: vi.fn(), retry: vi.fn() }),
}))

const now = 1_800_000_000_000 // fixed ms
const nowSec = Math.floor(now / 1000)
const old = nowSec - 10 * 3600

function run(p: Partial<Run> = {}): Run {
  return {
    id: 'pane-1', agent: 'claude', state: 'running', role: 'worker',
    activity: {}, tokens: { input: 0, output: 0 },
    caps: { terminal: true, reply: true, kill: true },
    updated_at: nowSec,
    ...p,
  } as Run
}

const dispatcher = (p: Partial<Run> = {}) => run({ id: 'd', role: 'dispatcher', branch: 'main', crew: { name: 'c' }, ...p })

function options(repos: { path: string; name: string; crews: string[]; home?: string[] }[]) {
  return {
    repos: repos.map((r) => ({ ...r, home: r.home ?? r.crews })),
    tiers: [], efforts: [], plans: [], engines: {}, engine_order: [], tier_models: {},
  }
}

const fetchOptions = vi.mocked(fetchDispatchOptions)
const replyRunMock = vi.mocked(replyRun)

function renderTab(runs: Run[], d: Run = dispatcher()) {
  return render(<CrewTab run={d} runs={[d, ...runs]} now={now} />)
}

beforeEach(() => {
  resetCrewReposCache()
  fetchOptions.mockResolvedValue(options([]))
})

afterEach(() => {
  cleanup()
  vi.clearAllMocks()
})

describe('CrewTab roster', () => {
  it('lists only this crew\'s workers on the dispatcher\'s host', () => {
    const { container } = renderTab([
      run({ id: 'a', crew: { name: 'c', codename: 'Mine' } }),
      run({ id: 'b', crew: { name: 'other', codename: 'Other' } }),
      run({ id: 'r', role: undefined, crew: { name: 'c', codename: 'Solo' } }),
      run({ id: 'h', crew: { name: 'c', codename: 'Remote' }, host: 'box2' }),
      run({ id: 'l', crew: { name: 'c', codename: 'Local' }, host: 'local' }),
    ])

    const names = Array.from(container.querySelectorAll('.crews-codename')).map((n) => n.textContent)
    expect(names.sort()).toEqual(['Local', 'Mine'])
  })

  it('shows the repo-less header: unknown repo, short crew id titled with the full id, counts', () => {
    const { container } = renderTab([
      run({ id: 'a', crew: { name: 'c' }, state: 'running' }),
      run({ id: 'b', crew: { name: 'c' }, state: 'blocked', attention: 'needs-you' }),
      run({ id: 'e', crew: { name: 'c' }, state: 'idle' }),
    ])

    expect(screen.getByText('unknown repo')).toBeTruthy()
    expect(container.querySelector('.crews-id')?.getAttribute('title')).toBe('c')
    expect(container.querySelector('.crews-counts')?.textContent).toBe('1 working · 1 needs you · 1 idle')
    expect(container.querySelector('.crews-dispatch')).toBeNull()
  })

  it('titles the crew by run.project and links Dispatch here via the repo of that name', async () => {
    fetchOptions.mockResolvedValue(options([{ path: '/home/me/qa-repo', name: 'qa-repo', crews: [] }]))
    const { container } = renderTab([], dispatcher({ project: 'qa-repo', crew: { name: 'crew-9' } }))

    expect(container.querySelector('.crews-repo')?.textContent).toBe('qa-repo')
    await waitFor(() => expect(container.querySelector('.crews-dispatch')).toBeTruthy())
    expect(container.querySelector('.crews-dispatch')?.getAttribute('href'))
      .toBe('#/dispatch?repo=%2Fhome%2Fme%2Fqa-repo&crew=crew-9')
  })

  it('prefers the repo that lists the crew and omits the link when nothing resolves', async () => {
    fetchOptions.mockResolvedValue(
      options([
        { path: '/a/listed', name: 'listed', crews: ['c'] },
        { path: '/b/app', name: 'app', crews: [] },
      ]),
    )
    const { container } = renderTab([], dispatcher({ project: 'app' }))
    await waitFor(() => expect(screen.getByText('listed')).toBeTruthy())
    expect(container.querySelector('.crews-dispatch')?.getAttribute('href')).toBe('#/dispatch?repo=%2Fa%2Flisted&crew=c')
  })

  it('renders the feed after the roster', () => {
    const { container } = renderTab([run({ id: 'a', crew: { name: 'c' } })])
    expect(container.querySelector('.crew-feed-section')).toBeTruthy()
    expect(screen.getByText('No crew activity yet.')).toBeTruthy()
  })

  it('opens a worker from its row', () => {
    renderTab([run({ id: 'a', crew: { name: 'c', codename: 'Apollo' } })])
    fireEvent.click(screen.getByRole('button', { name: /apollo/i }))
    expect(window.location.hash).toBe('#/fleet/a')
    window.location.hash = ''
  })

  it('lists needs-you workers first, then live ones, then history', () => {
    const { container } = renderTab([
      run({ id: 'hist', crew: { name: 'c', codename: 'Hist' }, state: 'done', updated_at: old }),
      run({ id: 'live', crew: { name: 'c', codename: 'Live' }, updated_at: nowSec - 5 }),
      run({ id: 'ask', crew: { name: 'c', codename: 'Ask' }, state: 'blocked', updated_at: nowSec - 50 }),
    ])
    expect(Array.from(container.querySelectorAll('.crews-codename')).map((n) => n.textContent)).toEqual(['Ask', 'Live', 'Hist'])
  })
})

describe('CrewTab member row', () => {
  const card = (container: HTMLElement, name: string) =>
    Array.from(container.querySelectorAll<HTMLElement>('.crews-member')).find((m) => m.textContent?.includes(name))!

  it('shows title, meta line, phase, age, swatch and sessions', () => {
    const { container } = renderTab([
      run({
        id: 'a', agent: 'codex', branch: 'feat/thing', activity: { tool: 'Edit', hint: 'main.go', task: 'garbage first prompt' },
        crew: { name: 'c', codename: 'Apollo', tier: 'deep', model: 'gpt', color: '#112233', title: 'Crew title', sessions: 3 },
        updated_at: nowSec - 120,
      }),
    ])

    expect(container.querySelector('.crews-codename')?.textContent).toBe('Apollo')
    expect(container.querySelector('.crews-title')?.textContent).toBe('Crew title')
    expect(container.querySelector('.crews-meta')?.textContent).toBe('deep · codex · gpt')
    expect(container.querySelector('.crews-phase')?.textContent).toBe('Edit · main.go')
    expect(container.querySelector('.run-age')?.textContent).toBe('2m')
    expect(container.querySelector('.crews-sessions')?.textContent).toBe('3 sessions')
    expect(container.textContent).not.toContain('garbage first prompt')
    expect((container.querySelector('.crews-swatch') as HTMLElement).style.background).toBe('#112233')
  })

  it('falls back from the crew title to the issue title to the branch, and labels a bare worker generically', () => {
    const { container } = renderTab([
      run({ id: 'a', crew: { name: 'c' }, branch: 'b', issue: { id: 'H-1', title: 'Issue title' }, updated_at: nowSec - 1 }),
      run({ id: 'b', crew: { name: 'c' }, branch: 'only-branch', updated_at: nowSec - 2 }),
    ])
    expect(Array.from(container.querySelectorAll('.crews-title')).map((n) => n.textContent)).toEqual(['Issue title', 'only-branch'])
    expect(Array.from(container.querySelectorAll('.crews-codename')).map((n) => n.textContent)).toEqual(['worker', 'worker'])
  })

  it('shows the attention chip, the stuck note, the bus detail phase and a PR link', () => {
    const { container } = renderTab([
      run({
        id: 'r', state: 'running', attention: 'stuck', attention_note: 'No output for 20 minutes.',
        crew: { name: 'c', codename: 'Rook', detail: 'executing step 3' }, updated_at: nowSec - 120,
      }),
      run({
        id: 'k', state: 'idle', attention: 'done', pr: { number: '12', url: 'https://github.com/x/y/pull/12' },
        crew: { name: 'c', codename: 'Kite' }, updated_at: nowSec - 100,
      }),
    ])

    const rook = card(container, 'Rook')
    expect(rook.classList.contains('stuck')).toBe(true)
    expect(rook.querySelector('.crews-attention')?.textContent).toBe('stuck')
    expect(rook.querySelector('.crews-phase')?.textContent).toBe('executing step 3')
    expect(rook.querySelector('.crews-note')?.textContent).toBe('No output for 20 minutes.')

    const kite = card(container, 'Kite')
    expect(kite.querySelector('a.crews-pr')?.getAttribute('href')).toBe('https://github.com/x/y/pull/12')
    expect(kite.querySelector('a.crews-pr')?.closest('button')).toBeNull()
  })

  it('shows an idle chip for an unflagged idle worker and prefers live tool activity over the bus detail', () => {
    const { container } = renderTab([
      run({ id: 'i', crew: { name: 'c', codename: 'Idler', detail: 'old line' }, state: 'idle', activity: { tool: 'Bash', hint: 'rm' } }),
      run({ id: 'w', crew: { name: 'c', codename: 'Busy', detail: 'old line' }, activity: { tool: 'Edit', hint: 'main.go' }, updated_at: nowSec - 1 }),
    ])
    expect(card(container, 'Idler').querySelector('.crews-idle')?.textContent).toBe('idle')
    expect(card(container, 'Idler').querySelector('.crews-phase')?.textContent).toBe('old line')
    expect(card(container, 'Busy').querySelector('.crews-phase')?.textContent).toBe('Edit · main.go')
  })

  it('mutes a flagged card that stopped reporting and marks a stale one', () => {
    const { container } = renderTab([
      run({ id: 'a', crew: { name: 'c', codename: 'Old' }, attention: 'stuck', updated_at: nowSec - 2 * 3600 }),
      run({ id: 'b', crew: { name: 'c', codename: 'Down' }, stale: true }),
    ])
    expect(card(container, 'Old').classList.contains('muted')).toBe(true)
    expect(card(container, 'Down').querySelector('.run-chip.stale')).toBeTruthy()
  })

  it('hides the phase line when it repeats the question', () => {
    const { container } = renderTab([
      run({ crew: { name: 'c' }, state: 'blocked', activity: { message: 'Keep it?' }, question: { text: 'Keep it?', via: 'crew' } }),
    ])
    expect(container.querySelector('.crews-phase')).toBeNull()
    expect(container.querySelector('.crews-question')?.textContent).toBe('Keep it?')
  })

  it('shows a reply composer only for a crew question and a terminal hint only for a pane question', () => {
    const { container } = renderTab([
      run({ id: 'run', crew: { name: 'c', codename: 'Runner' } }),
      run({ id: 'pane-q', crew: { name: 'c', codename: 'Pane' }, state: 'blocked', question: { text: 'y/n?', via: 'pane' }, updated_at: nowSec - 5 }),
      run({ id: 'crew-q', crew: { name: 'c', codename: 'Crewq' }, state: 'blocked', question: { text: 'proceed?', via: 'crew' }, updated_at: nowSec - 9 }),
    ])

    expect(container.querySelectorAll('.crew-reply')).toHaveLength(1)
    expect(card(container, 'Crewq').querySelector('.crew-reply')).toBeTruthy()
    expect(card(container, 'Pane').querySelector('.crews-hint')?.textContent).toBe('Answer in the terminal — tap to open.')
    expect(card(container, 'Crewq').querySelector('.crews-hint')).toBeNull()
  })

  it('sends a reply to the worker whose composer was used', async () => {
    replyRunMock.mockResolvedValue({ kind: 'delivered' })
    const { container } = renderTab([
      run({ id: 'first', crew: { name: 'c', codename: 'First' }, state: 'blocked', question: { text: 'one?', via: 'crew' }, updated_at: nowSec }),
      run({ id: 'second', crew: { name: 'c', codename: 'Second' }, state: 'blocked', question: { text: 'two?', via: 'crew' }, updated_at: nowSec - 5 }),
    ])

    const row = card(container, 'Second')
    fireEvent.change(within(row).getByLabelText(/reply to the crew/i), { target: { value: 'go ahead' } })
    fireEvent.click(within(row).getByRole('button', { name: /send/i }))

    await waitFor(() => expect(replyRunMock).toHaveBeenCalledTimes(1))
    expect(replyRunMock).toHaveBeenCalledWith('second', 'go ahead')
  })

  it('renders a PR whose url is not http(s) as a plain chip', () => {
    const { container } = renderTab([run({ crew: { name: 'c' }, pr: { number: '7', url: 'javascript:alert(1)' } })])
    expect(container.querySelector('a.run-chip.pr')).toBeNull()
    expect(container.querySelector('span.run-chip.pr')?.textContent).toBe('#7')
  })

  it('renders a PR with no url as a plain chip', () => {
    const { container } = renderTab([run({ crew: { name: 'c' }, pr: { number: '7' } })])
    expect(container.querySelector('a.run-chip.pr')).toBeNull()
    expect(container.querySelector('span.run-chip.pr')?.textContent).toBe('#7')
  })
})
