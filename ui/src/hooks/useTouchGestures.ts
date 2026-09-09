import { useEffect, useRef } from 'react'
import type { Terminal } from '@xterm/xterm'

// Discrete real font sizes zoom snaps to on pinch-end, clamped to [9, 24]px
// per the "text can't be too small/blurry" requirement.
const FONT_SIZES = [9, 10, 11, 12, 13, 14, 16, 18, 20, 22, 24]
const MIN_FONT_SIZE = FONT_SIZES[0]
const MAX_FONT_SIZE = FONT_SIZES[FONT_SIZES.length - 1]

// Inset matching TerminalPane's own PAD — xterm opens 6px in from the outer
// container's edge, so pan/clamp math needs to subtract it on both sides.
const PAD = 6

// Pixel distance from column 0 (translateX = 0) within which a released pan
// eases back to exactly 0 rather than leaving a near-zero offset.
const COLUMN_ZERO_THRESHOLD_PX = 24

// Duration of the eased pan animation (magnetism, follow-cursor, double-tap
// reset) — applied as a temporary CSS transition rather than a rAF loop, so
// the target value lands in translateXRef synchronously while the visual
// motion plays out over this window.
const PAN_EASE_MS = 180

// Double-tap detection: two touchends within this window, at roughly the
// same point, with neither leg exceeding the pan/scroll direction threshold.
const DOUBLE_TAP_WINDOW_MS = 300
const DOUBLE_TAP_MAX_DIST_PX = 24

export function snapFontSize(target: number): number {
  const clamped = Math.min(MAX_FONT_SIZE, Math.max(MIN_FONT_SIZE, target))
  return FONT_SIZES.reduce((closest, size) =>
    Math.abs(size - clamped) < Math.abs(closest - clamped) ? size : closest,
  )
}

/** Font size at which `currentTotalWidthPx` (the pane's full rendered width
 *  at `currentFontSize`) would exactly fill `viewportWidthPx` — floor-clamped
 *  at 9px per the "text can't be too small" rule even when that leaves
 *  content clipped. Cell metrics scale linearly with font size for a
 *  fixed-pitch terminal, so this is a straight ratio, not a re-measurement. */
export function computeFitFontSize(
  currentFontSize: number,
  currentTotalWidthPx: number,
  viewportWidthPx: number,
): number {
  if (currentTotalWidthPx <= 0) return MIN_FONT_SIZE
  const raw = currentFontSize * (viewportWidthPx / currentTotalWidthPx)
  return Math.max(MIN_FONT_SIZE, raw)
}

/** Target translateX so the cursor's column stays within the visible
 *  viewport (with a 2-cell margin), or `null` if it's already in view. */
export function computeFollowCursorTranslateX(
  cursorCol: number,
  cellWidthPx: number,
  viewportWidthPx: number,
  currentTx: number,
): number | null {
  if (cellWidthPx <= 0) return null
  const cursorPx = cursorCol * cellWidthPx
  const margin = cellWidthPx * 2
  const visibleLeft = -currentTx
  const visibleRight = visibleLeft + viewportWidthPx
  if (cursorPx < visibleLeft + margin) return margin - cursorPx
  if (cursorPx > visibleRight - margin) return viewportWidthPx - margin - cursorPx
  return null
}

/**
 * Touch gesture handler for mobile terminal interaction.
 * Handles: 1-finger vertical scroll, 1-finger horizontal pan (with column-0
 * magnetism and double-tap detection), 2-finger pinch-to-zoom.
 */
export function useTouchGestures(
  innerRef: React.RefObject<HTMLDivElement | null>,
  outerRef: React.RefObject<HTMLDivElement | null>,
  termRef: React.RefObject<Terminal | null>,
  enabled: boolean,
  onPinchEnd?: (fontSize: number) => void,
  onDoubleTap?: () => void,
) {
  const scaleRef = useRef(1)
  const translateXRef = useRef(0)
  const translateYRef = useRef(0)
  const minScaleRef = useRef(1)
  const termDimsRef = useRef({ w: 0, h: 0 })
  // True from touchstart until every finger has lifted — lets callers (e.g.
  // follow-the-cursor auto-pan) avoid fighting a gesture in progress.
  const gestureActiveRef = useRef(false)
  // Pending CSS-transition cleanup from the most recent eased pan, so a new
  // ease (or a hard pan) can cancel a stale one before it clears the
  // transition out from under it.
  const easeTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null)

  // Refs (not deps) so a fresh callback identity each render doesn't force
  // the gesture effect below to re-attach its touch listeners.
  const onPinchEndRef = useRef(onPinchEnd)
  const onDoubleTapRef = useRef(onDoubleTap)
  useEffect(() => {
    onPinchEndRef.current = onPinchEnd
    onDoubleTapRef.current = onDoubleTap
  })

  // Reset transform state (called when wide/fit mode changes)
  // minScale: zoom-out limit (fit-all scale). initialScale/tx/ty: starting viewport.
  const resetTransform = (
    minScale: number,
    dims: { w: number; h: number },
    initial?: { scale: number; tx: number; ty: number },
  ) => {
    minScaleRef.current = minScale
    scaleRef.current = initial?.scale ?? minScale
    translateXRef.current = initial?.tx ?? 0
    translateYRef.current = initial?.ty ?? 0
    termDimsRef.current = dims
  }

  const applyTransform = () => {
    const inner = innerRef.current
    if (!inner) return
    const s = scaleRef.current
    const tx = translateXRef.current
    const ty = translateYRef.current
    inner.style.transform = `translate(${tx}px, ${ty}px) scale(${s})`
  }

  // Clamp a candidate translateX to the pane's real horizontal bounds
  // (content can't pan past its own edges) without mutating any ref.
  const clampTx = (tx: number) => {
    const outer = outerRef.current
    if (!outer) return tx
    const outerW = outer.clientWidth - PAD * 2
    const visW = termDimsRef.current.w * scaleRef.current
    return Math.min(0, Math.max(outerW - visW, tx))
  }

  const clampPan = () => {
    if (!outerRef.current) return
    const outerH = outerRef.current.clientHeight - PAD * 2
    const s = scaleRef.current
    const visH = termDimsRef.current.h * s
    translateXRef.current = clampTx(translateXRef.current)
    translateYRef.current = Math.min(0, Math.max(outerH - visH, translateYRef.current))
  }

  // Ease translateX to `target` (already clamped to real bounds) via a
  // temporarily-applied CSS transition. The ref lands on the target
  // synchronously — callers/tests can assert on it immediately — while the
  // transition plays out the visual motion.
  const easeTranslateXTo = (target: number) => {
    const clamped = clampTx(target)
    translateXRef.current = clamped
    const inner = innerRef.current
    if (!inner) return
    if (easeTimerRef.current) clearTimeout(easeTimerRef.current)
    inner.style.transition = `transform ${PAN_EASE_MS}ms ease-out`
    applyTransform()
    easeTimerRef.current = setTimeout(() => {
      inner.style.transition = ''
      easeTimerRef.current = null
    }, PAN_EASE_MS)
  }

  // Apply a real font-size change (pinch-end snap, double-tap toggle) and
  // rescale the cached pixel dims to match — cell metrics scale linearly
  // with font size for a fixed-pitch terminal, so this avoids re-measuring
  // layout (which happens async, after xterm's own reflow).
  const applyFontSize = (term: Terminal, newSize: number) => {
    const oldSize = term.options.fontSize ?? newSize
    term.options.fontSize = newSize
    const ratio = oldSize > 0 ? newSize / oldSize : 1
    termDimsRef.current = {
      w: termDimsRef.current.w * ratio,
      h: termDimsRef.current.h * ratio,
    }
  }

  useEffect(() => {
    if (!enabled || !innerRef.current) return

    const inner = innerRef.current
    const screen = inner.querySelector('.xterm-screen') as HTMLElement | null
    if (!screen) return

    const DIRECTION_THRESHOLD = 8

    let gesture: 'none' | 'scroll' | 'pan' | 'pinch' = 'none'
    let dragOriginX = 0
    let dragOriginY = 0
    let directionLocked = false
    let scrollStartY = 0
    let scrollAcc = 0
    let panLastX = 0
    let pinchStartDist = 0
    let pinchStartScale = 0
    let pinchFocalCX = 0
    let pinchFocalCY = 0
    let pinchScreenMidX = 0
    let pinchScreenMidY = 0
    let pinchStartTX = 0
    let pinchStartTY = 0
    let lastTapTime: number | null = null
    let lastTapX = 0
    let lastTapY = 0

    const onTouchStart = (e: TouchEvent) => {
      e.stopPropagation()
      gestureActiveRef.current = true
      if (e.touches.length === 1) {
        gesture = 'scroll'
        directionLocked = false
        dragOriginX = e.touches[0].clientX
        dragOriginY = e.touches[0].clientY
        scrollStartY = e.touches[0].clientY
        panLastX = e.touches[0].clientX
        scrollAcc = 0
      } else if (e.touches.length === 2) {
        gesture = 'pinch'
        lastTapTime = null // a pinch invalidates any pending double-tap
        const t1 = e.touches[0], t2 = e.touches[1]
        pinchStartDist = Math.hypot(t2.clientX - t1.clientX, t2.clientY - t1.clientY)
        pinchStartScale = scaleRef.current
        pinchStartTX = translateXRef.current
        pinchStartTY = translateYRef.current
        pinchScreenMidX = (t1.clientX + t2.clientX) / 2
        pinchScreenMidY = (t1.clientY + t2.clientY) / 2
        const rect = outerRef.current!.getBoundingClientRect()
        const relX = pinchScreenMidX - rect.left - PAD
        const relY = pinchScreenMidY - rect.top - PAD
        pinchFocalCX = (relX - pinchStartTX) / pinchStartScale
        pinchFocalCY = (relY - pinchStartTY) / pinchStartScale
      }
    }

    const onTouchMove = (e: TouchEvent) => {
      e.preventDefault()
      e.stopPropagation()

      if ((gesture === 'scroll' || gesture === 'pan') && e.touches.length === 1) {
        if (!directionLocked) {
          const dx = Math.abs(e.touches[0].clientX - dragOriginX)
          const dy = Math.abs(e.touches[0].clientY - dragOriginY)
          if (dx < DIRECTION_THRESHOLD && dy < DIRECTION_THRESHOLD) return
          gesture = dx > dy ? 'pan' : 'scroll'
          directionLocked = true
          lastTapTime = null // a real drag invalidates any pending double-tap
        }

        if (gesture === 'scroll') {
          // Read fontSize/lineHeight fresh on every move rather than once at
          // effect setup — pinch-end zoom (below) mutates term.options.fontSize
          // at runtime, and a value closed over at effect scope would go
          // stale the instant the user zooms.
          const opts = termRef.current?.options
          const lineHeight = (opts?.fontSize ?? 13) * (opts?.lineHeight ?? 1)
          const deltaY = scrollStartY - e.touches[0].clientY
          scrollStartY = e.touches[0].clientY
          scrollAcc += deltaY
          const lines = Math.trunc(scrollAcc / lineHeight)
          if (lines !== 0) {
            scrollAcc -= lines * lineHeight
            termRef.current?.scrollLines(lines)
          }
        } else {
          if (inner.style.transition) inner.style.transition = ''
          const dx = e.touches[0].clientX - panLastX
          panLastX = e.touches[0].clientX
          translateXRef.current += dx
          clampPan()
          applyTransform()
        }
      } else if (e.touches.length === 2) {
        if (gesture !== 'pinch') {
          gesture = 'pinch'
          lastTapTime = null
          const t1 = e.touches[0], t2 = e.touches[1]
          pinchStartDist = Math.hypot(t2.clientX - t1.clientX, t2.clientY - t1.clientY)
          pinchStartScale = scaleRef.current
          pinchStartTX = translateXRef.current
          pinchStartTY = translateYRef.current
          pinchScreenMidX = (t1.clientX + t2.clientX) / 2
          pinchScreenMidY = (t1.clientY + t2.clientY) / 2
          const rect = outerRef.current!.getBoundingClientRect()
          pinchFocalCX = (pinchScreenMidX - rect.left - PAD - pinchStartTX) / pinchStartScale
          pinchFocalCY = (pinchScreenMidY - rect.top - PAD - pinchStartTY) / pinchStartScale
          return
        }

        const t1 = e.touches[0], t2 = e.touches[1]
        const dist = Math.hypot(t2.clientX - t1.clientX, t2.clientY - t1.clientY)
        const newScale = Math.max(minScaleRef.current, Math.min(2.0, pinchStartScale * (dist / pinchStartDist)))

        const newMidX = (t1.clientX + t2.clientX) / 2
        const newMidY = (t1.clientY + t2.clientY) / 2
        const rect = outerRef.current!.getBoundingClientRect()
        const relMidX = newMidX - rect.left - PAD
        const relMidY = newMidY - rect.top - PAD

        scaleRef.current = newScale
        translateXRef.current = relMidX - pinchFocalCX * newScale
        translateYRef.current = relMidY - pinchFocalCY * newScale
        clampPan()
        applyTransform()
      }
    }

    const onTouchEnd = (e: TouchEvent) => {
      e.stopPropagation()
      if (e.touches.length !== 0) return

      if (gesture === 'pinch') {
        const term = termRef.current
        if (term) {
          const snapped = snapFontSize((term.options.fontSize ?? 13) * scaleRef.current)
          applyFontSize(term, snapped)
          // Bake the pinch's CSS scale into the real font size — at rest the
          // transform's scale component is always 1.0, never resampled text.
          scaleRef.current = 1
          clampPan()
          applyTransform()
          onPinchEndRef.current?.(snapped)
        }
      } else if (gesture === 'pan') {
        // Column-0 magnetism: a release within COLUMN_ZERO_THRESHOLD_PX of
        // the terminal's own left edge eases the rest of the way to exactly
        // 0 rather than leaving a near-zero-but-not-zero offset.
        if (translateXRef.current !== 0 && Math.abs(translateXRef.current) <= COLUMN_ZERO_THRESHOLD_PX) {
          easeTranslateXTo(0)
        }
      } else if (gesture === 'scroll' && !directionLocked) {
        // Neither leg of this touch moved past the direction threshold —
        // it's a tap. Check it against the previous one for a double-tap.
        const now = performance.now()
        const isDoubleTap =
          lastTapTime !== null &&
          now - lastTapTime < DOUBLE_TAP_WINDOW_MS &&
          Math.hypot(dragOriginX - lastTapX, dragOriginY - lastTapY) < DOUBLE_TAP_MAX_DIST_PX
        if (isDoubleTap) {
          lastTapTime = null
          onDoubleTapRef.current?.()
        } else {
          lastTapTime = now
          lastTapX = dragOriginX
          lastTapY = dragOriginY
        }
      }
      gesture = 'none'
      gestureActiveRef.current = false
    }

    screen.addEventListener('touchstart', onTouchStart, { passive: true })
    screen.addEventListener('touchmove', onTouchMove, { passive: false })
    screen.addEventListener('touchend', onTouchEnd, { passive: true })

    return () => {
      screen.removeEventListener('touchstart', onTouchStart)
      screen.removeEventListener('touchmove', onTouchMove)
      screen.removeEventListener('touchend', onTouchEnd)
      if (easeTimerRef.current) {
        clearTimeout(easeTimerRef.current)
        easeTimerRef.current = null
      }
    }
  }, [enabled, innerRef, outerRef, termRef])

  return {
    scaleRef,
    translateXRef,
    translateYRef,
    minScaleRef,
    termDimsRef,
    gestureActiveRef,
    resetTransform,
    clampPan,
    applyTransform,
    easeTranslateXTo,
    applyFontSize,
  }
}
