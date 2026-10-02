import { describe, expect, it } from 'vitest'
import { layoutReducer, type LayoutState } from './useLayout'

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
