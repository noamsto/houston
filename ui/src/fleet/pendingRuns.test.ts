import { beforeEach, describe, expect, it } from 'vitest'
import {
  PENDING_RUN_TTL_MS,
  RESTART_FAILURE_TTL_MS,
  clearPendingRun,
  clearPendingRuns,
  clearRestartFailure,
  isPendingRun,
  markPendingRun,
  recordRestartFailure,
  relaunchAdvice,
  restartFailure,
} from './pendingRuns'

beforeEach(clearPendingRuns)

describe('pendingRuns', () => {
  it('is pending before the TTL and not after', () => {
    markPendingRun('pane-%7', 1000)
    expect(isPendingRun('pane-%7', 1000)).toBe(true)
    expect(isPendingRun('pane-%7', 1000 + PENDING_RUN_TTL_MS - 1)).toBe(true)
    expect(isPendingRun('pane-%7', 1000 + PENDING_RUN_TTL_MS)).toBe(false)
  })

  it('drops an expired entry for good', () => {
    markPendingRun('a', 0)
    expect(isPendingRun('a', PENDING_RUN_TTL_MS)).toBe(false)
    expect(isPendingRun('a', 1)).toBe(false)
  })

  it('does not know an unmarked id', () => {
    expect(isPendingRun('nope', 0)).toBe(false)
  })

  it('clears a single mark', () => {
    markPendingRun('a', 0)
    clearPendingRun('a')
    expect(isPendingRun('a', 1)).toBe(false)
  })
})

describe('restart failures', () => {
  const failure = { error: 'dispatcher exited', output: 'out', crew: '1-2', status: 422 }

  it('is readable before the TTL and gone after, and an expired entry is dropped for good', () => {
    recordRestartFailure('a', failure, 1000)
    expect(restartFailure('a', 1000)).toEqual(failure)
    expect(restartFailure('a', 1000 + RESTART_FAILURE_TTL_MS - 1)).toEqual(failure)
    expect(restartFailure('a', 1000 + RESTART_FAILURE_TTL_MS)).toBeNull()
    expect(restartFailure('a', 1001)).toBeNull()
  })

  it('can be cleared, and clearPendingRuns clears it too', () => {
    recordRestartFailure('a', failure, 0)
    clearRestartFailure('a')
    expect(restartFailure('a', 1)).toBeNull()
    recordRestartFailure('b', failure, 0)
    clearPendingRuns()
    expect(restartFailure('b', 1)).toBeNull()
  })
})

describe('relaunchAdvice', () => {
  it('names the host of a peer dispatcher', () => {
    const f = { error: 'dispatcher exited', crew: '1-2', host: 'b', status: 422 }
    expect(relaunchAdvice(f, '1-2')).toBe('Relaunch it from a terminal in the repo on b with CREW_ID=1-2 dispatcher.')
    recordRestartFailure('a', f, 0)
    expect(restartFailure('a', 1)?.host).toBe('b')
  })

  it('keeps the local wording without a host', () => {
    expect(relaunchAdvice({ error: 'x', status: 422 }, '1-2')).toBe('Relaunch it from a terminal in the repo with CREW_ID=1-2 dispatcher.')
  })

  it('does not advise a relaunch when the launcher may be running', () => {
    expect(relaunchAdvice({ error: 'x', host: 'b', status: 502 }, '1-2')).toBe(
      'Check Fleet for the new dispatcher before relaunching.',
    )
  })
})
