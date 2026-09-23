import { useCallback, useEffect, useRef, useState } from 'react'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import { UnicodeGraphemesAddon } from '@xterm/addon-unicode-graphemes'
import { WebLinksAddon } from '@xterm/addon-web-links'
import '@xterm/xterm/css/xterm.css'
import type { WSMeta } from '../api/types'
import { terminalKey, terminalSocketPath, type TerminalAddress } from '../api/terminal'
import { useTerminalFontSize } from '../hooks/useLayout'
import { usePaneSocket } from '../hooks/usePaneSocket'
import { useIsDesktop } from '../hooks/useMediaQuery'
import {
  computeFitFontSize,
  computeFollowCursorTranslateX,
  decideDetach,
  useTouchGestures,
} from '../hooks/useTouchGestures'
import { darkTheme, lightTheme } from '../lib/xterm'
import { mobileFitScale } from '../lib/mobileFitScale'
import { desktopFillScale } from '../lib/desktopFillScale'
import { PaneHeader } from './PaneHeader'
import { MobileInputBar } from './MobileInputBar'
import { ColumnScrubber } from './ColumnScrubber'

interface Props {
  address: TerminalAddress
  isFocused: boolean
  onFocus: () => void
  onClose: () => void
  hideHeader?: boolean
  onConnectionChange?: (connected: boolean) => void
  onEnded?: (reason: string) => void
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

export function TerminalPane({ address, isFocused, onFocus, onClose, hideHeader = false, onConnectionChange, onEnded }: Props) {
  const key = terminalKey(address)
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

  // The scale mobileFitScale last auto-applied on mobile. If the live scale
  // still matches this, no pinch happened since — safe to re-fit on the next
  // resize. If it diverged (user pinched), leave their zoom alone; only
  // reposition (ty), don't fight their gesture.
  const autoFitScaleRef = useRef<number | null>(null)

  const [fontSize, setFontSize] = useTerminalFontSize()

  // 'readable' = the persisted fontSize above; 'fit' = fit-width, computed
  // fresh each time double-tap toggles into it (not persisted).
  const zoomModeRef = useRef<'readable' | 'fit'>('readable')
  // Forward-declared: useTouchGestures needs a stable double-tap callback at
  // call time, but the callback's body needs values the hook itself returns
  // (termDimsRef, applyFontSize, ...). The ref is filled in after the call.
  const handleDoubleTapRef = useRef<() => void>(() => {})

  // Detached: the user panned/scrolled away from the live follow position, so
  // output and reseeds must not yank the view back. The ref is what async
  // callbacks read; the state (keyed by target, dropped during render when the
  // pane switches) drives the "Live" pill.
  const detachedRef = useRef(false)
  const [detachedTarget, setDetachedTarget] = useState<string | null>(null)
  if (detachedTarget !== null && detachedTarget !== key) setDetachedTarget(null)
  const detached = detachedTarget === key
  const setDetached = (value: boolean) => {
    detachedRef.current = value
    setDetachedTarget(value ? key : null)
  }

  // Forward-declared like handleDoubleTapRef: the body needs reattach, which
  // needs values the hook itself returns.
  const handleDragEndRef = useRef<(info: { movedX: boolean; startedAtBottom: boolean }) => void>(() => {})

  const {
    scaleRef,
    minScaleRef,
    termDimsRef,
    translateXRef,
    translateYRef,
    gestureActiveRef,
    resetTransform,
    clampPan,
    applyTransform,
    easeTranslateXTo,
    applyFontSize,
  } = useTouchGestures(
    innerRef, outerRef, termRef, !isDesktop && termMounted, setFontSize,
    () => handleDoubleTapRef.current(),
    (info) => handleDragEndRef.current(info),
  )

  // Desktop only: fills the container via the same transform refs the mobile
  // path uses. Writing inner.style.transform directly here would get
  // overwritten by afterSeedWritten's unconditional applyTransform() on the
  // next reseed.
  const applyDesktopFill = useCallback(
    (screenW: number, screenH: number) => {
      const outer = outerRef.current
      const inner = innerRef.current
      if (!outer || !inner) return
      const outerW = outer.clientWidth - PAD * 2
      const outerH = outer.clientHeight - PAD * 2
      const scale = desktopFillScale(outerW, outerH, screenW, screenH)
      inner.style.transformOrigin = '0 0'
      resetTransform(scale, { w: screenW, h: screenH }, { scale, tx: 0, ty: 0 })
      applyTransform()
    },
    [resetTransform, applyTransform],
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

  // Back to the live follow position: bottom of the scrollback, bottom-aligned
  // vertical pan, cursor in view. Shared by the Live pill and a drag that
  // scrolls back down to the bottom edge.
  const reattach = (term: Terminal) => {
    term.scrollToBottom()
    setDetached(false)
    const outer = outerRef.current
    if (outer) {
      const outerH = outer.clientHeight - PAD * 2
      translateYRef.current = Math.min(0, outerH - termDimsRef.current.h * scaleRef.current)
      clampPan()
      applyTransform()
    }
    followCursor(term)
  }

  useEffect(() => {
    handleDragEndRef.current = ({ movedX, startedAtBottom }) => {
      const term = termRef.current
      if (!term) return
      const buf = term.buffer.active
      const decision = decideDetach({ movedX, atBottom: buf.viewportY >= buf.baseY, startedAtBottom })
      if (decision === 'attach') reattach(term)
      else if (decision === 'detach') setDetached(true)
    }
  })

  // The view is held still while detached or mid-gesture, so a reseed doesn't
  // yank it out from under the user's finger.
  const holdingView = () => detachedRef.current || gestureActiveRef.current

  // After a seed lands: a mid-session reseed clears pan (old content is
  // simply gone — nothing to keep centered on), then follow the fresh cursor
  // unless the user's mid-gesture. A user holding the view keeps their pan and,
  // if they were scrolled up, their distance from the bottom (keepDistFromBottom).
  const afterSeedWritten = (term: Terminal, isReseed: boolean, keepDistFromBottom: number | null = null) => {
    if (isReseed && holdingView()) {
      clampPan()
      applyTransform()
      if (keepDistFromBottom !== null) term.scrollToLine(Math.max(0, term.buffer.active.baseY - keepDistFromBottom))
      return
    }
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
    const prevDims = paneDimsRef.current
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
          const { floor: minS, scale: initScale } = mobileFitScale(outerW, outerH, screenW, screenH)
          const visH = screenH * initScale
          // A reconnect re-sends unchanged dims: a detached user keeps their pan.
          const keepPan = holdingView() && prevDims?.cols === cols && prevDims.rows === rows
          if (!keepPan) autoFitScaleRef.current = initScale
          const scale = keepPan ? scaleRef.current : initScale
          const tx = keepPan ? translateXRef.current : 0
          const ty = keepPan ? translateYRef.current : Math.min(0, outerH - visH)
          inner.style.width = `${screenW}px`
          inner.style.height = `${screenH}px`
          resetTransform(minS, { w: screenW, h: screenH }, { scale, tx, ty })
          if (keepPan) clampPan()
          applyTransform()
        } else {
          // Desktop: size inner to match terminal, then scale it to fill the
          // container via the shared transform refs.
          inner.style.width = `${screenW}px`
          inner.style.height = `${screenH}px`
          applyDesktopFill(screenW, screenH)
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

  const { connected, ended, sendInput, sendResize } = usePaneSocket(terminalSocketPath(address), {
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
      // writeSnapshot rewrites the buffer, so note how far from the bottom a
      // scrolled-up user was before it lands.
      const buf = term.buffer.active
      const keepDistFromBottom = holdingView() && buf.viewportY < buf.baseY ? buf.baseY - buf.viewportY : null
      writeSnapshot(term, data, () => afterSeedWritten(term, isReseed, keepDistFromBottom))
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
          if (!gestureActiveRef.current && !detachedRef.current) followCursor(term)
        })
      }
    },
    onMeta: (m) => setMeta(m),
  })

  useEffect(() => {
    onConnectionChange?.(connected)
  }, [connected, onConnectionChange])

  useEffect(() => {
    if (ended) onEnded?.(ended)
  }, [ended, onEnded])

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
      disableStdin: !isDesktop,
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
    } else {
      // Desktop: clear any transform/scale state carried over from mobile mode
      // (e.g. a breakpoint flip) until the next onDims/seed establishes a real
      // fill scale.
      if (innerRef.current) innerRef.current.style.transform = ''
      resetTransform(1, { w: 0, h: 0 }, { scale: 1, tx: 0, ty: 0 })
    }

    term.open(innerRef.current)

    termRef.current = term
    fitAddonRef.current = fitAddon
    console.debug('[input] xterm mounted — isDesktop:', isDesktop, 'disableStdin:', term.options.disableStdin, 'key:', key)

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

    if (isDesktop) {
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
      // A breakpoint flip must not carry a detached pan into a mode that
      // treats it differently (desktop holdingView() would hold reseeds).
      setDetached(false)
    }
  }, [isDesktop]) // eslint-disable-line react-hooks/exhaustive-deps

  // Reset state when switching terminal addresses — clear terminal so old
  // content doesn't linger as "ghost text" while waiting for the new seed.
  useEffect(() => {
    lastSeedRef.current = null
    paneDimsRef.current = null
    pendingSeedRef.current = null
    firstSeedDoneRef.current = false
    detachedRef.current = false
    const term = termRef.current
    if (term) {
      term.clear()
    }
    if (isDesktop && innerRef.current) {
      innerRef.current.style.transform = ''
      resetTransform(1, { w: 0, h: 0 }, { scale: 1, tx: 0, ty: 0 })
    }
  }, [key, isDesktop]) // eslint-disable-line react-hooks/exhaustive-deps

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
          if (paneDimsRef.current && termDimsRef.current.w > 0) {
            applyDesktopFill(termDimsRef.current.w, termDimsRef.current.h)
          }
        }

        // Mobile: update position/height when container resizes
        // (e.g. keyboard opens/closes, quick buttons expand/collapse).
        // Re-snapping ty to the bottom here is intentionally not detached-aware.
        if (!isDesktop && innerRef.current) {
          const outerH = container.clientHeight - PAD * 2
          const curScale = innerRef.current.style.transform.match(/scale\(([\d.]+)\)/)
          const sc = curScale ? parseFloat(curScale[1]) : 1
          const tx = translateXRef.current
          const s = minScaleRef.current

          if (paneDimsRef.current) {
            // mobileFitScale accounts for available height too, so a short
            // (landscape) viewport shrinks below width-fit instead of cropping
            // the top off-screen. Only apply its default while the user hasn't
            // pinched away from it (autoFitScaleRef) — otherwise keep their zoom.
            const outerW = container.clientWidth - PAD * 2
            const { floor, scale: fitScale } = mobileFitScale(outerW, outerH, termDimsRef.current.w, termDimsRef.current.h)
            const atAutoFit = autoFitScaleRef.current === null || Math.abs(sc - autoFitScaleRef.current) < 0.001
            const newSc = atAutoFit ? fitScale : sc
            if (atAutoFit) autoFitScaleRef.current = fitScale
            const visH = termDimsRef.current.h * newSc
            const ty = Math.min(0, outerH - visH)
            innerRef.current.style.transform = `translate(${tx}px, ${ty}px) scale(${newSc})`
            resetTransform(atAutoFit ? floor : s, termDimsRef.current, { scale: newSc, tx, ty })
            clampPan()
            applyTransform()
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
  }, [sendResize, isDesktop, minScaleRef, resetTransform, termDimsRef, translateXRef, applyTransform, clampPan, applyDesktopFill])

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
      {isDesktop && !hideHeader && (
        <PaneHeader
          target={address.kind === 'pane' ? address.target : address.id}
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
        {isScrolledUp && isDesktop && (
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
        {detached && !isDesktop && (
          <button
            onClick={() => {
              const term = termRef.current
              if (term) reattach(term)
            }}
            style={{
              position: 'absolute',
              bottom: 12,
              left: '50%',
              transform: 'translateX(-50%)',
              background: 'var(--bg-surface)',
              border: '1px solid var(--border)',
              borderRadius: 18,
              height: 36,
              padding: '0 14px',
              cursor: 'pointer',
              color: 'var(--text-secondary)',
              fontSize: 14,
              boxShadow: '0 2px 8px rgba(0,0,0,0.3)',
              zIndex: 10,
            }}
            title="Follow live output"
          >
            ↓ Live
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
            setDetached(true)
          }}
        />
      )}
      {!isDesktop && (
        <MobileInputBar
          key={key}
          address={address}
          choices={meta?.choices}
          inputText={meta?.input_text}
          agent={meta?.agent}
        />
      )}
    </div>
  )
}
