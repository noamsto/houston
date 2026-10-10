import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, cleanup, fireEvent, render, screen, within } from '@testing-library/react'
import { MobileShell } from './MobileShell'
import { fetchDispatchOptions } from '../api/dispatch'
import type { Run } from '../api/runs'

vi.mock('./useWorkspace', () => ({
  useWorkspace: () => ({ workspace: null, error: null, loading: false }),
}))
// DispatchView is kept mounted like Crews, so it fetches as soon as
// MobileShell renders — never a real fetch in tests.
vi.mock('../api/dispatch', () => ({
  NEW_CREW: 'new',
  fetchDispatchOptions: vi.fn(() => Promise.resolve({
    repos: [{ path: '/repo', name: 'repo', crews: ['1-1'] }],
    tiers: ['trivial', 'standard', 'deep'],
    efforts: ['low', 'medium', 'high', 'xhigh', 'max'],
    plans: ['required', 'provided'],
    engines: { claude: ['opus', 'sonnet', 'haiku', 'fable'] },
    engine_order: ['claude'],
    tier_models: { claude: { trivial: 'haiku', standard: 'sonnet', deep: 'opus' } },
  })),
  submitDispatch: vi.fn(),
}))

function fakeVisualViewport(height: number) {
  const listeners = new Set<() => void>()
  const vv = {
    height,
    offsetTop: 0,
    scale: 1,
    addEventListener: (_t: string, fn: () => void) => listeners.add(fn),
    removeEventListener: (_t: string, fn: () => void) => listeners.delete(fn),
    emit: () => listeners.forEach((fn) => fn()),
  }
  vi.stubGlobal('visualViewport', vv)
  return vv
}

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  window.location.hash = ''
})

function dispatchTab(): HTMLElement {
  return screen.getByRole('button', { name: /dispatch/i })
}

describe('MobileShell on-screen keyboard', () => {
  it('shortens the shell above the keyboard and yields the tab bar, then restores', () => {
    const vv = fakeVisualViewport(window.innerHeight)
    const { container } = render(<MobileShell runs={[]} connected hasSnapshot now={0} mode="dispatcher" />)
    const shell = container.querySelector('.shell') as HTMLElement
    expect(shell.classList.contains('keyboard-open')).toBe(false)
    expect(shell.style.bottom).toBe('')

    act(() => {
      vv.height = window.innerHeight - 300
      vv.emit()
    })
    expect(shell.classList.contains('keyboard-open')).toBe(true)
    expect(shell.style.bottom).toBe('300px')

    act(() => {
      vv.height = window.innerHeight
      vv.emit()
    })
    expect(shell.classList.contains('keyboard-open')).toBe(false)
    expect(shell.style.bottom).toBe('')
  })
})

describe('MobileShell tabs', () => {
  it('shows the dispatch form on the Dispatch tab', async () => {
    render(<MobileShell runs={[]} connected hasSnapshot now={0} mode="dispatcher" />)

    fireEvent.click(dispatchTab())

    expect(await screen.findByLabelText('Title')).toBeTruthy()
  })

  it('opens on the Dispatch tab for a dispatch link', () => {
    window.location.hash = '#/dispatch?repo=%2Frepo&crew=new'
    render(<MobileShell runs={[]} connected hasSnapshot now={0} mode="dispatcher" />)

    expect(dispatchTab().getAttribute('aria-current')).toBe('true')
  })

  it('switches to the Dispatch tab when the hash becomes a dispatch link', () => {
    render(<MobileShell runs={[]} connected hasSnapshot now={0} mode="dispatcher" />)
    expect(dispatchTab().getAttribute('aria-current')).toBeNull()

    // happy-dom fires hashchange itself on a changing hash assignment.
    act(() => { window.location.hash = '#/dispatch?repo=%2Frepo' })

    expect(dispatchTab().getAttribute('aria-current')).toBe('true')
  })

  it('keeps a half-typed task across a switch to Fleet and back', async () => {
    render(<MobileShell runs={[]} connected hasSnapshot now={0} mode="dispatcher" />)
    fireEvent.click(dispatchTab())
    fireEvent.change(await screen.findByLabelText('Task'), { target: { value: 'half a thought' } })

    fireEvent.click(screen.getByRole('button', { name: /^.?Fleet/ }))
    fireEvent.click(dispatchTab())

    expect((screen.getByLabelText('Task') as HTMLTextAreaElement).value).toBe('half a thought')
  })

  it('follows Back to the empty hash and returns to Fleet', () => {
    render(<MobileShell runs={[]} connected hasSnapshot now={0} mode="dispatcher" />)
    fireEvent.click(screen.getByRole('button', { name: /workspace/i }))
    act(() => { window.dispatchEvent(new HashChangeEvent('hashchange')) })
    expect(window.location.hash).toBe('#/workspace')

    act(() => { window.location.hash = '' })

    expect(screen.getByRole('button', { name: /^.?Fleet/ }).getAttribute('aria-current')).toBe('true')
  })

  it('restores the tab from the hash on load', () => {
    window.location.hash = '#/workspace'
    render(<MobileShell runs={[]} connected hasSnapshot now={0} mode="dispatcher" />)

    expect(screen.getByRole('button', { name: /workspace/i }).getAttribute('aria-current')).toBe('true')
  })

  it('keeps the prior tab under a run detail and Back returns to it', () => {
    window.location.hash = '#/workspace'
    render(<MobileShell runs={[]} connected hasSnapshot now={0} mode="dispatcher" />)

    act(() => { window.location.hash = '#/fleet/gone/activity' })
    expect(within(screen.getByRole('navigation', { name: 'sections' })).getByRole('button', { name: /workspace/i }).getAttribute('aria-current')).toBe('true')

    fireEvent.click(screen.getAllByRole('button', { name: 'Back to Workspace' })[0])
    expect(window.location.hash).toBe('#/workspace')
  })
})

describe('MobileShell modes', () => {
  beforeEach(() => {
    vi.mocked(fetchDispatchOptions).mockClear()
  })

  it('dispatcher mode has no Crews tab button and lands #/crews on Fleet', () => {
    window.location.hash = '#/crews'
    render(<MobileShell runs={[]} connected hasSnapshot now={0} mode="dispatcher" />)

    expect(within(screen.getByRole('navigation', { name: 'sections' })).queryByRole('button', { name: /crews/i })).toBeNull()
    expect(window.location.hash).toBe('#/fleet')
    expect(screen.getByRole('button', { name: /^.?Fleet/ }).getAttribute('aria-current')).toBe('true')
  })

  it('tmux mode has no Crews or Dispatch and never loads dispatch options', async () => {
    render(<MobileShell runs={[]} connected hasSnapshot now={0} mode="tmux" />)
    await act(async () => {})

    expect(screen.queryByRole('button', { name: /crews/i })).toBeNull()
    expect(screen.queryByRole('button', { name: /dispatch/i })).toBeNull()
    expect(screen.getByRole('button', { name: /^.?Fleet/ })).toBeTruthy()
    expect(screen.getByRole('button', { name: /workspace/i })).toBeTruthy()
    expect(fetchDispatchOptions).not.toHaveBeenCalled()
  })

  it.each(['#/dispatch', '#/dispatch?repo=/r&crew=new'])('tmux mode redirects %s to Fleet', (hash) => {
    window.location.hash = hash
    render(<MobileShell runs={[]} connected hasSnapshot now={0} mode="tmux" />)

    expect(window.location.hash).toBe('#/fleet')
    expect(screen.getByRole('button', { name: /^.?Fleet/ }).getAttribute('aria-current')).toBe('true')
    expect(screen.queryByLabelText('Title')).toBeNull()
  })

  it.each(['#/crews', '#/dispatch?repo=/r&crew=new'])('tmux mode sends a later hashchange to %s back to Fleet', async (hash) => {
    render(<MobileShell runs={[]} connected hasSnapshot now={0} mode="tmux" />)

    await act(async () => {
      window.location.hash = hash
      window.dispatchEvent(new HashChangeEvent('hashchange'))
    })

    expect(window.location.hash).toBe('#/fleet')
    expect(screen.getByRole('button', { name: /^.?Fleet/ }).getAttribute('aria-current')).toBe('true')
  })

  it('an unknown mode rewrites #/crews to Fleet at once and loads no dispatch options', () => {
    window.location.hash = '#/crews'
    render(<MobileShell runs={[]} connected hasSnapshot now={0} mode={null} />)
    expect(window.location.hash).toBe('#/fleet')
    expect(fetchDispatchOptions).not.toHaveBeenCalled()
  })

  it('dispatcher mode stays on #/dispatch', () => {
    window.location.hash = '#/dispatch'
    render(<MobileShell runs={[]} connected hasSnapshot now={0} mode="dispatcher" />)

    expect(window.location.hash).toBe('#/dispatch')
  })
})

describe('MobileShell fleet entries', () => {
  const nowSec = 1_800_000_000
  const base = { agent: 'claude', activity: {}, tokens: { input: 0, output: 0 }, caps: { terminal: true, reply: true, kill: true }, updated_at: nowSec }
  const runs = [
    { ...base, id: 'd', state: 'idle', role: 'dispatcher', branch: 'main', crew: { name: '1-1' } },
    { ...base, id: 'w', state: 'blocked', role: 'worker', branch: 'w-one', crew: { name: '1-1', codename: 'blush' } },
  ] as Run[]

  it('passes the mode down: dispatcher mode nests the worker, tmux mode lists it flat', () => {
    const { container, rerender } = render(<MobileShell runs={runs} connected hasSnapshot now={nowSec * 1000} mode="dispatcher" />)
    expect(container.querySelectorAll('.fleet-group-card .worker-row').length).toBe(1)
    expect(container.querySelectorAll('.run-card').length).toBe(1)

    rerender(<MobileShell runs={runs} connected hasSnapshot now={nowSec * 1000} mode="tmux" />)
    expect(container.querySelector('.fleet-group-card')).toBeNull()
    expect(container.querySelectorAll('.run-card').length).toBe(2)
  })

  it('opens a worker row by writing its run hash', () => {
    const { container } = render(<MobileShell runs={runs} connected hasSnapshot now={nowSec * 1000} mode="dispatcher" />)
    fireEvent.click(container.querySelector('.worker-row-main') as HTMLElement)
    expect(window.location.hash).toBe('#/fleet/w')
  })
})

describe('MobileShell run tabs', () => {
  const nowSec = 1_800_000_000
  const run = {
    id: 'r', agent: 'claude', state: 'idle', branch: 'b', activity: {}, tokens: { input: 0, output: 0 },
    caps: { terminal: true, reply: true, kill: true, chat: true }, updated_at: nowSec,
  } as Run
  const bar = () => screen.getByRole('tablist', { name: 'run tabs' })

  it('swaps the shell tabs for the run tabs while a run is open, and back', () => {
    window.location.hash = '#/fleet/r/chat'
    const { container } = render(<MobileShell runs={[run]} connected hasSnapshot now={nowSec * 1000} mode="dispatcher" />)

    expect(within(bar()).getAllByRole('tab').map((t) => t.textContent)).toEqual(['✉Chat', '▸Terminal'])
    expect(within(bar()).getByRole('tab', { name: /chat/i }).getAttribute('aria-selected')).toBe('true')
    expect(screen.queryByRole('navigation', { name: 'sections' })).toBeNull()
    expect(container.querySelector('.run-detail-tabs')).toBeNull()

    fireEvent.click(within(bar()).getByRole('tab', { name: /terminal/i }))
    expect(window.location.hash).toBe('#/fleet/r/terminal')

    act(() => { window.location.hash = '#/fleet' })
    expect(screen.queryByRole('tablist', { name: 'run tabs' })).toBeNull()
    expect(screen.getByRole('navigation', { name: 'sections' })).toBeTruthy()
  })

  it('hides the bar while the keyboard is up', () => {
    const vv = fakeVisualViewport(window.innerHeight)
    window.location.hash = '#/fleet/r/chat'
    render(<MobileShell runs={[run]} connected hasSnapshot now={nowSec * 1000} mode="dispatcher" />)
    const shell = bar().closest('.shell') as HTMLElement
    expect(shell.classList.contains('keyboard-open')).toBe(false)

    act(() => {
      vv.height = window.innerHeight - 300
      vv.emit()
    })
    expect(shell.classList.contains('keyboard-open')).toBe(true)
    expect(bar().classList.contains('shell-tabs')).toBe(true)
  })
})
