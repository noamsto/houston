import { useEffect, useRef, useState } from 'react'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import { UnicodeGraphemesAddon } from '@xterm/addon-unicode-graphemes'
import { WebLinksAddon } from '@xterm/addon-web-links'
import '@xterm/xterm/css/xterm.css'
import type { WSMeta } from '../api/types'
import { useTerminalFontSize, type PaneInstance } from '../hooks/useLayout'
import { usePaneSocket } from '../hooks/usePaneSocket'
import { useIsDesktop } from '../hooks/useMediaQuery'
import { computeFitFontSize, computeFollowCursorTranslateX, useTouchGestures } from '../hooks/useTouchGestures'
import { darkTheme, lightTheme } from '../lib/xterm'
import { PaneHeader } from './PaneHeader'
import { MobileInputBar } from './MobileInputBar'
import { ColumnScrubber } from './ColumnScrubber'

interface Props {
  pane: PaneInstance
  isFocused: boolean
  onFocus: () => void
  onClose: () => void
  readOnly?: boolean
  onConnectionChange?: (connected: boolean) => void
}

/** Write a capture-pane snapshot into xterm.js.
 *  Clears scrollback + viewport via escape sequences (NOT term.reset() which
 *  would destroy terminal modes that the program set up before we connected).
 *  Content flows sequentially — natural scrolling moves earlier lines into
 *  the scrollback buffer, leaving the last term.rows lines visible. */
function writeSnapshot(term: Terminal, data: string, onDone?: () => void) {
  term.options.convertEol = true

  const lines = data.split('\n')
  if (lines[lines.length - 1] === '') lines.pop()

  // \x1b[?25l — hide cursor (program output will restore it)
  // \x1b[2J   — clear viewport (doesn't affect terminal modes)
  // \x1b[H    — cursor to (1,1)
  // NOTE: \x1b[3J (clear scrollback) intentionally omitted — it shifts the
  // viewport offset in xterm.js, causing content to render one line below.
  // Old scrollback is cleaned up by term.clear() on pane target switch.
  term.write('\x1b[?25l\x1b[2J\x1b[H' + lines.join('\n'), () => {
    term.options.convertEol = false
    onDone?.()
  })
}

// Mobile: wide terminal (~120 columns) with pinch-to-zoom and pan gestures
const MOBILE_TERM_WIDTH = 960
const PAD = 6

export function TerminalPane({ pane, isFocused, onFocus, onClose, readOnly = false, onConnectionChange }: Props) {
  // outerRef: observed by ResizeObserver; has padding that creates visual breathing room
  const outerRef = useRef<HTMLDivElement>(null)
  // innerRef: xterm.js is opened here so FitAddon measures the padded inner area
  const innerRef = useRef<HTMLDivElement>(null)
  const termRef = useRef<Terminal | null>(null)
  const fitAddonRef = useRef<FitAddon | null>(null)
  const [meta, setMeta] = useState<WSMeta | null>(null)
  const isDesktop = useIsDesktop()
  const [termMounted, setTermMounted] = useState(false)
  const [isScrolledUp, setIsScrolledUp] = useState(false)

  // Track browser zoom via devicePixelRatio — skip refit on zoom to preserve columns
  const dprRef = useRef(window.devicePixelRatio)
  const fittedWidthRef = useRef(0)

  const [fontSize, setFontSize] = useTerminalFontSize()

  // 'readable' = the persisted fontSize above; 'fit' = fit-width, computed
  // fresh each time double-tap toggles into it (not persisted).
  const zoomModeRef = useRef<'readable' | 'fit'>('readable')
  // Forward-declared: useTouchGestures needs a stable double-tap callback at
  // call time, but the callback's body needs values the hook itself returns
  // (termDimsRef, applyFontSize, ...). The ref is filled in after the call.
  const handleDoubleTapRef = useRef<() => void>(() => {})

  const {
    scaleRef,
    minScaleRef,
    termDimsRef,
    translateXRef,
    gestureActiveRef,
    resetTransform,
    clampPan,
    applyTransform,
    easeTranslateXTo,
    applyFontSize,
  } = useTouchGestures(
    innerRef, outerRef, termRef, !isDesktop && termMounted, setFontSize,
    () => handleDoubleTapRef.current(),
  )

  // Coarse horizontal position for the mobile column scrubber — polled at
  // animation-frame rate below since translateX/scale live in refs mutated
  // outside React's render cycle (touch handlers, eased pans).
  const [scrubberState, setScrubberState] = useState({ termWidthPx: 0, viewportWidthPx: 0, translateX: 0, scale: 1 })

  useEffect(() => {
    if (isDesktop || !termMounted) return
    let raf = 0
    const tick = () => {
      const outer = outerRef.current
      if (outer) {
        const viewportWidthPx = outer.clientWidth - PAD * 2
        const translateX = translateXRef.current
        const scale = scaleRef.current
        setScrubberState((prev) =>
          prev.termWidthPx === termDimsRef.current.w &&
          prev.viewportWidthPx === viewportWidthPx &&
          prev.translateX === translateX &&
          prev.scale === scale
            ? prev
            : { termWidthPx: termDimsRef.current.w, viewportWidthPx, translateX, scale },
        )
      }
      raf = requestAnimationFrame(tick)
    }
    raf = requestAnimationFrame(tick)
    return () => cancelAnimationFrame(raf)
  }, [isDesktop, termMounted, termDimsRef, translateXRef, scaleRef])

  // Distinguishes the very first `seed` after connecting (normal snapshot,
  // no pan reset) from a mid-session `seed` (scrollback just got cleared by
  // writeSnapshot, so any prior pan offset points at content that's gone).
  const firstSeedDoneRef = useRef(false)

  const followCursor = (term: Terminal) => {
    if (isDesktop || term.cols <= 0) return
    const outer = outerRef.current
    if (!outer) return
    const cellWidthPx = (termDimsRef.current.w / term.cols) * scaleRef.current
    const viewportWidthPx = outer.clientWidth - PAD * 2
    const target = computeFollowCursorTranslateX(
      term.buffer.active.cursorX,
      cellWidthPx,
      viewportWidthPx,
      translateXRef.current,
    )
    if (target !== null) easeTranslateXTo(target)
  }

  // After a seed lands: a mid-session reseed clears pan (old content is
  // simply gone — nothing to keep centered on), then follow the fresh cursor
  // unless the user's mid-gesture.
  const afterSeedWritten = (term: Terminal, isReseed: boolean) => {
    if (isReseed) {
      translateXRef.current = 0
      applyTransform()
    }
    if (!gestureActiveRef.current) followCursor(term)
  }

  // Double-tap: toggle between "readable" (the persisted font size, column 0)
  // and "fit-width" (computed fresh so the pane's full column count fills
  // the viewport, floor-clamped at 9px). fontSize/setFontSize is the
  // persisted "readable" preference — fit-width bypasses it entirely so
  // toggling back never sees a fit-computed value permanently overwrite it.
  useEffect(() => {
    handleDoubleTapRef.current = () => {
      const term = termRef.current
      const outer = outerRef.current
      if (!term || !outer) return
      if (zoomModeRef.current === 'fit') {
        applyFontSize(term, fontSize)
        zoomModeRef.current = 'readable'
      } else {
        const viewportWidthPx = outer.clientWidth - PAD * 2
        const fitSize = computeFitFontSize(term.options.fontSize ?? fontSize, termDimsRef.current.w, viewportWidthPx)
        applyFontSize(term, fitSize)
        zoomModeRef.current = 'fit'
      }
      clampPan()
      easeTranslateXTo(0)
    }
  })

  // Buffer %output writes and flush once per animation frame to prevent
  // visual tearing from mid-update renders (e.g. erase + redraw arriving
  // as separate WS messages within the same frame).
  const outputBufRef = useRef('')
  const rafIdRef = useRef(0)

  // Cache last seed so we can replay it when xterm remounts (e.g. isDesktop changes)
  // without waiting for the server to send a fresh seed.
  const lastSeedRef = useRef<string | null>(null)

  // When server sends pane dimensions, lock xterm.js to those dims.
  // FitAddon must not override — CC %output uses absolute cursor positions
  // based on the real pane size (controlled by kitty).
  const paneDimsRef = useRef<{ cols: number; rows: number } | null>(null)
  // Buffer seed until dims arrive — writing seed at wrong dimensions causes garbling
  const pendingSeedRef = useRef<string | null>(null)
  // First-seed/reseed distinction for the buffered seed above, captured at
  // receipt time (see onSeed) since that's what reflects "is this the first
  // seed for this connection" — not whenever the buffered write lands.
  const pendingSeedIsReseedRef = useRef(false)

  const applyDimsAndSeed = (term: Terminal, cols: number, rows: number) => {
    paneDimsRef.current = { cols, rows }
    term.resize(cols, rows)
    const inner = innerRef.current
    const outer = outerRef.current
    if (inner) {
      requestAnimationFrame(() => {
        const screen = inner.querySelector('.xterm-screen') as HTMLElement | null
        if (!screen) return
        const screenW = screen.offsetWidth
        const screenH = screen.offsetHeight
        inner.style.right = 'auto'
        inner.style.bottom = 'auto'

        if (!isDesktop && outer) {
          // Mobile: start zoomed to 1x at bottom-left (readable text, latest output visible).
          // User can pinch-zoom out to see the full terminal.
          const outerW = outer.clientWidth - PAD * 2
          const outerH = outer.clientHeight - PAD * 2
          const minS = outerW / screenW
          const initScale = Math.max(1.0, minS)
          const visH = screenH * initScale
          const ty = Math.min(0, outerH - visH)
          inner.style.width = `${screenW}px`
          inner.style.height = `${screenH}px`
          inner.style.transform = `translate(0px, ${ty}px) scale(${initScale})`
          resetTransform(minS, { w: screenW, h: screenH }, { scale: initScale, tx: 0, ty })
        } else {
          // Desktop: just size inner to match terminal
          inner.style.width = `${screenW}px`
          inner.style.height = `${screenH}px`
        }
      })
    }
    // Flush buffered seed now that dims are applied
    if (pendingSeedRef.current) {
      const data = pendingSeedRef.current
      const isReseed = pendingSeedIsReseedRef.current
      pendingSeedRef.current = null
      writeSnapshot(term, data, () => afterSeedWritten(term, isReseed))
    }
  }

  const { connected, sendInput, sendResize } = usePaneSocket(pane.target, {
    onDims: ({ cols, rows }) => {
      const term = termRef.current
      if (!term) return
      applyDimsAndSeed(term, cols, rows)
    },
    onSeed: (data) => {
      lastSeedRef.current = data
      const isReseed = firstSeedDoneRef.current
      firstSeedDoneRef.current = true
      const term = termRef.current
      if (!term) return
      // If dims haven't arrived yet, buffer the seed
      if (!paneDimsRef.current) {
        pendingSeedRef.current = data
        pendingSeedIsReseedRef.current = isReseed
        return
      }
      writeSnapshot(term, data, () => afterSeedWritten(term, isReseed))
    },
    onReseed: (data) => {
      lastSeedRef.current = data
      const term = termRef.current
      if (!term) return
      // Post-resize reseed: write data directly (contains its own \033[2J\033[H).
      // Don't clear scrollback — preserve scroll history.
      term.write(data + '\x1b[?25l\x1b[?1007l\x1b[?1l')
    },
    onOutput: (data) => {
      const term = termRef.current
      if (!term) return
      outputBufRef.current += data
      if (!rafIdRef.current) {
        rafIdRef.current = requestAnimationFrame(() => {
          rafIdRef.current = 0
          const buf = outputBufRef.current
          outputBufRef.current = ''
          term.write(buf)
          if (!gestureActiveRef.current) followCursor(term)
        })
      }
    },
    onMeta: (m) => setMeta(m),
  })

  useEffect(() => {
    onConnectionChange?.(connected)
  }, [connected, onConnectionChange])

  // Focus the xterm textarea when this pane becomes the active one (desktop only).
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

  // Mount xterm.js — remount when target or desktop mode changes
  useEffect(() => {
    if (!innerRef.current || !outerRef.current) return

    const isDark = !document.documentElement.classList.contains('light')
    const term = new Terminal({
      theme: isDark ? darkTheme : lightTheme,
      fontFamily: "'JetBrains Mono', 'Fira Code', 'Cascadia Code', monospace",
      fontSize,
      lineHeight: 1.2,
      cursorBlink: false,
      allowProposedApi: true,
      disableStdin: readOnly || !isDesktop,
      // convertEol is OFF: %output from CC mode delivers raw terminal data
      // with proper \r\n. Adding implicit \r to bare \n breaks TUI cursor
      // positioning. The seed data uses \r\n from the backend.
      convertEol: false,
      // 500 lines matches the tmux capture depth so the user can scroll through history.
      // writeSnapshot clears scrollback on each seed so it always reflects the latest state.
      scrollback: 500,
      // Tell FitAddon not to reserve 14px for the overview ruler (we don't use it).
      // Without this, FitAddon calculates fewer columns than actually fit.
      overviewRuler: { width: 0 },
    })

    const fitAddon = new FitAddon()
    term.loadAddon(fitAddon)
    term.loadAddon(new UnicodeGraphemesAddon())
    term.unicode.activeVersion = '15'
    term.loadAddon(new WebLinksAddon())

    // Mobile: wide terminal with pinch-to-zoom and pan.
    // Start at 1x bottom-left; real dims from server will override shortly.
    if (!isDesktop) {
      const outerW = outerRef.current.clientWidth - PAD * 2
      const outerH = outerRef.current.clientHeight - PAD * 2
      const minS = outerW / MOBILE_TERM_WIDTH
      const initScale = Math.max(1.0, minS)
      const visH = outerH * initScale
      const ty = Math.min(0, outerH - visH)
      innerRef.current.style.width = `${MOBILE_TERM_WIDTH}px`
      innerRef.current.style.height = `${outerH}px`
      innerRef.current.style.transform = `translate(0px, ${ty}px) scale(${initScale})`
      resetTransform(minS, { w: MOBILE_TERM_WIDTH, h: outerH }, { scale: initScale, tx: 0, ty })
    }

    term.open(innerRef.current)

    termRef.current = term
    fitAddonRef.current = fitAddon
    console.debug('[input] xterm mounted — isDesktop:', isDesktop, 'disableStdin:', term.options.disableStdin, 'target:', pane.target)

    // Defer initial fit so the DOM has its final layout before measuring.
    // Replay cached seed — when isDesktop changes, xterm remounts but the
    // WS server won't re-send a seed if the pane hasn't changed.
    requestAnimationFrame(() => {
      // Only fit to container if server hasn't sent pane dims yet.
      // Once dims arrive, xterm.js is locked to the real pane size.
      if (!paneDimsRef.current) {
        fitAddon.fit()
      }
      console.debug('[resize] initial mount — cols:', term.cols, 'rows:', term.rows, 'innerW:', innerRef.current?.clientWidth, 'outerW:', outerRef.current?.clientWidth)
      sendResize(term.cols, term.rows)
      if (isDesktop && innerRef.current) {
        fittedWidthRef.current = innerRef.current.clientWidth
      }
      if (lastSeedRef.current && paneDimsRef.current) {
        writeSnapshot(term, lastSeedRef.current)
      }
      if (isDesktop) term.focus()
      setTermMounted(true)
    })

    // Track scroll position to show/hide "scroll to bottom" button
    term.onScroll(() => {
      const buf = term.buffer.active
      setIsScrolledUp(buf.viewportY < buf.baseY)
    })

    if (isDesktop && !readOnly) {
      // Ensure xterm.js handles all keys (including Escape) instead of
      // letting the browser consume them.
      term.attachCustomKeyEventHandler(() => true)
      term.onData((data) => {
        console.debug('[input] xterm onData:', JSON.stringify(data))
        sendInput(data)
      })
    } else {
      console.debug('[input] skipped onData registration (mobile mode)')
    }

    return () => {
      if (rafIdRef.current) cancelAnimationFrame(rafIdRef.current)
      rafIdRef.current = 0
      outputBufRef.current = ''
      term.dispose()
      termRef.current = null
      fitAddonRef.current = null
      setTermMounted(false)
    }
  }, [isDesktop]) // eslint-disable-line react-hooks/exhaustive-deps

  // Reset state when switching pane targets — clear terminal so old content
  // doesn't linger as "ghost text" while waiting for the new seed.
  useEffect(() => {
    lastSeedRef.current = null
    paneDimsRef.current = null
    pendingSeedRef.current = null
    firstSeedDoneRef.current = false
    const term = termRef.current
    if (term) {
      term.clear()
    }
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

        // Desktop: detect browser zoom (DPR change) vs real window resize
        if (isDesktop) {
          const currentDpr = window.devicePixelRatio
          if (currentDpr !== dprRef.current) {
            dprRef.current = currentDpr
            // Zoomed in — lock dimensions and skip refit (scroll instead of wrap)
            const containerW = container.clientWidth - PAD * 2
            if (containerW < fittedWidthRef.current && innerRef.current) {
              innerRef.current.style.minWidth = `${fittedWidthRef.current}px`
              innerRef.current.style.minHeight = `${innerRef.current.clientHeight}px`
              return
            }
            // Zoomed out — clear locks and fall through to refit
          }
        }

        // Mobile: update position/height when container resizes
        // (e.g. keyboard opens/closes, quick buttons expand/collapse)
        if (!isDesktop && innerRef.current) {
          const outerH = container.clientHeight - PAD * 2
          const curScale = innerRef.current.style.transform.match(/scale\(([\d.]+)\)/)
          const sc = curScale ? parseFloat(curScale[1]) : 1
          const tx = translateXRef.current
          const s = minScaleRef.current

          if (paneDimsRef.current) {
            // Server controls terminal size — keep dims, auto-scroll to show bottom
            const visH = termDimsRef.current.h * sc
            const ty = Math.min(0, outerH - visH)
            innerRef.current.style.transform = `translate(${tx}px, ${ty}px) scale(${sc})`
            resetTransform(s, termDimsRef.current, { scale: sc, tx, ty })
          } else {
            // FitAddon controls — resize inner div to match container
            innerRef.current.style.height = `${outerH}px`
            termDimsRef.current = { ...termDimsRef.current, h: outerH }
            innerRef.current.style.transform = `translate(${tx}px, 0px) scale(${sc})`
            resetTransform(s, { w: termDimsRef.current.w, h: outerH }, { scale: sc, tx, ty: 0 })
          }
        }

        // Skip fit() if server controls terminal size via dims
        if (!paneDimsRef.current) {
          try {
            fit.fit()
            sendResize(term.cols, term.rows)
            if (isDesktop && innerRef.current) {
              fittedWidthRef.current = innerRef.current.clientWidth
              // Clear zoom locks — only set when zoom-in is detected
              innerRef.current.style.minWidth = ''
              innerRef.current.style.minHeight = ''
            }
          } catch {
            // fit() can throw if the container is hidden or has zero size
          }
        }
      }, 150)
    })

    ro.observe(container)
    return () => {
      clearTimeout(debounceTimer)
      ro.disconnect()
    }
  }, [sendResize, isDesktop, minScaleRef, resetTransform, termDimsRef, translateXRef])

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
      {isDesktop && !readOnly && (
        <PaneHeader
          target={pane.target}
          meta={meta}
          connected={connected}
          onClose={onClose}
          onResize={() => {
            if (paneDimsRef.current) return
            const term = termRef.current
            const fit = fitAddonRef.current
            const inner = innerRef.current
            if (term && fit) {
              if (inner) {
                inner.style.minWidth = ''
                inner.style.minHeight = ''
              }
              try {
                fit.fit()
                sendResize(term.cols, term.rows)
                if (inner) fittedWidthRef.current = inner.clientWidth
              } catch { /* */ }
            }
          }}
        />
      )}
      {/* Outer div: ResizeObserver target; has padding that creates visual breathing room */}
      <div
        ref={outerRef}
        style={{
          flex: 1,
          // Mobile: hidden — scrolling is via touch gestures (pan/zoom + scrollLines),
          // not native scroll. overflow:auto would create a scroll container for the
          // 960px inner div, causing a horizontal scrollbar that eats into height.
          // Desktop: auto — needed when terminal is zoom-locked to a wider width.
          overflow: isDesktop ? 'auto' : 'hidden',
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
            ...(isDesktop
              ? { right: 6, bottom: 6 }
              : { transformOrigin: '0 0' }),
          }}
        />
        {isScrolledUp && (
          <button
            onClick={() => {
              termRef.current?.scrollToBottom()
              setIsScrolledUp(false)
            }}
            style={{
              position: 'absolute',
              bottom: 12,
              right: 12,
              background: 'var(--bg-surface)',
              border: '1px solid var(--border)',
              borderRadius: '50%',
              width: 36,
              height: 36,
              display: 'flex',
              alignItems: 'center',
              justifyContent: 'center',
              cursor: 'pointer',
              color: 'var(--text-secondary)',
              fontSize: 18,
              boxShadow: '0 2px 8px rgba(0,0,0,0.3)',
              zIndex: 10,
            }}
            title="Scroll to bottom"
          >
            ↓
          </button>
        )}
      </div>
      {!isDesktop && (
        <ColumnScrubber
          termWidthPx={scrubberState.termWidthPx}
          viewportWidthPx={scrubberState.viewportWidthPx}
          translateX={scrubberState.translateX}
          scale={scrubberState.scale}
          onScrub={(tx) => {
            translateXRef.current = tx
            applyTransform()
          }}
        />
      )}
      {!isDesktop && !readOnly && (
        <MobileInputBar
          target={pane.target}
          choices={meta?.choices}
          inputText={meta?.input_text}
        />
      )}
    </div>
  )
}
