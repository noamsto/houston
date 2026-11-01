import { afterEach, describe, expect, it, vi } from 'vitest'
import { act, cleanup, fireEvent, render, screen } from '@testing-library/react'
import { QuestionCard } from './QuestionCard'
import type { QuestionItem } from './chatModel'

const item: QuestionItem = {
  kind: 'question', id: 'q1', seq: 1,
  call: { toolCallId: 'q1', tool: 'AskUserQuestion', status: 'in_progress', seq: 1 },
}

const plain = { question: 'Which scope?', options: [{ label: 'A', description: 'a' }, { label: 'B', description: 'b' }] }
const withPreview = {
  question: 'Which layout?',
  options: [{ label: 'Grid', preview: '+--+' }, { label: 'List', preview: '' }],
}
const blankPreview = { question: 'Which size?', options: [{ label: 'S', preview: '  \n' }, { label: 'L' }] }
const multiPreview = { question: 'Which toppings?', multiSelect: true, options: [{ label: 'X', preview: 'x' }, { label: 'Y' }] }

interface RawQuestion {
  question: string
  multiSelect?: boolean
  options: { label: string; description?: string; preview?: string }[]
}

// What the server's state reports for a call: tab 0, the questions as parsed.
function stateOf(questions: RawQuestion[]) {
  return {
    tabbed: questions.length > 1 || questions.some((q) => q.multiSelect),
    step: 0,
    count: questions.length,
    questions: questions.map((q) => ({
      question: q.question,
      multiSelect: q.multiSelect === true,
      preview: !q.multiSelect && q.options.some((o) => (o.preview ?? '').trim() !== ''),
      options: q.options.map((o) => ({ label: o.label, description: o.description })),
    })),
  }
}

const reply = (status: number, body: unknown, text = '') =>
  Promise.resolve({ status, ok: status < 300, json: async () => body, text: async () => text } as Response)

interface Install {
  questions: RawQuestion[]
  answerStatus?: number
  answerBody?: string
  // Overrides the tool detail response.
  tool?: () => Promise<Response>
  // 404 on every state read.
  noDialog?: boolean
}

function install({ questions, answerStatus = 204, answerBody = '', tool, noDialog }: Install) {
  const mock = vi.fn<(url: string, init?: RequestInit) => Promise<Response>>((url, init) => {
    if (url.includes('/chat/tool/')) {
      if (tool) return tool()
      const body = { toolCallId: 'q1', name: 'AskUserQuestion', input: { questions } }
      return reply(200, body, JSON.stringify(body))
    }
    if (url.includes('/answer')) {
      const kind = (JSON.parse(String(init?.body)) as { kind: string }).kind
      if (kind === 'state') return noDialog ? reply(404, null, 'no question') : reply(200, stateOf(questions))
      return reply(answerStatus, null, answerBody)
    }
    throw new Error(`unexpected fetch: ${url}`)
  })
  vi.stubGlobal('fetch', mock)
  return mock
}

const answerBodies = (mock: ReturnType<typeof install>) =>
  mock.mock.calls
    .filter(([u]) => String(u).includes('/answer'))
    .map(([, init]) => JSON.parse(String(init?.body)) as { kind: string })

const renderCard = (props: { answerable?: boolean } = {}) =>
  render(<QuestionCard item={item} runId="r1" canAnswer answerable={props.answerable ?? true} onLayout={() => {}} />)

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  vi.useRealTimers()
})

describe('QuestionCard', () => {
  it('renders no Other row for a single-select question with a preview', async () => {
    install({ questions: [withPreview] })
    renderCard()
    await screen.findByText('Which layout?')
    const group = screen.getByRole('radiogroup')
    expect(group.querySelectorAll('[role="radio"]')).toHaveLength(2)
    expect(group.textContent).not.toContain('Other')
  })

  it('renders an Other row for a plain single-select question', async () => {
    install({ questions: [plain] })
    renderCard()
    await screen.findByText('Which scope?')
    const group = screen.getByRole('radiogroup')
    expect(group.querySelectorAll('[role="radio"]')).toHaveLength(3)
    expect(group.textContent).toContain('Other')
  })

  it('keeps Other for blank previews and for multi-select questions', async () => {
    install({ questions: [blankPreview] })
    renderCard()
    await screen.findByText('Which size?')
    expect(screen.getByRole('radiogroup').textContent).toContain('Other')
    cleanup()

    install({ questions: [multiPreview] })
    renderCard()
    await screen.findByText('Which toppings?')
    expect(screen.getByRole('group', { name: 'Which toppings?' }).textContent).toContain('Other')
  })

  it('completes and sends a preview question from an option alone, arrows wrapping over the options', async () => {
    const mock = install({ questions: [withPreview] })
    renderCard()
    await screen.findByText('Which layout?')
    const send = screen.getByRole('button', { name: 'Send answer' }) as HTMLButtonElement
    expect(send.disabled).toBe(true)
    const [grid, list] = screen.getAllByRole('radio')
    grid.focus()
    fireEvent.keyDown(grid, { key: 'ArrowDown' })
    expect(list.getAttribute('aria-checked')).toBe('true')
    fireEvent.keyDown(list, { key: 'ArrowDown' })
    expect(grid.getAttribute('aria-checked')).toBe('true')
    expect(send.disabled).toBe(false)
    fireEvent.click(send)
    await screen.findByText('Sent — waiting for Claude')
    expect(answerBodies(mock).filter((b) => b.kind === 'step')).toEqual([
      { kind: 'step', toolCallId: 'q1', index: 0, answer: { options: [0] } },
    ])
  })

  it('says the terminal moved on after a 409 that is not partial', async () => {
    install({ questions: [plain], answerStatus: 409, answerBody: 'prompt changed' })
    renderCard()
    await screen.findByText('Which scope?')
    fireEvent.click(screen.getByRole('radio', { name: /^A/ }))
    fireEvent.click(screen.getByRole('button', { name: 'Send answer' }))
    await screen.findByText('Step 1: the terminal moved on — nothing was typed')
    expect(screen.getByRole('link', { name: 'Terminal tab' })).toBeTruthy()
  })

  it('links to the Terminal tab after an error result', async () => {
    install({ questions: [plain], answerStatus: 500, answerBody: 'boom' })
    renderCard()
    await screen.findByText('Which scope?')
    fireEvent.click(screen.getByRole('radio', { name: /^A/ }))
    fireEvent.click(screen.getByRole('button', { name: 'Send answer' }))
    await screen.findByText('boom')
    expect(screen.getByRole('link', { name: 'Terminal tab' }).getAttribute('href')).toBe('#/fleet/r1/terminal')
  })

  it('renders the wizard from the pane state when the tool detail is inputOmitted', async () => {
    install({
      questions: [plain],
      tool: () => reply(200, { toolCallId: 'q1', name: 'AskUserQuestion', inputOmitted: true }),
    })
    renderCard()
    await screen.findByText('Which scope?')
    expect(screen.getByRole('button', { name: 'Send answer' })).toBeTruthy()
    expect(screen.queryByTestId('question-fallback')).toBeNull()
  })
})

describe('QuestionCard tool detail retry', () => {
  const flush = () => act(async () => { await vi.advanceTimersByTimeAsync(0) })

  it('retries a failed detail fetch after 3 s and shows the card', async () => {
    vi.useFakeTimers()
    let calls = 0
    const mock = install({
      questions: [plain],
      tool: () => {
        calls++
        if (calls === 1) return Promise.reject(new TypeError('Failed to fetch'))
        const body = { toolCallId: 'q1', name: 'AskUserQuestion', input: { questions: [plain] } }
        return reply(200, body, JSON.stringify(body))
      },
    })
    renderCard({ answerable: false })
    await flush()
    expect(screen.getByTestId('question-fallback').textContent).toContain('Open Terminal')
    expect(screen.queryByText('Which scope?')).toBeNull()

    await act(async () => { await vi.advanceTimersByTimeAsync(2900) })
    expect(calls).toBe(1)
    await act(async () => { await vi.advanceTimersByTimeAsync(200) })
    expect(calls).toBe(2)
    expect(screen.getByText('Which scope?')).toBeTruthy()
    expect(screen.queryByTestId('question-fallback')).toBeNull()

    await act(async () => { await vi.advanceTimersByTimeAsync(10000) })
    expect(calls).toBe(2)
    expect(mock.mock.calls.filter(([u]) => String(u).includes('/answer'))).toHaveLength(0)
  })

  it('keeps retrying while the fetch keeps failing, and stops when the call completes', async () => {
    vi.useFakeTimers()
    let calls = 0
    install({ questions: [plain], tool: () => { calls++; return Promise.reject(new TypeError('Failed to fetch')) } })
    const { rerender } = renderCard({ answerable: false })
    await flush()
    for (let i = 0; i < 3; i++) await act(async () => { await vi.advanceTimersByTimeAsync(3000) })
    expect(calls).toBe(4)

    const completed: QuestionItem = { ...item, call: { ...item.call, status: 'completed' } }
    rerender(<QuestionCard item={completed} runId="r1" canAnswer answerable={false} onLayout={() => {}} />)
    await flush()
    const after = calls
    await act(async () => { await vi.advanceTimersByTimeAsync(9000) })
    expect(calls).toBe(after)
  })

  it('shows the questions read-only from a late detail when the pane shows no dialog', async () => {
    vi.useFakeTimers()
    let calls = 0
    install({
      questions: [plain],
      noDialog: true,
      tool: () => {
        calls++
        if (calls === 1) return Promise.reject(new TypeError('Failed to fetch'))
        const body = { toolCallId: 'q1', name: 'AskUserQuestion', input: { questions: [plain] } }
        return reply(200, body, JSON.stringify(body))
      },
    })
    renderCard()
    await flush()
    expect(screen.getByTestId('question-fallback')).toBeTruthy()
    await act(async () => { await vi.advanceTimersByTimeAsync(3100) })
    expect(screen.getByText('Which scope?')).toBeTruthy()
    expect(screen.getByText(/isn't showing this question/)).toBeTruthy()
    expect(screen.queryByRole('radio')).toBeNull()
  })
})
