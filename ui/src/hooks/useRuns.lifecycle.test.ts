import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, cleanup, renderHook } from '@testing-library/react'
import { useRuns } from './useRuns'
import { installFakeEventSource } from '../testing/fakeEventSource'
import type { FakeEventSourceHandle } from '../testing/fakeEventSource'
import type { Run } from '../api/runs'

function run(id: string, p: Partial<Run> = {}): Run {
  return {
    id, agent: 'claude', state: 'running',
    activity: {}, tokens: { input: 0, output: 0 },
    caps: { terminal: true, reply: true, kill: true },
    updated_at: 1000,
    ...p,
  } as Run
}

// Named (not anonymous) so eslint-plugin-react-hooks recognises this as a
// custom hook rather than an arbitrary function calling a hook.
function useRunsProbe() {
  return useRuns()
}

let fake: FakeEventSourceHandle

beforeEach(() => {
  fake = installFakeEventSource()
})

afterEach(() => {
  // Unmount (and run useRuns' real cleanup, against the fake) before
  // restoring the original EventSource.
  cleanup()
  fake.uninstall()
})

describe('useRuns lifecycle', () => {
  it('opens exactly one EventSource on mount, against /api/runs/stream', () => {
    renderHook(useRunsProbe)
    expect(fake.instances.length).toBe(1)
    expect(fake.instances[0].url).toBe('/api/runs/stream')
  })

  it('closes the EventSource on unmount', () => {
    const { unmount } = renderHook(useRunsProbe)
    const instance = fake.instances[0]
    unmount()
    expect(instance.closed).toBe(true)
  })

  it('opens a fresh EventSource on remount after unmount', () => {
    const first = renderHook(useRunsProbe)
    const firstInstance = fake.instances[0]
    first.unmount()

    renderHook(useRunsProbe)

    expect(fake.instances.length).toBe(2)
    const secondInstance = fake.instances[1]
    expect(secondInstance).not.toBe(firstInstance)
    expect(secondInstance.closed).toBe(false)
  })

  it('lands runs arriving over SSE in the store', () => {
    const { result } = renderHook(useRunsProbe)
    const instance = fake.instances[0]
    const r1 = run('pane-1')

    act(() => {
      instance.emit('snapshot', [r1])
    })
    expect(result.current.runs).toEqual([r1])

    const r2 = run('pane-2')
    act(() => {
      instance.emit('update', r2)
    })
    expect(result.current.runs).toEqual([r1, r2])
  })

  it('does not wedge the store on SSE error/disconnect', () => {
    const { result } = renderHook(useRunsProbe)
    const instance = fake.instances[0]

    act(() => {
      instance.open()
    })
    expect(result.current.connected).toBe(true)

    act(() => {
      instance.error()
    })
    expect(result.current.connected).toBe(false)

    const r1 = run('pane-1')
    act(() => {
      instance.emit('snapshot', [r1])
    })
    expect(result.current.runs).toEqual([r1])
    expect(result.current.connected).toBe(true)
  })

  it('sets hasSnapshot only after the first snapshot event, and never back to false', () => {
    const { result } = renderHook(useRunsProbe)
    expect(result.current.hasSnapshot).toBe(false)

    const instance = fake.instances[0]
    act(() => {
      instance.emit('snapshot', [run('pane-1')])
    })
    expect(result.current.hasSnapshot).toBe(true)

    act(() => {
      instance.error()
    })
    expect(result.current.hasSnapshot).toBe(true)
  })

  it('does not wedge the store on malformed JSON', () => {
    const { result } = renderHook(useRunsProbe)
    const instance = fake.instances[0]

    expect(() => {
      act(() => {
        instance.emitRaw('update', '{')
      })
    }).not.toThrow()

    const r1 = run('pane-1')
    act(() => {
      instance.emit('update', r1)
    })
    expect(result.current.runs).toEqual([r1])
  })

  describe('liveness', () => {
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

    it('stays on one EventSource while pings keep arriving', () => {
      renderHook(useRunsProbe)
      act(() => { fake.instances[0].open() })

      for (let t = 25; t <= 70; t += 25) {
        act(() => { vi.advanceTimersByTime(25_000) })
        act(() => { fake.instances[0].emit('ping', null) })
      }
      act(() => { vi.advanceTimersByTime(20_000) })

      expect(fake.instances.length).toBe(1)
      expect(fake.instances[0].closed).toBe(false)
    })

    it('replaces a silent EventSource after 60 s without any event', () => {
      const { result } = renderHook(useRunsProbe)
      const first = fake.instances[0]
      act(() => { first.open() })
      expect(result.current.connected).toBe(true)

      act(() => { vi.advanceTimersByTime(65_000) })

      expect(first.closed).toBe(true)
      expect(fake.instances.length).toBe(2)
      expect(fake.instances[1].url).toBe('/api/runs/stream')
      expect(fake.instances[1].closed).toBe(false)
      expect(result.current.connected).toBe(false)
    })

    it('reopens after the page was hidden 15 s, and the new snapshot replaces the map', () => {
      const { result } = renderHook(useRunsProbe)
      const first = fake.instances[0]
      act(() => { first.emit('snapshot', [run('old')]) })

      act(() => { setVisibility('hidden') })
      act(() => { vi.advanceTimersByTime(15_000) })
      act(() => { setVisibility('visible') })

      expect(first.closed).toBe(true)
      expect(fake.instances.length).toBe(2)
      act(() => { fake.instances[1].emit('snapshot', [run('new')]) })
      expect(result.current.runs.map((r) => r.id)).toEqual(['new'])
    })

    it('stops watching and closes the current source on unmount', () => {
      const { unmount } = renderHook(useRunsProbe)
      act(() => { vi.advanceTimersByTime(65_000) })
      expect(fake.instances.length).toBe(2)

      unmount()
      expect(fake.instances[1].closed).toBe(true)
      act(() => { vi.advanceTimersByTime(300_000) })
      expect(fake.instances.length).toBe(2)
    })
  })
})
