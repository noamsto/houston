import { afterEach, describe, expect, it, vi } from 'vitest'
import { act, cleanup, render, screen } from '@testing-library/react'
import { Shell } from './Shell'
import { installFakeEventSource } from '../testing/fakeEventSource'
import type { Run } from '../api/runs'

const now = 1_800_000_000_000 // fixed ms

vi.mock('./useWorkspace', () => ({
  useWorkspace: () => ({ workspace: null, error: null, loading: false }),
}))
// DispatchView is kept mounted like Crews/Workspace in both shells, so it
// fetches on mount — never a real fetch in tests.
vi.mock('../api/dispatch', () => ({
  fetchDispatchOptions: () => Promise.resolve({
    repos: [{ path: '/repo', name: 'repo', crews: ['1-1'] }],
    tiers: ['trivial', 'standard', 'deep'],
    efforts: ['low', 'medium', 'high', 'xhigh', 'max'],
    plans: ['required', 'provided'],
    engines: { claude: ['opus', 'sonnet', 'haiku', 'fable'] },
    engine_order: ['claude'],
  }),
  submitDispatch: vi.fn(),
}))

function run(p: Partial<Run> = {}): Run {
  return {
    id: 'pane-1', agent: 'claude', state: 'running',
    activity: {}, tokens: { input: 0, output: 0 },
    caps: { terminal: true, reply: true, kill: true },
    updated_at: Math.floor(now / 1000),
    ...p,
  } as Run
}

let mql: { matches: boolean; addEventListener: (type: string, h: (e: { matches: boolean }) => void) => void; removeEventListener: () => void }
let handler: ((e: { matches: boolean }) => void) | undefined
let originalMatchMedia: typeof window.matchMedia

function stubMatchMedia(matches: boolean): void {
  mql = {
    matches,
    addEventListener: (_type, h) => { handler = h },
    removeEventListener: () => {},
  }
  window.matchMedia = vi.fn(() => mql) as unknown as typeof window.matchMedia
}

function flip(matches: boolean): void {
  mql.matches = matches
  act(() => { handler?.({ matches }) })
}

afterEach(() => {
  cleanup()
  window.matchMedia = originalMatchMedia
  handler = undefined
})

describe('Shell layout selection', () => {
  it('renders MobileShell below the breakpoint', () => {
    originalMatchMedia = window.matchMedia
    stubMatchMedia(false)
    const events = installFakeEventSource()
    try {
      render(<Shell />)
      expect(screen.getByLabelText('sections')).toBeTruthy()
      expect(screen.queryByLabelText('console')).toBeNull()
    } finally {
      events.uninstall()
    }
  })

  it('renders ConsoleShell at/above the breakpoint', () => {
    originalMatchMedia = window.matchMedia
    stubMatchMedia(true)
    const events = installFakeEventSource()
    try {
      render(<Shell />)
      expect(screen.getByLabelText('console')).toBeTruthy()
      expect(screen.queryByLabelText('sections')).toBeNull()
    } finally {
      events.uninstall()
    }
  })

  it('swaps layout on a breakpoint change without reopening the EventSource', () => {
    originalMatchMedia = window.matchMedia
    stubMatchMedia(false)
    const events = installFakeEventSource()
    try {
      render(<Shell />)
      const r = run({ id: 'a', repo: 'repo-a', branch: 'branch-a' })
      act(() => { events.instances[0].emit('snapshot', [r]) })

      flip(true)

      expect(screen.getByLabelText('console')).toBeTruthy()
      expect(screen.getByText('repo-a/branch-a')).toBeTruthy()
      expect(events.instances.length).toBe(1)
      expect(events.instances[0].closed).toBe(false)
    } finally {
      events.uninstall()
    }
  })
})
