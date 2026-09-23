import { describe, expect, it } from 'vitest'
import { act, renderHook } from '@testing-library/react'
import { useTerminalLifecycle } from './useTerminalLifecycle'

describe('useTerminalLifecycle', () => {
  it('end sets ended immediately with the reason, bypassing the disconnect grace period', () => {
    const { result } = renderHook(() => useTerminalLifecycle('run-1', true, true, true))

    act(() => { result.current.end('tmux server changed') })

    expect(result.current.state).toBe('ended')
    expect(result.current.endedReason).toBe('tmux server changed')
  })

  it('reconnect goes live with the reason cleared and attempt incremented', () => {
    const { result } = renderHook(() => useTerminalLifecycle('run-1', true, true, true))

    act(() => { result.current.end('tmux server changed') })
    const attemptBefore = result.current.attempt

    act(() => { result.current.reconnect() })

    expect(result.current.state).toBe('live')
    expect(result.current.endedReason).toBeNull()
    expect(result.current.attempt).toBe(attemptBefore + 1)
  })
})
