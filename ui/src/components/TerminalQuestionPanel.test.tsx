import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { TerminalQuestionPanel } from './TerminalQuestionPanel'
import type { QuestionState } from '../api/answer'
import type { ToolCall } from '../fleet/chatModel'

const call: ToolCall = { toolCallId: 'q1', tool: 'AskUserQuestion', status: 'in_progress', seq: 1 }

const opt = (label: string) => ({ label })
const questions: QuestionState['questions'] = [
  { question: 'Which color?', header: 'Color', multiSelect: false, preview: false, options: [opt('Red'), opt('Blue')] },
  { question: 'Which size?', header: 'Size', multiSelect: false, preview: false, options: [opt('Small'), opt('Large')] },
]
const at = (step: number, extra: Partial<QuestionState> = {}): QuestionState => ({ tabbed: true, step, count: 2, questions, ...extra })

type Body = { kind: string; index?: number; answer?: unknown; review?: string[] }

function install(handler: (body: Body) => { status: number; body?: unknown }) {
  const mock = vi.fn(async (_url: string, init?: RequestInit) => {
    const r = handler(JSON.parse(String(init?.body)) as Body)
    return { status: r.status, ok: r.status < 300, text: async () => '', json: async () => r.body } as Response
  })
  vi.stubGlobal('fetch', mock)
  return mock
}

const posts = (mock: ReturnType<typeof install>, kind: string): Body[] =>
  mock.mock.calls.map(([, init]) => JSON.parse(String(init?.body)) as Body).filter((b) => b.kind === kind)

const panel = (c: HTMLElement) => c.querySelector<HTMLElement>('.terminal-question-panel')!

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

describe('TerminalQuestionPanel', () => {
  it('reports interactive and shows the step when the pane reports the dialog', async () => {
    install(() => ({ status: 200, body: at(0) }))
    const onInteractive = vi.fn()
    const { container } = render(<TerminalQuestionPanel runId="r1" call={call} onInteractive={onInteractive} />)
    await screen.findByText('1 of 2')
    await waitFor(() => expect(panel(container).hidden).toBe(false))
    expect(onInteractive).toHaveBeenLastCalledWith(true)
  })

  it('stays hidden and not interactive when the pane shows no dialog (404)', async () => {
    const mock = install(() => ({ status: 404 }))
    const onInteractive = vi.fn()
    const { container } = render(<TerminalQuestionPanel runId="r1" call={call} onInteractive={onInteractive} />)
    await waitFor(() => expect(mock).toHaveBeenCalled())
    expect(panel(container).hidden).toBe(true)
    expect(onInteractive).not.toHaveBeenCalledWith(true)
  })

  it('Next, Back and Submit post the same bodies as the Chat wizard', async () => {
    const mock = install((b) => {
      if (b.kind === 'step') return { status: 200, body: b.index === 0 ? at(1) : at(2, { review: ['Blue', 'Large'] }) }
      if (b.kind === 'back') return { status: 200, body: at(0) }
      if (b.kind === 'submit') return { status: 200, body: at(2, { review: ['Blue', 'Large'] }) }
      return { status: 200, body: at(0) }
    })
    render(<TerminalQuestionPanel runId="r1" call={call} onInteractive={() => {}} />)
    await screen.findByText('1 of 2')
    fireEvent.click(screen.getByRole('radio', { name: /Blue/ }))
    fireEvent.click(screen.getByRole('button', { name: 'Next' }))
    await screen.findByText('2 of 2')
    expect(posts(mock, 'step')).toEqual([{ kind: 'step', toolCallId: 'q1', index: 0, answer: { options: [1] } }])

    fireEvent.click(screen.getByRole('button', { name: 'Back' }))
    await screen.findByText('1 of 2')
    expect(posts(mock, 'back')).toEqual([{ kind: 'back', toolCallId: 'q1', index: 1 }])

    fireEvent.click(screen.getByRole('radio', { name: /Blue/ }))
    fireEvent.click(screen.getByRole('button', { name: 'Next' }))
    await screen.findByText('2 of 2')
    fireEvent.click(screen.getByRole('radio', { name: /Large/ }))
    fireEvent.click(screen.getByRole('button', { name: 'Review' }))
    const submit = await screen.findByRole('button', { name: /Submit/ })
    fireEvent.click(submit)
    await waitFor(() => expect(posts(mock, 'submit')).toEqual([{ kind: 'submit', toolCallId: 'q1', review: ['Blue', 'Large'] }]))
  })

  it('collapses behind a toggle but keeps the wizard mounted', async () => {
    install(() => ({ status: 200, body: at(0) }))
    render(<TerminalQuestionPanel runId="r1" call={call} onInteractive={() => {}} />)
    const toggle = await screen.findByRole('button', { name: 'Hide question' })
    expect(toggle.getAttribute('aria-expanded')).toBe('true')
    fireEvent.click(toggle)
    const show = screen.getByRole('button', { name: 'Show question' })
    expect(show.getAttribute('aria-expanded')).toBe('false')
    expect(screen.getByText('1 of 2').closest('[hidden]')).toBeTruthy()
  })
})
