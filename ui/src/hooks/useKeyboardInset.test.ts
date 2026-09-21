import { afterEach, describe, expect, it, vi } from 'vitest'
import { act, renderHook } from '@testing-library/react'
import { useKeyboardInset } from './useKeyboardInset'

function fakeVisualViewport(height: number, offsetTop = 0, scale = 1) {
  const listeners = new Map<string, Set<() => void>>()
  const vv = {
    height,
    offsetTop,
    scale,
    addEventListener: (t: string, fn: () => void) => {
      if (!listeners.has(t)) listeners.set(t, new Set())
      listeners.get(t)!.add(fn)
    },
    removeEventListener: (t: string, fn: () => void) => listeners.get(t)?.delete(fn),
    emit: (t: string) => listeners.get(t)?.forEach((fn) => fn()),
  }
  vi.stubGlobal('visualViewport', vv)
  return vv
}

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('useKeyboardInset', () => {
  it('is 0 when there is no visualViewport', () => {
    vi.stubGlobal('visualViewport', undefined)
    const { result } = renderHook(() => useKeyboardInset())
    expect(result.current).toBe(0)
  })

  it('is 0 when the visual viewport fills the layout viewport', () => {
    fakeVisualViewport(window.innerHeight)
    const { result } = renderHook(() => useKeyboardInset())
    expect(result.current).toBe(0)
  })

  it('tracks the keyboard as the viewport shrinks and grows back', () => {
    const vv = fakeVisualViewport(window.innerHeight)
    const { result } = renderHook(() => useKeyboardInset())

    act(() => {
      vv.height = window.innerHeight - 300
      vv.emit('resize')
    })
    expect(result.current).toBe(300)

    act(() => {
      vv.height = window.innerHeight
      vv.emit('resize')
    })
    expect(result.current).toBe(0)
  })

  it('subtracts the visual viewport scroll offset', () => {
    fakeVisualViewport(window.innerHeight - 300, 100)
    const { result } = renderHook(() => useKeyboardInset())
    expect(result.current).toBe(200)
  })

  it('ignores a pinch-zoomed viewport (not a keyboard)', () => {
    fakeVisualViewport(window.innerHeight - 300, 0, 2)
    const { result } = renderHook(() => useKeyboardInset())
    expect(result.current).toBe(0)
  })

  it('ignores gaps too small to be a keyboard', () => {
    fakeVisualViewport(window.innerHeight - 30)
    const { result } = renderHook(() => useKeyboardInset())
    expect(result.current).toBe(0)
  })
})
