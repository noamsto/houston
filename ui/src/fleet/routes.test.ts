import { act, cleanup, renderHook } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import {
  dispatchHash, parseDetailRoute, parseDispatchRoute, parseTabRoute, tabHash, useDetailRoute, useDispatchRoute, useShellTab,
} from './routes'

afterEach(() => {
  cleanup()
  window.location.hash = ''
})

describe('parseDetailRoute', () => {
  it.each([
    ['#/fleet', null],
    ['#/fleet/', null],
    ['#/fleet/pane-1', { id: 'pane-1', tab: 'activity' }],
    ['#/fleet/pane-1/terminal', { id: 'pane-1', tab: 'terminal' }],
    ['#/fleet/pane-1/bogus', { id: 'pane-1', tab: 'activity' }],
    ['#/fleet/pane-1/', { id: 'pane-1', tab: 'activity' }],
    ['#/agents', null],
    ['#/fleet/a/b/c', null],
  ])('%s -> %j', (hash, expected) => {
    expect(parseDetailRoute(hash)).toEqual(expected)
  })
})

describe('useDetailRoute', () => {
  it('reads the initial hash and updates on hashchange', () => {
    window.location.hash = '#/fleet/pane-1'
    const { result } = renderHook(() => useDetailRoute())
    expect(result.current).toEqual({ id: 'pane-1', tab: 'activity' })

    act(() => {
      window.location.hash = '#/fleet/pane-2/terminal'
      window.dispatchEvent(new HashChangeEvent('hashchange'))
    })
    expect(result.current).toEqual({ id: 'pane-2', tab: 'terminal' })
  })
})

describe('parseDispatchRoute', () => {
  it.each([
    ['#/dispatch', { repo: undefined, crew: undefined }],
    ['#/dispatch?repo=%2Frepo%2Fa&crew=new', { repo: '/repo/a', crew: 'new' }],
    ['#/dispatch?repo=/repo/a', { repo: '/repo/a', crew: undefined }],
    ['#/dispatch?repo=&crew=', { repo: undefined, crew: undefined }],
    ['#/fleet', null],
    ['#/fleet/pane-1', null],
    ['', null],
  ])('%s -> %j', (hash, expected) => {
    expect(parseDispatchRoute(hash)).toEqual(expected)
  })
})

describe('dispatchHash', () => {
  it('builds a bare hash with no params', () => {
    expect(dispatchHash({})).toBe('#/dispatch')
  })

  it('encodes repo and crew', () => {
    expect(dispatchHash({ repo: '/repo/a', crew: 'new' })).toBe('#/dispatch?repo=%2Frepo%2Fa&crew=new')
  })

  it('omits an empty crew', () => {
    expect(dispatchHash({ repo: '/repo/a' })).toBe('#/dispatch?repo=%2Frepo%2Fa')
  })
})

describe('useDispatchRoute', () => {
  it('returns null off a non-dispatch hash', () => {
    window.location.hash = '#/fleet'
    const { result } = renderHook(() => useDispatchRoute())
    expect(result.current).toBeNull()
  })

  it('reads the initial hash with seq 0', () => {
    window.location.hash = '#/dispatch?repo=/repo/a'
    const { result } = renderHook(() => useDispatchRoute())
    expect(result.current).toEqual({ repo: '/repo/a', crew: undefined, seq: 0 })
  })

  it('increments seq on every hashchange, even re-applying the same params twice', () => {
    // happy-dom fires hashchange itself on a hash assignment that actually
    // changes the value, so no manual dispatchEvent here (it would double-count).
    window.location.hash = '#/dispatch'
    const { result } = renderHook(() => useDispatchRoute())
    expect(result.current).toEqual({ repo: undefined, crew: undefined, seq: 0 })

    act(() => {
      window.location.hash = '#/dispatch?repo=/repo/a'
    })
    expect(result.current).toEqual({ repo: '/repo/a', crew: undefined, seq: 1 })

    act(() => {
      window.location.hash = '#/dispatch'
    })
    expect(result.current).toEqual({ repo: undefined, crew: undefined, seq: 2 })

    act(() => {
      window.location.hash = '#/dispatch?repo=/repo/a'
    })
    expect(result.current).toEqual({ repo: '/repo/a', crew: undefined, seq: 3 })
  })
})

describe('parseTabRoute', () => {
  it.each([
    ['', 'fleet'],
    ['#', 'fleet'],
    ['#/', 'fleet'],
    ['#/fleet', 'fleet'],
    ['#/crews', 'crews'],
    ['#/workspace', 'workspace'],
    ['#/dispatch', 'dispatch'],
    ['#/dispatch?repo=%2Frepo&crew=new', 'dispatch'],
    ['#/fleet/pane-1/terminal', null],
    ['#/agents', null],
  ])('%s -> %j', (hash, expected) => {
    expect(parseTabRoute(hash)).toBe(expected)
  })
})

describe('tabHash', () => {
  it('builds the hash for a tab', () => {
    expect(tabHash('fleet')).toBe('#/fleet')
    expect(tabHash('workspace')).toBe('#/workspace')
  })
})

describe('useShellTab', () => {
  it('starts from the hash and follows hashchange', () => {
    window.location.hash = '#/workspace'
    const { result } = renderHook(() => useShellTab())
    expect(result.current[0]).toBe('workspace')

    act(() => { window.location.hash = '#/crews' })
    expect(result.current[0]).toBe('crews')

    act(() => { window.location.hash = '' })
    expect(result.current[0]).toBe('fleet')
  })

  it('keeps the tab across a detail hash', () => {
    window.location.hash = '#/crews'
    const { result } = renderHook(() => useShellTab())

    act(() => { window.location.hash = '#/fleet/pane-1/activity' })
    expect(result.current[0]).toBe('crews')
  })

  it('goTab writes the hash when no detail is open', () => {
    const { result } = renderHook(() => useShellTab())
    act(() => result.current[1]('crews'))
    expect(result.current[0]).toBe('crews')
    expect(window.location.hash).toBe('#/crews')
  })

  it('goTab is state-only with a detail open unless closeDetail', () => {
    window.location.hash = '#/fleet/pane-1/activity'
    const { result } = renderHook(() => useShellTab())

    act(() => result.current[1]('crews'))
    expect(result.current[0]).toBe('crews')
    expect(window.location.hash).toBe('#/fleet/pane-1/activity')

    act(() => result.current[1]('workspace', true))
    expect(result.current[0]).toBe('workspace')
    expect(window.location.hash).toBe('#/workspace')
  })
})
