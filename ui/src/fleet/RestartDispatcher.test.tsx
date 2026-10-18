import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { RestartDispatcher } from './RestartDispatcher'
import { clearPendingRuns, isPendingRun, restartFailure } from './pendingRuns'
import { RunDetail } from './RunDetail'
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

  it('a 422 arriving after the control unmounted is shown on the old run route', async () => {
    let resolve: (r: Response) => void = () => {}
    fetchMock.mockImplementationOnce(() => new Promise<Response>((r) => { resolve = r }))
    const view = render(<RestartDispatcher run={dispatcher()} />)
    open()
    confirm()
    await screen.findByRole('button', { name: 'Restarting…' })

    view.unmount()
    resolve(reply(422, { error: 'dispatcher exited immediately', output: 'no such launcher', crew: '1700000000-42', old_ended: true }))
    await waitFor(() => expect(restartFailure('d/1', Date.now())).not.toBeNull())

    render(<RunDetail mode="dispatcher" runs={[]} hasSnapshot streamConnected now={Date.now()} id="d/1" />)
    expect(screen.getByText('The restart did not come up')).toBeTruthy()
    expect(screen.getByText('dispatcher exited immediately')).toBeTruthy()
    expect(screen.getByText('1700000000-42').tagName).toBe('CODE')
    expect(screen.getByText(/is intact — workers keep running/)).toBeTruthy()
    expect(screen.getByText('no such launcher')).toBeTruthy()
    expect(isPendingRun('d/1', Date.now())).toBe(false)
  })

  it.each([
    [409, { error: 'dispatcher is mid-turn' }],
    [422, { error: 'repo path not allowed', crew: '1700000000-42' }],
  ])('a pre-kill %i records no failure for the old run', async (status, body) => {
    fetchMock.mockImplementationOnce(() => Promise.resolve(reply(status, body)))
    render(<RestartDispatcher run={dispatcher()} />)
    open()
    confirm()
    await screen.findByText(body.error)

    expect(restartFailure('d/1', Date.now())).toBeNull()
    expect(isPendingRun('d/1', Date.now())).toBe(false)
  })

  it('an old_ended 422 shows the recovery line with no Retry; Cancel keeps the record for the route', async () => {
    fetchMock.mockImplementationOnce(() =>
      Promise.resolve(reply(422, { error: 'dispatcher exited immediately', crew: '1700000000-42', old_ended: true })),
    )
    const run = dispatcher()
    render(<RestartDispatcher run={run} />)
    open()
    confirm()
    await screen.findByText('dispatcher exited immediately')

    expect(screen.queryByText(/is unchanged\./)).toBeNull()
    expect(screen.getByText(/CREW_ID=1700000000-42 dispatcher\./)).toBeTruthy()
    expect(screen.queryByRole('button', { name: 'Retry' })).toBeNull()

    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))

    expect(screen.getByRole('button', { name: 'Restart' })).toBeTruthy()
    expect(restartFailure('d/1', Date.now())).not.toBeNull()
    cleanup()
    render(<RunDetail mode="dispatcher" runs={[]} hasSnapshot streamConnected now={Date.now()} id="d/1" />)
    expect(screen.getByText(/CREW_ID=1700000000-42 dispatcher\./)).toBeTruthy()
  })

  it('on a peer run Settings loads the peer host\'s options', async () => {
    render(<RestartDispatcher run={dispatcher({ host: 'b' })} />)
    open()
    fireEvent.click(screen.getByRole('button', { name: 'Settings' }))

    await waitFor(() => expect(fetchMock.mock.calls.map((c) => c[0])).toContain('/api/hosts/b/dispatch/options'))
  })

  it('on a peer run an old_ended 422 names the host in the relaunch line', async () => {
    fetchMock.mockImplementationOnce(() =>
      Promise.resolve(reply(422, { error: 'dispatcher exited immediately', crew: '1700000000-42', old_ended: true })),
    )
    render(<RestartDispatcher run={dispatcher({ host: 'b' })} />)
    open()
    confirm()
    await screen.findByText('dispatcher exited immediately')

    expect(screen.getByText(/on b with CREW_ID=1700000000-42 dispatcher\./)).toBeTruthy()
  })

  it('an old_ended unchecked 502 says to check Fleet, not to relaunch', async () => {
    fetchMock.mockImplementationOnce(() =>
      Promise.resolve(reply(502, { error: 'tmux unavailable', crew: '1700000000-42', old_ended: true })),
    )
    render(<RestartDispatcher run={dispatcher()} />)
    open()
    confirm()
    await screen.findByText('tmux unavailable')

    expect(screen.getByText(/Check Fleet for the new dispatcher before relaunching\./)).toBeTruthy()
    expect(screen.queryByText(/CREW_ID=/)).toBeNull()
    expect(screen.queryByRole('button', { name: 'Retry' })).toBeNull()
  })

  it('a non-old_ended failure keeps Retry and Cancel clears nothing it never recorded', async () => {
    fetchMock.mockImplementationOnce(() => Promise.resolve(reply(502, { error: 'tmux unavailable', crew: '1700000000-42' })))
    render(<RestartDispatcher run={dispatcher()} />)
    open()
    confirm()
    await screen.findByText('tmux unavailable')

    expect(screen.getByText(/is unchanged\./)).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Retry' })).toBeTruthy()
    expect(restartFailure('d/1', Date.now())).toBeNull()
  })

  it('a dropped connection shows a notice with no Retry, keeps the old id pending and records nothing', async () => {
    fetchMock.mockImplementationOnce(() => Promise.reject(new Error('network down')))
    render(<RestartDispatcher run={dispatcher()} />)
    open()
    confirm()

    await screen.findByText(/The connection dropped — the restart may still be running on the server\./)
    expect(screen.getByText(/Watch Fleet for the new dispatcher before retrying\./)).toBeTruthy()
    expect(screen.queryByRole('button', { name: 'Retry' })).toBeNull()
    expect(isPendingRun('d/1', Date.now())).toBe(true)
    expect(restartFailure('d/1', Date.now())).toBeNull()

    fireEvent.click(screen.getByRole('button', { name: 'Close' }))

    expect(screen.getByRole('button', { name: 'Restart' })).toBeTruthy()
  })

  it('the old id is pending while the request is in flight', async () => {
    let resolve: (r: Response) => void = () => {}
    fetchMock.mockImplementationOnce(() => new Promise<Response>((r) => { resolve = r }))
    const view = render(<RestartDispatcher run={dispatcher()} />)
    open()
    confirm()
    await screen.findByRole('button', { name: 'Restarting…' })
    view.unmount()

    render(<RunDetail mode="dispatcher" runs={[]} hasSnapshot streamConnected now={Date.now()} id="d/1" />)
    expect(screen.getByText(/starting the restarted dispatcher/i)).toBeTruthy()
    expect(screen.queryByText(/no longer available/i)).toBeNull()

    resolve(reply(200, started))
    await waitFor(() => expect(isPendingRun('d/1', Date.now())).toBe(false))
  })

  it('a new send clears the earlier recorded failure', async () => {
    fetchMock.mockImplementationOnce(() => Promise.resolve(reply(502, { error: 'tmux unavailable', old_ended: true })))
    render(<RestartDispatcher run={dispatcher()} />)
    open()
    confirm()
    await screen.findByText('tmux unavailable')
    expect(restartFailure('d/1', Date.now())).toEqual({ error: 'tmux unavailable', output: undefined, crew: undefined, status: 502 })

    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    open()
    confirm()

    await waitFor(() => expect(restartFailure('d/1', Date.now())).toBeNull())
  })

  it('a run that turns busy after the panel opened keeps the plain form and sends no force', async () => {
    const view = render(<RestartDispatcher run={dispatcher()} />)
    open()

    view.rerender(<RestartDispatcher run={dispatcher({ state: 'running' })} />)

    expect(screen.queryByText(/in the middle of a turn/)).toBeNull()
    expect(screen.queryByRole('button', { name: 'Force restart' })).toBeNull()
    confirm('Restart')

    await waitFor(() => expect(restartCalls()).toHaveLength(1))
    expect(lastBody()).toEqual({})
  })

  it('success after the user navigated away leaves the hash alone but still marks the new run pending', async () => {
    window.location.hash = '#/fleet/d%2F1/crew'
    let resolve: (r: Response) => void = () => {}
    fetchMock.mockImplementationOnce(() => new Promise<Response>((r) => { resolve = r }))
    render(<RestartDispatcher run={dispatcher()} />)
    open()
    confirm()
    await screen.findByRole('button', { name: 'Restarting…' })

    window.location.hash = '#/fleet'
    resolve(reply(200, started))

    await waitFor(() => expect(isPendingRun('pane-new', Date.now())).toBe(true))
    expect(window.location.hash).toBe('#/fleet')
  })
})
