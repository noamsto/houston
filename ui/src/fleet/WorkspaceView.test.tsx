import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render } from '@testing-library/react'
import { WorkspaceView } from './WorkspaceView'
import type { Workspace } from '../api/workspace'

let mockReturn: { workspace: Workspace | null; error: string | null; loading: boolean } = {
  workspace: null,
  error: null,
  loading: true,
}
vi.mock('./useWorkspace', () => ({
  useWorkspace: () => mockReturn,
}))

function fixture(): Workspace {
  return {
    host: '',
    sessions: [
      {
        name: 'sess-a',
        window_count: 2,
        main_checkout: [
          {
            index: 0,
            name: 'main',
            active: true,
            branch: 'feat/38-workspace-tab',
            crew_codename: 'firefly',
            panes: [
              { id: 'pane-1', index: 0, active: true, command: 'claude', agent: true, run_id: 'pane-1' },
            ],
          },
        ],
        worktrees: [
          {
            index: 1,
            name: 'shell',
            active: false,
            panes: [
              { id: 'pane-2', index: 0, active: false, command: 'zsh', agent: false },
              { id: 'pane-3', index: 1, active: true, command: 'vim', agent: false },
            ],
          },
        ],
      },
      {
        name: 'sess-b',
        window_count: 1,
        other: [
          {
            index: 0,
            name: 'scratch',
            active: false,
            panes: [
              { id: 'pane-4', index: 0, active: false, command: 'htop', agent: false },
            ],
          },
        ],
      },
    ],
  }
}

afterEach(cleanup)

describe('WorkspaceView states', () => {
  it('shows a loading state while the first fetch is in flight', () => {
    mockReturn = { workspace: null, error: null, loading: true }
    const { container } = render(<WorkspaceView />)
    expect(container.textContent).toMatch(/loading/i)
  })

  it('shows an error state when the first fetch fails and there is no prior workspace', () => {
    mockReturn = { workspace: null, error: 'fetchWorkspace: 500', loading: false }
    const { container } = render(<WorkspaceView />)
    expect(container.textContent).toContain('fetchWorkspace: 500')
  })

  it('renders the stale tree instead of the error state when an error arrives alongside a prior workspace', () => {
    mockReturn = { workspace: fixture(), error: 'fetchWorkspace: 500', loading: false }
    const { container } = render(<WorkspaceView />)
    expect(container.querySelectorAll('.fleet-group').length).toBe(2)
    expect(container.textContent).not.toContain('fetchWorkspace: 500')
  })

  it('shows an empty state when there are no sessions', () => {
    mockReturn = { workspace: { host: '', sessions: [] }, error: null, loading: false }
    const { container } = render(<WorkspaceView />)
    expect(container.textContent).toMatch(/no tmux sessions/i)
  })
})

describe('WorkspaceView buckets', () => {
  it('renders Main checkout and Worktrees buckets for sess-a, with no Other bucket', () => {
    mockReturn = { workspace: fixture(), error: null, loading: false }
    const { container } = render(<WorkspaceView />)

    const headers = Array.from(container.querySelectorAll('.fleet-group')).map((h) => h.textContent)
    expect(headers).toEqual(['sess-a2', 'sess-b1'])

    const sections = container.querySelectorAll('section')
    const sessA = sections[0]
    const buckets = Array.from(sessA.querySelectorAll('.ws-bucket')).map((b) => b.textContent)
    expect(buckets).toEqual(['Main checkout', 'Worktrees (1)'])

    expect(container.querySelectorAll('.run-chip.codename').length).toBe(1)
    expect(container.querySelector('.run-chip.codename')?.textContent).toBe('firefly')
  })

  it('renders an Other bucket for a session whose only window has no repo identity', () => {
    mockReturn = { workspace: fixture(), error: null, loading: false }
    const { container } = render(<WorkspaceView />)

    const sections = container.querySelectorAll('section')
    const sessB = sections[1]
    const buckets = Array.from(sessB.querySelectorAll('.ws-bucket')).map((b) => b.textContent)
    expect(buckets).toEqual(['Other'])
  })

  it('calls onOpen with exactly the agent pane\'s run id when clicked, and never for a non-agent pane', () => {
    mockReturn = { workspace: fixture(), error: null, loading: false }
    const onOpen = vi.fn()
    const { container } = render(<WorkspaceView onOpen={onOpen} />)

    const panes = container.querySelectorAll('.ws-pane')
    expect(panes.length).toBe(4)

    const agentPane = container.querySelector('.ws-pane.agent')!
    fireEvent.click(agentPane)
    expect(onOpen).toHaveBeenCalledTimes(1)
    expect(onOpen).toHaveBeenCalledWith('pane-1')

    const plainPanes = Array.from(panes).filter((p) => !p.classList.contains('agent'))
    expect(plainPanes.length).toBe(3)
    for (const p of plainPanes) fireEvent.click(p)

    expect(onOpen).toHaveBeenCalledTimes(1)
  })
})
