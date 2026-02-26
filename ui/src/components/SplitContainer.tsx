import { Allotment } from 'allotment'
import 'allotment/dist/style.css'
import type { PaneInstance, SplitLayout } from '../hooks/useLayout'
import { TerminalPane } from './TerminalPane'

interface Props {
  layout: SplitLayout
  panes: PaneInstance[]
  focusedPaneId: string | null
  onFocus: (paneId: string) => void
  onClose: (paneId: string) => void
}

function renderLayout(
  layout: SplitLayout,
  panes: PaneInstance[],
  focusedPaneId: string | null,
  onFocus: (paneId: string) => void,
  onClose: (paneId: string) => void,
): React.ReactNode {
  if (layout.type === 'empty') return null

  if (layout.type === 'single') {
    const pane = panes.find((p) => p.id === layout.paneId)
    if (!pane) return null
    return (
      <TerminalPane
        pane={pane}
        isFocused={pane.id === focusedPaneId}
        onFocus={() => onFocus(pane.id)}
        onClose={() => onClose(pane.id)}
      />
    )
  }

  // 'split' — recurse into children
  return (
    <Allotment vertical={layout.direction === 'vertical'}>
      <Allotment.Pane>
        {renderLayout(layout.first, panes, focusedPaneId, onFocus, onClose)}
      </Allotment.Pane>
      <Allotment.Pane>
        {renderLayout(layout.second, panes, focusedPaneId, onFocus, onClose)}
      </Allotment.Pane>
    </Allotment>
  )
}

export function SplitContainer({ layout, panes, focusedPaneId, onFocus, onClose }: Props) {
  return (
    <div style={{ height: '100%', overflow: 'hidden' }}>
      {renderLayout(layout, panes, focusedPaneId, onFocus, onClose)}
    </div>
  )
}
