import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, renderHook } from '@testing-library/react'
import type { Terminal } from '@xterm/xterm'
import {
  computeFitFontSize,
  computeFollowCursorTranslateX,
  computeScrollLineHeight,
  decideDetach,
  snapFontSize,
  useTouchGestures,
} from './useTouchGestures'

function touchEvent(type: string, touches: { clientX: number; clientY: number }[]) {
  const event = new Event(type, { bubbles: true, cancelable: true })
  Object.defineProperty(event, 'touches', { value: touches, configurable: true })
  return event
}

/** Give an element non-zero layout numbers — happy-dom has no layout engine,
 *  so clientWidth/clientHeight/getBoundingClientRect read 0 by default. */
function stubDims(el: HTMLElement, rect: { width: number; height: number; left?: number; top?: number }) {
  Object.defineProperty(el, 'clientWidth', { value: rect.width, configurable: true })
  Object.defineProperty(el, 'clientHeight', { value: rect.height, configurable: true })
  el.getBoundingClientRect = () =>
    ({
      width: rect.width,
      height: rect.height,
      left: rect.left ?? 0,
      top: rect.top ?? 0,
      right: (rect.left ?? 0) + rect.width,
      bottom: (rect.top ?? 0) + rect.height,
      x: rect.left ?? 0,
      y: rect.top ?? 0,
      toJSON() {},
    }) as DOMRect
}

/** Build the DOM shape useTouchGestures expects (an outer container wrapping
 *  an inner div that itself wraps a `.xterm-screen` node) and a fake xterm
 *  Terminal carrying only the fields the hook reads. */
function setup(
  termOptions: { fontSize?: number; lineHeight?: number },
  onPinchEnd?: (fontSize: number) => void,
  opts?: {
    rows?: number
    dims?: { w: number; h: number }
    onDoubleTap?: () => void
    onDragEnd?: (info: { movedX: boolean; startedAtBottom: boolean }) => void
    buffer?: { viewportY: number; baseY: number }
  },
) {
  const outer = document.createElement('div')
  const inner = document.createElement('div')
  const screenEl = document.createElement('div')
  screenEl.className = 'xterm-screen'
  inner.appendChild(screenEl)
  outer.appendChild(inner)
  document.body.appendChild(outer)
  stubDims(outer, { width: 400, height: 300 })

  const outerRef = { current: outer }
  const innerRef = { current: inner }
  const scrollLines = vi.fn()
  const termRef = {
    current: {
      options: termOptions,
      rows: opts?.rows,
      scrollLines,
      buffer: opts?.buffer && { active: opts.buffer },
    } as unknown as Terminal,
  }

  const { result } = renderHook(() =>
    useTouchGestures(innerRef, outerRef, termRef, true, onPinchEnd, opts?.onDoubleTap, opts?.onDragEnd),
  )
  result.current.resetTransform(1, opts?.dims ?? { w: 960, h: 300 }, { scale: 1, tx: 0, ty: 0 })

  return { screenEl, inner, scrollLines, termRef, result }
}

afterEach(() => {
  cleanup()
  document.body.innerHTML = ''
})

describe('useTouchGestures scroll line-height math', () => {
  it('uses the terminal actual font size, not a hardcoded 13px, for scroll-line math', () => {
    // At fontSize 20 (lineHeight 1.2) a real terminal line is 24px tall, so a
    // 20px vertical drag should not yet cross a full line. The hardcoded
    // `13 * 1.2 = 15.6px` line height wrongly crosses it and fires a scroll.
    const { screenEl, scrollLines } = setup({ fontSize: 20, lineHeight: 1.2 })

    screenEl.dispatchEvent(touchEvent('touchstart', [{ clientX: 50, clientY: 100 }]))
    screenEl.dispatchEvent(touchEvent('touchmove', [{ clientX: 50, clientY: 80 }]))

    expect(scrollLines).not.toHaveBeenCalled()
  })

  it('prefers the real measured cell height over the fontSize approximation once dims are known', () => {
    // fontSize 13 * lineHeight 1.2 approximates a 15.6px line — an 18px drag
    // would cross it. The real measured terminal (10 rows over a 200px-tall
    // `.xterm-screen`) is 20px per line, so that same drag should not fire.
    const { screenEl, scrollLines } = setup({ fontSize: 13, lineHeight: 1.2 }, undefined, {
      rows: 10,
      dims: { w: 960, h: 200 },
    })

    screenEl.dispatchEvent(touchEvent('touchstart', [{ clientX: 50, clientY: 100 }]))
    screenEl.dispatchEvent(touchEvent('touchmove', [{ clientX: 50, clientY: 82 }]))

    expect(scrollLines).not.toHaveBeenCalled()
  })

  it('tracks a font-size change made mid-gesture rather than a value captured once at mount', () => {
    // Starts at 20px/line (10 rows over a 200px-tall screen). A 10px drag
    // accumulates without firing. applyFontSize then doubles the font size,
    // which (per its own proportional rescale) doubles termDimsRef to
    // 400px — 40px/line. A second 10px drag brings the accumulator to 20px:
    // enough to cross the stale 20px/line height, not the current 40px/line
    // one. Only a fresh read on this second move avoids firing.
    const { screenEl, scrollLines, result, termRef } = setup({ fontSize: 13, lineHeight: 1.2 }, undefined, {
      rows: 10,
      dims: { w: 960, h: 200 },
    })

    screenEl.dispatchEvent(touchEvent('touchstart', [{ clientX: 50, clientY: 100 }]))
    screenEl.dispatchEvent(touchEvent('touchmove', [{ clientX: 50, clientY: 90 }]))
    expect(scrollLines).not.toHaveBeenCalled()

    result.current.applyFontSize(termRef.current, 26)

    screenEl.dispatchEvent(touchEvent('touchmove', [{ clientX: 50, clientY: 80 }]))
    expect(scrollLines).not.toHaveBeenCalled()
  })
})

/** Fire a one-finger touchstart at `from`, a touchmove per point, then touchend. */
function drag(
  screenEl: HTMLElement,
  from: { clientX: number; clientY: number },
  moves: { clientX: number; clientY: number }[],
) {
  screenEl.dispatchEvent(touchEvent('touchstart', [from]))
  for (const p of moves) screenEl.dispatchEvent(touchEvent('touchmove', [p]))
  screenEl.dispatchEvent(touchEvent('touchend', []))
}

describe('useTouchGestures free one-finger drag', () => {
  // fontSize 10 * lineHeight 1 = 10px per scrolled line (no measured dims).
  const term = { fontSize: 10, lineHeight: 1 }

  it('pans and scrolls together on a diagonal drag', () => {
    const { screenEl, scrollLines, result } = setup(term)

    drag(screenEl, { clientX: 200, clientY: 200 }, [{ clientX: 160, clientY: 170 }])

    expect(result.current.translateXRef.current).toBe(-40)
    expect(scrollLines).toHaveBeenCalledWith(3)
  })

  it('still pans on a mostly-vertical drag', () => {
    const { screenEl, scrollLines, result } = setup(term)

    drag(screenEl, { clientX: 200, clientY: 200 }, [{ clientX: 194, clientY: 100 }])

    expect(result.current.translateXRef.current).toBe(-6)
    expect(scrollLines).toHaveBeenCalledWith(10)
  })

  it('still scrolls on a mostly-horizontal drag', () => {
    const { screenEl, scrollLines, result } = setup(term)

    drag(screenEl, { clientX: 200, clientY: 200 }, [{ clientX: 100, clientY: 175 }])

    expect(result.current.translateXRef.current).toBe(-100)
    expect(scrollLines).toHaveBeenCalledWith(2)
  })

  it('leaves translateX untouched on a vertical-only drag', () => {
    const { screenEl, result } = setup(term)
    result.current.resetTransform(1, { w: 960, h: 300 }, { scale: 1, tx: -10, ty: 0 })

    drag(screenEl, { clientX: 200, clientY: 200 }, [{ clientX: 200, clientY: 100 }])

    expect(result.current.translateXRef.current).toBe(-10)
  })

  it('cancels the touchend that completes a double-tap so xterm gets no dblclick', () => {
    const { screenEl } = setup(term, undefined, { onDoubleTap: vi.fn() })
    const end = () => {
      const e = touchEvent('touchend', [])
      screenEl.dispatchEvent(touchEvent('touchstart', [{ clientX: 100, clientY: 100 }]))
      screenEl.dispatchEvent(e)
      return e
    }
    expect(end().defaultPrevented).toBe(false)
    expect(end().defaultPrevented).toBe(true)
    expect(end().defaultPrevented).toBe(false)
  })

  it('treats a touch that never leaves the slop as a tap and fires onDoubleTap on the second', () => {
    const onDoubleTap = vi.fn()
    const onDragEnd = vi.fn()
    const { screenEl, scrollLines, result } = setup(term, undefined, { onDoubleTap, onDragEnd })

    drag(screenEl, { clientX: 100, clientY: 100 }, [{ clientX: 103, clientY: 102 }])
    expect(onDoubleTap).not.toHaveBeenCalled()
    drag(screenEl, { clientX: 101, clientY: 100 }, [])

    expect(onDoubleTap).toHaveBeenCalledTimes(1)
    expect(onDragEnd).not.toHaveBeenCalled()
    expect(scrollLines).not.toHaveBeenCalled()
    expect(result.current.translateXRef.current).toBe(0)
  })

  it('eases to column 0 when a horizontal drag is released within the magnetism threshold', () => {
    const { screenEl, result } = setup(term)

    drag(screenEl, { clientX: 200, clientY: 200 }, [{ clientX: 180, clientY: 200 }])

    expect(result.current.translateXRef.current).toBe(0)
  })

  it('does not jump to column 0 from far away', () => {
    const { screenEl, result } = setup(term)

    drag(screenEl, { clientX: 200, clientY: 200 }, [{ clientX: 100, clientY: 200 }])

    expect(result.current.translateXRef.current).toBe(-100)
  })

  it('does not apply magnetism to a vertical drag that leaves a small offset', () => {
    const { screenEl, result } = setup(term)
    result.current.resetTransform(1, { w: 960, h: 300 }, { scale: 1, tx: -10, ty: 0 })

    drag(screenEl, { clientX: 200, clientY: 200 }, [{ clientX: 200, clientY: 100 }])

    expect(result.current.translateXRef.current).toBe(-10)
  })

  it('reports movedX false for x jitter around the origin during a vertical swipe', () => {
    const onDragEnd = vi.fn()
    const { screenEl } = setup(term, undefined, { onDragEnd })

    const moves = Array.from({ length: 15 }, (_, i) => ({
      clientX: 200 + (i % 2 === 0 ? 3 : -3),
      clientY: 200 - (i + 1) * 10,
    }))
    drag(screenEl, { clientX: 200, clientY: 200 }, moves)

    expect(onDragEnd).toHaveBeenCalledWith({ movedX: false, startedAtBottom: true })
  })

  it('reports movedX for a horizontal drag', () => {
    const onDragEnd = vi.fn()
    const { screenEl } = setup(term, undefined, { onDragEnd })

    drag(screenEl, { clientX: 200, clientY: 200 }, [{ clientX: 150, clientY: 203 }])

    expect(onDragEnd).toHaveBeenCalledWith({ movedX: true, startedAtBottom: true })
  })

  it('does not report movedX when the content cannot pan horizontally', () => {
    const onDragEnd = vi.fn()
    // Content narrower than the viewport: translateX is always clamped at 0.
    const { screenEl, result } = setup(term, undefined, { onDragEnd, dims: { w: 100, h: 300 } })

    drag(screenEl, { clientX: 200, clientY: 200 }, [{ clientX: 150, clientY: 200 }])

    expect(result.current.translateXRef.current).toBe(0)
    expect(onDragEnd).toHaveBeenCalledWith({ movedX: false, startedAtBottom: true })
  })

  it('does not report movedX when translateX is clamped at the edge for the whole drag', () => {
    const onDragEnd = vi.fn()
    const { screenEl, result } = setup(term, undefined, { onDragEnd })
    // Already panned to the right edge (visible width 388, content 960).
    result.current.resetTransform(1, { w: 960, h: 300 }, { scale: 1, tx: -572, ty: 0 })

    drag(screenEl, { clientX: 200, clientY: 200 }, [{ clientX: 100, clientY: 200 }])

    expect(result.current.translateXRef.current).toBe(-572)
    expect(onDragEnd).toHaveBeenCalledWith({ movedX: false, startedAtBottom: true })
  })

  it('reports startedAtBottom from the terminal buffer at touchstart', () => {
    const onDragEnd = vi.fn()
    const { screenEl } = setup(term, undefined, { onDragEnd, buffer: { viewportY: 10, baseY: 77 } })

    drag(screenEl, { clientX: 200, clientY: 200 }, [{ clientX: 200, clientY: 150 }])

    expect(onDragEnd).toHaveBeenCalledWith({ movedX: false, startedAtBottom: false })
  })

  it('reports startedAtBottom true when the viewport is at the bottom', () => {
    const onDragEnd = vi.fn()
    const { screenEl } = setup(term, undefined, { onDragEnd, buffer: { viewportY: 77, baseY: 77 } })

    drag(screenEl, { clientX: 200, clientY: 200 }, [{ clientX: 200, clientY: 150 }])

    expect(onDragEnd).toHaveBeenCalledWith({ movedX: false, startedAtBottom: true })
  })

  it('reports movedX true from a pinch end', () => {
    const onDragEnd = vi.fn()
    const { screenEl } = setup({ fontSize: 14, lineHeight: 1.2 }, undefined, { onDragEnd })

    screenEl.dispatchEvent(
      touchEvent('touchstart', [
        { clientX: 100, clientY: 150 },
        { clientX: 200, clientY: 150 },
      ]),
    )
    screenEl.dispatchEvent(
      touchEvent('touchmove', [
        { clientX: 50, clientY: 150 },
        { clientX: 250, clientY: 150 },
      ]),
    )
    screenEl.dispatchEvent(touchEvent('touchend', []))

    expect(onDragEnd).toHaveBeenCalledWith({ movedX: true, startedAtBottom: true })
  })
})

describe('useTouchGestures touchcancel', () => {
  const term = { fontSize: 10, lineHeight: 1 }

  it('ends a real drag like a lift: reports onDragEnd and clears the active flag', () => {
    const onDragEnd = vi.fn()
    const { screenEl, result } = setup(term, undefined, { onDragEnd })

    screenEl.dispatchEvent(touchEvent('touchstart', [{ clientX: 200, clientY: 200 }]))
    expect(result.current.gestureActiveRef.current).toBe(true)
    screenEl.dispatchEvent(touchEvent('touchmove', [{ clientX: 100, clientY: 200 }]))
    screenEl.dispatchEvent(touchEvent('touchcancel', []))

    expect(onDragEnd).toHaveBeenCalledWith({ movedX: true, startedAtBottom: true })
    expect(result.current.gestureActiveRef.current).toBe(false)
  })

  it('handles a pinch like touchend does', () => {
    const onPinchEnd = vi.fn()
    const onDragEnd = vi.fn()
    const { screenEl, result } = setup({ fontSize: 14, lineHeight: 1.2 }, onPinchEnd, { onDragEnd })

    screenEl.dispatchEvent(
      touchEvent('touchstart', [
        { clientX: 100, clientY: 150 },
        { clientX: 200, clientY: 150 },
      ]),
    )
    screenEl.dispatchEvent(
      touchEvent('touchmove', [
        { clientX: 50, clientY: 150 },
        { clientX: 250, clientY: 150 },
      ]),
    )
    screenEl.dispatchEvent(touchEvent('touchcancel', []))

    expect(onPinchEnd).toHaveBeenCalledWith(24)
    expect(onDragEnd).toHaveBeenCalledWith({ movedX: true, startedAtBottom: true })
    expect(result.current.gestureActiveRef.current).toBe(false)
  })

  it('never counts as a tap, so a cancelled touch cannot complete a double-tap', () => {
    const onDoubleTap = vi.fn()
    const { screenEl, result } = setup(term, undefined, { onDoubleTap })

    drag(screenEl, { clientX: 100, clientY: 100 }, [])
    screenEl.dispatchEvent(touchEvent('touchstart', [{ clientX: 101, clientY: 100 }]))
    screenEl.dispatchEvent(touchEvent('touchcancel', []))

    expect(onDoubleTap).not.toHaveBeenCalled()
    expect(result.current.gestureActiveRef.current).toBe(false)
  })

  it('forgets the previous tap, so tap + cancelled touch + tap is not a double-tap', () => {
    const onDoubleTap = vi.fn()
    const { screenEl } = setup(term, undefined, { onDoubleTap })

    drag(screenEl, { clientX: 100, clientY: 100 }, [])
    screenEl.dispatchEvent(touchEvent('touchstart', [{ clientX: 101, clientY: 100 }]))
    screenEl.dispatchEvent(touchEvent('touchcancel', []))
    drag(screenEl, { clientX: 100, clientY: 100 }, [])

    expect(onDoubleTap).not.toHaveBeenCalled()
  })
})

describe('decideDetach', () => {
  it.each([
    { movedX: false, atBottom: true, startedAtBottom: false, expected: 'attach' },
    { movedX: true, atBottom: true, startedAtBottom: false, expected: 'attach' },
    { movedX: true, atBottom: true, startedAtBottom: true, expected: 'detach' },
    { movedX: false, atBottom: true, startedAtBottom: true, expected: 'keep' },
    { movedX: false, atBottom: false, startedAtBottom: true, expected: 'detach' },
    { movedX: false, atBottom: false, startedAtBottom: false, expected: 'detach' },
    { movedX: true, atBottom: false, startedAtBottom: false, expected: 'detach' },
  ])('$expected for movedX=$movedX atBottom=$atBottom startedAtBottom=$startedAtBottom', ({ expected, ...args }) => {
    expect(decideDetach(args)).toBe(expected)
  })
})

describe('computeScrollLineHeight', () => {
  it('uses the measured height divided by rows when both are known', () => {
    expect(computeScrollLineHeight(10, 200, 13, 1.2)).toBe(20)
  })

  it('falls back to fontSize * lineHeight when rows is not yet known', () => {
    expect(computeScrollLineHeight(0, 200, 13, 1.2)).toBeCloseTo(15.6)
  })

  it('falls back to fontSize * lineHeight when the measured height is not yet known', () => {
    expect(computeScrollLineHeight(10, 0, 13, 1.2)).toBeCloseTo(15.6)
  })

  it('scales with font size when measured, matching a zoomed-in real terminal', () => {
    expect(computeScrollLineHeight(10, 400, 26, 1.2)).toBe(40)
  })
})

describe('useTouchGestures pinch-end font-size zoom', () => {
  it('snaps to a discrete font size, resets the CSS scale to 1.0, and reports the new size', () => {
    const onPinchEnd = vi.fn()
    const { screenEl, inner, termRef } = setup({ fontSize: 14, lineHeight: 1.2 }, onPinchEnd)

    // Pinch outward: two fingers starting 100px apart, ending 200px apart —
    // a 2x pinch scale on top of the starting fontSize (14 * 2 = 28, clamped
    // to the 24px ceiling).
    screenEl.dispatchEvent(
      touchEvent('touchstart', [
        { clientX: 100, clientY: 150 },
        { clientX: 200, clientY: 150 },
      ]),
    )
    screenEl.dispatchEvent(
      touchEvent('touchmove', [
        { clientX: 50, clientY: 150 },
        { clientX: 250, clientY: 150 },
      ]),
    )
    screenEl.dispatchEvent(touchEvent('touchend', []))

    expect(termRef.current.options.fontSize).toBe(24)
    expect(onPinchEnd).toHaveBeenCalledWith(24)
    // At rest, the wrapper transform's scale component is always 1.0 — the
    // pinch's magnification is baked into the real font size instead, so
    // text is never left resampled/blurry.
    expect(inner.style.transform).toContain('scale(1)')
  })
})

describe('computeFollowCursorTranslateX', () => {
  it('returns null when the cursor is already within the margin of the visible window', () => {
    // viewport [0, 200), margin 20px either side of a 10px cell — column 10
    // sits at 100px, comfortably inside [20, 180).
    expect(computeFollowCursorTranslateX(10, 10, 200, 0)).toBeNull()
  })

  it('returns a leftward pan when the cursor is past the right edge', () => {
    // Column 25 sits at 250px, past visibleRight(200) - margin(20) = 180.
    expect(computeFollowCursorTranslateX(25, 10, 200, 0)).toBe(200 - 20 - 250)
  })

  it('returns a rightward pan when the cursor is past the left edge', () => {
    // Panned right by 100px (currentTx=-100) puts visibleLeft at 100px;
    // column 5 sits at 50px, short of visibleLeft(100) + margin(20) = 120.
    expect(computeFollowCursorTranslateX(5, 10, 200, -100)).toBe(20 - 50)
  })

  it('returns null when cell width is not yet known', () => {
    expect(computeFollowCursorTranslateX(10, 0, 200, 0)).toBeNull()
  })
})

describe('snapFontSize', () => {
  it('snaps to the nearest discrete size', () => {
    expect(snapFontSize(14)).toBe(14)
    expect(snapFontSize(15)).toBe(14)
    expect(snapFontSize(17)).toBe(16)
  })

  it('clamps to the [9, 24] floor and ceiling', () => {
    expect(snapFontSize(3)).toBe(9)
    expect(snapFontSize(100)).toBe(24)
  })
})

describe('computeFitFontSize', () => {
  it('shrinks proportionally so the full width fits the narrower viewport', () => {
    // raw = currentFontSize * (viewportWidthPx / currentTotalWidthPx) = 20 * (800/1000) = 16
    expect(computeFitFontSize(20, 1000, 800)).toBe(16)
  })

  it('clamps to the 9px floor when the ratio would go smaller', () => {
    // raw = 13 * (100/1000) = 1.3, well under the 9px "text can't be too small" floor
    expect(computeFitFontSize(13, 1000, 100)).toBe(9)
  })

  it('returns the floor instead of dividing by zero when total width is not yet known', () => {
    expect(computeFitFontSize(13, 0, 500)).toBe(9)
    expect(computeFitFontSize(13, -50, 500)).toBe(9)
  })
})
