import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { RestartDispatcher } from './RestartDispatcher'
import { clearPendingRuns, isPendingRun } from './pendingRuns'
import type { DispatchOptions } from '../api/dispatch'
import type { Run } from '../api/runs'

const options: DispatchOptions = {
  repos: [],
  tiers: [],
  efforts: ['low', 'high'],
  plans: [],
  engines: { claude: ['opus', 'sonnet'], pi: ['glm'] },
  engine_order: ['claude', 'pi'],
  tier_models: {},
  dispatcher_engines: ['claude', 'pi'],
}

function dispatcher(p: Partial<Run> = {}): Run {
  return {
    id: 'd/1', agent: 'claude', state: 'idle', role: 'dispatcher', crew: { name: '1700000000-42' },
    activity: {}, tokens: { input: 0, output: 0 },
    caps: { terminal: true, reply: true, kill: true },
    updated_at: 1,
    ...p,
  } as Run
}

function reply(status: number, body: unknown): Response {
  const text = JSON.stringify(body)
  return { ok: status >= 200 && status < 300, status, text: async () => text, json: async () => body } as Response
}

const started = { crew: '1700000000-42', session: 's', window: '@1', pane: '%2', run_id: 'pane-new' }

const SAME = '\0same'

const fetchMock = vi.fn<(input: string, init?: RequestInit) => Promise<Response>>()

function restartCalls(): [string, RequestInit][] {
  return fetchMock.mock.calls.filter((c) => c[0].endsWith('/restart')) as [string, RequestInit][]
}

function lastBody(): unknown {
  const calls = restartCalls()
  return JSON.parse(String(calls[calls.length - 1][1].body))
}

beforeEach(() => {
  fetchMock.mockImplementation((url) =>
    Promise.resolve(url === '/api/dispatch/options' ? reply(200, options) : reply(200, started)),
  )
  vi.stubGlobal('fetch', fetchMock)
})

afterEach(() => {
  cleanup()
  clearPendingRuns()
  fetchMock.mockReset()
  vi.unstubAllGlobals()
})

const open = () => fireEvent.click(screen.getByRole('button', { name: 'Restart' }))
const confirm = (name = 'Restart') => fireEvent.click(screen.getByRole('button', { name }))

describe('RestartDispatcher', () => {
  it('the first tap sends nothing and asks for confirmation', () => {
    render(<RestartDispatcher run={dispatcher()} />)

    open()

    expect(fetchMock).not.toHaveBeenCalled()
    expect(screen.getByText(/Ends this dispatcher's session and starts a new one on crew/)).toBeTruthy()
    expect(screen.getByText(/Workers keep running\./)).toBeTruthy()
    expect(screen.queryByText(/in the middle of a turn/)).toBeNull()
  })

  it('Cancel returns to the closed state', () => {
    render(<RestartDispatcher run={dispatcher()} />)
    open()

    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))

    expect(screen.getByRole('button', { name: 'Restart' })).toBeTruthy()
    expect(screen.queryByText(/Workers keep running/)).toBeNull()
  })

  it('an idle run restarts with an empty body', async () => {
    render(<RestartDispatcher run={dispatcher()} />)
    open()

    confirm()

    await waitFor(() => expect(restartCalls()).toHaveLength(1))
    expect(restartCalls()[0][0]).toBe('/api/runs/d%2F1/restart')
    expect(lastBody()).toEqual({})
  })

  it('a busy run warns and force-restarts', async () => {
    render(<RestartDispatcher run={dispatcher({ state: 'running' })} />)
    open()

    expect(screen.getByText(/in the middle of a turn — restarting interrupts it/)).toBeTruthy()
    confirm('Force restart')

    await waitFor(() => expect(restartCalls()).toHaveLength(1))
    expect(lastBody()).toEqual({ force: true })
  })

  it('a server busy reply switches an idle-looking run to the force form', async () => {
    fetchMock.mockImplementationOnce(() => Promise.resolve(reply(409, { error: 'dispatcher is mid-turn', busy: true })))
    render(<RestartDispatcher run={dispatcher()} />)
    open()

    confirm()

    await screen.findByRole('button', { name: 'Force restart' })
    expect(screen.getByText(/in the middle of a turn/)).toBeTruthy()
    expect(lastBody()).toEqual({})

    confirm('Force restart')

    await waitFor(() => expect(restartCalls()).toHaveLength(2))
    expect(lastBody()).toEqual({ force: true })
  })

  it('success marks the new run pending and replaces the route without adding history', async () => {
    window.location.hash = '#/fleet/d%2F1/crew'
    const before = window.history.length
    render(<RestartDispatcher run={dispatcher()} />)
    open()

    confirm()

    await waitFor(() => expect(window.location.hash).toBe('#/fleet/pane-new/crew'))
    expect(window.history.length).toBe(before)
    expect(isPendingRun('pane-new', Date.now())).toBe(true)
  })

  it('shows Restarting… and disables the action while sending', async () => {
    let resolve: (r: Response) => void = () => {}
    fetchMock.mockImplementationOnce(() => new Promise<Response>((r) => { resolve = r }))
    render(<RestartDispatcher run={dispatcher()} />)
    open()

    confirm()

    const busy = await screen.findByRole('button', { name: 'Restarting…' })
    expect((busy as HTMLButtonElement).disabled).toBe(true)
    resolve(reply(200, started))
  })

  it('only the changed fields are sent, and changing the engine resets the model', async () => {
    render(<RestartDispatcher run={dispatcher()} />)
    open()
    fireEvent.click(screen.getByRole('button', { name: 'Settings' }))
    await screen.findByLabelText('Engine')

    fireEvent.change(screen.getByLabelText('Engine'), { target: { value: 'claude' } })
    fireEvent.change(screen.getByLabelText('Model'), { target: { value: 'opus' } })
    fireEvent.change(screen.getByLabelText('Engine'), { target: { value: 'pi' } })
    expect((screen.getByLabelText('Model') as HTMLSelectElement).value).toBe(SAME)
    fireEvent.change(screen.getByLabelText('Model'), { target: { value: 'glm' } })
    confirm()

    await waitFor(() => expect(restartCalls()).toHaveLength(1))
    expect(lastBody()).toEqual({ engine: 'pi', model: 'glm' })
  })

  it('launcher default model sends model "" alone', async () => {
    render(<RestartDispatcher run={dispatcher()} />)
    open()
    fireEvent.click(screen.getByRole('button', { name: 'Settings' }))
    await screen.findByLabelText('Model')

    fireEvent.change(screen.getByLabelText('Model'), { target: { value: '' } })
    confirm()

    await waitFor(() => expect(restartCalls()).toHaveLength(1))
    expect(lastBody()).toEqual({ model: '' })
  })

  it('offers only the same/default models until an engine is chosen, and sends launcher default effort as ""', async () => {
    render(<RestartDispatcher run={dispatcher()} />)
    open()
    fireEvent.click(screen.getByRole('button', { name: 'Settings' }))
    await screen.findByLabelText('Model')

    const models = Array.from((screen.getByLabelText('Model') as HTMLSelectElement).options).map((o) => o.textContent)
    expect(models).toEqual(['same as now', 'launcher default'])
    fireEvent.change(screen.getByLabelText('Effort'), { target: { value: '' } })
    confirm()

    await waitFor(() => expect(restartCalls()).toHaveLength(1))
    expect(lastBody()).toEqual({ effort: '' })
  })

  it('shows the error when the options cannot load', async () => {
    fetchMock.mockImplementation(() => Promise.resolve(reply(500, 'boom')))
    render(<RestartDispatcher run={dispatcher()} />)
    open()

    fireEvent.click(screen.getByRole('button', { name: 'Settings' }))

    expect(screen.getByText('Loading…')).toBeTruthy()
    await screen.findByText(/fetchDispatchOptions: 500/)
  })

  it('a 422 shows the error, the unchanged crew and the output, and Retry re-sends the request', async () => {
    fetchMock.mockImplementationOnce(() =>
      Promise.resolve(reply(422, { error: 'dispatcher exited immediately', output: 'no such launcher', crew: '1700000000-42' })),
    )
    render(<RestartDispatcher run={dispatcher()} />)
    open()

    confirm()

    await screen.findByText('dispatcher exited immediately')
    expect(screen.getByText('1700000000-42').tagName).toBe('CODE')
    expect(screen.getByText(/is unchanged\./)).toBeTruthy()
    expect(screen.getByText('Output')).toBeTruthy()
    expect(screen.getByText('no such launcher')).toBeTruthy()

    fireEvent.click(screen.getByRole('button', { name: 'Retry' }))

    await waitFor(() => expect(restartCalls()).toHaveLength(2))
    expect(lastBody()).toEqual({})
  })

  it('Cancel after a failure returns to the closed state', async () => {
    fetchMock.mockImplementationOnce(() => Promise.resolve(reply(502, { error: 'tmux unavailable' })))
    render(<RestartDispatcher run={dispatcher()} />)
    open()
    confirm()
    await screen.findByText('tmux unavailable')

    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))

    expect(screen.getByRole('button', { name: 'Restart' })).toBeTruthy()
    expect(screen.queryByText('tmux unavailable')).toBeNull()
  })
})
