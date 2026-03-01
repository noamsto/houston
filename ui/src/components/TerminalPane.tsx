import { useCallback, useEffect, useRef, useState } from 'react'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import { WebLinksAddon } from '@xterm/addon-web-links'
import '@xterm/xterm/css/xterm.css'
import type { WSMeta } from '../api/types'
import type { PaneInstance } from '../hooks/useLayout'
import { usePaneSocket } from '../hooks/usePaneSocket'
import { useIsDesktop } from '../hooks/useMediaQuery'
import { useTouchGestures } from '../hooks/useTouchGestures'
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
  const [termMounted, setTermMounted] = useState(false)

  const { minScaleRef, termDimsRef, resetTransform } = useTouchGestures(
    innerRef, outerRef, termRef, !isDesktop && termMounted,
  )

  // Pending output for RAF-deferred rendering — coalesces rapid updates into one frame
  const pendingOutputRef = useRef<string | null>(null)
  const rafRef = useRef<number>(0)
  // Deferred output: saved when user is scrolled up, applied when they scroll back to bottom
  const deferredOutputRef = useRef<string | null>(null)
  // Cache latest output so we can replay it when xterm remounts (e.g. isDesktop changes)
  // without waiting for the server to send a new capture (it deduplicates).
  const lastOutputRef = useRef<string | null>(null)

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

  // Show cursor for non-AI agents (regular shells, etc.)
  const agent = meta?.agent
  useEffect(() => {
    const term = termRef.current
    if (!term) return
    const showCursor = agent === 'generic'
    term.options.cursorBlink = showCursor
    term.options.cursorStyle = showCursor ? 'block' : 'underline'
  }, [agent])

  // Sync xterm theme when light/dark mode toggles
  useEffect(() => {
    const term = termRef.current
    if (!term) return
    const observer = new MutationObserver(() => {
      const isLight = document.documentElement.classList.contains('light')
      term.options.theme = isLight ? lightTheme : darkTheme
    })
    observer.observe(document.documentElement, { attributes: true, attributeFilter: ['class'] })
    return () => observer.disconnect()
  }, [termMounted])

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
      const minS = outerW / MOBILE_TERM_WIDTH_WIDE
      const termH = Math.round(outerH / minS)
      inner.style.width = `${MOBILE_TERM_WIDTH_WIDE}px`
      inner.style.height = `${termH}px`
      // Start zoomed in at bottom-left: scale 1.0, positioned so bottom edge aligns
      const initS = 1
      const initTY = outerH - termH * initS
      inner.style.transform = `translate(0px, ${initTY}px) scale(${initS})`
      resetTransform(minS, { w: MOBILE_TERM_WIDTH_WIDE, h: termH }, { scale: initS, tx: 0, ty: initTY })
    } else {
      inner.style.width = `${outerW}px`
      inner.style.height = `${outerH}px`
      inner.style.transform = 'none'
      resetTransform(1, { w: outerW, h: outerH })
    }

    try {
      fit.fit()
      sendResize(term.cols, term.rows)
      if (lastOutputRef.current) {
        writeSnapshot(term, lastOutputRef.current)
      }
    } catch { /* fit can throw if zero-size */ }
  }, [isDesktop, sendResize, resetTransform])

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
        const minS = outerW / MOBILE_TERM_WIDTH_WIDE
        const termH = Math.round(outerH / minS)
        innerRef.current.style.width = `${MOBILE_TERM_WIDTH_WIDE}px`
        innerRef.current.style.height = `${termH}px`
        // Start zoomed in at bottom-left
        const initS = 1
        const initTY = outerH - termH * initS
        innerRef.current.style.transform = `translate(0px, ${initTY}px) scale(${initS})`
        resetTransform(minS, { w: MOBILE_TERM_WIDTH_WIDE, h: termH }, { scale: initS, tx: 0, ty: initTY })
      } else {
        innerRef.current.style.width = `${outerW}px`
        innerRef.current.style.height = `${outerH}px`
        resetTransform(1, { w: outerW, h: outerH })
      }
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
      setTermMounted(true)
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

    return () => {
      viewport?.removeEventListener('scroll', onViewportScroll)
      cancelAnimationFrame(rafRef.current)
      rafRef.current = 0
      pendingOutputRef.current = null
      deferredOutputRef.current = null
      term.dispose()
      termRef.current = null
      fitAddonRef.current = null
      setTermMounted(false)
    }
  }, [pane.target, isDesktop]) // eslint-disable-line react-hooks/exhaustive-deps

  // Resize observer — refit when outer container dimensions change
  useEffect(() => {
    const container = outerRef.current
    if (!container) return

    let debounceTimer: ReturnType<typeof setTimeout>
    const ro = new ResizeObserver(() => {
      clearTimeout(debounceTimer)
      debounceTimer = setTimeout(() => {
        const fit = fitAddonRef.current
        const term = termRef.current
        if (!fit || !term) return

        // Mobile: update terminal dimensions when container resizes
        // (e.g. keyboard opens/closes, quick buttons expand/collapse)
        if (!isDesktop && innerRef.current) {
          const outerW = container.clientWidth - PAD * 2
          const outerH = container.clientHeight - PAD * 2
          const s = minScaleRef.current
          if (s < 1) {
            // WIDE mode: recalculate height and clamp translate to keep
            // content anchored at the bottom (no black gap above terminal)
            const termH = Math.round(outerH / s)
            innerRef.current.style.height = `${termH}px`
            termDimsRef.current = { ...termDimsRef.current, h: termH }
            // Re-anchor to bottom so keyboard doesn't leave a black gap
            const curScale = innerRef.current.style.transform.match(/scale\(([\d.]+)\)/)
            const sc = curScale ? parseFloat(curScale[1]) : s
            const ty = outerH - termH * sc
            innerRef.current.style.transform = `translate(0px, ${ty}px) scale(${sc})`
            resetTransform(s, { w: MOBILE_TERM_WIDTH_WIDE, h: termH }, { scale: sc, tx: 0, ty })
          } else {
            // FIT mode: just update dimensions
            innerRef.current.style.width = `${outerW}px`
            innerRef.current.style.height = `${outerH}px`
            termDimsRef.current = { w: outerW, h: outerH }
          }
        }

        try {
          fit.fit()
          sendResize(term.cols, term.rows)
        } catch {
          // fit() can throw if the container is hidden or has zero size
        }
      }, 150)
    })

    ro.observe(container)
    return () => {
      clearTimeout(debounceTimer)
      ro.disconnect()
    }
  }, [sendResize, isDesktop, minScaleRef, resetTransform, termDimsRef])

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
