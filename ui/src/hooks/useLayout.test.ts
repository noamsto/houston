import { beforeEach, describe, expect, it } from 'vitest'
import { renderHook } from '@testing-library/react'
import { layoutReducer, useLayout, type LayoutState } from './useLayout'

const empty: LayoutState = { panes: [], layout: { type: 'empty' }, focusedPaneId: null }

describe('layoutReducer pane identity', () => {
  it('OPEN_PANE into an empty layout stores target, paneId and server', () => {
    const s = layoutReducer(empty, { type: 'OPEN_PANE', pane: { target: 's:0.0', paneId: '%1', server: '11' } })
    expect(s.panes).toHaveLength(1)
    expect(s.panes[0]).toMatchObject({ target: 's:0.0', paneId: '%1', server: '11' })
  })

  it('in-place OPEN_PANE replaces target, paneId and server together', () => {
    const first = layoutReducer(empty, { type: 'OPEN_PANE', pane: { target: 's:0.0', paneId: '%1', server: '11' } })
    const s = layoutReducer(first, { type: 'OPEN_PANE', pane: { target: 's:1.0', paneId: '%2', server: '22' } })
    expect(s.panes).toHaveLength(1)
    expect(s.panes[0]).toMatchObject({ target: 's:1.0', paneId: '%2', server: '22' })
  })

  it('in-place OPEN_PANE with no identity clears the old one', () => {
    const first = layoutReducer(empty, { type: 'OPEN_PANE', pane: { target: 's:0.0', paneId: '%1', server: '11' } })
    const s = layoutReducer(first, { type: 'OPEN_PANE', pane: { target: 's:1.0' } })
    expect(s.panes[0].paneId).toBeUndefined()
    expect(s.panes[0].server).toBeUndefined()
  })

  it('SPLIT_PANE stores the identity on the new pane', () => {
    const first = layoutReducer(empty, { type: 'OPEN_PANE', pane: { target: 's:0.0', paneId: '%1', server: '11' } })
    const s = layoutReducer(first, {
      type: 'SPLIT_PANE',
      pane: { target: 's:1.0', paneId: '%2', server: '22' },
      direction: 'horizontal',
    })
    expect(s.panes).toHaveLength(2)
    expect(s.panes[1]).toMatchObject({ target: 's:1.0', paneId: '%2', server: '22' })
  })
})

describe('useLayout persisted state', () => {
  beforeEach(() => localStorage.clear())

  const persisted = (panes: { id: string; target: string; paneId?: string; server?: string }[]): LayoutState => ({
    panes,
    layout: { type: 'single', paneId: panes[0].id },
    focusedPaneId: panes[0].id,
  })

  it('discards a layout whose pane lacks identity', () => {
    localStorage.setItem('houston-layout', JSON.stringify(persisted([{ id: 'pane-1', target: 's:0.0' }])))
    const { result } = renderHook(() => useLayout())
    expect(result.current.panes).toEqual([])
    expect(result.current.layout).toEqual({ type: 'empty' })
  })

  it('discards the whole layout when any pane lacks a server', () => {
    localStorage.setItem(
      'houston-layout',
      JSON.stringify(persisted([
        { id: 'pane-1', target: 's:0.0', paneId: '%1', server: '11' },
        { id: 'pane-2', target: 's:1.0', paneId: '%2' },
      ])),
    )
    const { result } = renderHook(() => useLayout())
    expect(result.current.panes).toEqual([])
  })

  it('restores a layout whose panes all carry identity', () => {
    const state = persisted([{ id: 'pane-1', target: 's:0.0', paneId: '%1', server: '11' }])
    localStorage.setItem('houston-layout', JSON.stringify(state))
    const { result } = renderHook(() => useLayout())
    expect(result.current.panes).toEqual(state.panes)
    expect(result.current.focusedPaneId).toBe('pane-1')
  })
})
