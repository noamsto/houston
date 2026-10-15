import { beforeEach, describe, expect, it } from 'vitest'
import { PENDING_RUN_TTL_MS, clearPendingRuns, isPendingRun, markPendingRun } from './pendingRuns'

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
})
