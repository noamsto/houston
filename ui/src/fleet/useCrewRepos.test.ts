import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { cleanup, renderHook, waitFor } from '@testing-library/react'
import { resetCrewReposCache, resolveCrewRepo, useCrewRepos } from './useCrewRepos'
import type { CrewRepos } from './useCrewRepos'

function optionsResponse(repos: unknown[]): Response {
  const body = { repos }
  return { status: 200, ok: true, json: async () => body, text: async () => JSON.stringify(body) } as Response
}

let fetchMock: ReturnType<typeof vi.fn>

beforeEach(() => {
  resetCrewReposCache()
  fetchMock = vi.fn()
  vi.stubGlobal('fetch', fetchMock)
})

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

describe('useCrewRepos', () => {
  it('maps crews to their home repo and repo names to repos', async () => {
    fetchMock.mockResolvedValueOnce(
      optionsResponse([
        { name: 'alpha', path: '/g/alpha', home: ['1700000000-11'] },
        { name: 'beta', path: '/g/beta' },
      ]),
    )
    const { result } = renderHook(() => useCrewRepos(true))

    await waitFor(() => expect(result.current.byCrew.size).toBe(1))
    expect(result.current.byCrew.get('1700000000-11')).toEqual({ name: 'alpha', path: '/g/alpha' })
    expect(result.current.byName.get('alpha')).toEqual({ name: 'alpha', path: '/g/alpha' })
    expect(result.current.byName.get('beta')).toEqual({ name: 'beta', path: '/g/beta' })
  })

  it('maps a name shared by two repos to null', async () => {
    fetchMock.mockResolvedValueOnce(
      optionsResponse([
        { name: 'app', path: '/a/app' },
        { name: 'app', path: '/b/app' },
        { name: 'solo', path: '/a/solo' },
      ]),
    )
    const { result } = renderHook(() => useCrewRepos(true))

    await waitFor(() => expect(result.current.byName.size).toBe(2))
    expect(result.current.byName.get('app')).toBeNull()
    expect(result.current.byName.get('solo')).toEqual({ name: 'solo', path: '/a/solo' })
  })

  it('fetches once across hook instances', async () => {
    fetchMock.mockResolvedValue(optionsResponse([{ name: 'alpha', path: '/g/alpha' }]))
    const a = renderHook(() => useCrewRepos(true))
    const b = renderHook(() => useCrewRepos(true))

    await waitFor(() => expect(a.result.current.byName.size).toBe(1))
    await waitFor(() => expect(b.result.current.byName.size).toBe(1))
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })

  it('does not fetch while disabled', async () => {
    renderHook(() => useCrewRepos(false))
    await Promise.resolve()
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it('stays empty when the fetch fails', async () => {
    fetchMock.mockRejectedValueOnce(new TypeError('network down'))
    const { result } = renderHook(() => useCrewRepos(true))

    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1))
    await Promise.resolve()
    expect(result.current.byCrew.size).toBe(0)
    expect(result.current.byName.size).toBe(0)
  })
})

describe('resolveCrewRepo', () => {
  const alpha = { name: 'alpha', path: '/g/alpha' }
  const beta = { name: 'beta', path: '/g/beta' }
  const repos: CrewRepos = {
    byCrew: new Map([['c1', alpha]]),
    byName: new Map([['beta', beta], ['app', null]]),
  }

  it('prefers the crew home over the project name', () => {
    expect(resolveCrewRepo('c1', 'beta', repos)).toBe(alpha)
  })

  it('falls back to the project name', () => {
    expect(resolveCrewRepo('c2', 'beta', repos)).toBe(beta)
  })

  it('returns undefined for an ambiguous, unknown or missing project', () => {
    expect(resolveCrewRepo('c2', 'app', repos)).toBeUndefined()
    expect(resolveCrewRepo('c2', 'nope', repos)).toBeUndefined()
    expect(resolveCrewRepo('c2', undefined, repos)).toBeUndefined()
  })
})
