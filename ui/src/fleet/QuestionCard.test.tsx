import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen } from '@testing-library/react'
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

function install(questions: unknown[], answerStatus = 204, answerBody = '') {
  const mock = vi.fn<(url: string, init?: RequestInit) => Promise<Response>>((url) => {
    if (url.includes('/chat/tool/')) {
      const body = { toolCallId: 'q1', name: 'AskUserQuestion', input: { questions } }
      return Promise.resolve({ status: 200, ok: true, json: async () => body, text: async () => JSON.stringify(body) } as Response)
    }
    if (url.includes('/answer')) {
      return Promise.resolve({ status: answerStatus, ok: answerStatus < 300, text: async () => answerBody } as Response)
    }
    throw new Error(`unexpected fetch: ${url}`)
  })
  vi.stubGlobal('fetch', mock)
  return mock
}

const renderCard = () =>
  render(<QuestionCard item={item} runId="r1" canAnswer answerable onLayout={() => {}} />)

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

describe('QuestionCard', () => {
  it('renders no Other row for a single-select question with a preview, and one for a plain question', async () => {
    install([withPreview, plain])
    renderCard()
    await screen.findByText('Which layout?')
    const groups = screen.getAllByRole('radiogroup')
    expect(groups[0].querySelectorAll('[role="radio"]')).toHaveLength(2)
    expect(groups[0].textContent).not.toContain('Other')
    expect(groups[1].querySelectorAll('[role="radio"]')).toHaveLength(3)
    expect(groups[1].textContent).toContain('Other')
  })

  it('keeps Other for blank previews and for multi-select questions', async () => {
    install([blankPreview, multiPreview])
    renderCard()
    await screen.findByText('Which size?')
    expect(screen.getByRole('radiogroup').textContent).toContain('Other')
    expect(screen.getByRole('group', { name: 'Which toppings?' }).textContent).toContain('Other')
  })

  it('completes and sends a preview question from an option alone, arrows wrapping over the options', async () => {
    const mock = install([withPreview])
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
    const call = mock.mock.calls.find(([u]) => String(u).includes('/answer'))!
    expect(JSON.parse(String(call[1]?.body))).toEqual({
      kind: 'question', toolCallId: 'q1', answers: [{ question: 0, options: [0] }],
    })
  })

  it('links to the Terminal tab after a 409 that is not partial', async () => {
    install([plain], 409, 'prompt changed')
    renderCard()
    await screen.findByText('Which scope?')
    fireEvent.click(screen.getByRole('radio', { name: /^A/ }))
    fireEvent.click(screen.getByRole('button', { name: 'Send answer' }))
    await screen.findByText('The session moved on — refresh')
    expect(screen.getByRole('link', { name: 'Terminal tab' }).getAttribute('href')).toBe('#/fleet/r1/terminal')
  })

  it('links to the Terminal tab after an error result', async () => {
    install([plain], 500, 'boom')
    renderCard()
    await screen.findByText('Which scope?')
    fireEvent.click(screen.getByRole('radio', { name: /^A/ }))
    fireEvent.click(screen.getByRole('button', { name: 'Send answer' }))
    await screen.findByText('boom')
    expect(screen.getByRole('link', { name: 'Terminal tab' }).getAttribute('href')).toBe('#/fleet/r1/terminal')
  })
})
