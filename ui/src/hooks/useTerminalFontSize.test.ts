import { afterEach, describe, expect, it } from 'vitest'
import { act, cleanup, renderHook } from '@testing-library/react'
import { useTerminalFontSize } from './useLayout'

const FONT_SIZE_KEY = 'houston-terminal-font-size'
const LAYOUT_KEY = 'houston-layout'

afterEach(() => {
  cleanup()
  localStorage.clear()
})

describe('useTerminalFontSize', () => {
  it('defaults to 14 when nothing is persisted', () => {
    const { result } = renderHook(() => useTerminalFontSize())
    const [fontSize] = result.current
    expect(fontSize).toBe(14)
  })

  it('persists under its own storage key, separate from houston-layout', () => {
    const { result } = renderHook(() => useTerminalFontSize())

    act(() => {
      const [, setFontSize] = result.current
      setFontSize(18)
    })

    expect(localStorage.getItem(FONT_SIZE_KEY)).toBe('18')
    expect(localStorage.getItem(LAYOUT_KEY)).toBeNull()
  })

  it('round-trips the persisted value across a re-mount', () => {
    const first = renderHook(() => useTerminalFontSize())
    act(() => {
      const [, setFontSize] = first.result.current
      setFontSize(20)
    })
    first.unmount()

    const second = renderHook(() => useTerminalFontSize())
    const [fontSize] = second.result.current
    expect(fontSize).toBe(20)
  })
})
