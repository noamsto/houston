import { useCallback, useEffect, useRef, useState } from 'react'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import { WebLinksAddon } from '@xterm/addon-web-links'
import '@xterm/xterm/css/xterm.css'
import type { WSMeta } from '../api/types'
import type { PaneInstance } from '../hooks/useLayout'
import { usePaneSocket } from '../hooks/usePaneSocket'
import { useIsDesktop } from '../hooks/useMediaQuery'
import { darkTheme, lightTheme } from '../lib/xterm'
import { PaneHeader } from './PaneHeader'
import { MobileInputBar } from './MobileInputBar'

interface Props {
  pane: PaneInstance
  isFocused: boolean
  onFocus: () => void
  onClose: () => void
}

/** Write a full-screen snapshot into an xterm instance. */
function writeSnapshot(term: Terminal, data: string) {
  // \x1b[2J   clear visible screen  (no stale lines from previous write)
  // \x1b[3J   clear scrollback       (always reflects latest capture)
  // \x1b[H    home cursor
  // \x1b[?25l hide xterm cursor      (tmux capture renders the real one)
  // \x1b[?1007l disable alt-scroll   \  prevent wheel→arrow forwarding
  // \x1b[?1l  disable app-cursor-keys/  while keeping viewport scroll
  term.write('\x1b[2J\x1b[3J\x1b[H' + data + '\x1b[?25l\x1b[?1007l\x1b[?1l')
}

// Wide mode: ~120 columns for diffs and wide output. Fit mode: viewport width.
const MOBILE_TERM_WIDTH_WIDE = 960
const PAD = 6

export function TerminalPane({ pane, isFocused, onFocus, onClose }: Props) {
  // outerRef: observed by ResizeObserver; has padding that creates visual breathing room
  const outerRef = useRef<HTMLDivElement>(null)
  // innerRef: xterm.js is opened here so FitAddon measures the padded inner area
  const innerRef = useRef<HTMLDivElement>(null)
  const termRef = useRef<Terminal | null>(null)
  const fitAddonRef = useRef<FitAddon | null>(null)
  const [meta, setMeta] = useState<WSMeta | null>(null)
  const isDesktop = useIsDesktop()
  const [wideMode, setWideMode] = useState(true) // wide by default

  // Mobile zoom/pan state (refs for direct DOM manipulation — no re-renders)
  const scaleRef = useRef(1)
  const translateXRef = useRef(0)
  const translateYRef = useRef(0)
  const minScaleRef = useRef(1)
  const termDimsRef = useRef({ w: 0, h: 0 })

  // Pending output for RAF-deferred rendering — coalesces rapid updates into one frame
  const pendingOutputRef = useRef<string | null>(null)
  const rafRef = useRef<number>(0)
  // Deferred output: saved when user is scrolled up, applied when they scroll back to bottom
  const deferredOutputRef = useRef<string | null>(null)
  // Cache latest output so we can replay it when xterm remounts (e.g. isDesktop changes)
  // without waiting for the server to send a new capture (it deduplicates).
  const lastOutputRef = useRef<string | null>(null)

  // Recalculate mobile terminal dimensions without remounting xterm
  const applyMobileSize = useCallback((wide: boolean) => {
    const outer = outerRef.current
    const inner = innerRef.current
    const fit = fitAddonRef.current
    const term = termRef.current
    if (!outer || !inner || !fit || !term || isDesktop) return

    const outerW = outer.clientWidth - PAD * 2
    const outerH = outer.clientHeight - PAD * 2

    if (wide) {
      const s = outerW / MOBILE_TERM_WIDTH_WIDE
      const termH = Math.round(outerH / s)
      inner.style.width = `${MOBILE_TERM_WIDTH_WIDE}px`
      inner.style.height = `${termH}px`
      inner.style.transform = `scale(${s})`
      scaleRef.current = s
      minScaleRef.current = s
      termDimsRef.current = { w: MOBILE_TERM_WIDTH_WIDE, h: termH }
    } else {
      inner.style.width = `${outerW}px`
      inner.style.height = `${outerH}px`
      inner.style.transform = 'none'
      scaleRef.current = 1
      minScaleRef.current = 1
      termDimsRef.current = { w: outerW, h: outerH }
    }
    translateXRef.current = 0
    translateYRef.current = 0

    try {
      fit.fit()
      sendResize(term.cols, term.rows)
      if (lastOutputRef.current) {
        writeSnapshot(term, lastOutputRef.current)
      }
    } catch { /* fit can throw if zero-size */ }
  }, [isDesktop, sendResize])

  const { sendInput, sendResize } = usePaneSocket(pane.target, {
    onOutput: (data) => {
      lastOutputRef.current = data
      pendingOutputRef.current = data
      if (!rafRef.current) {
        rafRef.current = requestAnimationFrame(() => {
          rafRef.current = 0
          const term = termRef.current
          const pending = pendingOutputRef.current
          if (!term || pending === null) return
          pendingOutputRef.current = null
          // If user has scrolled up, defer the write to preserve their position
          const buf = term.buffer.active
          if (buf.viewportY < buf.baseY) {
            deferredOutputRef.current = pending
            return
          }
          writeSnapshot(term, pending)
        })
      }
    },
    onMeta: (m) => setMeta(m),
  })

  // Mount xterm.js — remount when target or desktop mode changes
  useEffect(() => {
    if (!innerRef.current || !outerRef.current) return

    const isDark = !document.documentElement.classList.contains('light')
    const term = new Terminal({
      theme: isDark ? darkTheme : lightTheme,
      fontFamily: "'JetBrains Mono', 'Fira Code', 'Cascadia Code', monospace",
      fontSize: 13,
      lineHeight: 1.2,
      cursorBlink: false,
      disableStdin: !isDesktop,
      // convertEol: make \n behave as \r\n so lines start at column 0.
      // tmux capture-pane uses \n separators; without this, cursor stays at
      // the same column after each newline, causing text to "float".
      convertEol: true,
      // 500 lines matches the tmux capture depth so the user can scroll up through history.
      // \x1b[3J clears old scrollback on each write so it always reflects the latest capture.
      scrollback: 500,
    })

    const fitAddon = new FitAddon()
    term.loadAddon(fitAddon)
    term.loadAddon(new WebLinksAddon())

    // Mobile: set terminal dimensions based on wide/fit mode
    if (!isDesktop) {
      const outerW = outerRef.current.clientWidth - PAD * 2
      const outerH = outerRef.current.clientHeight - PAD * 2

      if (wideMode) {
        const s = outerW / MOBILE_TERM_WIDTH_WIDE
        const termH = Math.round(outerH / s)
        innerRef.current.style.width = `${MOBILE_TERM_WIDTH_WIDE}px`
        innerRef.current.style.height = `${termH}px`
        innerRef.current.style.transform = `scale(${s})`
        scaleRef.current = s
        minScaleRef.current = s
        termDimsRef.current = { w: MOBILE_TERM_WIDTH_WIDE, h: termH }
      } else {
        innerRef.current.style.width = `${outerW}px`
        innerRef.current.style.height = `${outerH}px`
        scaleRef.current = 1
        minScaleRef.current = 1
        termDimsRef.current = { w: outerW, h: outerH }
      }
      translateXRef.current = 0
      translateYRef.current = 0
    }

    term.open(innerRef.current)

    termRef.current = term
    fitAddonRef.current = fitAddon

    // Defer initial fit so the DOM has its final layout before measuring.
    // Also replay cached output — when isDesktop changes, xterm remounts but the
    // WS server won't re-send output that hasn't changed since the last send.
    requestAnimationFrame(() => {
      fitAddon.fit()
      if (lastOutputRef.current) {
        writeSnapshot(term, lastOutputRef.current)
      }
    })

    if (isDesktop) {
      term.onData((data) => sendInput(data))
    }

    // When user scrolls back to bottom, apply any deferred output
    const viewport = innerRef.current.querySelector('.xterm-viewport')
    const onViewportScroll = () => {
      const buf = term.buffer.active
      if (buf.viewportY >= buf.baseY && deferredOutputRef.current !== null) {
        const data = deferredOutputRef.current
        deferredOutputRef.current = null
        writeSnapshot(term, data)
      }
    }
    viewport?.addEventListener('scroll', onViewportScroll)

    // Mobile touch gestures — xterm.js's internal Gesture class adds non-passive
    // touch listeners on document that call preventDefault(), blocking all touch
    // interaction. We intercept on the screen element and handle:
    //   1-finger vertical drag → scroll terminal history (term.scrollLines)
    //   2-finger pinch → zoom (CSS transform scale)
    //   2-finger drag → pan (CSS transform translate)
    const screen = innerRef.current.querySelector('.xterm-screen') as HTMLElement | null
    const lineHeight = 13 * 1.2 // fontSize * lineHeight

    let gesture: 'none' | 'scroll' | 'pan' | 'pinch' = 'none'
    // Direction detection — lock after first significant movement
    const DIRECTION_THRESHOLD = 8 // px before locking direction
    let dragOriginX = 0
    let dragOriginY = 0
    let directionLocked = false
    // Scroll state
    let scrollStartY = 0
    let scrollAcc = 0
    // Pan state (horizontal single-finger)
    let panLastX = 0
    // Pinch state
    let pinchStartDist = 0
    let pinchStartScale = 0
    let pinchFocalCX = 0 // focal point in content coordinates
    let pinchFocalCY = 0
    let pinchScreenMidX = 0 // starting midpoint on screen
    let pinchScreenMidY = 0
    let pinchStartTX = 0
    let pinchStartTY = 0

    const applyTransform = () => {
      if (!innerRef.current) return
      const s = scaleRef.current
      const tx = translateXRef.current
      const ty = translateYRef.current
      innerRef.current.style.transform = `translate(${tx}px, ${ty}px) scale(${s})`
    }

    const clampPan = () => {
      if (!outerRef.current) return
      const pad = 6
      const outerW = outerRef.current.clientWidth - pad * 2
      const outerH = outerRef.current.clientHeight - pad * 2
      const s = scaleRef.current
      const visW = termDimsRef.current.w * s
      const visH = termDimsRef.current.h * s
      // Keep content covering the viewport — don't let it pull away from edges
      translateXRef.current = Math.min(0, Math.max(outerW - visW, translateXRef.current))
      translateYRef.current = Math.min(0, Math.max(outerH - visH, translateYRef.current))
    }

    const onTouchStart = (e: TouchEvent) => {
      e.stopPropagation()
      if (e.touches.length === 1) {
        // Don't lock direction yet — wait for first significant movement
        gesture = 'scroll' // tentative, may switch to 'pan'
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

        // Midpoint on screen
        pinchScreenMidX = (t1.clientX + t2.clientX) / 2
        pinchScreenMidY = (t1.clientY + t2.clientY) / 2

        // Convert screen midpoint to content coordinates so we can keep
        // the focal point stable during zoom.
        // screen = containerOrigin + content * scale + translate
        const rect = outerRef.current!.getBoundingClientRect()
        const relX = pinchScreenMidX - rect.left - 6
        const relY = pinchScreenMidY - rect.top - 6
        pinchFocalCX = (relX - pinchStartTX) / pinchStartScale
        pinchFocalCY = (relY - pinchStartTY) / pinchStartScale
      }
    }

    const onTouchMove = (e: TouchEvent) => {
      e.preventDefault()
      e.stopPropagation()

      if ((gesture === 'scroll' || gesture === 'pan') && e.touches.length === 1) {
        // Detect drag direction on first significant movement
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
            term.scrollLines(lines)
          }
        } else {
          // Horizontal pan
          const dx = e.touches[0].clientX - panLastX
          panLastX = e.touches[0].clientX
          translateXRef.current += dx
          clampPan()
          applyTransform()
        }
      } else if (e.touches.length === 2) {
        // Switch to pinch if a second finger was added mid-gesture
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
          pinchFocalCX = (pinchScreenMidX - rect.left - 6 - pinchStartTX) / pinchStartScale
          pinchFocalCY = (pinchScreenMidY - rect.top - 6 - pinchStartTY) / pinchStartScale
          return
        }

        const t1 = e.touches[0], t2 = e.touches[1]
        const dist = Math.hypot(t2.clientX - t1.clientX, t2.clientY - t1.clientY)
        const newScale = Math.max(minScaleRef.current, Math.min(2.0, pinchStartScale * (dist / pinchStartDist)))

        // New midpoint (tracks two-finger pan)
        const newMidX = (t1.clientX + t2.clientX) / 2
        const newMidY = (t1.clientY + t2.clientY) / 2
        const rect = outerRef.current!.getBoundingClientRect()
        const relMidX = newMidX - rect.left - 6
        const relMidY = newMidY - rect.top - 6

        // Translate so the focal content point stays under the midpoint
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

    if (!isDesktop && screen) {
      screen.addEventListener('touchstart', onTouchStart, { passive: true })
      screen.addEventListener('touchmove', onTouchMove, { passive: false })
      screen.addEventListener('touchend', onTouchEnd, { passive: true })
    }

    return () => {
      viewport?.removeEventListener('scroll', onViewportScroll)
      if (screen) {
        screen.removeEventListener('touchstart', onTouchStart)
        screen.removeEventListener('touchmove', onTouchMove)
        screen.removeEventListener('touchend', onTouchEnd)
      }
      cancelAnimationFrame(rafRef.current)
      rafRef.current = 0
      pendingOutputRef.current = null
      deferredOutputRef.current = null
      term.dispose()
      termRef.current = null
      fitAddonRef.current = null
    }
  }, [pane.target, isDesktop]) // eslint-disable-line react-hooks/exhaustive-deps

  // Resize observer — refit when outer container dimensions change
  useEffect(() => {
    const container = outerRef.current
    if (!container) return

    const ro = new ResizeObserver(() => {
      const fit = fitAddonRef.current
      const term = termRef.current
      if (!fit || !term) return

      // Mobile: update terminal height to fill visual space after scaling
      if (!isDesktop && innerRef.current) {
        const outerH = container.clientHeight - PAD * 2
        const s = minScaleRef.current
        const termH = Math.round(outerH / s)
        innerRef.current.style.height = `${termH}px`
        termDimsRef.current = { ...termDimsRef.current, h: termH }
      }

      try {
        fit.fit()
        sendResize(term.cols, term.rows)
      } catch {
        // fit() can throw if the container is hidden or has zero size
      }
    })

    ro.observe(container)
    return () => ro.disconnect()
  }, [sendResize, isDesktop])

  return (
    <div
      style={{
        display: 'flex',
        flexDirection: 'column',
        height: '100%',
        outline: isFocused ? '1px solid var(--accent-working)' : 'none',
        outlineOffset: -1,
      }}
      onClick={onFocus}
    >
      <PaneHeader
        target={pane.target}
        meta={meta}
        onClose={onClose}
        wideMode={isDesktop ? undefined : wideMode}
        onToggleWide={isDesktop ? undefined : () => {
          const next = !wideMode
          setWideMode(next)
          applyMobileSize(next)
        }}
      />
      {/* Outer div: ResizeObserver target; background shows through as visual padding */}
      <div
        ref={outerRef}
        style={{
          flex: 1,
          overflow: 'hidden',
          minHeight: 0,
          position: 'relative',
          background: 'var(--bg-terminal)',
        }}
      >
        {/* Inner div: inset by 6px — xterm opens here; FitAddon measures this area.
            Desktop: stretches to fill. Mobile: fixed wider width, CSS-transformed to fit. */}
        <div
          ref={innerRef}
          style={{
            position: 'absolute',
            top: 6,
            left: 6,
            ...(isDesktop ? { right: 6, bottom: 6 } : { transformOrigin: '0 0' }),
          }}
        />
      </div>
      {!isDesktop && (
        <MobileInputBar
          target={pane.target}
          choices={meta?.choices}
        />
      )}
    </div>
  )
}
