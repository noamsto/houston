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

export function TerminalPane({ pane, isFocused, onFocus, onClose }: Props) {
  const containerRef = useRef<HTMLDivElement>(null)
  const termRef = useRef<Terminal | null>(null)
  const fitAddonRef = useRef<FitAddon | null>(null)
  const [meta, setMeta] = useState<WSMeta | null>(null)
  const isDesktop = useIsDesktop()

  const { sendInput, sendResize } = usePaneSocket(pane.target, {
    onOutput: (data) => termRef.current?.write(data),
    onMeta: (m) => setMeta(m),
  })

  // Mount xterm.js — remount when target or desktop mode changes
  useEffect(() => {
    if (!containerRef.current) return

    const isDark = !document.documentElement.classList.contains('light')
    const term = new Terminal({
      theme: isDark ? darkTheme : lightTheme,
      fontFamily: "'JetBrains Mono', 'Fira Code', 'Cascadia Code', monospace",
      fontSize: 13,
      lineHeight: 1.2,
      cursorBlink: true,
      disableStdin: !isDesktop,
      scrollback: 5000,
    })

    const fitAddon = new FitAddon()
    term.loadAddon(fitAddon)
    term.loadAddon(new WebLinksAddon())
    term.open(containerRef.current)

    termRef.current = term
    fitAddonRef.current = fitAddon

    // Defer initial fit so the DOM has its final layout before measuring
    requestAnimationFrame(() => fitAddon.fit())

    if (isDesktop) {
      term.onData((data) => sendInput(data))
    }

    return () => {
      term.dispose()
      termRef.current = null
      fitAddonRef.current = null
    }
  }, [pane.target, isDesktop]) // eslint-disable-line react-hooks/exhaustive-deps

  // Resize observer — refit when container dimensions change
  useEffect(() => {
    const container = containerRef.current
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
      <div
        ref={containerRef}
        style={{ flex: 1, overflow: 'hidden', minHeight: 0, background: 'var(--bg-terminal)' }}
      />
      {!isDesktop && (
        <MobileInputBar
          target={pane.target}
          choices={meta?.choices}
        />
      )}
    </div>
  )
}
