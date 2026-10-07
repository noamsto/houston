import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, cleanup, renderHook } from '@testing-library/react'
import { useComposerDraft } from './useComposerDraft'

const storageKey = (key: string) => `houston-draft:${key}`

beforeEach(() => localStorage.clear())

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
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

    expect(localStorage.getItem(storageKey('a'))).toBe('for a')
    expect(localStorage.getItem(storageKey('b'))).toBe('for b')
  })

  it('removes the stored item when the text becomes empty', () => {
    const { result } = renderHook(() => useComposerDraft('a'))
    act(() => result.current[1]('hello'))
    expect(localStorage.getItem(storageKey('a'))).toBe('hello')
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
    expect(localStorage.getItem(storageKey('a'))).toBe('sent plus more')
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
    expect(localStorage.getItem(storageKey('a'))).toBe('sent more')
  })
})
