import { afterEach, describe, expect, it, vi } from 'vitest'
import { act, cleanup, fireEvent, render } from '@testing-library/react'
import { useRef } from 'react'
import { EDGE_ZONE_PX, inEdgeZone, lockDirection, shouldCommit, useEdgeSwipeBack } from './useEdgeSwipeBack'

const WIDTH = 400

function Probe({ onBack, onInnerStart }: { onBack: () => void; onInnerStart?: () => void }) {
  const ref = useRef<HTMLDivElement>(null)
  useEdgeSwipeBack(ref, onBack)
  return (
    <div ref={ref} data-testid="overlay">
      <div data-testid="inner" onTouchStart={onInnerStart} />
    </div>
  )
}

function setup(opts: { reduced?: boolean } = {}) {
  vi.stubGlobal('matchMedia', () => ({ matches: Boolean(opts.reduced) }))
  const onBack = vi.fn()
  const onInnerStart = vi.fn()
  const view = render(<Probe onBack={onBack} onInnerStart={onInnerStart} />)
  const overlay = view.getByTestId('overlay')
  Object.defineProperty(overlay, 'clientWidth', { value: WIDTH })
  const inner = view.getByTestId('inner')
  // `at` is the fake clock's time in ms; the hook times a flick with performance.now().
  let clock = 0
  const touch = (type: 'touchStart' | 'touchMove' | 'touchEnd', x: number, y: number, at = 0) => {
    if (vi.isFakeTimers()) { act(() => { vi.advanceTimersByTime(Math.max(0, at - clock)) }); clock = Math.max(clock, at) }
    fireEvent[type](inner, { touches: type === 'touchEnd' ? [] : [{ clientX: x, clientY: y }] })
  }
  return { onBack, onInnerStart, overlay, inner, touch }
}

afterEach(() => {
  cleanup()
  vi.useRealTimers()
  vi.unstubAllGlobals()
  window.location.hash = ''
})

describe('pure decisions', () => {
  it('edge zone', () => {
    expect(inEdgeZone(0)).toBe(true)
    expect(inEdgeZone(EDGE_ZONE_PX)).toBe(true)
    expect(inEdgeZone(EDGE_ZONE_PX + 1)).toBe(false)
  })
  it('locks rightward horizontal, cancels vertical or leftward', () => {
    expect(lockDirection(3, 2)).toBe('undecided')
    expect(lockDirection(14, 3)).toBe('back')
    expect(lockDirection(4, 14)).toBe('cancel')
    expect(lockDirection(-14, 1)).toBe('cancel')
  })
  it('commits by distance or flick only', () => {
    expect(shouldCommit(0.4 * WIDTH, WIDTH, 2000)).toBe(true)
    expect(shouldCommit(0.2 * WIDTH, WIDTH, 2000)).toBe(false)
    expect(shouldCommit(60, WIDTH, 80)).toBe(true)
    expect(shouldCommit(20, WIDTH, 10)).toBe(false)
  })
})

describe('useEdgeSwipeBack', () => {
  it('commits past the distance threshold and calls onBack after the settle', () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'performance', 'Date'] })
    const { onBack, overlay, touch } = setup()
    touch('touchStart', 5, 300, 0)
    touch('touchMove', 60, 302, 100)
    expect(overlay.style.transform).toBe('translateX(55px)')
    touch('touchMove', 200, 305, 900)
    touch('touchEnd', 200, 305, 1000)
    expect(onBack).not.toHaveBeenCalled()
    act(() => { vi.advanceTimersByTime(250) })
    expect(onBack).toHaveBeenCalledTimes(1)
  })

  it('commits a fast flick shorter than the threshold', () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'performance', 'Date'] })
    const { onBack, touch } = setup()
    touch('touchStart', 5, 300, 0)
    touch('touchMove', 90, 300, 100)
    touch('touchEnd', 90, 300, 120)
    act(() => { vi.advanceTimersByTime(250) })
    expect(onBack).toHaveBeenCalledTimes(1)
  })

  it('springs back on a short slow swipe', () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'performance', 'Date'] })
    const { onBack, overlay, touch } = setup()
    touch('touchStart', 5, 300, 0)
    touch('touchMove', 70, 300, 800)
    touch('touchEnd', 70, 300, 1000)
    expect(overlay.style.transform).toBe('')
    act(() => { vi.advanceTimersByTime(250) })
    expect(onBack).not.toHaveBeenCalled()
  })

  it('cancels when vertical dominates', () => {
    const { onBack, overlay, touch } = setup()
    touch('touchStart', 5, 300, 0)
    touch('touchMove', 8, 340, 50)
    touch('touchMove', 250, 345, 100)
    touch('touchEnd', 250, 345, 150)
    expect(overlay.style.transform).toBe('')
    expect(onBack).not.toHaveBeenCalled()
  })

  it('ignores a start outside the edge zone and leaves it to descendants', () => {
    const { onBack, onInnerStart, overlay, touch } = setup()
    touch('touchStart', 120, 300, 0)
    touch('touchMove', 300, 300, 100)
    touch('touchEnd', 300, 300, 150)
    expect(onInnerStart).toHaveBeenCalledTimes(1)
    expect(overlay.style.transform).toBe('')
    expect(onBack).not.toHaveBeenCalled()
  })

  it('keeps an edge start away from descendants (terminal pan)', () => {
    const { onInnerStart, touch } = setup()
    touch('touchStart', 5, 300, 0)
    expect(onInnerStart).not.toHaveBeenCalled()
  })

  it('does not double-navigate when the hash moved during the gesture', () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'performance', 'Date'] })
    const { onBack, touch } = setup()
    touch('touchStart', 5, 300, 0)
    touch('touchMove', 200, 300, 100)
    window.location.hash = '#/fleet'
    touch('touchEnd', 200, 300, 150)
    act(() => { vi.advanceTimersByTime(250) })
    expect(onBack).not.toHaveBeenCalled()
  })

  it('treats touchcancel (browser took the gesture) as a cancel', () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'performance', 'Date'] })
    const { onBack, overlay, inner, touch } = setup()
    touch('touchStart', 5, 300, 0)
    touch('touchMove', 200, 300, 100)
    fireEvent.touchCancel(inner)
    act(() => { vi.advanceTimersByTime(250) })
    expect(overlay.style.transform).toBe('')
    expect(onBack).not.toHaveBeenCalled()
  })

  it('under reduced motion does not follow the finger but still commits', () => {
    const { onBack, overlay, touch } = setup({ reduced: true })
    touch('touchStart', 5, 300, 0)
    touch('touchMove', 200, 300, 100)
    expect(overlay.style.transform).toBe('')
    touch('touchEnd', 200, 300, 150)
    expect(onBack).toHaveBeenCalledTimes(1)
  })
})
