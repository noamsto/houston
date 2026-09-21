import { afterEach, describe, expect, it, vi } from 'vitest'
import { act, cleanup, renderHook } from '@testing-library/react'
import { useGroupByProject } from './useLayout'

const KEY = 'houston-fleet-group-by-project'

afterEach(() => {
  cleanup()
  localStorage.clear()
  vi.restoreAllMocks()
})

describe('useGroupByProject', () => {
  it('defaults to false when nothing is persisted', () => {
    const { result } = renderHook(() => useGroupByProject())
    expect(result.current[0]).toBe(false)
  })

  it('persists the choice and restores it on re-mount', () => {
    const first = renderHook(() => useGroupByProject())
    act(() => first.result.current[1](true))
    expect(localStorage.getItem(KEY)).toBe('true')
    first.unmount()

    const second = renderHook(() => useGroupByProject())
    expect(second.result.current[0]).toBe(true)
  })

  it('still works in memory when localStorage throws', () => {
    vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => { throw new Error('denied') })
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => { throw new Error('denied') })

    const { result } = renderHook(() => useGroupByProject())
    expect(result.current[0]).toBe(false)
    act(() => result.current[1](true))
    expect(result.current[0]).toBe(true)
  })
})
