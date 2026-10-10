import { useEffect, useRef } from 'react'

/** A touch must start within this many px of the left edge to belong to the back gesture. */
export const EDGE_ZONE_PX = 24
/** Travel before the gesture decides between a back swipe and something else. */
const LOCK_PX = 10
/** Commit past this fraction of the width... */
const COMMIT_FRACTION = 0.38
/** ...or on a flick at least this fast (px/ms) that travelled at least FLICK_MIN_PX. */
const FLICK_VELOCITY = 0.5
const FLICK_MIN_PX = 40
const SETTLE_MS = 200

export function inEdgeZone(startX: number): boolean {
  return startX >= 0 && startX <= EDGE_ZONE_PX
}

/** Whether the first LOCK_PX of movement is a rightward, horizontal-dominant swipe. */
export function lockDirection(dx: number, dy: number): 'back' | 'cancel' | 'undecided' {
  if (Math.hypot(dx, dy) < LOCK_PX) return 'undecided'
  return dx > 0 && dx > Math.abs(dy) ? 'back' : 'cancel'
}

export function shouldCommit(dx: number, width: number, elapsedMs: number): boolean {
  if (dx <= 0 || width <= 0) return false
  if (dx >= width * COMMIT_FRACTION) return true
  return dx >= FLICK_MIN_PX && elapsedMs > 0 && dx / elapsedMs >= FLICK_VELOCITY
}

function reducedMotion(): boolean {
  return Boolean(window.matchMedia?.('(prefers-reduced-motion: reduce)').matches)
}

/**
 * iOS-style edge swipe that closes a mobile overlay: a touch starting at the
 * left edge and moving mostly rightward drags `ref` along and, past a distance
 * or flick threshold, calls `onBack`.
 *
 * The listeners run in the capture phase on the overlay and stop an edge
 * touch from reaching descendants, so the terminal's own one-finger pan never
 * sees it; any other touch is left untouched. The on-screen keyboard does not
 * matter: the zone is a horizontal strip and a touch there is never a text
 * field's.
 *
 * Safari's own back/forward edge swipe cancels the touch (`touchcancel`) or
 * moves the hash; neither may also fire `onBack`, so a cancel never commits and
 * a commit is dropped if the hash changed since the touch began.
 */
export function useEdgeSwipeBack(
  ref: React.RefObject<HTMLElement | null>,
  onBack: () => void,
  enabled = true,
) {
  const onBackRef = useRef(onBack)
  useEffect(() => {
    onBackRef.current = onBack
  })

  useEffect(() => {
    const el = ref.current
    if (!enabled || !el) return

    let tracking = false
    let locked = false
    let startX = 0
    let startY = 0
    let startT = 0
    let startHash = ''
    let dx = 0
    let settle: ReturnType<typeof setTimeout> | null = null

    const reset = () => {
      tracking = false
      locked = false
      el.classList.remove('edge-swiping')
    }

    const setOffset = (px: number) => {
      el.style.transform = px ? `translateX(${px}px)` : ''
    }

    const springBack = () => {
      reset()
      if (reducedMotion() || dx === 0) {
        setOffset(0)
        return
      }
      el.style.transition = `transform ${SETTLE_MS}ms ease-out`
      setOffset(0)
      settle = setTimeout(() => { settle = null; el.style.transition = '' }, SETTLE_MS)
    }

    const commit = (width: number) => {
      reset()
      if (window.location.hash !== startHash) {
        setOffset(0)
        return
      }
      if (reducedMotion()) {
        onBackRef.current()
        return
      }
      el.style.transition = `transform ${SETTLE_MS}ms ease-out`
      setOffset(width)
      settle = setTimeout(() => {
        settle = null
        if (window.location.hash !== startHash) {
          el.style.transition = ''
          setOffset(0)
          return
        }
        onBackRef.current()
      }, SETTLE_MS)
    }

    const onStart = (e: TouchEvent) => {
      if (settle) return
      if (e.touches.length !== 1) {
        if (tracking) springBack()
        return
      }
      const t = e.touches[0]
      if (!inEdgeZone(t.clientX - el.getBoundingClientRect().left)) return
      e.stopPropagation()
      tracking = true
      locked = false
      startX = t.clientX
      startY = t.clientY
      startT = performance.now()
      startHash = window.location.hash
      dx = 0
    }

    const onMove = (e: TouchEvent) => {
      if (!tracking) return
      e.stopPropagation()
      // Before the lock too: once the browser starts a native scroll the
      // touch is no longer cancelable. Costs a vertical scroll begun in the strip.
      if (e.cancelable) e.preventDefault()
      const t = e.touches[0]
      const mx = t.clientX - startX
      const my = t.clientY - startY
      if (!locked) {
        const verdict = lockDirection(mx, my)
        if (verdict === 'undecided') return
        if (verdict === 'cancel') {
          reset()
          return
        }
        locked = true
        el.classList.add('edge-swiping')
      }
      dx = Math.max(0, mx)
      if (!reducedMotion()) setOffset(dx)
    }

    const onEnd = (e: TouchEvent) => {
      if (!tracking) return
      e.stopPropagation()
      if (!locked) {
        reset()
        return
      }
      const width = el.clientWidth
      if (shouldCommit(dx, width, performance.now() - startT)) commit(width)
      else springBack()
    }

    const onCancel = () => {
      if (!tracking) return
      if (locked) springBack()
      else reset()
    }

    el.addEventListener('touchstart', onStart, { capture: true, passive: true })
    el.addEventListener('touchmove', onMove, { capture: true, passive: false })
    el.addEventListener('touchend', onEnd, { capture: true })
    el.addEventListener('touchcancel', onCancel, { capture: true })
    return () => {
      el.removeEventListener('touchstart', onStart, { capture: true })
      el.removeEventListener('touchmove', onMove, { capture: true })
      el.removeEventListener('touchend', onEnd, { capture: true })
      el.removeEventListener('touchcancel', onCancel, { capture: true })
      if (settle) clearTimeout(settle)
      el.style.transform = ''
      el.style.transition = ''
    }
  }, [ref, enabled])
}
