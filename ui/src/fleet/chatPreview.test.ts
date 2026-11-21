import { describe, expect, it } from 'vitest'
import { previewText } from './chatPreview'

describe('previewText', () => {
  it('returns short text unchanged', () => {
    expect(previewText('hello\nworld')).toEqual({ preview: 'hello\nworld', truncated: false })
  })

  it('truncates more than 8 lines to the first 6', () => {
    const text = Array.from({ length: 9 }, (_, i) => `line${i + 1}`).join('\n')
    const r = previewText(text)
    expect(r.truncated).toBe(true)
    expect(r.preview).toBe('line1\nline2\nline3\nline4\nline5\nline6…')
  })

  it('cuts a long single line at a word boundary within 400 chars', () => {
    const text = 'abcdefg '.repeat(100)
    const r = previewText(text)
    expect(r.truncated).toBe(true)
    expect(r.preview.length).toBeLessThanOrEqual(401)
    expect(r.preview.endsWith('abcdefg…')).toBe(true)
  })

  it('hard-cuts a long line with no spaces', () => {
    const r = previewText('x'.repeat(700))
    expect(r.truncated).toBe(true)
    expect(r.preview).toBe('x'.repeat(400) + '…')
  })

  it('does not truncate text exactly at the thresholds', () => {
    const eight = Array.from({ length: 8 }, () => 'a').join('\n')
    expect(previewText(eight).truncated).toBe(false)
    expect(previewText('y'.repeat(600)).truncated).toBe(false)
    expect(previewText('y'.repeat(601)).truncated).toBe(true)
  })
})
