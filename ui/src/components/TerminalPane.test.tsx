import { afterEach, describe, expect, it, vi } from 'vitest'
import { act, cleanup, render, screen } from '@testing-library/react'
import { TerminalPane } from './TerminalPane'
import type { TerminalAddress } from '../api/terminal'
import type { Terminal } from '@xterm/xterm'

// Subclass the real Terminal so other describes in this file (which rely on
// real xterm DOM/buffer behavior) keep working, while letting tests inspect
// the instance TerminalPane actually constructed (options, term.input(), ...).
vi.mock('@xterm/xterm', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@xterm/xterm')>()
  const instances: InstanceType<typeof actual.Terminal>[] = []
  class T extends actual.Terminal {
    constructor(o?: ConstructorParameters<typeof actual.Terminal>[0]) {
      super(o)
      instances.push(this)
    }
  }
  return { ...actual, Terminal: T, __instances: instances }
})

async function lastTerminalInstance(): Promise<Terminal> {
  const { __instances } = (await import('@xterm/xterm')) as unknown as { __instances: Terminal[] }
  return __instances[__instances.length - 1]
}

const sendInput = vi.fn()
const sendResize = vi.fn()

interface CapturedCallbacks {
  onDims: (dims: { cols: number; rows: number }) => void
  onSeed: (data: string) => void
  onReseed: (data: string) => void
  onOutput: (data: string) => void
  onMeta: (meta: unknown) => void
}

let capturedCallbacks: CapturedCallbacks | null = null

vi.mock('../hooks/usePaneSocket', () => ({
  usePaneSocket: (_path: string, callbacks: CapturedCallbacks) => {
    capturedCallbacks = callbacks
    return { connected: false, sendInput, sendResize }
  },
}))

let desktop = true
vi.mock('../hooks/useMediaQuery', () => ({
  useIsDesktop: () => desktop,
}))

// Real pinch/pan/font math (computeFitFontSize, computeFollowCursorTranslateX)
// stays real; only the hook's stateful refs are swapped for plain objects a
// test can poke directly — happy-dom has no layout engine, so driving these
// features through real touch events and real measured dims isn't feasible.
const touchGesturesMock = {
  scaleRef: { current: 1 },
  minScaleRef: { current: 1 },
  termDimsRef: { current: { w: 0, h: 0 } },
  translateXRef: { current: 0 },
  gestureActiveRef: { current: false },
  resetTransform: vi.fn(),
  clampPan: vi.fn(),
  applyTransform: vi.fn(),
  easeTranslateXTo: vi.fn(),
  applyFontSize: vi.fn(),
}

vi.mock('../hooks/useTouchGestures', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../hooks/useTouchGestures')>()
  return { ...actual, useTouchGestures: () => touchGesturesMock }
})

const target = 'sess:0.0'
const address: TerminalAddress = { kind: 'pane', target }

afterEach(async () => {
  cleanup()
  vi.clearAllMocks()
  capturedCallbacks = null
  touchGesturesMock.scaleRef.current = 1
  touchGesturesMock.translateXRef.current = 0
  touchGesturesMock.termDimsRef.current = { w: 0, h: 0 }
  touchGesturesMock.gestureActiveRef.current = false
  const { __instances } = (await import('@xterm/xterm')) as unknown as { __instances: Terminal[] }
  __instances.length = 0
})

describe('TerminalPane input', () => {
  it('renders PaneHeader on desktop by default', () => {
    desktop = true
    render(<TerminalPane address={address} isFocused onFocus={() => {}} onClose={() => {}} />)
    expect(screen.getByText(target)).toBeTruthy()
  })

  it('renders the composer on mobile', () => {
    desktop = false
    render(<TerminalPane address={address} isFocused onFocus={() => {}} onClose={() => {}} />)
    expect(screen.getByPlaceholderText('Send a message...')).toBeTruthy()
  })

  it('forwards desktop keystrokes to the pane socket', async () => {
    desktop = true
    render(<TerminalPane address={address} isFocused onFocus={() => {}} onClose={() => {}} />)
    const term = await lastTerminalInstance()
    expect(term.options.disableStdin).toBe(false)

    act(() => {
      term.input('a')
    })
    expect(sendInput).toHaveBeenCalledWith('a')
  })
})

describe('TerminalPane hideHeader', () => {
  it('suppresses PaneHeader on desktop when hideHeader is set, without affecting input', async () => {
    desktop = true
    render(<TerminalPane address={address} isFocused onFocus={() => {}} onClose={() => {}} hideHeader />)
    expect(screen.queryByText(target)).toBeNull()

    const term = await lastTerminalInstance()
    expect(term.options.disableStdin).toBe(false)

    act(() => {
      term.input('a')
    })
    expect(sendInput).toHaveBeenCalledWith('a')
  })

  it('does not affect composer visibility on mobile', () => {
    desktop = false
    render(<TerminalPane address={address} isFocused onFocus={() => {}} onClose={() => {}} hideHeader />)
    expect(screen.getByPlaceholderText('Send a message...')).toBeTruthy()
  })

  it('defaults to false — PaneHeader still renders on desktop when omitted', () => {
    desktop = true
    render(<TerminalPane address={address} isFocused onFocus={() => {}} onClose={() => {}} />)
    expect(screen.getByText(target)).toBeTruthy()
  })
})

describe('TerminalPane column scrubber', () => {
  it('renders on mobile', () => {
    desktop = false
    render(<TerminalPane address={address} isFocused onFocus={() => {}} onClose={() => {}} />)
    expect(screen.getByTestId('column-scrubber-thumb')).toBeTruthy()
  })

  it('does not render on desktop', () => {
    desktop = true
    render(<TerminalPane address={address} isFocused onFocus={() => {}} onClose={() => {}} />)
    expect(screen.queryByTestId('column-scrubber-thumb')).toBeNull()
  })
})

describe('TerminalPane mid-session reseed', () => {
  it('resets pan to column 0 on a reseed, but not on the first seed since connect', async () => {
    desktop = false
    render(<TerminalPane address={address} isFocused onFocus={() => {}} onClose={() => {}} />)
    const callbacks = capturedCallbacks!

    act(() => {
      callbacks.onDims({ cols: 80, rows: 24 })
    })

    // Simulate a prior pan away from column 0.
    touchGesturesMock.translateXRef.current = -120

    // First seed since connect — a normal snapshot, not a scrollback clear.
    act(() => {
      callbacks.onSeed('first seed\n')
    })
    expect(touchGesturesMock.translateXRef.current).toBe(-120)

    // A later seed on the same connection — scrollback just got cleared, so
    // the old pan offset points at content that's gone.
    act(() => {
      callbacks.onSeed('second seed\n')
    })
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 0))
    })
    expect(touchGesturesMock.translateXRef.current).toBe(0)
    expect(touchGesturesMock.applyTransform).toHaveBeenCalled()
  })
})

describe('TerminalPane address change on a mounted instance', () => {
  it('treats a fresh seed on a new address as first-seed, not a reseed', async () => {
    desktop = false
    const { rerender } = render(
      <TerminalPane address={{ kind: 'pane', target: 't1' }} isFocused onFocus={() => {}} onClose={() => {}} />,
    )

    act(() => {
      capturedCallbacks!.onDims({ cols: 80, rows: 24 })
    })
    act(() => {
      capturedCallbacks!.onSeed('first seed on t1\n')
    })

    // Simulate a pan away from column 0 on the first run's terminal.
    touchGesturesMock.translateXRef.current = -120

    // Same TerminalPane instance, new address identity — the `[key]`
    // effect should reset firstSeedDoneRef so the next seed isn't mistaken
    // for a mid-session reseed of run A's content.
    rerender(<TerminalPane address={{ kind: 'pane', target: 't2' }} isFocused onFocus={() => {}} onClose={() => {}} />)

    act(() => {
      capturedCallbacks!.onDims({ cols: 80, rows: 24 })
    })
    act(() => {
      capturedCallbacks!.onSeed('first seed on t2\n')
    })
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 0))
    })

    // A reseed would have forced this back to 0 — it doesn't, proving the
    // new target's seed was treated as genuinely first.
    expect(touchGesturesMock.translateXRef.current).toBe(-120)
  })
})

describe('TerminalPane follow-cursor auto-pan', () => {
  it('follows the cursor after output, but not while a touch gesture is active', async () => {
    desktop = false
    touchGesturesMock.termDimsRef.current = { w: 800, h: 400 }
    const { container } = render(<TerminalPane address={address} isFocused onFocus={() => {}} onClose={() => {}} />)
    const callbacks = capturedCallbacks!
    const outer = container.querySelector('.xterm')!.parentElement!.parentElement as HTMLElement
    Object.defineProperty(outer, 'clientWidth', { value: 400, configurable: true })

    act(() => {
      callbacks.onDims({ cols: 80, rows: 24 })
    })

    touchGesturesMock.gestureActiveRef.current = true
    act(() => {
      callbacks.onOutput('x')
    })
    await act(async () => {
      await new Promise((resolve) => requestAnimationFrame(resolve))
    })
    expect(touchGesturesMock.easeTranslateXTo).not.toHaveBeenCalled()

    touchGesturesMock.gestureActiveRef.current = false
    act(() => {
      callbacks.onOutput('x')
    })
    await act(async () => {
      await new Promise((resolve) => requestAnimationFrame(resolve))
    })
    expect(touchGesturesMock.easeTranslateXTo).toHaveBeenCalled()
  })
})
