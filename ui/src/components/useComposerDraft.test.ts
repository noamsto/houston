import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, cleanup, renderHook } from '@testing-library/react'
import { useComposerDraft } from './useComposerDraft'

const storageKey = (key: string) => `houston-draft:${key}`
const DAY = 24 * 60 * 60_000
const NOW = Date.parse('2026-01-01T00:00:00Z')
const stored = (key: string) => {
  const raw = localStorage.getItem(storageKey(key))
  return raw === null ? null : (JSON.parse(raw) as { t: string; at: number })
}
const store = (key: string, t: string, at: number) =>
  localStorage.setItem(storageKey(key), JSON.stringify({ t, at }))

beforeEach(() => {
  localStorage.clear()
  vi.useFakeTimers({ toFake: ['Date'] })
  vi.setSystemTime(NOW)
})

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
  vi.useRealTimers()
})

describe('useComposerDraft', () => {
  it('starts empty when nothing is stored', () => {
    const { result } = renderHook(() => useComposerDraft('a'))
    expect(result.current[0]).toBe('')
  })

  it('restores the draft after unmount and remount', () => {
    const first = renderHook(() => useComposerDraft('a'))
    act(() => first.result.current[1]('line one\nline two '))
    first.unmount()

    const second = renderHook(() => useComposerDraft('a'))
    expect(second.result.current[0]).toBe('line one\nline two ')
  })

  it('keeps drafts of different keys isolated', () => {
    const a = renderHook(() => useComposerDraft('a'))
    act(() => a.result.current[1]('for a'))
    const b = renderHook(() => useComposerDraft('b'))
    expect(b.result.current[0]).toBe('')
    act(() => b.result.current[1]('for b'))

    expect(stored('a')?.t).toBe('for a')
    expect(stored('b')?.t).toBe('for b')
  })

  it('removes the stored item when the text becomes empty', () => {
    const { result } = renderHook(() => useComposerDraft('a'))
    act(() => result.current[1]('hello'))
    expect(stored('a')).toEqual({ t: 'hello', at: NOW })
    act(() => result.current[1](''))
    expect(localStorage.getItem(storageKey('a'))).toBeNull()
  })

  it('still works in memory when storage throws', () => {
    vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => { throw new Error('denied') })
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => { throw new Error('denied') })
    vi.spyOn(Storage.prototype, 'removeItem').mockImplementation(() => { throw new Error('denied') })

    const { result } = renderHook(() => useComposerDraft('a'))
    expect(result.current[0]).toBe('')
    act(() => result.current[1]('typed'))
    expect(result.current[0]).toBe('typed')
    act(() => result.current[1](''))
    expect(result.current[0]).toBe('')
    act(() => result.current[2]('typed'))
  })

  it('clearIf clears text and storage when the text is still the sent text', () => {
    const { result } = renderHook(() => useComposerDraft('a'))
    act(() => result.current[1]('sent '))
    act(() => result.current[2]('sent '))
    expect(result.current[0]).toBe('')
    expect(localStorage.getItem(storageKey('a'))).toBeNull()
  })

  it('clearIf keeps newer text and its stored copy', () => {
    const { result } = renderHook(() => useComposerDraft('a'))
    act(() => result.current[1]('sent'))
    act(() => result.current[1]('sent plus more'))
    act(() => result.current[2]('sent'))
    expect(result.current[0]).toBe('sent plus more')
    expect(stored('a')?.t).toBe('sent plus more')
  })

  it('clearIf after unmount removes the stored copy', () => {
    const { result, unmount } = renderHook(() => useComposerDraft('a'))
    act(() => result.current[1]('sent'))
    const clearIf = result.current[2]
    unmount()
    clearIf('sent')
    expect(localStorage.getItem(storageKey('a'))).toBeNull()
  })

  it('clearIf after unmount leaves a newer stored draft', () => {
    const { result, unmount } = renderHook(() => useComposerDraft('a'))
    act(() => result.current[1]('sent more'))
    const clearIf = result.current[2]
    unmount()
    clearIf('sent')
    expect(stored('a')?.t).toBe('sent more')
  })

  it('clearIf from one instance clears every instance of the same key', () => {
    const first = renderHook(() => useComposerDraft('a'))
    act(() => first.result.current[1]('deploy'))
    const second = renderHook(() => useComposerDraft('a'))
    expect(second.result.current[0]).toBe('deploy')

    act(() => first.result.current[2]('deploy'))
    expect(first.result.current[0]).toBe('')
    expect(second.result.current[0]).toBe('')
    expect(stored('a')).toBeNull()
  })

  it('clearIf leaves an instance that holds newer text', () => {
    const first = renderHook(() => useComposerDraft('a'))
    act(() => first.result.current[1]('deploy'))
    const clearIf = first.result.current[2]
    first.unmount()
    const second = renderHook(() => useComposerDraft('a'))
    act(() => second.result.current[1]('deploy now'))

    act(() => clearIf('deploy'))
    expect(second.result.current[0]).toBe('deploy now')
    expect(stored('a')?.t).toBe('deploy now')
  })

  it('clearIf does not touch instances of other keys', () => {
    const a = renderHook(() => useComposerDraft('a'))
    const b = renderHook(() => useComposerDraft('b'))
    act(() => b.result.current[1]('same'))
    act(() => a.result.current[1]('same'))
    act(() => a.result.current[2]('same'))
    expect(b.result.current[0]).toBe('same')
  })

  it('a clearIf from an unmounted instance clears a remounted one', () => {
    const first = renderHook(() => useComposerDraft('a'))
    act(() => first.result.current[1]('deploy'))
    const clearIf = first.result.current[2]
    first.unmount()
    const second = renderHook(() => useComposerDraft('a'))
    expect(second.result.current[0]).toBe('deploy')

    act(() => clearIf('deploy'))
    expect(second.result.current[0]).toBe('')
    expect(stored('a')).toBeNull()
  })
})

describe('useComposerDraft expiry', () => {
  it('restores a draft younger than 24h', () => {
    store('a', 'fresh', NOW - DAY + 1)
    const { result } = renderHook(() => useComposerDraft('a'))
    expect(result.current[0]).toBe('fresh')
  })

  it('drops and removes a draft older than 24h', () => {
    store('a', 'stale', NOW - DAY - 1)
    const { result } = renderHook(() => useComposerDraft('a'))
    expect(result.current[0]).toBe('')
    expect(localStorage.getItem(storageKey('a'))).toBeNull()
  })

  it.each([['plain text'], ['{"t":1,"at":2}'], ['{"t":"x"}'], ['null']])(
    'treats %j as malformed: empty and removed',
    (raw) => {
      localStorage.setItem(storageKey('a'), raw)
      const { result } = renderHook(() => useComposerDraft('a'))
      expect(result.current[0]).toBe('')
      expect(localStorage.getItem(storageKey('a'))).toBeNull()
    },
  )

  it('reopening a draft does not extend its lifetime', () => {
    store('a', 'old', NOW - DAY + 1000)
    renderHook(() => useComposerDraft('a')).unmount()
    expect(stored('a')?.at).toBe(NOW - DAY + 1000)
  })

  it('sweeps other expired or malformed drafts on first use per page load', async () => {
    vi.resetModules()
    const { useComposerDraft: fresh } = await import('./useComposerDraft')
    store('old', 'stale', NOW - 2 * DAY)
    store('young', 'fresh', NOW - 1000)
    localStorage.setItem(storageKey('junk'), 'not json')
    localStorage.setItem('unrelated', 'keep')

    renderHook(() => fresh('x'))
    expect(localStorage.getItem(storageKey('old'))).toBeNull()
    expect(localStorage.getItem(storageKey('junk'))).toBeNull()
    expect(stored('young')?.t).toBe('fresh')
    expect(localStorage.getItem('unrelated')).toBe('keep')
  })
})
