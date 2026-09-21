import { useEffect, useState } from 'react'

const MIN_KEYBOARD_PX = 80

/**
 * Height of the on-screen keyboard as the gap between the layout viewport and
 * the visual viewport. iOS Safari (and Android with interactive-widget
 * overlays-content) leaves the layout viewport full-height under the keyboard,
 * so anything docked to the bottom ends up hidden; browsers that resize the
 * layout viewport report 0 here and need no help.
 */
export function useKeyboardInset(): number {
  const [inset, setInset] = useState(0)

  useEffect(() => {
    const vv = window.visualViewport
    if (!vv) return
    const update = () => {
      // Pinch-zoom also shrinks the visual viewport; only an unzoomed gap that
      // is keyboard-sized counts.
      const gap = Math.round(window.innerHeight - vv.height - vv.offsetTop)
      setInset(vv.scale > 1.01 || gap < MIN_KEYBOARD_PX ? 0 : gap)
    }
    update()
    vv.addEventListener('resize', update)
    vv.addEventListener('scroll', update)
    return () => {
      vv.removeEventListener('resize', update)
      vv.removeEventListener('scroll', update)
    }
  }, [])

  return inset
}
