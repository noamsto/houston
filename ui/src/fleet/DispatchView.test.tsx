import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, within } from '@testing-library/react'
import { DispatchView } from './DispatchView'
import type { DispatchOptions, DispatchOutcome, DispatchRequest } from '../api/dispatch'

const fetchDispatchOptionsMock = vi.fn<() => Promise<DispatchOptions>>()
const submitDispatchMock = vi.fn<(req: DispatchRequest) => Promise<DispatchOutcome>>()
vi.mock('../api/dispatch', () => ({
  fetchDispatchOptions: () => fetchDispatchOptionsMock(),
  submitDispatch: (req: DispatchRequest) => submitDispatchMock(req),
}))

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
}

async function renderLoaded(): Promise<void> {
  fetchDispatchOptionsMock.mockResolvedValue(optionsFixture)
  render(<DispatchView />)
  await screen.findByLabelText('Repo')
}

afterEach(() => {
  cleanup()
  fetchDispatchOptionsMock.mockReset()
  submitDispatchMock.mockReset()
})

describe('DispatchView options', () => {
  it('renders the fetched options with the documented defaults', async () => {
    await renderLoaded()

    expect((screen.getByLabelText('Repo') as HTMLSelectElement).value).toBe('/repo/a')
    expect((screen.getByLabelText('Tier') as HTMLSelectElement).value).toBe('standard')
    expect((screen.getByLabelText('Engine') as HTMLSelectElement).value).toBe('claude')
    expect((screen.getByLabelText('Model') as HTMLSelectElement).value).toBe('opus')
    expect((screen.getByLabelText('Effort') as HTMLSelectElement).value).toBe('high')
    expect((screen.getByLabelText('Crew') as HTMLSelectElement).value).toBe('200-1')
  })

  it('shows the options error and refetches on retry', async () => {
    fetchDispatchOptionsMock.mockRejectedValueOnce(new Error('fetchDispatchOptions: 500 boom'))
    render(<DispatchView />)
    expect(await screen.findByText('fetchDispatchOptions: 500 boom')).toBeTruthy()

    fetchDispatchOptionsMock.mockResolvedValueOnce(optionsFixture)
    fireEvent.click(screen.getByRole('button', { name: 'Retry' }))

    expect(await screen.findByLabelText('Repo')).toBeTruthy()
  })
})

describe('DispatchView field resets', () => {
  it('engine change filters the model list and resets to its first model', async () => {
    await renderLoaded()

    fireEvent.change(screen.getByLabelText('Engine'), { target: { value: 'codex' } })

    const modelSelect = screen.getByLabelText('Model') as HTMLSelectElement
    expect(modelSelect.value).toBe('gpt-5.6-sol')
    expect(within(modelSelect).getAllByRole('option').map((o) => (o as HTMLOptionElement).value)).toEqual([
      'gpt-5.6-sol', 'gpt-5.6-terra',
    ])
  })

  it('repo change resets the crew list to the new repo\'s newest', async () => {
    await renderLoaded()

    fireEvent.change(screen.getByLabelText('Repo'), { target: { value: '/repo/b' } })

    expect(screen.queryByLabelText('Crew')).toBeNull()
    expect(screen.getByText(/No crews for this repo/)).toBeTruthy()
  })

  it('a repo with no crews disables submit and shows the hint', async () => {
    await renderLoaded()
    fireEvent.change(screen.getByLabelText('Title'), { target: { value: 'do the thing' } })

    fireEvent.change(screen.getByLabelText('Repo'), { target: { value: '/repo/b' } })

    expect(screen.getByText(/No crews for this repo/)).toBeTruthy()
    expect((screen.getByRole('button', { name: 'Dispatch' }) as HTMLButtonElement).disabled).toBe(true)
  })
})

describe('DispatchView submit', () => {
  it('posts the exact request object', async () => {
    await renderLoaded()
    submitDispatchMock.mockResolvedValue({ kind: 'started', workerId: 'worker:feat/1-x#s1', branch: 'feat/1-x' })

    fireEvent.change(screen.getByLabelText('Title'), { target: { value: '  Fix the thing  ' } })
    fireEvent.change(screen.getByLabelText('Task'), { target: { value: 'Body text' } })
    fireEvent.change(screen.getByLabelText('Issue (optional)'), { target: { value: ' 53 ' } })
    fireEvent.click(screen.getByRole('button', { name: 'Dispatch' }))

    expect(submitDispatchMock).toHaveBeenCalledWith({
      repo: '/repo/a',
      title: 'Fix the thing',
      spec: 'Body text',
      tier: 'standard',
      engine: 'claude',
      model: 'opus',
      effort: 'high',
      crew: '200-1',
      issue: '53',
    })
    await screen.findByText(/appear in Fleet shortly/)
  })

  it('produces exactly one submitDispatch call from two rapid clicks', async () => {
    await renderLoaded()
    let resolve!: (o: DispatchOutcome) => void
    submitDispatchMock.mockReturnValue(new Promise((r) => { resolve = r }))
    fireEvent.change(screen.getByLabelText('Title'), { target: { value: 'go' } })

    const button = screen.getByRole('button', { name: 'Dispatch' })
    fireEvent.click(button)
    fireEvent.click(button)

    expect(submitDispatchMock).toHaveBeenCalledTimes(1)
    resolve({ kind: 'started', workerId: 'worker:feat/1-x#s1', branch: 'feat/1-x' })
    await screen.findByText(/appear in Fleet shortly/)
  })

  it('success shows worker_id, branch and issue link, and clears the title', async () => {
    await renderLoaded()
    submitDispatchMock.mockResolvedValue({
      kind: 'started',
      workerId: 'worker:feat/53-x#s1',
      branch: 'feat/53-x',
      issueUrl: 'https://github.com/acme/houston/issues/53',
    })
    fireEvent.change(screen.getByLabelText('Title'), { target: { value: 'go' } })
    fireEvent.click(screen.getByRole('button', { name: 'Dispatch' }))

    expect(await screen.findByText('worker:feat/53-x#s1')).toBeTruthy()
    expect(screen.getByText('feat/53-x')).toBeTruthy()
    const link = screen.getByRole('link', { name: /view issue/i }) as HTMLAnchorElement
    expect(link.href).toBe('https://github.com/acme/houston/issues/53')
    expect((screen.getByLabelText('Title') as HTMLInputElement).value).toBe('')
  })

  it('failure shows the server error text verbatim', async () => {
    await renderLoaded()
    submitDispatchMock.mockResolvedValue({ kind: 'failed', status: 422, error: 'dispatch: model not allowed for engine' })
    fireEvent.change(screen.getByLabelText('Title'), { target: { value: 'go' } })
    fireEvent.click(screen.getByRole('button', { name: 'Dispatch' }))

    expect(await screen.findByText('dispatch: model not allowed for engine')).toBeTruthy()
  })
})
