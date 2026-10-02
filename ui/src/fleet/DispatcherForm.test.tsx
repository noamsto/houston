import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, within } from '@testing-library/react'
import { DispatcherForm } from './DispatcherForm'
import type { DispatchOptions, DispatcherOutcome, DispatcherRequest } from '../api/dispatch'
import type { Run } from '../api/runs'

const submitDispatcherMock = vi.fn<(req: DispatcherRequest) => Promise<DispatcherOutcome>>()
vi.mock('../api/dispatch', () => ({
  submitDispatcher: (req: DispatcherRequest) => submitDispatcherMock(req),
}))

const options: DispatchOptions = {
  repos: [{ path: '/repo/a', name: 'repo-a', crews: [] }],
  tiers: ['standard'],
  efforts: ['low', 'medium', 'high'],
  plans: ['required'],
  engines: { claude: ['opus', 'sonnet'], pi: ['glm'] },
  engine_order: ['claude', 'pi'],
  tier_models: {},
  dispatcher_engines: ['pi', 'claude'],
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

function setup(o: DispatchOptions = options, runs: Run[] = []) {
  return render(<DispatcherForm options={o} repo="/repo/a" runs={runs} />)
}

function change(label: string, v: string): void {
  fireEvent.change(screen.getByLabelText(label), { target: { value: v } })
}

function optionValues(label: string): string[] {
  return within(screen.getByLabelText(label)).getAllByRole('option').map((o) => (o as HTMLOptionElement).value)
}

const startButton = () => screen.getByRole('button', { name: /Start dispatcher|Starting/ }) as HTMLButtonElement

afterEach(() => {
  cleanup()
  submitDispatcherMock.mockReset()
})

describe('DispatcherForm task rows', () => {
  it('starts with one empty row', () => {
    setup()

    expect((screen.getByLabelText('Task 1') as HTMLTextAreaElement).value).toBe('')
    expect(screen.queryByLabelText('Task 2')).toBeNull()
  })

  it('adds a row and removes it again', () => {
    setup()

    fireEvent.click(screen.getByRole('button', { name: 'Add task' }))
    expect(screen.getByLabelText('Task 2')).toBeTruthy()

    fireEvent.click(screen.getByRole('button', { name: 'Remove task 2' }))
    expect(screen.queryByLabelText('Task 2')).toBeNull()
  })

  it('cannot add past 20 rows', () => {
    setup()
    for (let i = 0; i < 19; i++) fireEvent.click(screen.getByRole('button', { name: 'Add task' }))

    expect(screen.getByLabelText('Task 20')).toBeTruthy()
    expect((screen.getByRole('button', { name: 'Add task' }) as HTMLButtonElement).disabled).toBe(true)
  })

  it('a row starting with "-" disables submit and says why', () => {
    setup()

    change('Task 1', '-rf everything')

    expect(startButton().disabled).toBe(true)
    expect(screen.getByText('A task cannot start with "-"')).toBeTruthy()
  })
})

describe('DispatcherForm launcher options', () => {
  it('lists exactly the dispatcher engines, in order', () => {
    setup()

    expect(optionValues('Engine')).toEqual(['pi', 'claude'])
  })

  it('offers launcher default plus the chosen engine\'s models', () => {
    setup()

    expect(optionValues('Model')).toEqual(['', 'glm'])
    expect(within(screen.getByLabelText('Model')).getAllByRole('option')[0].textContent).toBe('launcher default')

    change('Engine', 'claude')
    expect(optionValues('Model')).toEqual(['', 'opus', 'sonnet'])
  })

  it('offers launcher default plus the efforts', () => {
    setup()

    expect(optionValues('Effort')).toEqual(['', 'low', 'medium', 'high'])
  })

  it('with no engines shows the reason and disables submit', () => {
    setup({ ...options, dispatcher_engines: [], dispatcher_engines_error: 'dispatch --engines failed' })

    expect(screen.getByText('dispatch --engines failed')).toBeTruthy()
    expect(startButton().disabled).toBe(true)
  })

  it('with no engines and no reason shows a generic hint', () => {
    setup({ ...options, dispatcher_engines: [] })

    expect(screen.getByText('No dispatcher engines available')).toBeTruthy()
    expect(startButton().disabled).toBe(true)
  })
})

describe('DispatcherForm submit', () => {
  it('sends normalized non-empty tasks with engine, model and effort', async () => {
    setup()
    submitDispatcherMock.mockResolvedValue({ kind: 'started', crew: '1-2', session: 's', window: '@1', pane: '%7', runId: 'pane-7' })

    change('Task 1', '  fix\n the  bug ')
    fireEvent.click(screen.getByRole('button', { name: 'Add task' }))
    fireEvent.click(screen.getByRole('button', { name: 'Add task' }))
    change('Task 3', 'second')
    change('Engine', 'claude')
    change('Model', 'opus')
    change('Effort', 'high')
    fireEvent.click(startButton())

    expect(submitDispatcherMock).toHaveBeenCalledWith({
      repo: '/repo/a', tasks: ['fix the bug', 'second'], engine: 'claude', model: 'opus', effort: 'high',
    })
    await screen.findByText(/Waiting for it to appear in Fleet/)
  })

  it('omits model and effort left at launcher default', async () => {
    setup()
    submitDispatcherMock.mockResolvedValue({ kind: 'started', crew: '1-2', session: 's', window: '@1', pane: '%7', runId: 'pane-7' })

    fireEvent.click(startButton())

    expect(submitDispatcherMock).toHaveBeenCalledWith({ repo: '/repo/a', tasks: [], engine: 'pi' })
    await screen.findByText(/Waiting for it to appear in Fleet/)
  })

  it('sends one request from two rapid clicks', async () => {
    setup()
    let resolve!: (o: DispatcherOutcome) => void
    submitDispatcherMock.mockReturnValue(new Promise((r) => { resolve = r }))

    const button = startButton()
    fireEvent.click(button)
    fireEvent.click(button)

    expect(submitDispatcherMock).toHaveBeenCalledTimes(1)
    expect(startButton().textContent).toBe('Starting…')
    resolve({ kind: 'started', crew: '1-2', session: 's', window: '@1', pane: '%7', runId: 'pane-7' })
    await screen.findByText(/Waiting for it to appear in Fleet/)
  })
})

describe('DispatcherForm result', () => {
  const started: DispatcherOutcome = { kind: 'started', crew: '1-2', session: 'proj', window: '@1', pane: '%7', runId: 'pane-7' }

  it('shows session and crew, waiting until the run is in the stream', async () => {
    setup()
    submitDispatcherMock.mockResolvedValue(started)
    fireEvent.click(startButton())

    expect(await screen.findByText('proj')).toBeTruthy()
    expect(screen.getByText('1-2')).toBeTruthy()
    expect(screen.getByText(/Waiting for it to appear in Fleet/)).toBeTruthy()
    expect(screen.queryByRole('link', { name: 'Open run' })).toBeNull()
  })

  it('links to the run once it is listed', async () => {
    const { rerender } = setup()
    submitDispatcherMock.mockResolvedValue(started)
    fireEvent.click(startButton())
    await screen.findByText(/Waiting for it to appear in Fleet/)

    rerender(<DispatcherForm options={options} repo="/repo/a" runs={[run({ id: 'pane-7' })]} />)

    const link = screen.getByRole('link', { name: 'Open run' }) as HTMLAnchorElement
    expect(link.getAttribute('href')).toBe('#/fleet/pane-7')
    expect(screen.queryByText(/Waiting for it to appear/)).toBeNull()
  })

  it('shows the error and output on failure', async () => {
    setup()
    submitDispatcherMock.mockResolvedValue({ kind: 'failed', status: 422, error: 'launcher died', output: 'boom on line 3' })
    fireEvent.click(startButton())

    expect(await screen.findByText('launcher died')).toBeTruthy()
    expect(screen.getByText('boom on line 3')).toBeTruthy()
  })
})
