import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { TerminalArea } from './TerminalArea'
import type { useLayout } from '../hooks/useLayout'
import type { SessionsData, SessionWithWindows } from '../api/types'

vi.mock('./SplitContainer', () => ({ SplitContainer: () => null }))

afterEach(cleanup)

const mkSession = (name: string, paneId: string, server: string) =>
  ({
    session: { name },
    windows: [
      {
        window: { index: 0, name: 'w', path: '' },
        pane: { session: name, window: 0, index: 0 },
        parse_result: { type: 'idle', activity: '' },
        preview: [],
        needs_attention: false,
        branch: '',
        process: '',
        agent_type: 'claude-code',
        pane_id: paneId,
        tmux_server: server,
      },
    ],
    attention_count: 0,
    has_working: false,
  }) as unknown as SessionWithWindows

const sessions: SessionsData = {
  needs_attention: [],
  active: [mkSession('a', '%1', '11'), mkSession('b', '%2', '22')],
  idle: [],
}

function setup() {
  const dispatch = vi.fn()
  const layout = {
    panes: [{ id: 'pane-1', target: 'a:0.0', paneId: '%1', server: '11' }],
    layout: { type: 'single', paneId: 'pane-1' },
    focusedPaneId: 'pane-1',
    dispatch,
  } as unknown as ReturnType<typeof useLayout>
  render(<TerminalArea layout={layout} sessions={sessions} onMenuClick={vi.fn()} isDesktop={false} />)
  return dispatch
}

describe('TerminalArea mobile nav', () => {
  it('next opens the next entry with its pane identity', () => {
    const dispatch = setup()
    fireEvent.click(screen.getByLabelText('Next session'))
    expect(dispatch).toHaveBeenCalledWith({
      type: 'OPEN_PANE',
      pane: { target: 'b:0.0', paneId: '%2', server: '22' },
    })
  })

  it('prev wraps to the last entry with its pane identity', () => {
    const dispatch = setup()
    fireEvent.click(screen.getByLabelText('Previous session'))
    expect(dispatch).toHaveBeenCalledWith({
      type: 'OPEN_PANE',
      pane: { target: 'b:0.0', paneId: '%2', server: '22' },
    })
  })
})
