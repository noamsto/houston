import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, render, screen } from '@testing-library/react'
import { TerminalPane } from './TerminalPane'

const sendInput = vi.fn()
const sendResize = vi.fn()

vi.mock('../hooks/usePaneSocket', () => ({
  usePaneSocket: () => ({ connected: false, sendInput, sendResize }),
}))

let desktop = true
vi.mock('../hooks/useMediaQuery', () => ({
  useIsDesktop: () => desktop,
}))

const pane = { id: 'pane-1', target: 'sess:0.0' }

afterEach(() => {
  cleanup()
  vi.clearAllMocks()
})

describe('TerminalPane readOnly', () => {
  it('renders PaneHeader on desktop by default', () => {
    desktop = true
    render(<TerminalPane pane={pane} isFocused onFocus={() => {}} onClose={() => {}} />)
    expect(screen.getByText(pane.target)).toBeTruthy()
  })

  it('suppresses PaneHeader on desktop when readOnly', () => {
    desktop = true
    render(<TerminalPane pane={pane} isFocused onFocus={() => {}} onClose={() => {}} readOnly />)
    expect(screen.queryByText(pane.target)).toBeNull()
  })

  it('renders MobileInputBar on mobile by default', () => {
    desktop = false
    render(<TerminalPane pane={pane} isFocused onFocus={() => {}} onClose={() => {}} />)
    expect(screen.getByPlaceholderText('Send a message...')).toBeTruthy()
  })

  it('suppresses MobileInputBar on mobile when readOnly', () => {
    desktop = false
    render(<TerminalPane pane={pane} isFocused onFocus={() => {}} onClose={() => {}} readOnly />)
    expect(screen.queryByPlaceholderText('Send a message...')).toBeNull()
  })
})
