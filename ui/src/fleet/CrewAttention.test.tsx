import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { CrewAttention } from './CrewAttention'
import type { CrewAttention as Item } from '../api/runs'
import { runCrewAction } from '../api/crewAction'

vi.mock('../api/crewAction', () => ({ runCrewAction: vi.fn() }))

const run = vi.mocked(runCrewAction)

function item(p: Partial<Item> = {}): Item {
  return { id: 'stall:c1:feat/e', kind: 'stalled_worker', severity: 'warning', message: 'stuck', since: 5, ...p }
}

beforeEach(() => run.mockResolvedValue({ ok: true, output: '' }))
afterEach(() => {
  cleanup()
  vi.resetAllMocks()
})

describe('CrewAttention', () => {
  it('renders nothing for an empty list', () => {
    const { container } = render(<CrewAttention runId="run-1" items={[]} />)
    expect(container.firstChild).toBeNull()
  })

  it('shows kind and severity class', () => {
    const { container } = render(<CrewAttention runId="run-1" items={[item()]} />)
    expect(screen.getByText('stalled worker')).toBeTruthy()
    expect(container.querySelector('.sev-warning')).toBeTruthy()
  })

  it('runs a nudge action directly', async () => {
    const action = { verb: 'nudge', label: 'nudge', command: 'crew nudge x', needs_input: false }
    render(<CrewAttention runId="run-1" items={[item({ action })]} />)
    fireEvent.click(screen.getByRole('button', { name: 'nudge' }))
    await waitFor(() => expect(run).toHaveBeenCalledWith('run-1', 'stall:c1:feat/e'))
  })

  it('reply needs non-blank text', async () => {
    const action = { verb: 'reply', label: 'reply', command: 'crew reply x', needs_input: true }
    render(<CrewAttention runId="run-1" items={[item({ action })]} />)
    fireEvent.click(screen.getByRole('button', { name: 'reply' }))
    const input = screen.getByRole('textbox')
    fireEvent.change(input, { target: { value: '  ' } })
    fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    expect(run).not.toHaveBeenCalled()
    fireEvent.change(input, { target: { value: 'yes' } })
    fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    await waitFor(() => expect(run).toHaveBeenCalledWith('run-1', 'stall:c1:feat/e', { text: 'yes' }))
  })

  it('keeps the reply input and text on failure, retries, and clears on success', async () => {
    const action = { verb: 'reply', label: 'reply', command: 'crew reply x', needs_input: true }
    run.mockResolvedValueOnce({ ok: false, status: 0, message: 'network error' })
    render(<CrewAttention runId="run-1" items={[item({ action })]} />)
    fireEvent.click(screen.getByRole('button', { name: 'reply' }))
    fireEvent.change(screen.getByRole('textbox'), { target: { value: 'my answer' } })
    fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    expect((await screen.findByRole('alert')).textContent).toBe('network error')
    expect((screen.getByRole('textbox') as HTMLInputElement).value).toBe('my answer')
    fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    await waitFor(() => expect(run).toHaveBeenCalledTimes(2))
    expect(run).toHaveBeenLastCalledWith('run-1', 'stall:c1:feat/e', { text: 'my answer' })
    await waitFor(() => expect(screen.queryByRole('textbox')).toBeNull())
    expect(screen.queryByRole('alert')).toBeNull()
  })

  it('merge asks for confirmation', async () => {
    const action = { verb: 'merge', label: 'squash-merge', command: 'crew merge', needs_input: false, pr: 12 }
    render(<CrewAttention runId="run-1" items={[item({ action })]} />)
    fireEvent.click(screen.getByRole('button', { name: 'squash-merge' }))
    expect(run).not.toHaveBeenCalled()
    expect(screen.getByText('Squash-merge PR #12?')).toBeTruthy()
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(screen.queryByText('Squash-merge PR #12?')).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: 'squash-merge' }))
    fireEvent.click(screen.getByRole('button', { name: 'Confirm' }))
    await waitFor(() => expect(run).toHaveBeenCalledWith('run-1', 'stall:c1:feat/e', { confirm: true, pr: 12 }))
  })

  it('shows server confirm text on 428', async () => {
    const action = { verb: 'merge', label: 'squash-merge', command: 'crew merge', needs_input: false, pr: 12 }
    run.mockResolvedValue({ ok: false, status: 428, message: 'really merge?', confirm: 'really merge?' })
    render(<CrewAttention runId="run-1" items={[item({ action })]} />)
    fireEvent.click(screen.getByRole('button', { name: 'squash-merge' }))
    fireEvent.click(screen.getByRole('button', { name: 'Confirm' }))
    await screen.findByText('really merge?')
    expect(screen.getByRole('button', { name: 'Confirm' })).toBeTruthy()
  })

  it('shows display-only command without a button', () => {
    const { container } = render(<CrewAttention runId="run-1" items={[item({ command: 'crew hold due --crew c1' })]} />)
    expect(container.querySelector('code')?.textContent).toBe('crew hold due --crew c1')
    expect(screen.queryByRole('button')).toBeNull()
  })

  it('shows no button with neither action nor command', () => {
    render(<CrewAttention runId="run-1" items={[item()]} />)
    expect(screen.queryByRole('button')).toBeNull()
  })

  it('renders the message as literal text', () => {
    const { container } = render(<CrewAttention runId="run-1" items={[item({ message: '<img src=x onerror=alert(1)>' })]} />)
    expect(screen.getByText('<img src=x onerror=alert(1)>')).toBeTruthy()
    expect(container.querySelector('img')).toBeNull()
  })

  it('shows ok output as text', async () => {
    const action = { verb: 'nudge', label: 'nudge', command: 'c', needs_input: false }
    run.mockResolvedValue({ ok: true, output: 'where: %12' })
    render(<CrewAttention runId="run-1" items={[item({ action })]} />)
    fireEvent.click(screen.getByRole('button', { name: 'nudge' }))
    expect((await screen.findByText('where: %12')).tagName).toBe('PRE')
  })

  it('shows an error in an alert', async () => {
    const action = { verb: 'nudge', label: 'nudge', command: 'c', needs_input: false }
    run.mockResolvedValue({ ok: false, status: 500, message: 'boom' })
    render(<CrewAttention runId="run-1" items={[item({ action })]} />)
    fireEvent.click(screen.getByRole('button', { name: 'nudge' }))
    expect((await screen.findByRole('alert')).textContent).toBe('boom')
  })

  it('a 409 re-confirm shows the new prompt and the next Confirm sends its pr', async () => {
    const action = { verb: 'merge', label: 'squash-merge', command: 'crew merge', needs_input: false, pr: 12 }
    run.mockResolvedValueOnce({ ok: false, status: 409, message: 'Squash-merge PR #13?', confirm: 'Squash-merge PR #13?', pr: 13 })
    render(<CrewAttention runId="run-1" items={[item({ action })]} />)
    fireEvent.click(screen.getByRole('button', { name: 'squash-merge' }))
    fireEvent.click(screen.getByRole('button', { name: 'Confirm' }))
    await screen.findByText('Squash-merge PR #13?')
    fireEvent.click(screen.getByRole('button', { name: 'Confirm' }))
    await waitFor(() => expect(run).toHaveBeenLastCalledWith('run-1', 'stall:c1:feat/e', { confirm: true, pr: 13 }))
  })

  it('ignores a second click while an action is in flight', () => {
    const action = { verb: 'nudge', label: 'nudge', command: 'c', needs_input: false }
    run.mockReturnValue(new Promise(() => {}))
    render(<CrewAttention runId="run-1" items={[item({ action })]} />)
    const btn = screen.getByRole('button', { name: 'nudge' }) as HTMLButtonElement
    fireEvent.click(btn)
    expect(btn.disabled).toBe(true)
    fireEvent.click(btn)
    expect(run).toHaveBeenCalledTimes(1)
  })

  it('ignores a second merge Confirm while in flight', () => {
    const action = { verb: 'merge', label: 'squash-merge', command: 'crew merge', needs_input: false, pr: 12 }
    run.mockReturnValue(new Promise(() => {}))
    render(<CrewAttention runId="run-1" items={[item({ action })]} />)
    fireEvent.click(screen.getByRole('button', { name: 'squash-merge' }))
    const confirm = screen.getByRole('button', { name: 'Confirm' }) as HTMLButtonElement
    fireEvent.click(confirm)
    expect(confirm.disabled).toBe(true)
    fireEvent.click(confirm)
    expect(run).toHaveBeenCalledTimes(1)
  })
})
