import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, cleanup, renderHook } from '@testing-library/react'
import { useWorkspace } from './useWorkspace'
import type { Workspace } from '../api/workspace'

const fetchWorkspaceMock = vi.fn<() => Promise<Workspace>>()
vi.mock('../api/workspace', () => ({
  fetchWorkspace: () => fetchWorkspaceMock(),
}))

function workspace(host = 'local'): Workspace {
  return { host, projects: [] }
}

// Named (not anonymous) so eslint-plugin-react-hooks recognises this as a
// custom hook rather than an arbitrary function calling a hook.
function useWorkspaceProbe() {
  return useWorkspace()
}

beforeEach(() => {
  vi.useFakeTimers()
})

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
  vi.useRealTimers()
  fetchWorkspaceMock.mockReset()
})

describe('useWorkspace lifecycle', () => {
  it('fetches once on mount and populates state', async () => {
    fetchWorkspaceMock.mockResolvedValue(workspace())
    const { result } = renderHook(useWorkspaceProbe)

    // Flush the microtask queue from the synchronous mount-time fetch,
    // without advancing the clock far enough to fire the poll interval.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0)
    })

    expect(fetchWorkspaceMock).toHaveBeenCalledTimes(1)
    expect(result.current.workspace).toEqual(workspace())
    expect(result.current.loading).toBe(false)
  })

  it('polls again after the interval elapses', async () => {
    fetchWorkspaceMock.mockResolvedValue(workspace())
    renderHook(useWorkspaceProbe)

    await act(async () => {
      await vi.advanceTimersByTimeAsync(0)
    })
    expect(fetchWorkspaceMock).toHaveBeenCalledTimes(1)

    await act(async () => {
      await vi.advanceTimersByTimeAsync(3000)
    })
    expect(fetchWorkspaceMock).toHaveBeenCalledTimes(2)
  })

  it('stops polling after unmount', async () => {
    fetchWorkspaceMock.mockResolvedValue(workspace())
    const { unmount } = renderHook(useWorkspaceProbe)

    await act(async () => {
      await vi.advanceTimersByTimeAsync(0)
    })
    expect(fetchWorkspaceMock).toHaveBeenCalledTimes(1)

    unmount()

    await act(async () => {
      await vi.advanceTimersByTimeAsync(9000)
    })
    expect(fetchWorkspaceMock).toHaveBeenCalledTimes(1)
  })

  it('a rejected poll sets error and keeps the prior workspace value', async () => {
    fetchWorkspaceMock.mockResolvedValueOnce(workspace('first'))
    const { result } = renderHook(useWorkspaceProbe)

    await act(async () => {
      await vi.advanceTimersByTimeAsync(0)
    })
    expect(result.current.workspace).toEqual(workspace('first'))
    expect(result.current.error).toBeNull()

    fetchWorkspaceMock.mockRejectedValueOnce(new Error('network down'))
    await act(async () => {
      await vi.advanceTimersByTimeAsync(3000)
    })

    expect(result.current.workspace).toEqual(workspace('first'))
    expect(result.current.error).toBe('network down')
  })

  it('a rejected first fetch sets error, leaves workspace null, and stops loading', async () => {
    fetchWorkspaceMock.mockRejectedValueOnce(new Error('network down'))
    const { result } = renderHook(useWorkspaceProbe)

    await act(async () => {
      await vi.advanceTimersByTimeAsync(0)
    })

    expect(result.current.workspace).toBeNull()
    expect(result.current.error).toBe('network down')
    expect(result.current.loading).toBe(false)
  })

  it('does not poll while the tab is hidden, and fetches immediately when it becomes visible', async () => {
    fetchWorkspaceMock.mockResolvedValue(workspace())
    renderHook(useWorkspaceProbe)
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0)
    })
    expect(fetchWorkspaceMock).toHaveBeenCalledTimes(1)

    const hidden = vi.spyOn(document, 'hidden', 'get').mockReturnValue(true)
    await act(async () => {
      await vi.advanceTimersByTimeAsync(9000)
    })
    expect(fetchWorkspaceMock).toHaveBeenCalledTimes(1)

    hidden.mockReturnValue(false)
    await act(async () => {
      document.dispatchEvent(new Event('visibilitychange'))
      await vi.advanceTimersByTimeAsync(0)
    })
    expect(fetchWorkspaceMock).toHaveBeenCalledTimes(2)

    await act(async () => {
      await vi.advanceTimersByTimeAsync(3000)
    })
    expect(fetchWorkspaceMock).toHaveBeenCalledTimes(3)
  })

  it('an older, slower response cannot overwrite a newer one', async () => {
    let resolveOld!: (w: Workspace) => void
    fetchWorkspaceMock.mockImplementationOnce(() => new Promise<Workspace>((r) => { resolveOld = r }))
    const { result } = renderHook(useWorkspaceProbe)

    fetchWorkspaceMock.mockResolvedValueOnce(workspace('new'))
    await act(async () => {
      result.current.refresh()
      await vi.advanceTimersByTimeAsync(0)
    })
    expect(result.current.workspace).toEqual(workspace('new'))
    expect(result.current.refreshing).toBe(false)

    await act(async () => {
      resolveOld(workspace('old'))
      await vi.advanceTimersByTimeAsync(0)
    })
    expect(result.current.workspace).toEqual(workspace('new'))
  })

  it('refresh() fetches on demand and clears a previous error', async () => {
    fetchWorkspaceMock.mockRejectedValueOnce(new Error('network down'))
    const { result } = renderHook(useWorkspaceProbe)
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0)
    })
    expect(result.current.error).toBe('network down')

    fetchWorkspaceMock.mockResolvedValueOnce(workspace('again'))
    await act(async () => {
      result.current.refresh()
      await vi.advanceTimersByTimeAsync(0)
    })
    expect(result.current.error).toBeNull()
    expect(result.current.workspace).toEqual(workspace('again'))
  })
})
