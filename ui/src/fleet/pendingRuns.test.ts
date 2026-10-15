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
  const failure = { error: 'dispatcher exited', output: 'out', crew: '1-2' }

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
