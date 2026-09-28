import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { act, renderHook } from '@testing-library/react'
import { useStickyScroll } from './useStickyScroll'

describe('useStickyScroll', () => {
  const proto = HTMLElement.prototype
  const saved = {
    scrollHeight: Object.getOwnPropertyDescriptor(proto, 'scrollHeight'),
    clientHeight: Object.getOwnPropertyDescriptor(proto, 'clientHeight'),
  }

  beforeEach(() => {
    Object.defineProperty(proto, 'scrollHeight', { configurable: true, get: () => 1000 })
    Object.defineProperty(proto, 'clientHeight', { configurable: true, get: () => 200 })
  })

  afterEach(() => {
    for (const key of ['scrollHeight', 'clientHeight'] as const) {
      const d = saved[key]
      if (d) Object.defineProperty(proto, key, d)
      else delete (proto as unknown as Record<string, unknown>)[key]
    }
  })

  function attach(result: { current: { ref: { current: HTMLDivElement | null } } }) {
    const el = document.createElement('div')
    document.body.appendChild(el)
    result.current.ref.current = el
    return el
  }

  it('stays stuck at the slop boundary and unsticks just past it', () => {
    const { result, rerender } = renderHook(
      ({ deps }: { deps: unknown[] }) => useStickyScroll<HTMLDivElement>(deps, null),
      { initialProps: { deps: [0] } },
    )
    const el = attach(result)

    el.scrollTop = 1000 - 200 - 23 // distance 23 < STICK_SLOP_PX (24) => stuck
    act(() => result.current.onScroll())
    expect(result.current.stuck).toBe(true)

    el.scrollTop = 1000 - 200 - 24 // distance 24 => not stuck
    act(() => result.current.onScroll())
    expect(result.current.stuck).toBe(false)

    // sanity: not rerendered/unmounted mid-test
    rerender({ deps: [0] })
  })

  it('preserveAnchor then growth restores the same visual offset', () => {
    const { result, rerender } = renderHook(
      ({ anchorKey }: { anchorKey: unknown }) => useStickyScroll<HTMLDivElement>([], anchorKey),
      { initialProps: { anchorKey: 1 } },
    )
    const el = attach(result)
    el.scrollTop = 100

    act(() => result.current.preserveAnchor())
    expect(result.current.stuck).toBe(false)

    // Content grew above the anchor (scrollHeight goes from 1000 to 1400).
    Object.defineProperty(proto, 'scrollHeight', { configurable: true, get: () => 1400 })
    rerender({ anchorKey: 2 })

    expect(el.scrollTop).toBe(100 + (1400 - 1000))
  })

  it('scrollToLatest jumps to the bottom and re-sticks', () => {
    const { result } = renderHook(() => useStickyScroll<HTMLDivElement>([], null))
    const el = attach(result)
    el.scrollTop = 100

    act(() => result.current.onScroll())
    expect(result.current.stuck).toBe(false)

    act(() => result.current.scrollToLatest())
    expect(el.scrollTop).toBe(1000)
    expect(result.current.stuck).toBe(true)
  })

  it('scrolls to bottom on stickDeps change while stuck, and leaves it alone once unstuck', () => {
    const { result, rerender } = renderHook(
      ({ deps }: { deps: unknown[] }) => useStickyScroll<HTMLDivElement>(deps, null),
      { initialProps: { deps: [0] } },
    )
    const el = attach(result)
    el.scrollTop = 0

    rerender({ deps: [1] })
    expect(el.scrollTop).toBe(1000)

    el.scrollTop = 100
    act(() => result.current.onScroll())
    expect(result.current.stuck).toBe(false)

    rerender({ deps: [2] })
    expect(el.scrollTop).toBe(100)
  })
})
