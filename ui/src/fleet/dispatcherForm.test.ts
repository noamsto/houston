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

describe('normalizeTask Go parity', () => {
  it('collapses U+0085 and the other unicode.IsSpace members', () => {
    expect(normalizeTask('a\u0085b')).toBe('a b')
    expect(normalizeTask('a\u00a0\u1680\u2003\u202f\u205f\u3000b')).toBe('a b')
    expect(normalizeTask('\u0085x\u2029')).toBe('x')
  })

  it('keeps U+FEFF, which Go does not treat as space', () => {
    expect(normalizeTask('a\ufeffb')).toBe('a\ufeffb')
    expect(normalizeTask('\ufeffx')).toBe('\ufeffx')
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

  it('rejects a leading "-" hidden behind U+0085', () => {
    expect(taskRowsProblem(['\u0085-x'])).toMatch(/cannot start with "-"/)
  })

  it('rejects a control character, naming the row by its form label', () => {
    expect(taskRowsProblem(['ok', '', 'a\u001bb'])).toBe('Task 3 contains a control character')
    expect(taskRowsProblem(['a\u007fb'])).toMatch(/control character/)
    expect(taskRowsProblem(['a\u0000b'])).toMatch(/control character/)
  })

  it('numbers a start-with-dash problem by the raw row, not the post-drop index', () => {
    expect(taskRowsProblem(['', '', '-x'])).toBe('Task 3 cannot start with "-"')
  })

  it('caps the composed prompt at 8 KiB, composing like the server', () => {
    const four = Array(4).fill('a'.repeat(2000)) // "4 tasks:" + 4 * (" (n) " + 2000) = 8028
    expect(taskRowsProblem([...four, 'a'.repeat(159)])).toBeNull() // exactly 8192
    expect(taskRowsProblem([...four, 'a'.repeat(160)])).toMatch(/too long together \(8 KB max\)/)
  })

  it('counts bytes, not characters, toward the 8 KiB cap', () => {
    const rows = Array.from({ length: 3 }, () => 'é'.repeat(1500))
    expect(taskRowsProblem(rows)).toMatch(/too long together/)
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
