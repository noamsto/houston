import { describe, expect, it } from 'vitest'
import { MAX_TASKS, buildDispatcherRequest, normalizeTask, taskRowsProblem } from './dispatcherForm'

describe('normalizeTask', () => {
  it('matches the shared normalization vectors', () => {
    expect(normalizeTask('  fix\n the  bug\r\n')).toBe('fix the bug')
    expect(normalizeTask('a\tb c')).toBe('a b c')
    expect(normalizeTask('\n\n')).toBe('')
    expect(normalizeTask('👨‍👩‍👧 x')).toBe('👨‍👩‍👧 x')
  })

  it('collapses U+2028 and leaves U+200D intact', () => {
    expect(normalizeTask('a b')).toBe('a b')
    expect(normalizeTask('a‍b')).toBe('a‍b')
  })
})

describe('taskRowsProblem', () => {
  it('accepts a normal set of rows', () => {
    expect(taskRowsProblem(['fix a', 'fix b', ''])).toBeNull()
    expect(taskRowsProblem([])).toBeNull()
  })

  it('rejects more than MAX_TASKS non-empty rows', () => {
    const rows = Array.from({ length: MAX_TASKS + 1 }, (_, i) => `task ${i}`)
    expect(taskRowsProblem(rows)).toMatch(/20/)
    expect(taskRowsProblem(rows.slice(0, MAX_TASKS))).toBeNull()
  })

  it('does not count empty rows toward the limit', () => {
    const rows = [...Array.from({ length: MAX_TASKS }, (_, i) => `task ${i}`), '  ', '']
    expect(taskRowsProblem(rows)).toBeNull()
  })

  it('rejects a row starting with "-" after normalization', () => {
    expect(taskRowsProblem(['-x'])).not.toBeNull()
    expect(taskRowsProblem(['  \n -x'])).not.toBeNull()
    expect(taskRowsProblem(['a -x'])).toBeNull()
  })

  it('rejects a row over 2000 characters', () => {
    expect(taskRowsProblem(['a'.repeat(2001)])).toMatch(/2000/)
    expect(taskRowsProblem(['a'.repeat(2000)])).toBeNull()
  })
})

describe('buildDispatcherRequest', () => {
  it('drops empty rows, normalizes, and keeps model/effort', () => {
    expect(
      buildDispatcherRequest({
        repo: '/r',
        rows: ['  fix\n the  bug ', '   ', '', 'second'],
        engine: 'claude',
        model: 'opus',
        effort: 'high',
      }),
    ).toEqual({ repo: '/r', tasks: ['fix the bug', 'second'], engine: 'claude', model: 'opus', effort: 'high' })
  })

  it('omits blank model and effort', () => {
    const req = buildDispatcherRequest({ repo: '/r', rows: ['x'], engine: 'pi', model: '', effort: '' })
    expect(req).toEqual({ repo: '/r', tasks: ['x'], engine: 'pi' })
    expect('model' in req).toBe(false)
    expect('effort' in req).toBe(false)
  })
})
