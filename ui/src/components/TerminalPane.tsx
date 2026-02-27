import { useEffect, useRef, useState } from 'react'
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

export function TerminalPane({ pane, isFocused, onFocus, onClose }: Props) {
  // outerRef: observed by ResizeObserver; has padding that creates visual breathing room
  const outerRef = useRef<HTMLDivElement>(null)
  // innerRef: xterm.js is opened here so FitAddon measures the padded inner area
  const innerRef = useRef<HTMLDivElement>(null)
  const termRef = useRef<Terminal | null>(null)
  const fitAddonRef = useRef<FitAddon | null>(null)
  const [meta, setMeta] = useState<WSMeta | null>(null)
  const isDesktop = useIsDesktop()

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

  // Mount xterm.js — remount when target or desktop mode changes
  useEffect(() => {
    if (!innerRef.current) return

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

    return () => {
      viewport?.removeEventListener('scroll', onViewportScroll)
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
      try {
        fit.fit()
        sendResize(term.cols, term.rows)
      } catch {
        // fit() can throw if the container is hidden or has zero size
      }
    })

    ro.observe(container)
    return () => ro.disconnect()
  }, [sendResize])

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
      <PaneHeader target={pane.target} meta={meta} onClose={onClose} />
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
        {/* Inner div: inset by 6px — xterm opens here; FitAddon measures this area */}
        <div
          ref={innerRef}
          style={{ position: 'absolute', top: 6, left: 6, right: 6, bottom: 6, touchAction: 'pan-y' }}
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
