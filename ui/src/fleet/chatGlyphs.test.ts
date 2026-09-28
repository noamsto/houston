import { describe, expect, it } from 'vitest'
import { KIND_GLYPH, TOOL_KINDS, kindGlyph, statusGlyph } from './chatGlyphs'

// Any codepoint with an emoji form, text-default or not.
const HAS_EMOJI_FORM = /\p{Emoji}|\p{Extended_Pictographic}/u

describe('chatGlyphs', () => {
  it('has a distinct glyph for every ACP tool kind', () => {
    const glyphs = TOOL_KINDS.map((k) => KIND_GLYPH[k])
    expect(glyphs.every((g) => g.length > 0)).toBe(true)
    expect(new Set(glyphs).size).toBe(TOOL_KINDS.length)
  })

  it('falls back to the "other" glyph for an unknown or missing kind', () => {
    expect(kindGlyph('switch_mode')).toBe(KIND_GLYPH.other)
    expect(kindGlyph('constructor')).toBe(KIND_GLYPH.other)
    expect(kindGlyph(undefined)).toBe(KIND_GLYPH.other)
    expect(kindGlyph('execute')).toBe(KIND_GLYPH.execute)
  })

  it('maps call statuses to glyphs', () => {
    expect(statusGlyph('completed')).toBe('✓')
    expect(statusGlyph('failed')).toBe('✗')
    expect(statusGlyph('pending')).toBe('◌')
    expect(statusGlyph('in_progress')).toBe('◌')
    expect(statusGlyph('weird')).toBeUndefined()
  })

  it('uses no codepoint that has an emoji form', () => {
    const all = [...Object.values(KIND_GLYPH), '✓', '✗', '◌', '✦', '⑂', '●', '⋯']
    for (const g of all) expect(g).not.toMatch(HAS_EMOJI_FORM)
  })
})
