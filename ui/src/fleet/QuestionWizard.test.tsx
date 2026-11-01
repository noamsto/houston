import { afterEach, describe, expect, it, vi } from 'vitest'
import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { QuestionWizard } from './QuestionWizard'
import type { QuestionState, StateQuestion } from '../api/answer'
import type { ToolCall } from './chatModel'

const call: ToolCall = { toolCallId: 'q1', tool: 'AskUserQuestion', status: 'in_progress', seq: 1 }

const opt = (label: string) => ({ label })
const questions: StateQuestion[] = [
  { question: 'Which color?', header: 'Color', multiSelect: false, preview: false, options: [opt('Red'), opt('Blue'), opt('Green')] },
  { question: 'Which toppings?', header: 'Toppings', multiSelect: true, preview: false, options: [opt('Cheese'), opt('Olives'), opt('Basil')] },
  { question: 'Which size?', header: 'Size', multiSelect: false, preview: false, options: [opt('Small'), opt('Large')] },
]

const at = (step: number, extra: Partial<QuestionState> = {}): QuestionState => ({ tabbed: true, step, count: 3, questions, ...extra })
const review = (): QuestionState => at(3, { review: ['Blue', 'Cheese, extra pepper', 'Large'] })

interface Reply { status: number; body?: unknown; text?: string }
type Body = { kind: string; index?: number; answer?: unknown; review?: string[] }

function install(handler: (body: Body, n: number) => Reply | Promise<Reply>) {
  const counts = new Map<string, number>()
  const mock = vi.fn(async (_url: string, init?: RequestInit) => {
    const body = JSON.parse(String(init?.body)) as Body
    const n = (counts.get(body.kind) ?? 0) + 1
    counts.set(body.kind, n)
    const r = await handler(body, n)
    return {
      status: r.status,
      ok: r.status < 300,
      text: async () => r.text ?? '',
      json: async () => r.body,
    } as Response
  })
  vi.stubGlobal('fetch', mock)
  return mock
}

const posts = (mock: ReturnType<typeof install>, kind: string): Body[] =>
  mock.mock.calls.map(([, init]) => JSON.parse(String(init?.body)) as Body).filter((b) => b.kind === kind)

const ok = (body: unknown): Reply => ({ status: 200, body })

const renderWizard = (fallbackQuestions: StateQuestion[] = []) =>
  render(<QuestionWizard runId="r1" call={call} fallbackQuestions={fallbackQuestions} onLayout={() => {}} />)

const next = () => screen.getByRole('button', { name: /^(Next|Review)$/ }) as HTMLButtonElement
const back = () => screen.getByRole('button', { name: 'Back' }) as HTMLButtonElement

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  vi.useRealTimers()
})

describe('QuestionWizard steps', () => {
  it('Next posts the staged answer for the step and moves to the returned step', async () => {
    const mock = install((b) => ok(b.kind === 'step' ? at(1) : at(0)))
    renderWizard()
    await screen.findByText('1 of 3')
    expect(next().disabled).toBe(true)
    expect(screen.queryByRole('button', { name: 'Back' })).toBeNull()

    fireEvent.click(screen.getByRole('radio', { name: /Blue/ }))
    expect(next().disabled).toBe(false)
    expect(next().getAttribute('class')).toContain('chat-question-wizard-next')
    fireEvent.click(next())

    await screen.findByText('2 of 3')
    expect(posts(mock, 'step')).toEqual([{ kind: 'step', toolCallId: 'q1', index: 0, answer: { options: [1] } }])
    const current = screen.getByText('Toppings', { selector: 'li' })
    expect(current.getAttribute('aria-current')).toBe('step')
    expect(screen.getByText('Color', { selector: 'li' }).getAttribute('aria-current')).toBeNull()
    expect(screen.getByRole('group', { name: 'Which toppings?' })).toBeTruthy()
  })

  it('labels the last step Next as Review', async () => {
    install(() => ok(at(2)))
    renderWizard()
    await screen.findByText('3 of 3')
    expect(screen.getByRole('button', { name: 'Review' })).toBeTruthy()
    expect(screen.queryByRole('button', { name: 'Next' })).toBeNull()
  })

  it('Back posts back and shows the selection the pane reports', async () => {
    const mock = install((b) => ok(b.kind === 'back' ? at(0, { options: [1] }) : at(1)))
    renderWizard()
    await screen.findByText('2 of 3')
    fireEvent.click(back())
    await screen.findByText('1 of 3')
    expect(posts(mock, 'back')).toEqual([{ kind: 'back', toolCallId: 'q1', index: 1 }])
    expect(screen.getByRole('radio', { name: /Blue/ }).getAttribute('aria-checked')).toBe('true')
    expect(next().disabled).toBe(false)
  })

  it("keeps the client's exact Other text when the pane's text only differs in whitespace", async () => {
    install((b) => {
      if (b.kind === 'step') return ok(at(1))
      if (b.kind === 'back') return ok(at(0, { options: [], text: 'no onions' }))
      return ok(at(0))
    })
    renderWizard()
    await screen.findByText('1 of 3')
    fireEvent.click(screen.getByRole('radio', { name: 'Other' }))
    fireEvent.change(screen.getByLabelText('Other answer: Which color?'), { target: { value: 'no  onions' } })
    fireEvent.click(next())
    await screen.findByText('2 of 3')
    fireEvent.click(back())
    await screen.findByText('1 of 3')
    expect((screen.getByLabelText('Other answer: Which color?') as HTMLInputElement).value).toBe('no  onions')
  })

  it('disables the controls while a step request is in flight', async () => {
    let release: (r: Reply) => void = () => {}
    install((b) => (b.kind === 'step' ? new Promise<Reply>((res) => { release = res }) : ok(at(0))))
    renderWizard()
    await screen.findByText('1 of 3')
    fireEvent.click(screen.getByRole('radio', { name: /Red/ }))
    fireEvent.click(next())
    expect(next().disabled).toBe(true)
    expect((screen.getByRole('radio', { name: /Red/ }) as HTMLButtonElement).disabled).toBe(true)
    await act(async () => { release(ok(at(1))) })
    await screen.findByText('2 of 3')
  })
})

describe('QuestionWizard focus', () => {
  it('keeps focus inside the wizard after Next resolves', async () => {
    install((b) => ok(b.kind === 'step' ? at(1) : at(0)))
    const { container } = renderWizard()
    await screen.findByText('1 of 3')
    fireEvent.click(screen.getByRole('radio', { name: /Blue/ }))
    next().focus()
    fireEvent.click(next())
    await screen.findByText('2 of 3')
    await waitFor(() => expect(screen.getByRole('group', { name: 'Which toppings?' }).contains(document.activeElement)).toBe(true))
    expect(container.contains(document.activeElement)).toBe(true)
  })

  it('moves focus into the review screen after the last Review', async () => {
    install((b) => ok(b.kind === 'step' ? review() : at(2)))
    const { container } = renderWizard()
    await screen.findByText('3 of 3')
    fireEvent.click(screen.getByRole('radio', { name: /Small/ }))
    next().focus()
    fireEvent.click(next())
    await screen.findByText('Review your answers')
    await waitFor(() => expect(screen.getByRole('button', { name: /Color/ })).toBe(document.activeElement))
    expect(container.contains(document.activeElement)).toBe(true)
  })

  it('does not take focus from elsewhere on the page', async () => {
    install((b) => ok(b.kind === 'step' ? at(1) : at(0)))
    const outside = document.createElement('input')
    document.body.appendChild(outside)
    renderWizard()
    await screen.findByText('1 of 3')
    fireEvent.click(screen.getByRole('radio', { name: /Blue/ }))
    outside.focus()
    fireEvent.click(next())
    await screen.findByText('2 of 3')
    expect(document.activeElement).toBe(outside)
    outside.remove()
  })
})

describe('QuestionWizard review', () => {
  it('lists the pane review strings and submits them', async () => {
    const mock = install((b) => (b.kind === 'submit' ? { status: 204 } : ok(review())))
    renderWizard()
    const row = await screen.findByRole('button', { name: /Toppings/ })
    expect(row.textContent).toContain('Cheese, extra pepper')
    expect(screen.getByRole('button', { name: /Color/ }).textContent).toContain('Blue')
    expect(screen.getByRole('button', { name: /Size/ }).textContent).toContain('Large')

    fireEvent.click(screen.getByRole('button', { name: 'Submit' }))
    await screen.findByText('Sent — waiting for Claude')
    expect(posts(mock, 'submit')).toEqual([
      { kind: 'submit', toolCallId: 'q1', review: ['Blue', 'Cheese, extra pepper', 'Large'] },
    ])
    expect((screen.getByRole('button', { name: 'Submit' }) as HTMLButtonElement).disabled).toBe(true)
  })

  it('tapping a row posts back once per tab in between and lands on that step', async () => {
    const mock = install((b) => {
      if (b.kind === 'back') return ok(at(b.index! - 1, { options: [0] }))
      return ok(review())
    })
    renderWizard()
    fireEvent.click(await screen.findByRole('button', { name: /Toppings/ }))
    await screen.findByText('2 of 3')
    expect(posts(mock, 'back').map((b) => b.index)).toEqual([3, 2])
    expect(screen.getByText('Toppings', { selector: 'li' }).getAttribute('aria-current')).toBe('step')
  })

  it('stops at the first failing back and shows the step it reached', async () => {
    const mock = install((b) => {
      if (b.kind === 'back') return b.index === 3 ? ok(at(2)) : { status: 409, text: 'prompt changed' }
      return ok(review())
    })
    renderWizard()
    fireEvent.click(await screen.findByRole('button', { name: /Color/ }))
    await screen.findByText('Going back from step 3: the terminal moved on — nothing was typed')
    expect(posts(mock, 'back').map((b) => b.index)).toEqual([3, 2])
    expect(screen.getByRole('link', { name: 'Terminal tab' }).getAttribute('href')).toBe('#/fleet/r1/terminal')
  })

  it('a partial failure part-way back names the step it was leaving', async () => {
    const mock = install((b) => {
      if (b.kind === 'back') return b.index === 3 ? ok(at(2)) : { status: 409, text: '{"partial":true}' }
      return ok(review())
    })
    renderWizard()
    fireEvent.click(await screen.findByRole('button', { name: /Color/ }))
    await screen.findByText("Going back from step 3 didn't finish — check the Terminal")
    expect(posts(mock, 'back').map((b) => b.index)).toEqual([3, 2])
  })

  it('names the review when the first back fails', async () => {
    install((b) => (b.kind === 'back' ? { status: 409, text: '{"partial":true}' } : ok(review())))
    renderWizard()
    fireEvent.click(await screen.findByRole('button', { name: /Color/ }))
    await screen.findByText("Going back from the review didn't finish — check the Terminal")
  })

  it('reads the state once after a back that typed nothing', async () => {
    const mock = install((b) => (b.kind === 'back' ? { status: 409, text: 'prompt changed' } : ok(review())))
    renderWizard()
    const row = await screen.findByRole('button', { name: /Color/ })
    const before = posts(mock, 'state').length
    fireEvent.click(row)
    await screen.findByText('Going back from the review: the terminal moved on — nothing was typed')
    await waitFor(() => expect(posts(mock, 'state')).toHaveLength(before + 1))
  })
})

describe('QuestionWizard errors', () => {
  it('a partial step failure names the step, links to the Terminal, offers Re-check and never re-sends', async () => {
    vi.useFakeTimers()
    const mock = install((b) => (b.kind === 'step' ? { status: 409, text: '{"partial":true}' } : ok(at(0))))
    renderWizard()
    await act(async () => { await vi.advanceTimersByTimeAsync(0) })
    fireEvent.click(screen.getByRole('radio', { name: /Red/ }))
    fireEvent.click(next())
    await act(async () => { await vi.advanceTimersByTimeAsync(0) })

    expect(screen.getByText(/Step 1 didn't finish — check the Terminal/)).toBeTruthy()
    expect(screen.getByRole('link', { name: 'Terminal tab' }).getAttribute('href')).toBe('#/fleet/r1/terminal')
    await act(async () => { await vi.advanceTimersByTimeAsync(10000) })
    expect(posts(mock, 'step')).toHaveLength(1)

    const before = posts(mock, 'state').length
    fireEvent.click(screen.getByRole('button', { name: 'Re-check' }))
    await act(async () => { await vi.advanceTimersByTimeAsync(0) })
    expect(posts(mock, 'state')).toHaveLength(before + 1)
    expect(posts(mock, 'step')).toHaveLength(1)
  })

  it('a 409 with nothing typed reads the state once and says nothing was typed', async () => {
    vi.useFakeTimers()
    const mock = install((b) => (b.kind === 'step' ? { status: 409, text: 'prompt changed' } : ok(at(0))))
    renderWizard()
    await act(async () => { await vi.advanceTimersByTimeAsync(0) })
    expect(posts(mock, 'state')).toHaveLength(1)
    fireEvent.click(screen.getByRole('radio', { name: /Red/ }))
    fireEvent.click(next())
    await act(async () => { await vi.advanceTimersByTimeAsync(0) })

    expect(screen.getByText('Step 1: the terminal moved on — nothing was typed')).toBeTruthy()
    expect(screen.getByRole('link', { name: 'Terminal tab' }).getAttribute('href')).toBe('#/fleet/r1/terminal')
    expect(posts(mock, 'state')).toHaveLength(2)
    expect(posts(mock, 'step')).toHaveLength(1)
  })

  it('a 200 step result without a readable state shows the error, not Sent', async () => {
    install((b) => (b.kind === 'step' ? { status: 200, text: 'oops', body: undefined } : ok(at(0))))
    renderWizard()
    await screen.findByText('1 of 3')
    fireEvent.click(screen.getByRole('radio', { name: /Red/ }))
    fireEvent.click(next())
    await screen.findByText('could not read the terminal state')
    expect(screen.queryByText('Sent — waiting for Claude')).toBeNull()
    expect(screen.getByRole('link', { name: 'Terminal tab' })).toBeTruthy()
    expect(next().disabled).toBe(false)
  })

  it('shows another error with the Terminal link and keeps the staging', async () => {
    install((b) => (b.kind === 'step' ? { status: 429, text: 'busy' } : ok(at(0))))
    renderWizard()
    await screen.findByText('1 of 3')
    fireEvent.click(screen.getByRole('radio', { name: /Red/ }))
    fireEvent.click(next())
    await screen.findByText('busy')
    expect(screen.getByRole('link', { name: 'Terminal tab' })).toBeTruthy()
    expect(screen.getByRole('radio', { name: /Red/ }).getAttribute('aria-checked')).toBe('true')
    expect(next().disabled).toBe(false)
  })
})

describe('QuestionWizard plain dialog', () => {
  const plainQ: StateQuestion = { question: 'Which scope?', multiSelect: false, preview: false, options: [opt('A'), opt('B')] }
  const plainState: QuestionState = { tabbed: false, step: 0, count: 1, questions: [plainQ] }

  it('renders the single picker with Send answer and posts step index 0', async () => {
    const mock = install((b) => (b.kind === 'step' ? { status: 204 } : ok(plainState)))
    const { container } = renderWizard()
    await screen.findByText('Which scope?')
    expect(screen.queryByText(/of 1/)).toBeNull()
    expect(screen.queryByRole('button', { name: /^(Next|Back)$/ })).toBeNull()
    expect(screen.getAllByRole('radio')).toHaveLength(3)
    const send = screen.getByRole('button', { name: 'Send answer' }) as HTMLButtonElement
    expect(send.className).toContain('chat-question-send-btn')
    expect(send.disabled).toBe(true)
    expect(container.querySelector('.chat-question-wizard')).toBeNull()

    fireEvent.click(screen.getByRole('radio', { name: /^B/ }))
    fireEvent.click(send)
    await screen.findByText('Sent — waiting for Claude')
    expect(posts(mock, 'step')).toEqual([{ kind: 'step', toolCallId: 'q1', index: 0, answer: { options: [1] } }])
  })
})

describe('QuestionWizard polling', () => {
  it('re-reads the state every 2 s, and not while a request is in flight', async () => {
    vi.useFakeTimers()
    let release: (r: Reply) => void = () => {}
    const mock = install((b) => (b.kind === 'step' ? new Promise<Reply>((res) => { release = res }) : ok(at(0))))
    renderWizard()
    await act(async () => { await vi.advanceTimersByTimeAsync(0) })
    expect(posts(mock, 'state')).toHaveLength(1)
    await act(async () => { await vi.advanceTimersByTimeAsync(2000) })
    expect(posts(mock, 'state')).toHaveLength(2)

    fireEvent.click(screen.getByRole('radio', { name: /Red/ }))
    fireEvent.click(next())
    await act(async () => { await vi.advanceTimersByTimeAsync(6000) })
    expect(posts(mock, 'state')).toHaveLength(2)

    await act(async () => { release(ok(at(1))) })
    await act(async () => { await vi.advanceTimersByTimeAsync(2000) })
    expect(posts(mock, 'state')).toHaveLength(3)
  })

  it('does not poll while the page is hidden', async () => {
    vi.useFakeTimers()
    const visibility = vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('hidden')
    const mock = install(() => ok(at(0)))
    renderWizard()
    await act(async () => { await vi.advanceTimersByTimeAsync(0) })
    await act(async () => { await vi.advanceTimersByTimeAsync(6000) })
    expect(posts(mock, 'state')).toHaveLength(1)
    visibility.mockReturnValue('visible')
    await act(async () => { await vi.advanceTimersByTimeAsync(2000) })
    expect(posts(mock, 'state')).toHaveLength(2)
    visibility.mockRestore()
  })

  it('a poll updates an untouched step but never overwrites one the user edited', async () => {
    vi.useFakeTimers()
    let selection: number[] = []
    install(() => ok(at(0, { options: selection })))
    renderWizard()
    await act(async () => { await vi.advanceTimersByTimeAsync(0) })
    expect(screen.getByRole('radio', { name: /Blue/ }).getAttribute('aria-checked')).toBe('false')

    selection = [1]
    await act(async () => { await vi.advanceTimersByTimeAsync(2000) })
    expect(screen.getByRole('radio', { name: /Blue/ }).getAttribute('aria-checked')).toBe('true')

    fireEvent.click(screen.getByRole('radio', { name: /Red/ }))
    selection = [2]
    await act(async () => { await vi.advanceTimersByTimeAsync(2000) })
    expect(screen.getByRole('radio', { name: /Red/ }).getAttribute('aria-checked')).toBe('true')
    expect(screen.getByRole('radio', { name: /Green/ }).getAttribute('aria-checked')).toBe('false')
  })

  it('a poll that reports another step moves the card, and a failed poll keeps the last state', async () => {
    vi.useFakeTimers()
    let reply: Reply = ok(at(0))
    install(() => reply)
    renderWizard()
    await act(async () => { await vi.advanceTimersByTimeAsync(0) })
    expect(screen.getByText('1 of 3')).toBeTruthy()

    reply = { status: 503, text: 'tmux unavailable' }
    await act(async () => { await vi.advanceTimersByTimeAsync(2000) })
    expect(screen.getByText('1 of 3')).toBeTruthy()

    reply = ok(review())
    await act(async () => { await vi.advanceTimersByTimeAsync(2000) })
    expect(screen.getByRole('button', { name: 'Submit' })).toBeTruthy()
  })
})

describe('QuestionWizard without a dialog', () => {
  it('shows the questions read-only with a hint and a Terminal link when the pane shows none, and keeps polling', async () => {
    vi.useFakeTimers()
    const mock = install(() => ({ status: 404, text: 'no question' }))
    renderWizard(questions)
    await act(async () => { await vi.advanceTimersByTimeAsync(0) })
    expect(screen.getByText('Which color?')).toBeTruthy()
    expect(screen.queryByRole('radio')).toBeNull()
    expect(screen.queryByRole('button', { name: /^(Next|Send answer)$/ })).toBeNull()
    expect(screen.getByText(/isn't showing this question/)).toBeTruthy()
    expect(within(screen.getByRole('status')).getByRole('link', { name: 'Terminal tab' }).getAttribute('href')).toBe('#/fleet/r1/terminal')

    await act(async () => { await vi.advanceTimersByTimeAsync(2000) })
    expect(posts(mock, 'state')).toHaveLength(2)
  })

  it('points to the Terminal when there is neither a state nor questions to show', async () => {
    install(() => ({ status: 503, text: 'tmux unavailable' }))
    renderWizard()
    expect(await screen.findByRole('link', { name: 'Open Terminal' })).toBeTruthy()
  })

  it('comes back to the wizard when the dialog reappears', async () => {
    vi.useFakeTimers()
    let reply: Reply = { status: 404, text: 'no question' }
    install(() => reply)
    renderWizard(questions)
    await act(async () => { await vi.advanceTimersByTimeAsync(0) })
    expect(screen.queryByRole('radio')).toBeNull()
    reply = ok(at(0))
    await act(async () => { await vi.advanceTimersByTimeAsync(2000) })
    expect(screen.getAllByRole('radio').length).toBeGreaterThan(0)
  })
})
