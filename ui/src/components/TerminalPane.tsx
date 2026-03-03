import { useCallback, useEffect, useRef, useState } from 'react'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import { UnicodeGraphemesAddon } from '@xterm/addon-unicode-graphemes'
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

// Wide mode: ~120 columns for diffs and wide output. Fit mode: viewport width.
const MOBILE_TERM_WIDTH_WIDE = 960
const PAD = 6
// Rows to clip from bottom on mobile for claude-code input area
// (status line + mode line + prompt). The ──── separator stays visible.
const CLAUDE_INPUT_CLIP_ROWS = 3

export function TerminalPane({ pane, isFocused, onFocus, onClose }: Props) {
  // outerRef: observed by ResizeObserver; has padding that creates visual breathing room
  const outerRef = useRef<HTMLDivElement>(null)
  // innerRef: xterm.js is opened here so FitAddon measures the padded inner area
  const innerRef = useRef<HTMLDivElement>(null)
  const termRef = useRef<Terminal | null>(null)
  const fitAddonRef = useRef<FitAddon | null>(null)
  const [meta, setMeta] = useState<WSMeta | null>(null)
  const isDesktop = useIsDesktop()
  // Mobile: wide by default; Desktop: fit by default
  const [wideMode, setWideMode] = useState(
    () => !window.matchMedia('(min-width: 1024px)').matches,
  )
  const [termMounted, setTermMounted] = useState(false)
  // Terminal cell height in px — used to compute dynamic clip for mobile.
  // State (not ref) so the render picks up changes for clipPath/marginTop.
  const [cellHeight, setCellHeight] = useState(0)

  // Track browser zoom via devicePixelRatio — skip refit on zoom to preserve columns
  const dprRef = useRef(window.devicePixelRatio)
  const fittedWidthRef = useRef(0)

  const { minScaleRef, termDimsRef, translateXRef, resetTransform } = useTouchGestures(
    innerRef, outerRef, termRef, !isDesktop && termMounted,
  )

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
          // Mobile: apply wide/fit scaling after server dims arrive
          const outerW = outer.clientWidth - PAD * 2
          inner.style.width = `${screenW}px`
          inner.style.height = `${screenH}px`
          if (wideMode) {
            // Wide: natural size, user zooms/scrolls via gestures
            const minS = outerW / screenW
            inner.style.transform = 'translate(0px, 0px) scale(1)'
            resetTransform(minS, { w: screenW, h: screenH }, { scale: 1, tx: 0, ty: 0 })
          } else {
            // Fit: scale down to viewport width
            const scale = outerW / screenW
            inner.style.transform = `translate(0px, 0px) scale(${scale})`
            resetTransform(scale, { w: screenW, h: screenH }, { scale, tx: 0, ty: 0 })
          }
        } else {
          // Desktop: just size inner to match terminal
          inner.style.width = `${screenW}px`
          inner.style.height = `${screenH}px`
        }

        // Recompute cell height for mobile clip calculation
        if (term.rows > 0) {
          setCellHeight(screenH / term.rows)
        }
      })
    }
    // Flush buffered seed now that dims are applied
    if (pendingSeedRef.current) {
      writeSnapshot(term, pendingSeedRef.current)
      pendingSeedRef.current = null
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
      const term = termRef.current
      if (!term) return
      // If dims haven't arrived yet, buffer the seed
      if (!paneDimsRef.current) {
        pendingSeedRef.current = data
        return
      }
      writeSnapshot(term, data)
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
        })
      }
    },
    onMeta: (m) => setMeta(m),
  })

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

  // Recalculate mobile terminal dimensions without remounting xterm
  const applyMobileSize = useCallback((wide: boolean) => {
    const outer = outerRef.current
    const inner = innerRef.current
    const fit = fitAddonRef.current
    const term = termRef.current
    if (!outer || !inner || !fit || !term || isDesktop) return

    const outerW = outer.clientWidth - PAD * 2
    const outerH = outer.clientHeight - PAD * 2

    if (paneDimsRef.current) {
      // Server controls terminal dimensions — just change CSS transform
      const screen = inner.querySelector('.xterm-screen') as HTMLElement | null
      if (!screen) return
      const screenW = screen.offsetWidth
      const screenH = screen.offsetHeight
      inner.style.width = `${screenW}px`
      inner.style.height = `${screenH}px`
      if (wide) {
        // Wide: natural size, user zooms/scrolls via gestures
        const minS = outerW / screenW
        inner.style.transform = 'translate(0px, 0px) scale(1)'
        resetTransform(minS, { w: screenW, h: screenH }, { scale: 1, tx: 0, ty: 0 })
      } else {
        // Fit: scale down to viewport width
        const scale = outerW / screenW
        inner.style.transform = `translate(0px, 0px) scale(${scale})`
        resetTransform(scale, { w: screenW, h: screenH }, { scale, tx: 0, ty: 0 })
      }
      return
    }

    // No server dims — FitAddon controls terminal size
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
      sendResize(term.cols, term.rows)
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
    // Replay cached seed — when isDesktop changes, xterm remounts but the
    // WS server won't re-send a seed if the pane hasn't changed.
    requestAnimationFrame(() => {
      // Only fit to container if server hasn't sent pane dims yet.
      // Once dims arrive, xterm.js is locked to the real pane size.
      if (!paneDimsRef.current) {
        fitAddon.fit()
      }
      // Compute cell height from rendered terminal for dynamic clip calculation
      const screen = innerRef.current?.querySelector('.xterm-screen') as HTMLElement | null
      if (screen && term.rows > 0) {
        setCellHeight(screen.clientHeight / term.rows)
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
    }
  }, [isDesktop]) // eslint-disable-line react-hooks/exhaustive-deps

  // Refit terminal when desktop wide/fit mode toggles
  useEffect(() => {
    if (!isDesktop) return
    const inner = innerRef.current
    const fit = fitAddonRef.current
    const term = termRef.current
    if (!inner || !fit || !term) return
    requestAnimationFrame(() => {
      inner.style.minWidth = ''
      inner.style.minHeight = ''
      if (!paneDimsRef.current) {
        try {
          fit.fit()
          console.debug('[resize] wideMode toggle — cols:', term.cols, 'rows:', term.rows, 'innerW:', inner.clientWidth, 'innerH:', inner.clientHeight)
          sendResize(term.cols, term.rows)
          fittedWidthRef.current = inner.clientWidth
        } catch { /* fit can throw if zero-size */ }
      }
    })
  }, [wideMode, isDesktop, sendResize])

  // Reset state when switching pane targets — clear terminal so old content
  // doesn't linger as "ghost text" while waiting for the new seed.
  useEffect(() => {
    lastSeedRef.current = null
    paneDimsRef.current = null
    pendingSeedRef.current = null
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

        // Mobile: update terminal dimensions when container resizes
        // (e.g. keyboard opens/closes, quick buttons expand/collapse)
        if (!isDesktop && innerRef.current) {
          const outerW = container.clientWidth - PAD * 2
          const outerH = container.clientHeight - PAD * 2
          const s = minScaleRef.current
          if (s < 1) {
            // WIDE mode: update height to match viewport, keep current scale and pan
            innerRef.current.style.height = `${outerH}px`
            termDimsRef.current = { ...termDimsRef.current, h: outerH }
            const curScale = innerRef.current.style.transform.match(/scale\(([\d.]+)\)/)
            const sc = curScale ? parseFloat(curScale[1]) : 1
            const tx = translateXRef.current
            innerRef.current.style.transform = `translate(${tx}px, 0px) scale(${sc})`
            resetTransform(s, { w: MOBILE_TERM_WIDTH_WIDE, h: outerH }, { scale: sc, tx, ty: 0 })
          } else {
            // FIT mode: just update dimensions
            innerRef.current.style.width = `${outerW}px`
            innerRef.current.style.height = `${outerH}px`
            termDimsRef.current = { w: outerW, h: outerH }
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
      {isDesktop && (
        <PaneHeader
          target={pane.target}
          meta={meta}
          connected={connected}
          onClose={onClose}
          wideMode={wideMode}
          onToggleWide={() => setWideMode((w) => !w)}
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
          overflow: 'auto',
          minHeight: 0,
          position: 'relative',
          background: 'var(--bg-terminal)',
          // On mobile, clip claude-code's input area (status + mode + prompt).
          // Computed from actual cell height so it adapts to any font/zoom.
          // MobileInputBar uses negative margin to overlap the clipped zone.
          clipPath: !isDesktop && meta?.agent === 'claude-code' && cellHeight > 0
            ? `inset(0 0 ${Math.round(cellHeight * CLAUDE_INPUT_CLIP_ROWS)}px 0)`
            : undefined,
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
              ? wideMode
                ? { width: MOBILE_TERM_WIDTH_WIDE, bottom: 6 }
                : { right: 6, bottom: 6 }
              : { transformOrigin: '0 0' }),
          }}
        />
      </div>
      {!isDesktop && (
        <div style={{
          // Pull input bar up to overlap the clipped zone so there's no gap
          marginTop: meta?.agent === 'claude-code' && cellHeight > 0
            ? -Math.round(cellHeight * CLAUDE_INPUT_CLIP_ROWS)
            : undefined,
          position: 'relative',
          zIndex: 1,
        }}>
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
        </div>
      )}
    </div>
  )
}
