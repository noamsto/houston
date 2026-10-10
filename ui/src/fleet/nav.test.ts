import { act, cleanup, renderHook } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import type { Run } from '../api/runs'
import { installFakeHistory, settle, type FakeHistory } from '../testing/fakeHistory'
import { back, backLabel, navEntry, openRun, popToRoot, rootLabel, selectRun, switchRunTab } from './nav'
import { useDispatchRoute, useShellTab, navigateHash } from './routes'

let fake: FakeHistory
let uninstall: () => void

beforeEach(() => {
  window.location.hash = '#/fleet'
  ;({ history: fake, uninstall } = installFakeHistory())
})

afterEach(() => {
  cleanup()
  uninstall()
  window.location.hash = ''
})

const pop = async (fn: () => void) => {
  fn()
  await act(async () => { await settle() })
}

describe('navigation stack', () => {
  it('run tab switches add no entry and one Back returns to the list', async () => {
    const before = fake.length
    openRun('r', 'chat')
    switchRunTab('r', 'terminal')
    switchRunTab('r', 'chat')
    switchRunTab('r', 'terminal')
    expect(fake.length).toBe(before + 1)
    expect(window.location.hash).toBe('#/fleet/r/terminal')

    await pop(() => back('#/fleet'))
    expect(window.location.hash).toBe('#/fleet')
  })

  it('a worker opened from a dispatcher backs to the dispatcher tab, then the list', async () => {
    openRun('disp')
    switchRunTab('disp', 'chat')
    openRun('worker')
    expect(navEntry()?.depth).toBe(2)

    await pop(() => back('#/fleet'))
    expect(window.location.hash).toBe('#/fleet/disp/chat')
    await pop(() => back('#/fleet'))
    expect(window.location.hash).toBe('#/fleet')
  })

  it('Back on a deep-linked run replaces to the root without adding or leaving entries', async () => {
    fake.replaceState(null, '', '#/fleet/x')
    const length = fake.length
    back('#/fleet')
    expect(window.location.hash).toBe('#/fleet')
    expect(fake.length).toBe(length)
    const index = fake.index
    fake.forward()
    await act(async () => { await settle() })
    expect(fake.index).toBe(index)
  })

  it('popToRoot from depth 2 lands on the root in one traversal', async () => {
    openRun('disp')
    openRun('worker')
    await pop(() => popToRoot('#/fleet'))
    expect(window.location.hash).toBe('#/fleet')
    expect(fake.index).toBe(0)
  })

  it('popToRoot on a stack that started on a deep-linked run replaces to the root', () => {
    fake.replaceState(null, '', '#/fleet/x')
    openRun('y')
    expect(navEntry()).toEqual({ depth: 1, from: '#/fleet/x', root: null })
    popToRoot('#/workspace')
    expect(window.location.hash).toBe('#/workspace')
  })

  it('re-tapping the current shell tab adds no entry; another tab adds one', () => {
    const { result } = renderHook(() => useShellTab('dispatcher'))
    const length = fake.length
    act(() => result.current[1]('fleet', true))
    expect(fake.length).toBe(length)
    act(() => result.current[1]('workspace', true))
    expect(fake.length).toBe(length + 1)
    expect(window.location.hash).toBe('#/workspace')
  })

  it('re-opening the run already shown replaces and keeps the stack entry', () => {
    openRun('r', 'chat')
    const length = fake.length
    const entry = navEntry()
    openRun('r', 'terminal')
    expect(fake.length).toBe(length)
    expect(navEntry()).toEqual(entry)
    expect(window.location.hash).toBe('#/fleet/r/terminal')
  })

  it('selectRun pushes from a list and replaces laterally from a shown run', async () => {
    const length = fake.length
    selectRun('a')
    expect(fake.length).toBe(length + 1)
    selectRun('b')
    expect(fake.length).toBe(length + 1)
    expect(window.location.hash).toBe('#/fleet/b')
    await pop(() => back('#/fleet'))
    expect(window.location.hash).toBe('#/fleet')
  })

  it('a second Back while a pop is in flight is ignored until popstate', async () => {
    openRun('disp')
    openRun('worker')
    back('#/fleet')
    back('#/fleet')
    await act(async () => { await settle() })
    expect(window.location.hash).toBe('#/fleet/disp')
    await pop(() => back('#/fleet'))
    expect(window.location.hash).toBe('#/fleet')
  })

  it('useDispatchRoute still bumps seq on navigateHash changes', () => {
    const { result } = renderHook(() => useDispatchRoute())
    act(() => navigateHash('pushState', null, '#/dispatch?repo=%2Fr'))
    expect(result.current).toEqual({ repo: '/r', crew: undefined, seq: 1 })
    act(() => navigateHash('replaceState', null, '#/dispatch?repo=%2Fr&crew=new'))
    expect(result.current?.seq).toBe(2)
  })
})

describe('labels', () => {
  const runs = [{ id: 'd', branch: 'main', crew: { name: '1-1', codename: 'iris' } }, { id: 'e', branch: 'feat' }] as Run[]

  it('names the parent run by codename, else the shell tab', () => {
    expect(backLabel({ depth: 1, from: '#/fleet/d/chat', root: '#/fleet' }, runs, 'fleet')).toBe('iris')
    expect(backLabel({ depth: 1, from: '#/fleet/e', root: '#/fleet' }, runs, 'fleet')).toBe('feat')
    expect(backLabel({ depth: 1, from: '#/workspace', root: '#/workspace' }, runs, 'fleet')).toBe('Workspace')
    expect(backLabel(null, runs, 'dispatch')).toBe('Dispatch')
  })

  it('names the root by its shell tab', () => {
    expect(rootLabel({ depth: 2, from: '#/fleet/d', root: '#/workspace' }, 'fleet')).toBe('Workspace')
    expect(rootLabel(null, 'fleet')).toBe('Fleet')
  })
})
