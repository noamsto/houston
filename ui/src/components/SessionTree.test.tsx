import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { SessionTree } from './SessionTree'
import type { SessionsData, WindowWithStatus } from '../api/types'

afterEach(cleanup)

const win = {
  window: { index: 0, name: 'editor', path: '/x/proj' },
  pane: { session: 's', window: 0, index: 0 },
  parse_result: { type: 'idle', activity: '' },
  preview: [],
  needs_attention: false,
  branch: 'feat-x',
  process: 'claude',
  agent_type: 'claude-code',
  pane_id: '%7',
  tmux_server: '99',
} as unknown as WindowWithStatus

const sessions = {
  needs_attention: [],
  active: [{ session: { name: 's' }, windows: [win], attention_count: 0, has_working: false }],
  idle: [],
} as unknown as SessionsData

describe('SessionTree', () => {
  it('selecting a window passes its target with the pane identity', () => {
    const onSelect = vi.fn()
    render(<SessionTree sessions={sessions} onSelect={onSelect} onSplit={vi.fn()} />)
    fireEvent.click(screen.getByText('feat-x'))
    expect(onSelect).toHaveBeenCalledWith({ target: 's:0.0', paneId: '%7', server: '99' })
  })

  it('ctrl-click splits with the same identity', () => {
    const onSplit = vi.fn()
    render(<SessionTree sessions={sessions} onSelect={vi.fn()} onSplit={onSplit} />)
    fireEvent.click(screen.getByText('feat-x'), { ctrlKey: true })
    expect(onSplit).toHaveBeenCalledWith({ target: 's:0.0', paneId: '%7', server: '99' })
  })
})
