import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { trackHidden, watchLiveness } from './streamLiveness'

function setVisibility(state: DocumentVisibilityState) {
  Object.defineProperty(document, 'visibilityState', { value: state, configurable: true })
  document.dispatchEvent(new Event('visibilitychange'))
}

beforeEach(() => {
  vi.useFakeTimers()
})

afterEach(() => {
  setVisibility('visible')
  vi.useRealTimers()
})

describe('watchLiveness', () => {
  it('fires once after 60 s without seen(), not on every check', () => {
    const onStale = vi.fn()
    const w = watchLiveness(onStale)

    vi.advanceTimersByTime(60_000)
    expect(onStale).not.toHaveBeenCalled()
    vi.advanceTimersByTime(5_000)
    expect(onStale).toHaveBeenCalledTimes(1)
    vi.advanceTimersByTime(50_000)
    expect(onStale).toHaveBeenCalledTimes(1)
    w.stop()
  })

  it('does not fire while seen() keeps coming', () => {
    const onStale = vi.fn()
    const w = watchLiveness(onStale)

    for (let i = 0; i < 10; i++) {
      vi.advanceTimersByTime(25_000)
      w.seen()
    }
    expect(onStale).not.toHaveBeenCalled()
    w.stop()
  })

  it('does not fire from the interval while the page is hidden', () => {
    const onStale = vi.fn()
    const w = watchLiveness(onStale)

    setVisibility('hidden')
    vi.advanceTimersByTime(120_000)
    expect(onStale).not.toHaveBeenCalled()
    w.stop()
  })

  it('fires on visible after 10 s hidden', () => {
    const onStale = vi.fn()
    const w = watchLiveness(onStale)

    setVisibility('hidden')
    vi.advanceTimersByTime(10_000)
    setVisibility('visible')
    expect(onStale).toHaveBeenCalledTimes(1)
    w.stop()
  })

  it('does not fire on visible after 5 s hidden', () => {
    const onStale = vi.fn()
    const w = watchLiveness(onStale)

    setVisibility('hidden')
    vi.advanceTimersByTime(5_000)
    setVisibility('visible')
    expect(onStale).not.toHaveBeenCalled()
    w.stop()
  })

  it('stop() ends both triggers', () => {
    const onStale = vi.fn()
    const w = watchLiveness(onStale)
    w.stop()

    vi.advanceTimersByTime(120_000)
    setVisibility('hidden')
    vi.advanceTimersByTime(20_000)
    setVisibility('visible')
    expect(onStale).not.toHaveBeenCalled()
  })
})

describe('trackHidden', () => {
  it('counts a spell that began before it was mounted', () => {
    setVisibility('hidden')
    const onVisible = vi.fn()
    const untrack = trackHidden(onVisible)

    vi.advanceTimersByTime(15_000)
    setVisibility('visible')

    expect(onVisible).toHaveBeenCalledTimes(1)
    expect(onVisible.mock.calls[0][0]).toBeGreaterThanOrEqual(15_000)
    untrack()
  })
})
