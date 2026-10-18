import { describe, expect, it } from 'vitest'
import type { Run } from '../api/runs'
import { offeredTabs, terminalTarget } from './runTabs'

function run(p: Partial<Run> = {}): Run {
  return {
    id: 'pane-1', agent: 'claude', state: 'idle',
    activity: {}, tokens: { input: 0, output: 0 },
    caps: { terminal: true, reply: true, kill: true },
    updated_at: 1,
    ...p,
  } as Run
}

describe('offeredTabs', () => {
  const peerCaps = { terminal: true, chat: true, reply: false, kill: false }

  it('offers a peer run Chat and Terminal from its forwarded caps, no tmux needed', () => {
    const peer = run({ host: 'b', caps: peerCaps })
    expect(offeredTabs(peer, 'dispatcher')).toEqual(['chat', 'terminal'])
  })

  it('offers a peer dispatcher its Crew tab in dispatcher mode only', () => {
    const peerDispatcher = run({ host: 'b', role: 'dispatcher', crew: { name: '1700-1' }, caps: peerCaps })
    expect(offeredTabs(peerDispatcher, 'dispatcher')).toEqual(['crew', 'chat', 'terminal'])
    expect(offeredTabs(peerDispatcher, 'tmux')).toEqual(['chat', 'terminal'])
  })

  it('offers a stale peer run (caps cleared) no Chat or Terminal tab', () => {
    const stale = run({ host: 'b', stale: true, caps: { terminal: false, chat: false, reply: false, kill: false } })
    expect(offeredTabs(stale, 'dispatcher')).toEqual([])
  })

  it('still offers a local dispatcher its Crew tab', () => {
    const local = run({ role: 'dispatcher', crew: { name: '1700-1' } })
    expect(offeredTabs(local, 'dispatcher')).toEqual(['crew', 'terminal'])
  })
})

describe('terminalTarget', () => {
  it('is true for a local pane or a peer run, false for neither', () => {
    expect(terminalTarget(run({ tmux: { session: 's', window: 0, pane_id: '%1' } }))).toBe(true)
    expect(terminalTarget(run({ host: 'b' }))).toBe(true)
    expect(terminalTarget(run())).toBe(false)
  })
})
