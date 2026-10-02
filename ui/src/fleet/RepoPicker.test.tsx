import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { RepoPicker } from './RepoPicker'
import type { RepoCandidate, RepoEntry } from '../api/repos'

const fetchReposMock = vi.fn<() => Promise<{ roots: string[]; repos: RepoEntry[] }>>()
const fetchCandidatesMock = vi.fn<(q: string) => Promise<{ roots: string[]; candidates: RepoCandidate[]; truncated: boolean }>>()
const addRepoMock = vi.fn<(path: string) => Promise<{ ok: true; path: string } | { ok: false; error: string }>>()
const removeRepoMock = vi.fn<(path: string) => Promise<{ ok: true } | { ok: false; error: string }>>()
vi.mock('../api/repos', () => ({
  fetchRepos: () => fetchReposMock(),
  fetchRepoCandidates: (q: string) => fetchCandidatesMock(q),
  addRepo: (path: string) => addRepoMock(path),
  removeRepo: (path: string) => removeRepoMock(path),
}))

const registered: RepoEntry[] = [
  { path: '/git/alpha', name: 'alpha', valid: true },
  { path: '/git/gone', name: 'gone', valid: false },
]

function setup(candidates: RepoCandidate[] = [], truncated = false) {
  fetchReposMock.mockResolvedValue({ roots: ['/git'], repos: registered })
  fetchCandidatesMock.mockResolvedValue({ roots: ['/git'], candidates, truncated })
  const onChanged = vi.fn()
  const onClose = vi.fn()
  render(<RepoPicker onChanged={onChanged} onClose={onClose} />)
  return { onChanged, onClose }
}

afterEach(() => {
  cleanup()
  fetchReposMock.mockReset()
  fetchCandidatesMock.mockReset()
  addRepoMock.mockReset()
  removeRepoMock.mockReset()
})

describe('RepoPicker', () => {
  it('lists registered repos and labels an invalid one "missing"', async () => {
    setup()

    expect(await screen.findByText('alpha')).toBeTruthy()
    expect(screen.getByText('gone')).toBeTruthy()
    expect(screen.getAllByText('missing')).toHaveLength(1)
  })

  it('searches candidates after typing', async () => {
    setup([{ path: '/git/beta', name: 'beta', registered: false }])

    fireEvent.change(await screen.findByLabelText('Search repos'), { target: { value: 'be' } })

    await waitFor(() => expect(fetchCandidatesMock).toHaveBeenCalledWith('be'))
    expect(await screen.findByText('beta')).toBeTruthy()
  })

  it('adds a candidate and reports its path', async () => {
    const { onChanged } = setup([{ path: '/git/beta', name: 'beta', registered: false }])
    addRepoMock.mockResolvedValue({ ok: true, path: '/git/beta' })

    fireEvent.click(await screen.findByRole('button', { name: 'Add beta' }))

    await waitFor(() => expect(onChanged).toHaveBeenCalledWith('/git/beta'))
    expect(addRepoMock).toHaveBeenCalledWith('/git/beta')
  })

  it('disables Add for an already registered candidate', async () => {
    setup([{ path: '/git/alpha', name: 'alpha', registered: true }])

    const add = (await screen.findByRole('button', { name: 'Add alpha' })) as HTMLButtonElement

    expect(add.disabled).toBe(true)
  })

  it('removes a repo and reports the change without a path', async () => {
    const { onChanged } = setup()
    removeRepoMock.mockResolvedValue({ ok: true })

    fireEvent.click(await screen.findByRole('button', { name: 'Remove alpha' }))

    await waitFor(() => expect(onChanged).toHaveBeenCalledWith(undefined))
    expect(removeRepoMock).toHaveBeenCalledWith('/git/alpha')
  })

  it('shows an add error and does not report a change', async () => {
    const { onChanged } = setup([{ path: '/git/beta', name: 'beta', registered: false }])
    addRepoMock.mockResolvedValue({ ok: false, error: 'not a git main checkout' })

    fireEvent.click(await screen.findByRole('button', { name: 'Add beta' }))

    expect(await screen.findByText('not a git main checkout')).toBeTruthy()
    expect(onChanged).not.toHaveBeenCalled()
  })

  it('closes', async () => {
    const { onClose } = setup()

    fireEvent.click(await screen.findByRole('button', { name: 'Close' }))

    expect(onClose).toHaveBeenCalled()
  })
})
