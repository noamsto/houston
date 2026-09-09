import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, renderHook } from '@testing-library/react'
import type { Terminal } from '@xterm/xterm'
import {
  computeFitFontSize,
  computeFollowCursorTranslateX,
  computeScrollLineHeight,
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
  opts?: { rows?: number; dims?: { w: number; h: number } },
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
    current: { options: termOptions, rows: opts?.rows, scrollLines } as unknown as Terminal,
  }

  const { result } = renderHook(() => useTouchGestures(innerRef, outerRef, termRef, true, onPinchEnd))
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
