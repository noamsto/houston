import { describe, expect, it } from 'vitest'
import { emptyStage, isComplete, setText, toggleOption, toggleOther, toWire } from './questionStage'

describe('single-select staging', () => {
  it('choosing an option replaces the previous one', () => {
    const s = toggleOption(toggleOption(emptyStage(), 0, false), 2, false)
    expect(s.options).toEqual([2])
  })

  it('re-tapping the chosen option keeps it', () => {
    expect(toggleOption(toggleOption(emptyStage(), 1, false), 1, false).options).toEqual([1])
  })

  it('an option clears Other, and Other clears the option', () => {
    const other = setText(toggleOther(toggleOption(emptyStage(), 1, false), false), 'mine')
    expect(other).toEqual({ options: [], otherOn: true, text: 'mine' })
    expect(toggleOption(other, 0, false)).toEqual({ options: [0], otherOn: false, text: 'mine' })
  })
})

describe('multi-select staging', () => {
  it('toggles options, keeping them ascending', () => {
    let s = emptyStage()
    for (const i of [2, 0, 1]) s = toggleOption(s, i, true)
    expect(s.options).toEqual([0, 1, 2])
    expect(toggleOption(s, 1, true).options).toEqual([0, 2])
  })

  it('Other toggles independently of the options', () => {
    const s = toggleOther(toggleOption(emptyStage(), 1, true), true)
    expect(s).toEqual({ options: [1], otherOn: true, text: '' })
    expect(toggleOther(s, true).otherOn).toBe(false)
  })
})

describe('isComplete', () => {
  it('needs every question answered', () => {
    expect(isComplete([])).toBe(false)
    expect(isComplete([toggleOption(emptyStage(), 0, false), emptyStage()])).toBe(false)
    expect(isComplete([toggleOption(emptyStage(), 0, false), toggleOption(emptyStage(), 1, true)])).toBe(true)
  })

  it('Other needs non-blank text', () => {
    const on = toggleOther(emptyStage(), false)
    expect(isComplete([on])).toBe(false)
    expect(isComplete([setText(on, '   ')])).toBe(false)
    expect(isComplete([setText(on, 'x')])).toBe(true)
  })

  it('a multi-select with an option and a blank Other is incomplete', () => {
    expect(isComplete([toggleOther(toggleOption(emptyStage(), 0, true), true)])).toBe(false)
  })

  it('a multi-select accepts options and Other together', () => {
    expect(isComplete([setText(toggleOther(toggleOption(emptyStage(), 0, true), true), 'x')])).toBe(true)
  })
})

describe('toWire', () => {
  it('numbers the questions and trims the Other text', () => {
    const other = setText(toggleOther(toggleOption(emptyStage(), 1, true), true), '  mine ')
    expect(toWire([toggleOption(emptyStage(), 0, false), other])).toEqual([
      { question: 0, options: [0] },
      { question: 1, options: [1], text: 'mine' },
    ])
  })

  it('omits text when Other is off, even if text remains', () => {
    const s = toggleOption(setText(toggleOther(emptyStage(), false), 'stale'), 0, false)
    expect(toWire([s])).toEqual([{ question: 0, options: [0] }])
  })
})
