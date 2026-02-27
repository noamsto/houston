import { useEffect, useRef } from 'react'
import type { Terminal } from '@xterm/xterm'

/**
 * Touch gesture handler for mobile terminal interaction.
 * Handles: 1-finger vertical scroll, 1-finger horizontal pan, 2-finger pinch-to-zoom.
 */
export function useTouchGestures(
  innerRef: React.RefObject<HTMLDivElement | null>,
  outerRef: React.RefObject<HTMLDivElement | null>,
  termRef: React.RefObject<Terminal | null>,
  enabled: boolean,
) {
  const scaleRef = useRef(1)
  const translateXRef = useRef(0)
  const translateYRef = useRef(0)
  const minScaleRef = useRef(1)
  const termDimsRef = useRef({ w: 0, h: 0 })

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

  useEffect(() => {
    if (!enabled || !innerRef.current) return

    const inner = innerRef.current
    const screen = inner.querySelector('.xterm-screen') as HTMLElement | null
    if (!screen) return

    const PAD = 6
    const DIRECTION_THRESHOLD = 8
    const lineHeight = 13 * 1.2 // fontSize * lineHeight

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

    const applyTransform = () => {
      if (!inner) return
      const s = scaleRef.current
      const tx = translateXRef.current
      const ty = translateYRef.current
      inner.style.transform = `translate(${tx}px, ${ty}px) scale(${s})`
    }

    const clampPan = () => {
      if (!outerRef.current) return
      const outerW = outerRef.current.clientWidth - PAD * 2
      const outerH = outerRef.current.clientHeight - PAD * 2
      const s = scaleRef.current
      const visW = termDimsRef.current.w * s
      const visH = termDimsRef.current.h * s
      translateXRef.current = Math.min(0, Math.max(outerW - visW, translateXRef.current))
      translateYRef.current = Math.min(0, Math.max(outerH - visH, translateYRef.current))
    }

    const onTouchStart = (e: TouchEvent) => {
      e.stopPropagation()
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
        }

        if (gesture === 'scroll') {
          const deltaY = scrollStartY - e.touches[0].clientY
          scrollStartY = e.touches[0].clientY
          scrollAcc += deltaY
          const lines = Math.trunc(scrollAcc / lineHeight)
          if (lines !== 0) {
            scrollAcc -= lines * lineHeight
            termRef.current?.scrollLines(lines)
          }
        } else {
          const dx = e.touches[0].clientX - panLastX
          panLastX = e.touches[0].clientX
          translateXRef.current += dx
          clampPan()
          applyTransform()
        }
      } else if (e.touches.length === 2) {
        if (gesture !== 'pinch') {
          gesture = 'pinch'
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
      if (e.touches.length === 0) gesture = 'none'
    }

    screen.addEventListener('touchstart', onTouchStart, { passive: true })
    screen.addEventListener('touchmove', onTouchMove, { passive: false })
    screen.addEventListener('touchend', onTouchEnd, { passive: true })

    return () => {
      screen.removeEventListener('touchstart', onTouchStart)
      screen.removeEventListener('touchmove', onTouchMove)
      screen.removeEventListener('touchend', onTouchEnd)
    }
  }, [enabled, innerRef, outerRef, termRef])

  return { scaleRef, translateXRef, translateYRef, minScaleRef, termDimsRef, resetTransform }
}
