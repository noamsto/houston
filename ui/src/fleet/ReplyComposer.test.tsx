import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { ReplyComposer } from './ReplyComposer'
import type { ReplyOutcome } from '../api/runs'

const replyRunMock = vi.fn<(id: string, text: string) => Promise<ReplyOutcome>>()
vi.mock('../api/runs', () => ({
  replyRun: (id: string, text: string) => replyRunMock(id, text),
}))

async function typeAndSend(value = 'go ahead') {
  fireEvent.change(screen.getByLabelText(/reply to the crew/i), { target: { value } })
  fireEvent.click(screen.getByRole('button', { name: /send/i }))
}

afterEach(() => {
  cleanup()
  replyRunMock.mockReset()
})

describe('ReplyComposer accessibility', () => {
  it('exposes the field and send action by name, with no adjacent question text to anchor them', () => {
    render(<ReplyComposer runId="run-1" />)

    expect(screen.getByRole('textbox', { name: /reply to the crew/i })).toBeTruthy()
    expect(screen.getByRole('button', { name: /send/i })).toBeTruthy()
  })
})

describe('ReplyComposer double-tap guard', () => {
  it('produces exactly one replyRun call from two rapid clicks', async () => {
    let resolve!: (o: ReplyOutcome) => void
    replyRunMock.mockReturnValue(new Promise((r) => { resolve = r }))

    render(<ReplyComposer runId="run-1" />)
    fireEvent.change(screen.getByLabelText(/reply to the crew/i), { target: { value: 'go ahead' } })
    const button = screen.getByRole('button', { name: /send/i })
    fireEvent.click(button)
    fireEvent.click(button)

    expect(replyRunMock).toHaveBeenCalledTimes(1)
    resolve({ kind: 'delivered' })
    await screen.findByText(/waiting to be picked up/i)
  })
})

describe('ReplyComposer outcomes', () => {
  it('delivered: says the answer is waiting to be picked up, not that the worker resumed, and clears the field', async () => {
    replyRunMock.mockResolvedValue({ kind: 'delivered' })
    render(<ReplyComposer runId="run-1" />)
    await typeAndSend()

    const status = await screen.findByText(/waiting to be picked up/i)
    expect(status.textContent).not.toMatch(/resum/i)
    expect(status.className).toContain('delivered')
    expect((screen.getByLabelText(/reply to the crew/i) as HTMLInputElement).value).toBe('')
  })

  it('refused (409): shows the worker\'s own refusal verbatim and keeps the text', async () => {
    replyRunMock.mockResolvedValue({ kind: 'refused', reason: 'crew: session terminal' })
    render(<ReplyComposer runId="run-1" />)
    await typeAndSend()

    const status = await screen.findByText('crew: session terminal')
    expect(status.className).toContain('refused')
    expect((screen.getByLabelText(/reply to the crew/i) as HTMLInputElement).value).toBe('go ahead')
  })

  it('rejected (400/404/413): shows houston\'s precondition reason', async () => {
    replyRunMock.mockResolvedValue({ kind: 'rejected', reason: 'no crew for this run' })
    render(<ReplyComposer runId="run-1" />)
    await typeAndSend()

    const status = await screen.findByText('no crew for this run')
    expect(status.className).toContain('rejected')
  })

  it('failed (502/504/network): shows houston\'s own failure reason', async () => {
    replyRunMock.mockResolvedValue({ kind: 'failed', reason: 'crew not found on PATH' })
    render(<ReplyComposer runId="run-1" />)
    await typeAndSend()

    const status = await screen.findByText('crew not found on PATH')
    expect(status.className).toContain('failed')
  })
})
