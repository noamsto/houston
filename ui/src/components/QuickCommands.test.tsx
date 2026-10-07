import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, cleanup, fireEvent, render, screen } from '@testing-library/react'
import { QuickCommands } from './QuickCommands'
import type { TerminalAddress } from '../api/terminal'

const address: TerminalAddress = { kind: 'run', id: 'run-1' }

function bodies(): unknown[] {
  return vi.mocked(fetch).mock.calls.map(([, init]) => JSON.parse(String(init?.body)))
}

beforeEach(() => {
  vi.stubGlobal('fetch', vi.fn(() => Promise.resolve({ ok: true } as Response)))
})

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  vi.useRealTimers()
})

async function click(el: HTMLElement) {
  await act(async () => {
    fireEvent.click(el)
  })
}

async function open() {
  await click(screen.getByRole('button', { name: 'Commands' }))
}

function setup(props: Partial<React.ComponentProps<typeof QuickCommands>> = {}) {
  const onPrefill = vi.fn(() => true)
  const view = render(<QuickCommands address={address} agent="claude" state="idle" onPrefill={onPrefill} {...props} />)
  return { ...view, onPrefill }
}

describe('QuickCommands visibility', () => {
  it.each(['claude', 'claude-code'])('renders for %s', (agent) => {
    setup({ agent })
    expect(screen.getByRole('button', { name: 'Commands' })).toBeTruthy()
  })

  it.each(['amp', 'pi', undefined])('is absent for %s', (agent) => {
    setup({ agent })
    expect(screen.queryByRole('button', { name: 'Commands' })).toBeNull()
  })
})

describe('QuickCommands sending', () => {
  it.each(['/compact', '/context'])('%s sends text', async (cmd) => {
    setup()
    await open()
    await click(screen.getByRole('button', { name: cmd }))
    expect(bodies()).toEqual([{ type: 'text', text: cmd }])
  })

  it('/compact… prefills instead of sending', async () => {
    const { onPrefill } = setup()
    await open()
    await click(screen.getByRole('button', { name: '/compact…' }))
    expect(onPrefill).toHaveBeenCalledWith('/compact ')
    expect(fetch).not.toHaveBeenCalled()
  })

  it('/compact… tells the user to clear the draft when the composer refuses', async () => {
    const onPrefill = vi.fn(() => false)
    setup({ onPrefill })
    await open()
    await click(screen.getByRole('button', { name: '/compact…' }))
    expect(onPrefill).toHaveBeenCalledWith('/compact ')
    expect(screen.getByRole('status').textContent).toBe('Clear the draft and attachment first, then tap /compact…')
    expect(fetch).not.toHaveBeenCalled()
  })

  it('/clear needs a confirm', async () => {
    setup()
    await open()
    await click(screen.getByRole('button', { name: '/clear' }))
    expect(fetch).not.toHaveBeenCalled()
    expect(screen.getByText('Send /clear? This wipes the session context.')).toBeTruthy()
    await click(screen.getByRole('button', { name: 'Confirm' }))
    expect(bodies()).toEqual([{ type: 'text', text: '/clear' }])
  })

  it('/clear Cancel sends nothing', async () => {
    setup()
    await open()
    await click(screen.getByRole('button', { name: '/clear' }))
    await click(screen.getByRole('button', { name: 'Cancel' }))
    expect(fetch).not.toHaveBeenCalled()
    expect(screen.queryByText(/wipes the session context/)).toBeNull()
  })
})

describe('QuickCommands guard', () => {
  it.each(['running', 'blocked', 'thinking'])('disables commands while %s', async (state) => {
    setup({ state, suggestion: '/compact x' })
    await open()
    for (const name of ['/compact', '/compact…', '/clear', '/context', '↳ suggested']) {
      const b = screen.getByRole('button', { name }) as HTMLButtonElement
      expect(b.disabled).toBe(true)
      expect(b.title).not.toBe('')
    }
    await click(screen.getByRole('button', { name: '/compact' }))
    expect(fetch).not.toHaveBeenCalled()
  })

  it('shows the reason under the row', async () => {
    setup({ state: 'blocked' })
    await open()
    expect(screen.getByText(/waiting on a prompt/)).toBeTruthy()
  })

  it('enables commands when idle', async () => {
    setup()
    await open()
    expect((screen.getByRole('button', { name: '/compact' }) as HTMLButtonElement).disabled).toBe(false)
  })
})

describe('QuickCommands suggestion', () => {
  it('has no pill without a suggestion', () => {
    setup()
    expect(screen.queryByRole('button', { name: '↳ suggested' })).toBeNull()
  })

  it('opens a prefilled sheet and sends the edited text', async () => {
    setup({ suggestion: '/compact keep the plan' })
    await click(screen.getByRole('button', { name: '↳ suggested' }))
    const field = screen.getByLabelText('Suggested command') as HTMLTextAreaElement
    expect(field.value).toBe('/compact keep the plan')
    fireEvent.change(field, { target: { value: '  /compact keep tests  ' } })
    await click(screen.getByRole('button', { name: 'Send command' }))
    expect(bodies()).toEqual([{ type: 'text', text: '/compact keep tests' }])
    expect(screen.queryByLabelText('Suggested command')).toBeNull()
  })

  it('Cancel closes the sheet without sending', async () => {
    setup({ suggestion: '/compact' })
    await click(screen.getByRole('button', { name: '↳ suggested' }))
    await click(screen.getByRole('button', { name: 'Cancel' }))
    expect(screen.queryByLabelText('Suggested command')).toBeNull()
    expect(fetch).not.toHaveBeenCalled()
  })

  it('keeps the sheet open on a failed send', async () => {
    vi.stubGlobal('fetch', vi.fn(() => Promise.resolve({ ok: false, status: 500 } as Response)))
    setup({ suggestion: '/compact' })
    await click(screen.getByRole('button', { name: '↳ suggested' }))
    await click(screen.getByRole('button', { name: 'Send command' }))
    expect(screen.getByLabelText('Suggested command')).toBeTruthy()
    expect(screen.getByRole('alert').textContent).toBe('Not sent: HTTP 500')
  })
})

describe('QuickCommands suggestion dismissal', () => {
  it('hides the pill after a successful send until the suggestion changes', async () => {
    const onPrefill = vi.fn(() => true)
    const { rerender } = setup({ suggestion: '/compact a', onPrefill })
    await click(screen.getByRole('button', { name: '↳ suggested' }))
    await click(screen.getByRole('button', { name: 'Send command' }))
    expect(screen.queryByRole('button', { name: '↳ suggested' })).toBeNull()
    rerender(<QuickCommands address={address} agent="claude" state="idle" suggestion="/compact a" onPrefill={onPrefill} />)
    expect(screen.queryByRole('button', { name: '↳ suggested' })).toBeNull()
    rerender(<QuickCommands address={address} agent="claude" state="idle" suggestion="/compact b" onPrefill={onPrefill} />)
    expect(screen.getByRole('button', { name: '↳ suggested' })).toBeTruthy()
  })

  it('shows the pill again when the same command is suggested after a gap', async () => {
    const onPrefill = vi.fn(() => true)
    const el = (suggestion: string | null) => (
      <QuickCommands address={address} agent="claude" state="idle" suggestion={suggestion} onPrefill={onPrefill} />
    )
    const { rerender } = setup({ suggestion: '/compact a', onPrefill })
    await click(screen.getByRole('button', { name: '↳ suggested' }))
    await click(screen.getByRole('button', { name: 'Send command' }))
    expect(screen.queryByRole('button', { name: '↳ suggested' })).toBeNull()
    rerender(el(null))
    rerender(el('/compact a'))
    expect(screen.getByRole('button', { name: '↳ suggested' })).toBeTruthy()
  })

  it('keeps the pill after Cancel', async () => {
    setup({ suggestion: '/compact a' })
    await click(screen.getByRole('button', { name: '↳ suggested' }))
    await click(screen.getByRole('button', { name: 'Cancel' }))
    expect(screen.getByRole('button', { name: '↳ suggested' })).toBeTruthy()
  })
})

describe('QuickCommands feedback', () => {
  it('reports a failed send', async () => {
    vi.stubGlobal('fetch', vi.fn(() => Promise.resolve({ ok: false, status: 500 } as Response)))
    setup()
    await open()
    await click(screen.getByRole('button', { name: '/context' }))
    expect(screen.getByRole('alert').textContent).toBe('Not sent: HTTP 500')
  })

  it('goes from sent to accepted once the run leaves idle', async () => {
    const { rerender, onPrefill } = setup()
    await open()
    await click(screen.getByRole('button', { name: '/context' }))
    expect(screen.getByRole('status').textContent).toBe('Sent /context — waiting for the agent…')
    rerender(<QuickCommands address={address} agent="claude" state="thinking" onPrefill={onPrefill} />)
    expect(screen.getByRole('status').textContent).toBe('/context accepted')
  })

  it('says nothing was seen after 10s', async () => {
    vi.useFakeTimers()
    setup()
    await open()
    await click(screen.getByRole('button', { name: '/context' }))
    await act(async () => {
      vi.advanceTimersByTime(10_000)
    })
    expect(screen.getByRole('status').textContent).toBe('Sent — this command may not change the run state; check the terminal.')
  })

  it('keeps the strong stalled text for /compact', async () => {
    vi.useFakeTimers()
    setup()
    await open()
    await click(screen.getByRole('button', { name: '/compact' }))
    await act(async () => {
      vi.advanceTimersByTime(10_000)
    })
    expect(screen.getByRole('status').textContent).toBe('No reaction seen yet — check the terminal.')
  })

  it('restarts the 10s timer on a back-to-back send', async () => {
    vi.useFakeTimers()
    setup()
    await open()
    await click(screen.getByRole('button', { name: '/context' }))
    await act(async () => {
      vi.advanceTimersByTime(6_000)
    })
    await click(screen.getByRole('button', { name: '/context' }))
    await act(async () => {
      vi.advanceTimersByTime(6_000)
    })
    expect(screen.getByRole('status').textContent).toBe('Sent /context — waiting for the agent…')
    await act(async () => {
      vi.advanceTimersByTime(4_000)
    })
    expect(screen.getByRole('status').textContent).toContain('may not change the run state')
  })
})
