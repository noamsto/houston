import type { QuestionAnswer } from '../api/answer'

/** One question's staged answer; `options` holds ascending 0-based indexes. */
export interface Stage {
  options: number[]
  otherOn: boolean
  text: string
}

export const emptyStage = (): Stage => ({ options: [], otherOn: false, text: '' })

/** Single-select is exclusive: an option clears Other, and re-tapping the
 *  chosen option keeps it chosen (a radio never unselects). */
export function toggleOption(s: Stage, index: number, multi: boolean): Stage {
  if (!multi) return { ...s, options: [index], otherOn: false }
  const options = s.options.includes(index) ? s.options.filter((i) => i !== index) : [...s.options, index].sort((a, b) => a - b)
  return { ...s, options }
}

export function toggleOther(s: Stage, multi: boolean): Stage {
  if (!multi) return { ...s, options: [], otherOn: true }
  return { ...s, otherOn: !s.otherOn }
}

export const setText = (s: Stage, text: string): Stage => ({ ...s, text })

const hasText = (s: Stage) => s.otherOn && s.text.trim() !== ''

function isStageComplete(s: Stage): boolean {
  if (s.otherOn && !hasText(s)) return false
  return s.options.length > 0 || hasText(s)
}

export function isComplete(stages: Stage[]): boolean {
  return stages.length > 0 && stages.every(isStageComplete)
}

export function toWire(stages: Stage[]): QuestionAnswer[] {
  return stages.map((s, question) => {
    const answer: QuestionAnswer = { question, options: s.options }
    if (hasText(s)) answer.text = s.text.trim()
    return answer
  })
}
