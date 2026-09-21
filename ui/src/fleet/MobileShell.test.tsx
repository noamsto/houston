import { afterEach, describe, expect, it, vi } from 'vitest'
import { act, cleanup, fireEvent, render, screen } from '@testing-library/react'
import { MobileShell } from './MobileShell'

vi.mock('./useWorkspace', () => ({
  useWorkspace: () => ({ workspace: null, error: null, loading: false }),
}))
// DispatchView is kept mounted like Crews, so it fetches as soon as
// MobileShell renders — never a real fetch in tests.
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
})

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

    fireEvent.click(screen.getByRole('button', { name: /dispatch/i }))

    expect(await screen.findByLabelText('Title')).toBeTruthy()
  })
})
