import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render } from '@testing-library/react'
import { WorkspaceView } from './WorkspaceView'
import type { Workspace } from '../api/workspace'

const refresh = vi.fn()
let mockReturn: { workspace: Workspace | null; error: string | null; loading: boolean; refreshing: boolean; refresh: () => void } = {
  workspace: null,
  error: null,
  loading: true,
  refreshing: false,
  refresh,
}
vi.mock('./useWorkspace', () => ({
  useWorkspace: () => mockReturn,
}))

const nowSec = () => Math.floor(Date.now() / 1000)

function fixture(): Workspace {
  return {
    host: '',
    projects: [
      {
        name: 'houston',
        window_count: 2,
        main_checkout: [
          {
            index: 0,
            name: 'main',
            active: true,
            session: 'sess-a',
            branch: 'feat/38-workspace-tab',
            crew_codename: 'firefly',
            issue_id: '#38',
            pr_number: '71',
            pr_check_state: 'failure',
            panes: [
              { id: 'pane-1', index: 0, active: true, command: 'claude', agent: true, run_id: 'pane-1', agent_type: 'claude-code', state: 'running', updated_at: nowSec() - 30, detail: 'Edit · a.go' },
            ],
          },
        ],
        worktrees: [
          {
            index: 1,
            name: 'shell',
            active: false,
            session: 'sess-a',
            panes: [
              { id: 'pane-2', index: 0, active: false, command: 'zsh', agent: false },
              { id: 'pane-3', index: 1, active: true, command: 'vim', agent: false },
            ],
          },
        ],
      },
      {
        name: '',
        window_count: 1,
        other: [
          {
            index: 0,
            name: 'scratch',
            active: false,
            session: 'sess-b',
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
    mockReturn = { workspace: null, error: null, loading: true, refreshing: false, refresh }
    const { container } = render(<WorkspaceView />)
    expect(container.textContent).toMatch(/loading/i)
  })

  it('shows an error state when the first fetch fails and there is no prior workspace', () => {
    mockReturn = { workspace: null, error: 'fetchWorkspace: 500', loading: false, refreshing: false, refresh }
    const { container } = render(<WorkspaceView />)
    expect(container.textContent).toContain('fetchWorkspace: 500')
  })

  it('renders the stale tree instead of the error state when an error arrives alongside a prior workspace', () => {
    mockReturn = { workspace: fixture(), error: 'fetchWorkspace: 500', loading: false, refreshing: false, refresh }
    const { container } = render(<WorkspaceView />)
    expect(container.querySelectorAll('.fleet-group').length).toBe(2)
    expect(container.textContent).toContain('Showing last known data')
    expect(container.textContent).toContain('fetchWorkspace: 500')
  })

  it('Retry in the banner and in the first-load error state both call refresh', () => {
    refresh.mockClear()
    mockReturn = { workspace: fixture(), error: 'boom', loading: false, refreshing: false, refresh }
    const a = render(<WorkspaceView />)
    fireEvent.click(a.getByText('Retry'))
    a.unmount()

    mockReturn = { workspace: null, error: 'boom', loading: false, refreshing: false, refresh }
    const b = render(<WorkspaceView />)
    fireEvent.click(b.getByText('Retry'))
    expect(refresh).toHaveBeenCalledTimes(2)
  })

  it('shows an empty state when there are no sessions', () => {
    mockReturn = { workspace: { host: '', projects: [] }, error: null, loading: false, refreshing: false, refresh }
    const { container } = render(<WorkspaceView />)
    expect(container.textContent).toMatch(/no tmux sessions/i)
  })
})

describe('WorkspaceView buckets', () => {
  it('renders Main checkout and Worktrees buckets for sess-a, with no Other bucket', () => {
    mockReturn = { workspace: fixture(), error: null, loading: false, refreshing: false, refresh }
    const { container } = render(<WorkspaceView />)

    const headers = Array.from(container.querySelectorAll('.fleet-group')).map((h) => h.textContent)
    expect(headers).toEqual(['houston1 agent', 'Other sessions0 agents'])

    const sections = container.querySelectorAll('section')
    const sessA = sections[0]
    const buckets = Array.from(sessA.querySelectorAll('.ws-bucket')).map((b) => b.textContent)
    expect(buckets).toEqual(['Main checkout', 'Worktrees (1)'])

    expect(container.querySelectorAll('.run-chip.codename').length).toBe(1)
    expect(container.querySelector('.run-chip.codename')?.textContent).toBe('firefly')
  })

  it('renders an Other bucket for a session whose only window has no repo identity', () => {
    mockReturn = { workspace: fixture(), error: null, loading: false, refreshing: false, refresh }
    const { container } = render(<WorkspaceView />)

    const sections = container.querySelectorAll('section')
    const sessB = sections[1]
    const buckets = Array.from(sessB.querySelectorAll('.ws-bucket')).map((b) => b.textContent)
    expect(buckets).toEqual(['Other'])
  })

  it('renders the no-repo project group header as "Other sessions"', () => {
    mockReturn = { workspace: fixture(), error: null, loading: false, refreshing: false, refresh }
    const { container } = render(<WorkspaceView />)

    const sections = container.querySelectorAll('section')
    const header = sections[1].querySelector('.fleet-group span')
    expect(header?.textContent).toBe('Other sessions')
  })

  it('renders windows from different tmux sessions under one project group', () => {
    const ws = fixture()
    ws.projects[0].worktrees!.push({
      index: 1,
      name: 'other-shell',
      active: false,
      session: 'sess-c',
      panes: [{ id: 'pane-6', index: 0, active: false, command: 'bash', agent: false }],
    })
    mockReturn = { workspace: ws, error: null, loading: false, refreshing: false, refresh }
    const { container } = render(<WorkspaceView />)

    const sections = container.querySelectorAll('section')
    const sessionLabels = Array.from(sections[0].querySelectorAll('.ws-window-session')).map((n) => n.textContent)
    expect(sessionLabels).toEqual(['sess-a', 'sess-a', 'sess-c'])
  })

  it('calls onOpen with exactly the agent pane\'s run id when clicked; plain panes are not interactive', () => {
    mockReturn = { workspace: fixture(), error: null, loading: false, refreshing: false, refresh }
    const onOpen = vi.fn()
    const { container } = render(<WorkspaceView onOpen={onOpen} />)

    const rows = container.querySelectorAll('button.ws-pane')
    expect(rows.length).toBe(1)
    fireEvent.click(rows[0])
    expect(onOpen).toHaveBeenCalledTimes(1)
    expect(onOpen).toHaveBeenCalledWith('pane-1')

    const plain = container.querySelectorAll('.ws-plain')
    expect(plain.length).toBe(2)
    for (const p of plain) expect(p.tagName).not.toBe('BUTTON')
  })
})

describe('WorkspaceView rows', () => {
  it('tells what runs where: agent type, state and detail on the agent row', () => {
    mockReturn = { workspace: fixture(), error: null, loading: false, refreshing: false, refresh }
    const { container } = render(<WorkspaceView />)
    const row = container.querySelector('button.ws-pane')!
    expect(row.querySelector('.ws-pane-agent')?.textContent).toBe('claude-code')
    expect(row.querySelector('.ws-pane-state')?.textContent).toBe('running')
    expect(row.querySelector('.ws-pane-detail')?.textContent).toBe('Edit · a.go')
    expect(row.classList.contains('attention')).toBe(false)
  })

  it('shows the branch once (not name + branch) and PR / issue badges from window options', () => {
    mockReturn = { workspace: fixture(), error: null, loading: false, refreshing: false, refresh }
    const { container } = render(<WorkspaceView />)
    const win = container.querySelector('.ws-window')!
    expect(win.querySelector('.ws-window-title')?.textContent).toBe('feat/38-workspace-tab')
    expect(win.textContent).not.toContain('main')
    expect(win.querySelector('.run-chip.issue')?.textContent).toBe('#38')
    const pr = win.querySelector('.run-chip.pr')!
    expect(pr.textContent).toBe('#71')
    expect(pr.classList.contains('failing')).toBe(true)
  })

  it('does not repeat the state as the detail line', () => {
    const ws = fixture()
    const pane = ws.projects[0].main_checkout![0].panes[0]
    if (!pane.agent) throw new Error('fixture pane must be an agent')
    pane.detail = 'running'
    mockReturn = { workspace: ws, error: null, loading: false, refreshing: false, refresh }
    const { container } = render(<WorkspaceView />)
    expect(container.querySelector('.ws-pane-detail')).toBeNull()
  })

  it('disables Retry while a refresh is in flight', () => {
    mockReturn = { workspace: fixture(), error: 'boom', loading: false, refreshing: true, refresh }
    const { getByText } = render(<WorkspaceView />)
    expect((getByText('Retry') as HTMLButtonElement).disabled).toBe(true)
  })

  it('marks a merged or closed PR on its chip, and leaves an open one bare', () => {
    const ws = fixture()
    ws.projects[0].main_checkout![0].pr_state = 'merged'
    mockReturn = { workspace: ws, error: null, loading: false, refreshing: false, refresh }
    const { container } = render(<WorkspaceView />)
    expect(container.querySelector('.run-chip.pr')?.textContent).toBe('#71 merged')
  })

  it('falls back to the window name when there is no branch', () => {
    mockReturn = { workspace: fixture(), error: null, loading: false, refreshing: false, refresh }
    const { container } = render(<WorkspaceView />)
    const titles = Array.from(container.querySelectorAll('.ws-window-title')).map((t) => t.textContent)
    expect(titles).toEqual(['feat/38-workspace-tab', 'shell', 'scratch'])
  })

  it('collapses panes with no run into one deduped, neutral summary line', () => {
    const ws = fixture()
    ws.projects[0].worktrees![0].panes.push(
      { id: 'pane-5', index: 2, active: false, command: 'zsh', agent: false },
    )
    mockReturn = { workspace: ws, error: null, loading: false, refreshing: false, refresh }
    const { container } = render(<WorkspaceView />)
    const summaries = Array.from(container.querySelectorAll('.ws-plain')).map((n) => n.textContent)
    expect(summaries).toEqual(['3 panes · zsh, vim', '1 pane · htop'])
  })
})

describe('WorkspaceView needs-you', () => {
  function withBlocked(ageSec: number): Workspace {
    const ws = fixture()
    const pane = ws.projects[0].main_checkout![0].panes[0]
    if (!pane.agent) throw new Error('fixture pane must be an agent')
    pane.state = 'blocked'
    pane.updated_at = nowSec() - ageSec
    pane.detail = 'waiting on you'
    return ws
  }

  it('highlights a freshly blocked pane and counts it in the session header', () => {
    mockReturn = { workspace: withBlocked(30 * 60), error: null, loading: false, refreshing: false, refresh }
    const { container } = render(<WorkspaceView />)
    expect(container.querySelector('.ws-need')?.textContent).toBe('1 need you')
    expect(container.querySelector('button.ws-pane')!.classList.contains('attention')).toBe(true)
  })

  it('does not count a pane that has been blocked for hours, matching the other tabs', () => {
    mockReturn = { workspace: withBlocked(2 * 60 * 60), error: null, loading: false, refreshing: false, refresh }
    const { container } = render(<WorkspaceView />)
    expect(container.querySelector('.ws-need')).toBeNull()
    expect(container.querySelector('button.ws-pane')!.classList.contains('attention')).toBe(false)
  })
})
