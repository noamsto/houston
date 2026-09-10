import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, cleanup, renderHook } from '@testing-library/react'
import { useWorkspace } from './useWorkspace'
import type { Workspace } from '../api/workspace'

const fetchWorkspaceMock = vi.fn<() => Promise<Workspace>>()
vi.mock('../api/workspace', () => ({
  fetchWorkspace: () => fetchWorkspaceMock(),
}))

function workspace(host = 'local'): Workspace {
  return { host, sessions: [] }
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
})
