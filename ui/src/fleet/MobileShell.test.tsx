import { afterEach, describe, expect, it, vi } from 'vitest'
import { act, cleanup, fireEvent, render, screen, within } from '@testing-library/react'
import { MobileShell } from './MobileShell'

vi.mock('./useWorkspace', () => ({
  useWorkspace: () => ({ workspace: null, error: null, loading: false }),
}))
// DispatchView is kept mounted like Crews, so it fetches as soon as
// MobileShell renders — never a real fetch in tests.
vi.mock('../api/dispatch', () => ({
  NEW_CREW: 'new',
  fetchDispatchOptions: () => Promise.resolve({
    repos: [{ path: '/repo', name: 'repo', crews: ['1-1'] }],
    tiers: ['trivial', 'standard', 'deep'],
    efforts: ['low', 'medium', 'high', 'xhigh', 'max'],
    plans: ['required', 'provided'],
    engines: { claude: ['opus', 'sonnet', 'haiku', 'fable'] },
    engine_order: ['claude'],
    tier_models: { claude: { trivial: 'haiku', standard: 'sonnet', deep: 'opus' } },
  }),
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
    const { container } = render(<MobileShell runs={[]} connected hasSnapshot now={0} />)
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
    render(<MobileShell runs={[]} connected hasSnapshot now={0} />)

    fireEvent.click(dispatchTab())

    expect(await screen.findByLabelText('Title')).toBeTruthy()
  })

  it('opens on the Dispatch tab for a dispatch link', () => {
    window.location.hash = '#/dispatch?repo=%2Frepo&crew=new'
    render(<MobileShell runs={[]} connected hasSnapshot now={0} />)

    expect(dispatchTab().getAttribute('aria-current')).toBe('true')
  })

  it('switches to the Dispatch tab when the hash becomes a dispatch link', () => {
    render(<MobileShell runs={[]} connected hasSnapshot now={0} />)
    expect(dispatchTab().getAttribute('aria-current')).toBeNull()

    // happy-dom fires hashchange itself on a changing hash assignment.
    act(() => { window.location.hash = '#/dispatch?repo=%2Frepo' })

    expect(dispatchTab().getAttribute('aria-current')).toBe('true')
  })

  it('keeps a half-typed task across a switch to Fleet and back', async () => {
    render(<MobileShell runs={[]} connected hasSnapshot now={0} />)
    fireEvent.click(dispatchTab())
    fireEvent.change(await screen.findByLabelText('Task'), { target: { value: 'half a thought' } })

    fireEvent.click(screen.getByRole('button', { name: /^.?Fleet/ }))
    fireEvent.click(dispatchTab())

    expect((screen.getByLabelText('Task') as HTMLTextAreaElement).value).toBe('half a thought')
  })

  it('follows Back to the empty hash and returns to Fleet', () => {
    render(<MobileShell runs={[]} connected hasSnapshot now={0} />)
    fireEvent.click(screen.getByRole('button', { name: /crews/i }))
    act(() => { window.dispatchEvent(new HashChangeEvent('hashchange')) })
    expect(window.location.hash).toBe('#/crews')

    act(() => { window.location.hash = '' })

    expect(screen.getByRole('button', { name: /^.?Fleet/ }).getAttribute('aria-current')).toBe('true')
  })

  it('restores the tab from the hash on load', () => {
    window.location.hash = '#/workspace'
    render(<MobileShell runs={[]} connected hasSnapshot now={0} />)

    expect(screen.getByRole('button', { name: /workspace/i }).getAttribute('aria-current')).toBe('true')
  })

  it('keeps the prior tab under a run detail and Back returns to it', () => {
    window.location.hash = '#/crews'
    render(<MobileShell runs={[]} connected hasSnapshot now={0} />)

    act(() => { window.location.hash = '#/fleet/gone/activity' })
    expect(within(screen.getByRole('navigation', { name: 'sections' })).getByRole('button', { name: /crews/i }).getAttribute('aria-current')).toBe('true')

    fireEvent.click(screen.getAllByRole('button', { name: 'Back to Crews' })[0])
    expect(window.location.hash).toBe('#/crews')
  })
})
