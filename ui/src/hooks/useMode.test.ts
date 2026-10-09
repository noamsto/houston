import { act, cleanup, renderHook } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { useMode } from './useMode'
import { fetchMode } from '../api/mode'

vi.mock('../api/mode', () => ({ fetchMode: vi.fn() }))

beforeEach(() => {
  vi.useFakeTimers()
  vi.mocked(fetchMode).mockReset()
})

afterEach(() => {
  cleanup()
  vi.useRealTimers()
})

describe('useMode', () => {
  it('is null until the mode arrives, then keeps it without refetching', async () => {
    vi.mocked(fetchMode).mockResolvedValue('dispatcher')
    const { result } = renderHook(() => useMode())
    expect(result.current).toBeNull()

    await act(() => vi.advanceTimersByTimeAsync(0))
    expect(result.current).toBe('dispatcher')

    await act(() => vi.advanceTimersByTimeAsync(20_000))
    expect(fetchMode).toHaveBeenCalledTimes(1)
  })

  it('retries after 5 s when the fetch rejects', async () => {
    vi.mocked(fetchMode).mockRejectedValueOnce(new Error('down')).mockResolvedValueOnce('tmux')
    const { result } = renderHook(() => useMode())

    await act(() => vi.advanceTimersByTimeAsync(4999))
    expect(result.current).toBeNull()
    expect(fetchMode).toHaveBeenCalledTimes(1)

    await act(() => vi.advanceTimersByTimeAsync(5000))
    expect(result.current).toBe('tmux')
    expect(fetchMode).toHaveBeenCalledTimes(2)
  })

  it('does not retry after unmount', async () => {
    vi.mocked(fetchMode).mockRejectedValue(new Error('down'))
    const { unmount } = renderHook(() => useMode())
    await act(() => vi.advanceTimersByTimeAsync(0))

    unmount()
    await act(() => vi.advanceTimersByTimeAsync(20_000))

    expect(fetchMode).toHaveBeenCalledTimes(1)
  })
})
