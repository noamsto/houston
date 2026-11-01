import type { QuestionAnswer, StepAnswer } from '../api/answer'

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

/** Seeds a stage from the pane's selection; Other is on when it holds text. */
export function stageFromSelection(options: number[] | undefined, text: string | undefined, multi: boolean): Stage {
  const sorted = [...(options ?? [])].sort((a, b) => a - b)
  return { options: multi ? sorted : sorted.slice(0, 1), otherOn: !!text, text: text ?? '' }
}

export const setText = (s: Stage, text: string): Stage => ({ ...s, text })

const hasText = (s: Stage) => s.otherOn && s.text.trim() !== ''

export function isStageComplete(s: Stage): boolean {
  if (s.otherOn && !hasText(s)) return false
  return s.options.length > 0 || hasText(s)
}

export function isComplete(stages: Stage[]): boolean {
  return stages.length > 0 && stages.every(isStageComplete)
}

export function toAnswer(s: Stage): StepAnswer {
  const answer: StepAnswer = { options: s.options }
  if (hasText(s)) answer.text = s.text.trim()
  return answer
}

export function toWire(stages: Stage[]): QuestionAnswer[] {
  return stages.map((s, question) => {
    const answer: QuestionAnswer = { question, options: s.options }
    if (hasText(s)) answer.text = s.text.trim()
    return answer
  })
}
