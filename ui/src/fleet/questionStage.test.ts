import { describe, expect, it } from 'vitest'
import {
  emptyStage,
  isStageComplete,
  setText,
  stageFromSelection,
  toAnswer,
  toggleOption,
  toggleOther,
} from './questionStage'

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

describe('stageFromSelection', () => {
  it('seeds options and Other from the pane selection', () => {
    expect(stageFromSelection([0, 2], 'mine', true)).toEqual({ options: [0, 2], otherOn: true, text: 'mine' })
  })

  it('treats missing options and text as an empty stage', () => {
    expect(stageFromSelection(undefined, undefined, false)).toEqual(emptyStage())
  })

  it('sorts a copy of the options', () => {
    const options = [2, 0, 1]
    expect(stageFromSelection(options, undefined, true).options).toEqual([0, 1, 2])
    expect(options).toEqual([2, 0, 1])
  })

  it('single-select keeps at most the first option', () => {
    expect(stageFromSelection([1, 2], undefined, false).options).toEqual([1])
  })

  it('Other is on only when the text is non-empty', () => {
    expect(stageFromSelection([0], '', true).otherOn).toBe(false)
    expect(stageFromSelection([0], 'x', true).otherOn).toBe(true)
  })
})

describe('toAnswer', () => {
  it('sends options and trimmed Other text without a question field', () => {
    const other = setText(toggleOther(toggleOption(emptyStage(), 1, true), true), '  mine ')
    expect(toAnswer(other)).toEqual({ options: [1], text: 'mine' })
  })

  it('omits text when Other is off or blank', () => {
    expect(toAnswer(toggleOption(setText(toggleOther(emptyStage(), false), 'stale'), 0, false))).toEqual({ options: [0] })
    expect(toAnswer(setText(toggleOther(toggleOption(emptyStage(), 0, true), true), '  '))).toEqual({ options: [0] })
  })
})

describe('isStageComplete', () => {
  it('needs an option or non-blank Other text, and a blank Other blocks', () => {
    expect(isStageComplete(emptyStage())).toBe(false)
    expect(isStageComplete(toggleOption(emptyStage(), 0, false))).toBe(true)
    expect(isStageComplete(toggleOther(toggleOption(emptyStage(), 0, true), true))).toBe(false)
    expect(isStageComplete(setText(toggleOther(emptyStage(), false), 'x'))).toBe(true)
  })
})
