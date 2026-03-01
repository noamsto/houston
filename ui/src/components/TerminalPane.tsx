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

/** Write a full-screen snapshot into an xterm instance.
 *  Returns via callback when the write has been fully processed by xterm. */
function writeSnapshot(term: Terminal, data: string, onDone?: () => void) {
  // \x1b[2J   clear visible screen  (no stale lines from previous write)
  // \x1b[3J   clear scrollback       (always reflects latest capture)
  // \x1b[H    home cursor
  // \x1b[?25l hide xterm cursor      (tmux capture renders the real one)
  // \x1b[?1007l disable alt-scroll   \  prevent wheel→arrow forwarding
  // \x1b[?1l  disable app-cursor-keys/  while keeping viewport scroll
  term.write('\x1b[2J\x1b[3J\x1b[H' + data + '\x1b[?25l\x1b[?1007l\x1b[?1l', onDone)
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
  // Track whether xterm is mid-write — term.write() is async; checking buffer
  // state before the previous write completes yields stale results.
  const writingRef = useRef(false)

  const { connected, sendInput, sendResize } = usePaneSocket(pane.target, {
    onOutput: (data) => {
      lastOutputRef.current = data
      pendingOutputRef.current = data
      scheduleFlush()
    },
    onMeta: (m) => setMeta(m),
  })

  function scheduleFlush() {
    if (rafRef.current || writingRef.current) return
    rafRef.current = requestAnimationFrame(() => {
      rafRef.current = 0
      const term = termRef.current
      const pending = pendingOutputRef.current
      if (!term || pending === null || writingRef.current) return
      pendingOutputRef.current = null
      // If user has scrolled up, defer the write to preserve their position
      const buf = term.buffer.active
      if (buf.viewportY < buf.baseY) {
        deferredOutputRef.current = pending
        return
      }
      writingRef.current = true
      writeSnapshot(term, pending, () => {
        writingRef.current = false
        // If new output arrived during the write, schedule another flush
        if (pendingOutputRef.current !== null) {
          scheduleFlush()
        }
      })
    })
  }

  // Focus the xterm textarea when this pane becomes the active one (desktop only).
  // No resize here — houston isn't a real tmux client (just capture-pane + send-keys),
  // so resizing is an uninvited side effect. Use the manual ⊞ button instead.
  useEffect(() => {
    if (isFocused && isDesktop) {
      termRef.current?.focus()
    }
  }, [isFocused, isDesktop])

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
      inner.style.width = `${MOBILE_TERM_WIDTH_WIDE}px`
      inner.style.height = `${outerH}px`
      inner.style.transform = 'translate(0px, 0px) scale(1)'
      resetTransform(minS, { w: MOBILE_TERM_WIDTH_WIDE, h: outerH }, { scale: 1, tx: 0, ty: 0 })
    } else {
      inner.style.width = `${outerW}px`
      inner.style.height = `${outerH}px`
      inner.style.transform = 'none'
      resetTransform(1, { w: outerW, h: outerH })
    }

    try {
      fit.fit()
      if (!wide) sendResize(term.cols, term.rows)
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
        innerRef.current.style.width = `${MOBILE_TERM_WIDTH_WIDE}px`
        innerRef.current.style.height = `${outerH}px`
        innerRef.current.style.transform = 'translate(0px, 0px) scale(1)'
        resetTransform(minS, { w: MOBILE_TERM_WIDTH_WIDE, h: outerH }, { scale: 1, tx: 0, ty: 0 })
      } else {
        innerRef.current.style.width = `${outerW}px`
        innerRef.current.style.height = `${outerH}px`
        resetTransform(1, { w: outerW, h: outerH })
      }
    }

    term.open(innerRef.current)

    termRef.current = term
    fitAddonRef.current = fitAddon
    console.debug('[input] xterm mounted — isDesktop:', isDesktop, 'disableStdin:', term.options.disableStdin, 'target:', pane.target)

    // Defer initial fit so the DOM has its final layout before measuring.
    // Also replay cached output — when isDesktop changes, xterm remounts but the
    // WS server won't re-send output that hasn't changed since the last send.
    requestAnimationFrame(() => {
      fitAddon.fit()
      if (lastOutputRef.current) {
        writeSnapshot(term, lastOutputRef.current)
      }
      if (isDesktop) term.focus()
      setTermMounted(true)
    })

    if (isDesktop) {
      term.onData((data) => {
        console.debug('[input] xterm onData:', JSON.stringify(data))
        sendInput(data)
      })
    } else {
      console.debug('[input] skipped onData registration (mobile mode)')
    }

    // When user scrolls back to bottom, apply any deferred output
    const viewport = innerRef.current.querySelector('.xterm-viewport')
    const onViewportScroll = () => {
      const buf = term.buffer.active
      if (buf.viewportY >= buf.baseY && deferredOutputRef.current !== null) {
        pendingOutputRef.current = deferredOutputRef.current
        deferredOutputRef.current = null
        scheduleFlush()
      }
    }
    viewport?.addEventListener('scroll', onViewportScroll)

    return () => {
      viewport?.removeEventListener('scroll', onViewportScroll)
      cancelAnimationFrame(rafRef.current)
      rafRef.current = 0
      pendingOutputRef.current = null
      deferredOutputRef.current = null
      writingRef.current = false
      term.dispose()
      termRef.current = null
      fitAddonRef.current = null
      setTermMounted(false)
    }
  }, [isDesktop]) // eslint-disable-line react-hooks/exhaustive-deps

  // Reset output state when switching pane targets — xterm stays mounted (no blink),
  // old content remains visible until the new WS connection delivers fresh output.
  useEffect(() => {
    cancelAnimationFrame(rafRef.current)
    rafRef.current = 0
    lastOutputRef.current = null
    pendingOutputRef.current = null
    deferredOutputRef.current = null
    writingRef.current = false
  }, [pane.target])

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
            // WIDE mode: update height to match viewport, keep current scale
            innerRef.current.style.height = `${outerH}px`
            termDimsRef.current = { ...termDimsRef.current, h: outerH }
            const curScale = innerRef.current.style.transform.match(/scale\(([\d.]+)\)/)
            const sc = curScale ? parseFloat(curScale[1]) : 1
            innerRef.current.style.transform = `translate(0px, 0px) scale(${sc})`
            resetTransform(s, { w: MOBILE_TERM_WIDTH_WIDE, h: outerH }, { scale: sc, tx: 0, ty: 0 })
          } else {
            // FIT mode: just update dimensions
            innerRef.current.style.width = `${outerW}px`
            innerRef.current.style.height = `${outerH}px`
            termDimsRef.current = { w: outerW, h: outerH }
          }
        }

        try {
          fit.fit()
          // Only auto-resize tmux in mobile fit mode.
          // Desktop resize is triggered on pane focus or manual button
          // to avoid fighting with other clients (e.g. Kitty).
          if (!isDesktop && minScaleRef.current >= 1) {
            sendResize(term.cols, term.rows)
          }
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
      onClick={() => {
        onFocus()
        // After onFocus, ensure xterm gets keyboard focus
        const term = termRef.current
        if (term && isDesktop) {
          term.focus()
          const ta = innerRef.current?.querySelector('textarea')
          console.debug('[input] pane clicked — textarea active:', document.activeElement === ta, 'WS connected:', connected)
        }
      }}
    >
      {isDesktop && (
        <PaneHeader
          target={pane.target}
          meta={meta}
          connected={connected}
          onClose={onClose}
          onResize={() => {
            const term = termRef.current
            const fit = fitAddonRef.current
            if (term && fit) {
              try {
                fit.fit()
                sendResize(term.cols, term.rows)
              } catch { /* */ }
            }
          }}
        />
      )}
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
          wideMode={wideMode}
          onToggleWide={() => {
            const next = !wideMode
            setWideMode(next)
            applyMobileSize(next)
          }}
        />
      )}
    </div>
  )
}
