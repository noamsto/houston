import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { WorkerRow } from './WorkerRow'
import type { Run } from '../api/runs'
import { agoLabel } from './format'

// WorkerRow calls agoLabel once per render, which makes it a render counter.
vi.mock('./format', async (orig) => {
  const actual = await orig<typeof import('./format')>()
  return { ...actual, agoLabel: vi.fn(actual.agoLabel) }
})

const now = 1_800_000_000_000 // fixed ms
const nowSec = Math.floor(now / 1000)

function run(p: Partial<Run> = {}): Run {
  return {
    id: 'w', agent: 'claude', state: 'running', role: 'worker', branch: 'feat/x',
    activity: {}, tokens: { input: 0, output: 0 },
    caps: { terminal: true, reply: true, kill: true },
    updated_at: nowSec - 120,
    ...p,
  } as Run
}

afterEach(cleanup)

describe('WorkerRow', () => {
  it('shows swatch, codename, title and the tier, engine and model line', () => {
    const { container } = render(
      <WorkerRow
        run={run({ crew: { name: 'c', codename: 'blush', color: '#d75f87', title: 'Fix it', tier: 'standard', model: 'sonnet' } })}
        now={now}
      />,
    )
    expect(container.querySelector('.worker-row-codename')?.textContent).toBe('blush')
    expect((container.querySelector('.worker-row-swatch') as HTMLElement).style.background).toBe('#d75f87')
    expect(container.querySelector('.worker-row-title')?.textContent).toBe('Fix it')
    expect(container.querySelector('.worker-row-meta')?.textContent).toBe('standard · claude · sonnet')
    expect(container.querySelector('.run-age')?.textContent).toBe('2m')
  })

  it('falls back to the issue title, then the branch, and to "worker" without a codename', () => {
    const { container, rerender } = render(<WorkerRow run={run({ issue: { id: 'A-1', title: 'From issue' } })} now={now} />)
    expect(container.querySelector('.worker-row-title')?.textContent).toBe('From issue')
    expect(container.querySelector('.worker-row-codename')?.textContent).toBe('worker')
    rerender(<WorkerRow run={run()} now={now} />)
    expect(container.querySelector('.worker-row-title')?.textContent).toBe('feat/x')
  })

  it('shows an attention chip, an idle chip only when unflagged, and nothing for a working run', () => {
    const { container, rerender } = render(<WorkerRow run={run({ attention: 'stuck' })} now={now} />)
    expect(container.querySelector('.worker-row-attention.stuck')?.textContent).toBe('stuck')
    expect(container.querySelector('.worker-row-idle')).toBeNull()

    rerender(<WorkerRow run={run({ state: 'idle' })} now={now} />)
    expect(container.querySelector('.worker-row-idle')?.textContent).toBe('idle')

    rerender(<WorkerRow run={run({ state: 'idle', attention: 'done' })} now={now} />)
    expect(container.querySelector('.worker-row-attention.done')?.textContent).toBe('done')
    expect(container.querySelector('.worker-row-idle')).toBeNull()

    rerender(<WorkerRow run={run()} now={now} />)
    expect(container.querySelector('.run-chip')).toBeNull()
  })

  it('accents a blocked row as needs-you and mutes it once stale', () => {
    const { container, rerender } = render(<WorkerRow run={run({ state: 'blocked' })} now={now} />)
    expect(container.querySelector('.worker-row.needs-you')).toBeTruthy()
    expect(container.querySelector('.worker-row.muted')).toBeNull()
    expect(container.querySelector('.worker-row-attention.needs-you')?.textContent).toBe('needs you')

    rerender(<WorkerRow run={run({ state: 'blocked', updated_at: nowSec - 2 * 3600 })} now={now} />)
    expect(container.querySelector('.worker-row.needs-you.muted')).toBeTruthy()
  })

  it('opens the worker from the button and marks the selected row current', () => {
    const onOpen = vi.fn()
    const r = run()
    const { rerender } = render(<WorkerRow run={r} now={now} onOpen={onOpen} />)
    expect(screen.getByRole('button').getAttribute('aria-current')).toBeNull()
    fireEvent.click(screen.getByRole('button'))
    expect(onOpen).toHaveBeenCalledWith(r)

    rerender(<WorkerRow run={r} now={now} onOpen={onOpen} selected />)
    expect(screen.getByRole('button').getAttribute('aria-current')).toBe('true')
  })

  it('keeps the PR chip outside the button, linked only for http(s) urls', () => {
    const { container, rerender } = render(
      <WorkerRow run={run({ pr: { number: '12', url: 'https://example.com/pull/12' } })} now={now} />,
    )
    const link = container.querySelector('a.run-chip.pr') as HTMLAnchorElement
    expect(link.textContent).toBe('#12')
    expect(link.closest('button')).toBeNull()

    rerender(<WorkerRow run={run({ pr: { number: '12', url: 'javascript:alert(1)' } })} now={now} />)
    expect(container.querySelector('a')).toBeNull()
    expect(container.querySelector('.run-chip.pr')?.textContent).toBe('#12')
  })

  it('does not re-render for an unchanged run', () => {
    const r = run()
    const onOpen = vi.fn()
    const { rerender } = render(<WorkerRow run={r} now={now} onOpen={onOpen} />)
    vi.mocked(agoLabel).mockClear()

    rerender(<WorkerRow run={r} now={now} onOpen={onOpen} />)
    expect(agoLabel).not.toHaveBeenCalled()

    rerender(<WorkerRow run={{ ...r }} now={now} onOpen={onOpen} />)
    expect(agoLabel).toHaveBeenCalledTimes(1)
  })
})
