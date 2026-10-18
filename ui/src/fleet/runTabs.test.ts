import { describe, expect, it } from 'vitest'
import type { Run } from '../api/runs'
import { offeredTabs } from './runTabs'

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
  const peerDispatcher = run({
    host: 'b', role: 'dispatcher', crew: { name: '1700-1' },
    caps: { terminal: false, reply: false, kill: false },
  })

  it('offers a peer dispatcher no tabs in either mode', () => {
    expect(offeredTabs(peerDispatcher, 'dispatcher')).toEqual([])
    expect(offeredTabs(peerDispatcher, 'tmux')).toEqual([])
  })

  it('still offers a local dispatcher its Crew tab', () => {
    const local = run({ role: 'dispatcher', crew: { name: '1700-1' } })
    expect(offeredTabs(local, 'dispatcher')).toEqual(['crew', 'terminal'])
  })
})
