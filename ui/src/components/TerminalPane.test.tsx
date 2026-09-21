import { afterEach, describe, expect, it, vi } from 'vitest'
import { act, cleanup, fireEvent, render, screen } from '@testing-library/react'
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
  translateYRef: { current: 0 },
  gestureActiveRef: { current: false },
  resetTransform: vi.fn(),
  clampPan: vi.fn(),
  applyTransform: vi.fn(),
  easeTranslateXTo: vi.fn(),
  applyFontSize: vi.fn(),
}

// The args TerminalPane passed to the hook on its latest render, so a test can
// invoke its onDragEnd callback (index 6) as the real gesture handler would.
let touchGestureArgs: unknown[] = []

vi.mock('../hooks/useTouchGestures', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../hooks/useTouchGestures')>()
  return {
    ...actual,
    useTouchGestures: (...args: unknown[]) => {
      touchGestureArgs = args
      return touchGesturesMock
    },
  }
})

const target = 'sess:0.0'
const address: TerminalAddress = { kind: 'pane', target }

afterEach(async () => {
  cleanup()
  vi.clearAllMocks()
  capturedCallbacks = null
  touchGesturesMock.scaleRef.current = 1
  touchGesturesMock.translateXRef.current = 0
  touchGesturesMock.translateYRef.current = 0
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

describe('TerminalPane detached mode', () => {
  type DragInfo = { movedX: boolean; startedAtBottom: boolean }
  const dragEnd = (info: DragInfo) =>
    act(() => {
      ;(touchGestureArgs[6] as (i: DragInfo) => void)(info)
    })
  const nextFrame = () =>
    act(async () => {
      await new Promise((resolve) => requestAnimationFrame(resolve))
    })
  const livePill = () => screen.queryByText('↓ Live')

  async function mountMobile() {
    desktop = false
    touchGesturesMock.termDimsRef.current = { w: 800, h: 400 }
    const { container, rerender } = render(<TerminalPane address={address} isFocused onFocus={() => {}} onClose={() => {}} />)
    const rerenderTarget = (target: string) =>
      rerender(<TerminalPane address={{ kind: 'pane', target }} isFocused onFocus={() => {}} onClose={() => {}} />)
    const outer = container.querySelector('.xterm')!.parentElement!.parentElement as HTMLElement
    Object.defineProperty(outer, 'clientWidth', { value: 400, configurable: true })
    Object.defineProperty(outer, 'clientHeight', { value: 300, configurable: true })
    const callbacks = capturedCallbacks!
    act(() => {
      callbacks.onDims({ cols: 80, rows: 24 })
    })
    return { callbacks, term: await lastTerminalInstance(), container, rerender, rerenderTarget }
  }

  // happy-dom has no layout, so xterm's viewport never actually scrolls; report
  // the scroll position directly instead.
  const stubScroll = (term: Terminal, viewportY: number, baseY: number) =>
    vi.spyOn(term.buffer, 'active', 'get').mockReturnValue({ viewportY, baseY, cursorX: 0 } as Terminal['buffer']['active'])
  const settle = () =>
    act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 0))
    })

  it('shows the Live pill after a drag that panned horizontally', async () => {
    await mountMobile()
    expect(livePill()).toBeNull()

    dragEnd({ movedX: true, startedAtBottom: true })
    expect(livePill()).not.toBeNull()
  })

  it('stops following output while detached, and follows while attached', async () => {
    const { callbacks } = await mountMobile()

    act(() => {
      callbacks.onOutput('x')
    })
    await nextFrame()
    expect(touchGesturesMock.easeTranslateXTo).toHaveBeenCalled()

    touchGesturesMock.easeTranslateXTo.mockClear()
    dragEnd({ movedX: true, startedAtBottom: true })
    act(() => {
      callbacks.onOutput('x')
    })
    await nextFrame()
    expect(touchGesturesMock.easeTranslateXTo).not.toHaveBeenCalled()
  })

  it('keeps pan through a reconnect (same dims, then reseed) while detached', async () => {
    const { callbacks } = await mountMobile()
    act(() => {
      callbacks.onSeed('first seed\n')
    })
    await nextFrame()

    touchGesturesMock.translateXRef.current = -120
    touchGesturesMock.translateYRef.current = -30
    dragEnd({ movedX: true, startedAtBottom: true })
    touchGesturesMock.resetTransform.mockClear()
    touchGesturesMock.applyTransform.mockClear()

    // Reconnect: dims arrive again, unchanged, before the reseed.
    act(() => {
      callbacks.onDims({ cols: 80, rows: 24 })
    })
    await nextFrame()
    expect(touchGesturesMock.resetTransform).toHaveBeenLastCalledWith(
      expect.anything(),
      expect.anything(),
      expect.objectContaining({ tx: -120, ty: -30 }),
    )

    act(() => {
      callbacks.onSeed('second seed\n')
    })
    await settle()
    expect(touchGesturesMock.translateXRef.current).toBe(-120)
    expect(touchGesturesMock.applyTransform).toHaveBeenCalled()
    expect(touchGesturesMock.easeTranslateXTo).not.toHaveBeenCalled()
  })

  it('keeps the user scale and clamps the kept pan on a reconnect where the fit scale exceeds 1', async () => {
    const { callbacks, container } = await mountMobile()
    const xtermScreen = container.querySelector('.xterm-screen') as HTMLElement
    Object.defineProperty(xtermScreen, 'offsetWidth', { value: 200, configurable: true })
    Object.defineProperty(xtermScreen, 'offsetHeight', { value: 100, configurable: true })
    act(() => {
      callbacks.onSeed('first seed\n')
    })
    await nextFrame()

    touchGesturesMock.scaleRef.current = 2.5
    touchGesturesMock.translateXRef.current = -120
    touchGesturesMock.translateYRef.current = -30
    dragEnd({ movedX: true, startedAtBottom: true })
    touchGesturesMock.resetTransform.mockClear()
    touchGesturesMock.clampPan.mockClear()
    touchGesturesMock.applyTransform.mockClear()

    act(() => {
      callbacks.onDims({ cols: 80, rows: 24 })
    })
    await nextFrame()

    // Fit scale is (400 - 12) / 200 = 1.94, so the initial scale would be 1.94.
    expect(touchGesturesMock.resetTransform).toHaveBeenLastCalledWith(
      1.94,
      { w: 200, h: 100 },
      { scale: 2.5, tx: -120, ty: -30 },
    )
    const [resetOrder] = touchGesturesMock.resetTransform.mock.invocationCallOrder.slice(-1)
    const [clampOrder] = touchGesturesMock.clampPan.mock.invocationCallOrder.slice(-1)
    const [applyOrder] = touchGesturesMock.applyTransform.mock.invocationCallOrder.slice(-1)
    expect(resetOrder).toBeLessThan(clampOrder)
    expect(clampOrder).toBeLessThan(applyOrder)
  })

  it('restores the distance from the bottom across a reseed when detached and scrolled up', async () => {
    const { callbacks, term } = await mountMobile()
    act(() => {
      callbacks.onSeed('first seed\n')
    })
    await nextFrame()
    const buffer = stubScroll(term, 10, 77)
    dragEnd({ movedX: false, startedAtBottom: true })
    expect(livePill()).not.toBeNull()

    const scrollToLine = vi.spyOn(term, 'scrollToLine')
    act(() => {
      callbacks.onSeed('second seed\n')
    })
    // The rewritten buffer has more scrollback than the one captured above.
    buffer.mockReturnValue({ viewportY: 0, baseY: 80, cursorX: 0 } as Terminal['buffer']['active'])
    await settle()
    expect(scrollToLine).toHaveBeenCalledWith(13)
  })

  it('keeps pan through a reseed that arrives mid-gesture', async () => {
    const { callbacks } = await mountMobile()
    act(() => {
      callbacks.onSeed('first seed\n')
    })
    await nextFrame()

    touchGesturesMock.translateXRef.current = -120
    touchGesturesMock.translateYRef.current = -30
    touchGesturesMock.gestureActiveRef.current = true
    touchGesturesMock.resetTransform.mockClear()

    act(() => {
      callbacks.onDims({ cols: 80, rows: 24 })
    })
    await nextFrame()
    expect(touchGesturesMock.resetTransform).toHaveBeenLastCalledWith(
      expect.anything(),
      expect.anything(),
      expect.objectContaining({ tx: -120, ty: -30 }),
    )

    act(() => {
      callbacks.onSeed('second seed\n')
    })
    await settle()
    expect(touchGesturesMock.translateXRef.current).toBe(-120)
  })

  it('detaches when the column scrubber is used, and stops following output', async () => {
    Element.prototype.setPointerCapture ??= () => {}
    const { callbacks } = await mountMobile()
    // Let the scrubber's rAF poll pick up the terminal width.
    await nextFrame()
    await nextFrame()

    const track = screen.getByTestId('column-scrubber-thumb').parentElement!
    act(() => {
      fireEvent.pointerDown(track, { clientX: 100 })
    })
    expect(livePill()).not.toBeNull()

    touchGesturesMock.easeTranslateXTo.mockClear()
    act(() => {
      callbacks.onOutput('x')
    })
    await nextFrame()
    expect(touchGesturesMock.easeTranslateXTo).not.toHaveBeenCalled()
    expect(livePill()).not.toBeNull()
  })

  it('tapping Live scrolls to the bottom, hides the pill and follows the cursor again', async () => {
    const { term } = await mountMobile()
    const scrollToBottom = vi.spyOn(term, 'scrollToBottom')
    dragEnd({ movedX: true, startedAtBottom: true })
    touchGesturesMock.easeTranslateXTo.mockClear()

    act(() => {
      fireEvent.click(livePill()!)
    })

    expect(scrollToBottom).toHaveBeenCalled()
    expect(livePill()).toBeNull()
    expect(touchGesturesMock.easeTranslateXTo).toHaveBeenCalled()
  })

  it('tapping Live re-snaps the vertical pan to the bottom edge', async () => {
    await mountMobile()
    dragEnd({ movedX: true, startedAtBottom: true })
    // A pinch left ty somewhere other than bottom-aligned.
    touchGesturesMock.translateYRef.current = -30
    touchGesturesMock.applyTransform.mockClear()
    touchGesturesMock.clampPan.mockClear()

    act(() => {
      fireEvent.click(livePill()!)
    })

    // outerH = 300 - 12, content height 400 at scale 1.
    expect(touchGesturesMock.translateYRef.current).toBe(-112)
    expect(touchGesturesMock.clampPan).toHaveBeenCalled()
    expect(touchGesturesMock.applyTransform).toHaveBeenCalled()
  })

  it('re-attaches when a drag that began scrolled up ends at the bottom, but not when it ends scrolled up', async () => {
    const { term } = await mountMobile()
    dragEnd({ movedX: true, startedAtBottom: true })
    expect(livePill()).not.toBeNull()

    const buffer = stubScroll(term, 10, 77)
    dragEnd({ movedX: false, startedAtBottom: false })
    expect(livePill()).not.toBeNull()

    buffer.mockReturnValue({ viewportY: 77, baseY: 77, cursorX: 0 } as Terminal['buffer']['active'])
    dragEnd({ movedX: false, startedAtBottom: false })
    expect(livePill()).toBeNull()
  })

  it('re-attaches and re-snaps ty when a swipe with sideways drift scrolls back to the bottom', async () => {
    const { term } = await mountMobile()
    dragEnd({ movedX: true, startedAtBottom: true })
    expect(livePill()).not.toBeNull()

    stubScroll(term, 77, 77)
    touchGesturesMock.translateYRef.current = -30
    dragEnd({ movedX: true, startedAtBottom: false })

    expect(livePill()).toBeNull()
    expect(touchGesturesMock.translateYRef.current).toBe(-112)
  })

  it('does not re-attach on a swipe that both starts and ends at the bottom while pan-detached', async () => {
    const { term } = await mountMobile()
    dragEnd({ movedX: true, startedAtBottom: true })
    stubScroll(term, 77, 77)

    dragEnd({ movedX: false, startedAtBottom: true })

    expect(livePill()).not.toBeNull()
  })

  it('hides the pill and follows output again when the pane target changes while detached', async () => {
    const { rerenderTarget } = await mountMobile()
    dragEnd({ movedX: true, startedAtBottom: true })
    expect(livePill()).not.toBeNull()

    rerenderTarget('sess:1.0')
    expect(livePill()).toBeNull()

    touchGesturesMock.easeTranslateXTo.mockClear()
    act(() => {
      capturedCallbacks!.onOutput('x')
    })
    await nextFrame()
    expect(touchGesturesMock.easeTranslateXTo).toHaveBeenCalled()
  })

  it('does not resurrect the pill when switching A -> B -> A', async () => {
    const { rerenderTarget } = await mountMobile()
    dragEnd({ movedX: true, startedAtBottom: true })
    expect(livePill()).not.toBeNull()

    rerenderTarget('sess:1.0')
    rerenderTarget(target)
    expect(livePill()).toBeNull()
  })

  it('clears detached state when the desktop breakpoint flips', async () => {
    const { rerender } = await mountMobile()
    dragEnd({ movedX: true, startedAtBottom: true })
    expect(livePill()).not.toBeNull()

    desktop = true
    rerender(<TerminalPane address={address} isFocused onFocus={() => {}} onClose={() => {}} />)
    desktop = false
    rerender(<TerminalPane address={address} isFocused onFocus={() => {}} onClose={() => {}} />)
    expect(livePill()).toBeNull()

    touchGesturesMock.easeTranslateXTo.mockClear()
    act(() => {
      capturedCallbacks!.onOutput('x')
    })
    await nextFrame()
    expect(touchGesturesMock.easeTranslateXTo).toHaveBeenCalled()
  })

  it('never shows the Live pill on desktop', () => {
    desktop = true
    render(<TerminalPane address={address} isFocused onFocus={() => {}} onClose={() => {}} />)
    dragEnd({ movedX: true, startedAtBottom: true })
    expect(livePill()).toBeNull()
  })
})
