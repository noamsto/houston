import { afterEach, describe, expect, it, vi } from 'vitest'
import { act, cleanup, render } from '@testing-library/react'
import { MobileShell } from './MobileShell'

vi.mock('./useWorkspace', () => ({
  useWorkspace: () => ({ workspace: null, error: null, loading: false }),
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
