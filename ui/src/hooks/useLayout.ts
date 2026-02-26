import { useEffect, useReducer } from 'react'

export interface PaneInstance {
  id: string
  target: string // "session:window.pane"
}

export type SplitLayout =
  | { type: 'empty' }
  | { type: 'single'; paneId: string }
  | {
      type: 'split'
      direction: 'horizontal' | 'vertical'
      ratio: number
      first: SplitLayout
      second: SplitLayout
    }

interface LayoutState {
  panes: PaneInstance[]
  layout: SplitLayout
  focusedPaneId: string | null
}

type LayoutAction =
  | { type: 'OPEN_PANE'; target: string }
  | { type: 'SPLIT_PANE'; target: string; direction: 'horizontal' | 'vertical' }
  | { type: 'CLOSE_PANE'; paneId: string }
  | { type: 'FOCUS_PANE'; paneId: string }

let nextId = 1
function genId() {
  return `pane-${nextId++}`
}

function layoutReducer(state: LayoutState, action: LayoutAction): LayoutState {
  switch (action.type) {
    case 'OPEN_PANE': {
      const id = genId()
      const pane: PaneInstance = { id, target: action.target }

      if (state.layout.type === 'empty') {
        return { panes: [pane], layout: { type: 'single', paneId: id }, focusedPaneId: id }
      }

      // Replace focused pane's target in-place rather than opening a new one
      if (state.focusedPaneId) {
        const updated = state.panes.map((p) =>
          p.id === state.focusedPaneId ? { ...p, target: action.target } : p,
        )
        return { ...state, panes: updated }
      }

      return state
    }

    case 'SPLIT_PANE': {
      const id = genId()
      const pane: PaneInstance = { id, target: action.target }

      if (state.layout.type === 'empty') {
        return { panes: [pane], layout: { type: 'single', paneId: id }, focusedPaneId: id }
      }

      if (state.layout.type === 'single') {
        return {
          panes: [...state.panes, pane],
          layout: {
            type: 'split',
            direction: action.direction,
            ratio: 0.5,
            first: state.layout,
            second: { type: 'single', paneId: id },
          },
          focusedPaneId: id,
        }
      }

      // For already-split layouts: add alongside the current split root
      return {
        panes: [...state.panes, pane],
        layout: {
          type: 'split',
          direction: action.direction,
          ratio: 0.5,
          first: state.layout,
          second: { type: 'single', paneId: id },
        },
        focusedPaneId: id,
      }
    }

    case 'CLOSE_PANE': {
      const remaining = state.panes.filter((p) => p.id !== action.paneId)
      if (remaining.length === 0) {
        return { panes: [], layout: { type: 'empty' }, focusedPaneId: null }
      }
      const newLayout = removePaneFromLayout(state.layout, action.paneId)
      return {
        panes: remaining,
        layout: newLayout,
        focusedPaneId: remaining[0]?.id ?? null,
      }
    }

    case 'FOCUS_PANE':
      return { ...state, focusedPaneId: action.paneId }

    default:
      return state
  }
}

function removePaneFromLayout(layout: SplitLayout, paneId: string): SplitLayout {
  if (layout.type === 'single') {
    return layout.paneId === paneId ? { type: 'empty' } : layout
  }
  if (layout.type === 'split') {
    const first = removePaneFromLayout(layout.first, paneId)
    const second = removePaneFromLayout(layout.second, paneId)
    if (first.type === 'empty') return second
    if (second.type === 'empty') return first
    return { ...layout, first, second }
  }
  return layout
}

const STORAGE_KEY = 'houston-layout'

function loadState(): LayoutState {
  try {
    const saved = localStorage.getItem(STORAGE_KEY)
    if (saved) return JSON.parse(saved) as LayoutState
  } catch {
    // ignore
  }
  return { panes: [], layout: { type: 'empty' }, focusedPaneId: null }
}

export function useLayout() {
  const [state, dispatch] = useReducer(layoutReducer, undefined, loadState)

  useEffect(() => {
    localStorage.setItem(STORAGE_KEY, JSON.stringify(state))
  }, [state])

  return { ...state, dispatch }
}
