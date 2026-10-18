import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { RepoPicker } from './RepoPicker'
import type { RepoCandidate, RepoEntry } from '../api/repos'

const fetchReposMock = vi.fn<() => Promise<{ roots: string[]; repos: RepoEntry[] }>>()
const fetchCandidatesMock = vi.fn<(q: string) => Promise<{ roots: string[]; candidates: RepoCandidate[]; truncated: boolean }>>()
const addRepoMock = vi.fn<(path: string) => Promise<{ ok: true; path: string } | { ok: false; error: string }>>()
const removeRepoMock = vi.fn<(path: string) => Promise<{ ok: true } | { ok: false; error: string }>>()
// Every call's host, so the local-only assertions keep their plain arguments.
const hostsSeen: { fn: string; host?: string }[] = []
vi.mock('../api/repos', () => ({
  fetchRepos: (host?: string) => {
    hostsSeen.push({ fn: 'fetchRepos', host })
    return fetchReposMock()
  },
  fetchRepoCandidates: (q: string, host?: string) => {
    hostsSeen.push({ fn: 'fetchRepoCandidates', host })
    return fetchCandidatesMock(q)
  },
  addRepo: (path: string, host?: string) => {
    hostsSeen.push({ fn: 'addRepo', host })
    return addRepoMock(path)
  },
  removeRepo: (path: string, host?: string) => {
    hostsSeen.push({ fn: 'removeRepo', host })
    return removeRepoMock(path)
  },
}))

const registered: RepoEntry[] = [
  { path: '/git/alpha', name: 'alpha', valid: true },
  { path: '/git/gone', name: 'gone', valid: false },
]

function setup(candidates: RepoCandidate[] = [], truncated = false, host = 'local') {
  fetchReposMock.mockResolvedValue({ roots: ['/git'], repos: registered })
  fetchCandidatesMock.mockResolvedValue({ roots: ['/git'], candidates, truncated })
  const onChanged = vi.fn()
  const onClose = vi.fn()
  render(<RepoPicker host={host} onChanged={onChanged} onClose={onClose} />)
  return { onChanged, onClose }
}

afterEach(() => {
  cleanup()
  fetchReposMock.mockReset()
  fetchCandidatesMock.mockReset()
  addRepoMock.mockReset()
  removeRepoMock.mockReset()
  hostsSeen.length = 0
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

  it('clears the search error once a later search succeeds', async () => {
    setup()
    await screen.findByText('alpha')
    fetchCandidatesMock.mockRejectedValueOnce(new Error('search blew up'))
    fireEvent.change(screen.getByLabelText('Search repos'), { target: { value: 'be' } })
    expect(await screen.findByRole('alert')).toBeTruthy()

    fireEvent.change(screen.getByLabelText('Search repos'), { target: { value: 'bet' } })

    await waitFor(() => expect(screen.queryByRole('alert')).toBeNull())
  })

  it('keeps a failed repo-list error until the list loads again', async () => {
    fetchReposMock.mockRejectedValueOnce(new Error('list blew up'))
    fetchCandidatesMock.mockResolvedValue({ roots: [], candidates: [], truncated: false })
    render(<RepoPicker host="local" onChanged={vi.fn()} onClose={vi.fn()} />)

    expect(await screen.findByText('list blew up')).toBeTruthy()
    await waitFor(() => expect(fetchCandidatesMock).toHaveBeenCalled())
    expect(screen.getByRole('alert').textContent).toBe('list blew up')
  })

  it('sends every call to the local host as no host', async () => {
    const { onChanged } = setup([{ path: '/git/beta', name: 'beta', registered: false }])
    addRepoMock.mockResolvedValue({ ok: true, path: '/git/beta' })
    fireEvent.click(await screen.findByRole('button', { name: 'Add beta' }))
    await waitFor(() => expect(onChanged).toHaveBeenCalled())

    expect(hostsSeen.length).toBeGreaterThan(0)
    expect(hostsSeen.every((c) => c.host === undefined)).toBe(true)
  })

  it('sends every call to a peer host', async () => {
    const { onChanged } = setup([{ path: '/git/beta', name: 'beta', registered: false }], false, 'b')
    addRepoMock.mockResolvedValue({ ok: true, path: '/git/beta' })
    removeRepoMock.mockResolvedValue({ ok: true })

    fireEvent.click(await screen.findByRole('button', { name: 'Add beta' }))
    await waitFor(() => expect(onChanged).toHaveBeenCalledTimes(1))
    fireEvent.click(await screen.findByRole('button', { name: 'Remove alpha' }))
    await waitFor(() => expect(onChanged).toHaveBeenCalledTimes(2))

    const fns = new Set(hostsSeen.map((c) => c.fn))
    expect([...fns].sort()).toEqual(['addRepo', 'fetchRepoCandidates', 'fetchRepos', 'removeRepo'])
    expect(hostsSeen.every((c) => c.host === 'b')).toBe(true)
  })

  it('closes', async () => {
    const { onClose } = setup()

    fireEvent.click(await screen.findByRole('button', { name: 'Close' }))

    expect(onClose).toHaveBeenCalled()
  })
})
