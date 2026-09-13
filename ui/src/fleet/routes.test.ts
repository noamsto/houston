import { act, cleanup, renderHook } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import { parseDetailRoute, useDetailRoute } from './routes'

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
