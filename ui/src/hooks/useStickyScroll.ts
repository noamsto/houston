import { useLayoutEffect, useRef, useState } from 'react'
import type { RefObject } from 'react'

export const STICK_SLOP_PX = 24

interface UseStickyScroll<T extends HTMLElement> {
  ref: RefObject<T | null>
  stuck: boolean
  onScroll: () => void
  /** Capture the current scroll offset before prepending content, and unstick. */
  preserveAnchor: () => void
  scrollToLatest: () => void
}

/**
 * Keeps a scrollable container pinned to the bottom while the user hasn't
 * scrolled away (`stickDeps` — new content that should yank the view down
 * when stuck), and restores the scroll offset across an anchor-preserving
 * content change keyed by `anchorKey` (e.g. "Show earlier" prepending rows).
 */
export function useStickyScroll<T extends HTMLElement>(stickDeps: unknown[], anchorKey: unknown): UseStickyScroll<T> {
  const [stuck, setStuck] = useState(true)
  const ref = useRef<T | null>(null)
  const stuckRef = useRef(true)
  const anchor = useRef<{ height: number; top: number } | null>(null)

  const setStick = (v: boolean) => {
    stuckRef.current = v
    setStuck(v)
  }

  const onScroll = () => {
    const el = ref.current
    if (!el) return
    setStick(el.scrollHeight - el.scrollTop - el.clientHeight < STICK_SLOP_PX)
  }

  const preserveAnchor = () => {
    const el = ref.current
    if (el) anchor.current = { height: el.scrollHeight, top: el.scrollTop }
    setStick(false)
  }

  const scrollToLatest = () => {
    const el = ref.current
    if (el) el.scrollTop = el.scrollHeight
    setStick(true)
  }

  useLayoutEffect(() => {
    const el = ref.current
    if (!el || !anchor.current) return
    el.scrollTop = anchor.current.top + (el.scrollHeight - anchor.current.height)
    anchor.current = null
  }, [anchorKey])

  useLayoutEffect(() => {
    const el = ref.current
    if (el && stuckRef.current) el.scrollTop = el.scrollHeight
    // stickDeps is caller-supplied, so its contents can't be statically checked.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, stickDeps)

  return { ref, stuck, onScroll, preserveAnchor, scrollToLatest }
}
