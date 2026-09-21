import { afterEach, describe, expect, it, vi } from 'vitest'
import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { DispatchView } from './DispatchView'
import type { DispatchOptions, DispatchOutcome, DispatchRequest } from '../api/dispatch'
import type { Run } from '../api/runs'

const fetchDispatchOptionsMock = vi.fn<() => Promise<DispatchOptions>>()
const submitDispatchMock = vi.fn<(req: DispatchRequest) => Promise<DispatchOutcome>>()
vi.mock('../api/dispatch', () => ({
  NEW_CREW: 'new',
  fetchDispatchOptions: () => fetchDispatchOptionsMock(),
  submitDispatch: (req: DispatchRequest) => submitDispatchMock(req),
}))

const PREFS_KEY = 'houston-dispatch-prefs'

const optionsFixture: DispatchOptions = {
  repos: [
    { path: '/repo/a', name: 'repo-a', crews: ['200-1', '100-2'] },
    { path: '/repo/b', name: 'repo-b', crews: [] },
  ],
  tiers: ['trivial', 'standard', 'deep'],
  efforts: ['low', 'medium', 'high', 'xhigh', 'max'],
  plans: ['required', 'provided'],
  engines: {
    claude: ['opus', 'sonnet', 'haiku', 'fable'],
    codex: ['gpt-5.6-sol', 'gpt-5.6-terra'],
  },
  engine_order: ['claude', 'codex'],
  tier_models: {
    claude: { trivial: 'haiku', standard: 'sonnet', deep: 'opus' },
    codex: { trivial: 'gpt-5.6-sol', standard: 'gpt-5.6-terra', deep: 'gpt-5.6-sol' },
  },
}

function run(p: Partial<Run> = {}): Run {
  return {
    id: 'pane-1', agent: 'claude', state: 'running',
    activity: {}, tokens: { input: 0, output: 0 },
    caps: { terminal: true, reply: true, kill: true },
    updated_at: 1,
    ...p,
  } as Run
}

async function renderLoaded(runs: Run[] = []) {
  fetchDispatchOptionsMock.mockResolvedValue(structuredClone(optionsFixture))
  const utils = render(<DispatchView runs={runs} />)
  await screen.findByLabelText('Repo')
  return utils
}

function value(label: string): string {
  return (screen.getByLabelText(label) as HTMLSelectElement | HTMLInputElement).value
}

function change(label: string, v: string): void {
  fireEvent.change(screen.getByLabelText(label), { target: { value: v } })
}

async function submitTitle(t = 'go'): Promise<void> {
  change('Title', t)
  fireEvent.click(screen.getByRole('button', { name: 'Dispatch' }))
  await waitFor(() => expect(submitDispatchMock).toHaveBeenCalled())
}

afterEach(() => {
  cleanup()
  fetchDispatchOptionsMock.mockReset()
  submitDispatchMock.mockReset()
  localStorage.clear()
  window.location.hash = ''
  vi.unstubAllGlobals()
})

describe('DispatchView options', () => {
  it('defaults to claude / standard / sonnet / medium and the repo\'s newest crew', async () => {
    await renderLoaded()

    expect(value('Repo')).toBe('/repo/a')
    expect(value('Tier')).toBe('standard')
    expect(value('Engine')).toBe('claude')
    expect(value('Model')).toBe('sonnet')
    expect(value('Effort')).toBe('medium')
    expect(value('Crew')).toBe('200-1')
  })

  it('shows the options error and refetches on retry', async () => {
    fetchDispatchOptionsMock.mockRejectedValueOnce(new Error('fetchDispatchOptions: 500 boom'))
    render(<DispatchView runs={[]} />)
    expect(await screen.findByText('fetchDispatchOptions: 500 boom')).toBeTruthy()

    fetchDispatchOptionsMock.mockResolvedValueOnce(optionsFixture)
    fireEvent.click(screen.getByRole('button', { name: 'Retry' }))

    expect(await screen.findByLabelText('Repo')).toBeTruthy()
  })

  it('restores remembered prefs', async () => {
    localStorage.setItem(PREFS_KEY, JSON.stringify({ repo: '/repo/b', engine: 'codex', model: 'gpt-5.6-sol', tier: 'deep', effort: 'xhigh' }))
    await renderLoaded()

    expect(value('Repo')).toBe('/repo/b')
    expect(value('Engine')).toBe('codex')
    expect(value('Model')).toBe('gpt-5.6-sol')
    expect(value('Tier')).toBe('deep')
    expect(value('Effort')).toBe('xhigh')
  })

  it('ignores remembered prefs that are no longer valid', async () => {
    localStorage.setItem(PREFS_KEY, JSON.stringify({ repo: '/gone', engine: 'nope', model: 'gpt-4', tier: 'epic', effort: 'huge' }))
    await renderLoaded()

    expect(value('Repo')).toBe('/repo/a')
    expect(value('Engine')).toBe('claude')
    expect(value('Model')).toBe('sonnet')
    expect(value('Tier')).toBe('standard')
    expect(value('Effort')).toBe('medium')
  })

  it('labels existing crews with their age and always offers New crew', async () => {
    await renderLoaded()

    const labels = within(screen.getByLabelText('Crew')).getAllByRole('option').map((o) => o.textContent)
    expect(labels[0]).toMatch(/^200-1 · \d+d ago$/)
    expect(labels[1]).toMatch(/^100-2 · \d+d ago$/)
    expect(labels[2]).toBe('New crew')
  })
})

describe('DispatchView field resets', () => {
  it('tier change resets the model to that tier\'s default', async () => {
    await renderLoaded()

    change('Tier', 'deep')

    expect(value('Model')).toBe('opus')
  })

  it('engine change filters the model list and resets to the tier default', async () => {
    await renderLoaded()

    change('Engine', 'codex')

    const modelSelect = screen.getByLabelText('Model') as HTMLSelectElement
    expect(modelSelect.value).toBe('gpt-5.6-terra')
    expect(within(modelSelect).getAllByRole('option').map((o) => (o as HTMLOptionElement).value)).toEqual([
      'gpt-5.6-sol', 'gpt-5.6-terra',
    ])
  })

  it('a crewless repo selects New crew, hints, and can still submit', async () => {
    await renderLoaded()
    submitDispatchMock.mockResolvedValue({ kind: 'started', workerId: 'w', branch: 'feat/1-x', crew: '300-9' })

    change('Repo', '/repo/b')

    expect(value('Crew')).toBe('new')
    expect(screen.getByText(/No crew yet — dispatching starts a new one/)).toBeTruthy()
    await submitTitle()
    expect(submitDispatchMock.mock.calls[0][0].crew).toBe('new')
  })
})

describe('DispatchView submit', () => {
  it('posts the exact request object', async () => {
    await renderLoaded()
    submitDispatchMock.mockResolvedValue({ kind: 'started', workerId: 'worker:feat/1-x#s1', branch: 'feat/1-x' })

    change('Title', '  Fix the thing  ')
    change('Task', 'Body text')
    change('Issue (optional)', ' 53 ')
    fireEvent.click(screen.getByRole('button', { name: 'Dispatch' }))

    expect(submitDispatchMock).toHaveBeenCalledWith({
      repo: '/repo/a',
      title: 'Fix the thing',
      spec: 'Body text',
      tier: 'standard',
      engine: 'claude',
      model: 'sonnet',
      effort: 'medium',
      crew: '200-1',
      issue: '53',
    })
    await screen.findByText(/Waiting for it to appear in Fleet/)
  })

  it('produces exactly one submitDispatch call from two rapid clicks', async () => {
    await renderLoaded()
    let resolve!: (o: DispatchOutcome) => void
    submitDispatchMock.mockReturnValue(new Promise((r) => { resolve = r }))
    change('Title', 'go')

    const button = screen.getByRole('button', { name: 'Dispatch' })
    fireEvent.click(button)
    fireEvent.click(button)

    expect(submitDispatchMock).toHaveBeenCalledTimes(1)
    resolve({ kind: 'started', workerId: 'worker:feat/1-x#s1', branch: 'feat/1-x' })
    await screen.findByText(/Waiting for it to appear in Fleet/)
  })

  it('success shows worker_id, branch, crew and issue link, and clears the title', async () => {
    await renderLoaded()
    submitDispatchMock.mockResolvedValue({
      kind: 'started',
      workerId: 'worker:feat/53-x#s1',
      branch: 'feat/53-x',
      issueUrl: 'https://github.com/acme/houston/issues/53',
      crew: '200-1',
    })
    await submitTitle()

    expect(await screen.findByText('worker:feat/53-x#s1')).toBeTruthy()
    expect(screen.getByText('feat/53-x')).toBeTruthy()
    expect(screen.getByText('200-1')).toBeTruthy()
    expect(screen.queryByText(/new crew\)/)).toBeNull()
    const link = screen.getByRole('link', { name: /view issue/i }) as HTMLAnchorElement
    expect(link.href).toBe('https://github.com/acme/houston/issues/53')
    expect(value('Title')).toBe('')
  })

  it('failure shows the server error text verbatim', async () => {
    await renderLoaded()
    submitDispatchMock.mockResolvedValue({ kind: 'failed', status: 422, error: 'dispatch: model not allowed for engine' })
    await submitTitle()

    expect(await screen.findByText('dispatch: model not allowed for engine')).toBeTruthy()
  })

  it('saves prefs only on a started dispatch', async () => {
    await renderLoaded()
    change('Tier', 'deep')
    change('Effort', 'high')

    submitDispatchMock.mockResolvedValue({ kind: 'failed', status: 422, error: 'nope' })
    await submitTitle()
    await screen.findByText('nope')
    expect(localStorage.getItem(PREFS_KEY)).toBeNull()

    submitDispatchMock.mockResolvedValue({ kind: 'started', workerId: 'w', branch: 'feat/1-x' })
    fireEvent.click(screen.getByRole('button', { name: 'Dispatch' }))
    await screen.findByText(/Waiting for it to appear in Fleet/)
    expect(JSON.parse(localStorage.getItem(PREFS_KEY)!)).toEqual({
      repo: '/repo/a', engine: 'claude', model: 'opus', tier: 'deep', effort: 'high',
    })
  })

  it('a new-crew success selects the minted crew and lists it for that repo', async () => {
    await renderLoaded()
    change('Repo', '/repo/b')
    submitDispatchMock.mockResolvedValue({ kind: 'started', workerId: 'w', branch: 'feat/1-x', crew: '300-9' })
    await submitTitle()

    expect(await screen.findByText(/\(new crew\)/)).toBeTruthy()
    expect(value('Crew')).toBe('300-9')
    const values = within(screen.getByLabelText('Crew')).getAllByRole('option').map((o) => (o as HTMLOptionElement).value)
    expect(values).toEqual(['300-9', 'new'])
    expect(screen.queryByText(/No crew yet/)).toBeNull()
  })

  it('links to the run once it appears in the stream', async () => {
    const { rerender } = await renderLoaded()
    submitDispatchMock.mockResolvedValue({ kind: 'started', workerId: 'w', branch: 'feat/7-x', crew: '200-1' })
    await submitTitle()
    await screen.findByText(/Waiting for it to appear in Fleet/)

    rerender(<DispatchView runs={[run({ id: 'crew:w', branch: 'feat/7-x', crew: { name: '200-1' } })]} />)

    const link = screen.getByRole('link', { name: 'Open run' }) as HTMLAnchorElement
    expect(link.getAttribute('href')).toBe('#/fleet/crew:w/activity')
    expect(screen.queryByText(/Waiting for it to appear/)).toBeNull()
  })

  it('falls back to the run\'s issue link when dispatch minted none', async () => {
    const runs = [run({ id: 'r1', branch: 'feat/8-x', issue: { id: '8', url: 'https://github.com/acme/houston/issues/8' } })]
    await renderLoaded(runs)
    submitDispatchMock.mockResolvedValue({ kind: 'started', workerId: 'w', branch: 'feat/8-x' })
    await submitTitle()

    const link = (await screen.findByRole('link', { name: /view issue/i })) as HTMLAnchorElement
    expect(link.href).toBe('https://github.com/acme/houston/issues/8')
  })
})

describe('DispatchView URL prefill', () => {
  it('applies repo and crew=new from the hash, then replaces the hash', async () => {
    window.location.hash = '#/dispatch?repo=%2Frepo%2Fb&crew=new'
    await renderLoaded()

    await waitFor(() => expect(value('Repo')).toBe('/repo/b'))
    expect(value('Crew')).toBe('new')
    expect(window.location.hash).toBe('#/dispatch')
  })

  it('applies an existing crew of the linked repo', async () => {
    window.location.hash = '#/dispatch?repo=%2Frepo%2Fa&crew=100-2'
    await renderLoaded()

    await waitFor(() => expect(value('Crew')).toBe('100-2'))
  })

  it('shows a notice for an unknown repo and keeps the default', async () => {
    window.location.hash = '#/dispatch?repo=%2Fnowhere&crew=new'
    await renderLoaded()

    expect(await screen.findByText(/Linked repo isn't one houston knows/)).toBeTruthy()
    expect(value('Repo')).toBe('/repo/a')
    expect(value('Crew')).toBe('200-1')
    expect(window.location.hash).toBe('#/dispatch')
  })

  it('re-applies the same link on each hashchange, leaving the draft alone', async () => {
    await renderLoaded()
    change('Title', 'my draft')
    change('Task', 'draft body')
    change('Tier', 'deep')

    act(() => { window.location.hash = '#/dispatch?repo=%2Frepo%2Fb' })
    await waitFor(() => expect(value('Repo')).toBe('/repo/b'))
    expect(window.location.hash).toBe('#/dispatch')

    change('Repo', '/repo/a')
    act(() => { window.location.hash = '#/dispatch?repo=%2Frepo%2Fb' })
    await waitFor(() => expect(value('Repo')).toBe('/repo/b'))

    expect(value('Title')).toBe('my draft')
    expect(value('Task')).toBe('draft body')
    expect(value('Tier')).toBe('deep')
    expect(value('Model')).toBe('opus')
  })

  it('a bare #/dispatch changes nothing', async () => {
    window.location.hash = '#/dispatch'
    await renderLoaded()

    expect(value('Repo')).toBe('/repo/a')
    expect(value('Crew')).toBe('200-1')
  })
})

describe('DispatchView task field', () => {
  function stubScrollHeight(el: HTMLElement, h: number): void {
    Object.defineProperty(el, 'scrollHeight', { configurable: true, get: () => h })
  }

  it('grows to the viewport cap, then scrolls internally', async () => {
    vi.stubGlobal('visualViewport', { height: 500 })
    await renderLoaded()
    const task = screen.getByLabelText('Task') as HTMLTextAreaElement
    expect(task.style.touchAction).toBe('pan-y')

    stubScrollHeight(task, 900)
    change('Task', 'long\n'.repeat(80))
    expect(task.style.height).toBe('200px')
    expect(task.style.overflowY).toBe('auto')

    stubScrollHeight(task, 60)
    change('Task', 'short')
    expect(task.style.height).toBe('120px')
    expect(task.style.overflowY).toBe('hidden')
  })
})
