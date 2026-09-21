import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { RunCard } from './RunCard'
import type { Run } from '../api/runs'

const now = 1_800_000_000_000 // fixed ms

function run(p: Partial<Run> = {}): Run {
  return {
    id: 'pane-1', agent: 'claude', state: 'running',
    activity: {}, tokens: { input: 0, output: 0 },
    caps: { terminal: true, reply: true, kill: true },
    updated_at: Math.floor(now / 1000),
    ...p,
  } as Run
}

afterEach(cleanup)

describe('RunCard crew chips', () => {
  it('shows the codename chip and not the crew id', () => {
    const r = run({ crew: { name: 'crew-42', codename: 'blush' } })
    render(<RunCard run={r} now={now} />)
    expect(screen.getByText('blush')).toBeTruthy()
    expect(screen.queryByText('crew-42')).toBeNull()
  })

  it('omits the codename chip when the crew has no codename', () => {
    const r = run({ crew: { name: 'crew-42' } })
    const { container } = render(<RunCard run={r} now={now} />)
    expect(container.querySelector('.run-chip.codename')).toBeNull()
  })

  it('shows a tier chip only when crew.tier is set', () => {
    const withTier = run({ crew: { name: 'crew-42', tier: 'lead' } })
    const { container, rerender } = render(<RunCard run={withTier} now={now} />)
    expect(container.querySelector('.run-chip.tier')?.textContent).toBe('lead')

    const withoutTier = run({ crew: { name: 'crew-42' } })
    rerender(<RunCard run={withoutTier} now={now} />)
    expect(container.querySelector('.run-chip.tier')).toBeNull()
  })
})

describe('RunCard accent', () => {
  it('sets --run-accent when crew.color is present', () => {
    const r = run({ crew: { name: 'crew-42', color: '#d75f87' } })
    const { container } = render(<RunCard run={r} now={now} />)
    const card = container.querySelector('.run-card') as HTMLElement
    expect(card.style.getPropertyValue('--run-accent')).toBe('#d75f87')
  })

  it('leaves --run-accent unset when crew.color is empty', () => {
    const r = run({ crew: { name: 'crew-42', color: '' } })
    const { container } = render(<RunCard run={r} now={now} />)
    const card = container.querySelector('.run-card') as HTMLElement
    expect(card.style.getPropertyValue('--run-accent')).toBe('')
  })

  it('keeps the attention class on an accented card that is also blocked', () => {
    const r = run({ state: 'blocked', crew: { name: 'crew-42', color: '#d75f87' } })
    const { container } = render(<RunCard run={r} now={now} />)
    const card = container.querySelector('.run-card') as HTMLElement
    expect(card.classList.contains('attention')).toBe(true)
    expect(card.style.getPropertyValue('--run-accent')).toBe('#d75f87')
  })
})

describe('RunCard selected', () => {
  it('sets aria-current and the selected class when selected', () => {
    const r = run()
    const { container } = render(<RunCard run={r} now={now} selected />)
    const card = container.querySelector('.run-card') as HTMLElement
    expect(card.getAttribute('aria-current')).toBe('true')
    expect(card.classList.contains('selected')).toBe(true)
  })

  it('sets neither when not selected', () => {
    const r = run()
    const { container } = render(<RunCard run={r} now={now} />)
    const card = container.querySelector('.run-card') as HTMLElement
    expect(card.getAttribute('aria-current')).toBeNull()
    expect(card.classList.contains('selected')).toBe(false)
  })
})

describe('RunCard transport-stale', () => {
  it('shows a stale chip when run.stale', () => {
    const r = run({ stale: true })
    render(<RunCard run={r} now={now} />)
    expect(screen.getByText('stale')).toBeTruthy()
  })

  it('omits the stale chip when run.stale is unset', () => {
    const r = run()
    const { container } = render(<RunCard run={r} now={now} />)
    expect(container.querySelector('.run-chip.stale')).toBeNull()
  })

  it('shows the chip even when the run is time-fresh', () => {
    // A recent updated_at would otherwise keep the card looking fully live.
    const r = run({ stale: true, updated_at: Math.floor(now / 1000) })
    const { container } = render(<RunCard run={r} now={now} />)
    expect(container.querySelector('.run-chip.stale')?.textContent).toBe('stale')
  })
})

describe('RunCard crew-bus fields', () => {
  it('renders title, model chip and live detail', () => {
    const r = run({ crew: { name: 'c', tier: 'standard', title: 'fix the ws drop', model: 'sonnet', detail: 'code review' } })
    const { container } = render(<RunCard run={r} now={now} />)
    expect(container.querySelector('.run-title')?.textContent).toBe('fix the ws drop')
    expect(container.querySelector('.run-chip.model')?.textContent).toBe('sonnet')
    expect(container.querySelector('.run-chip.tier')?.textContent).toBe('standard')
    expect(container.querySelector('.run-detail')?.textContent).toBe('code review')
  })

  it('hides a detail that just repeats the question', () => {
    const r = run({ state: 'blocked', crew: { name: 'c', detail: 'Keep it?' }, question: { text: 'Keep it?', via: 'crew' } })
    const { container } = render(<RunCard run={r} now={now} />)
    expect(container.querySelector('.run-detail')).toBeNull()
  })

  it('does not render a link for a non-http PR url', () => {
    render(<RunCard run={run({ pr: { number: '9', url: 'javascript:alert(1)' } })} now={now} />)
    expect(screen.queryByRole('link')).toBeNull()
  })

  it('links the PR chip without opening the run', () => {
    const onOpen = vi.fn()
    const r = run({ pr: { number: '9', url: 'https://github.com/x/y/pull/9' } })
    render(<RunCard run={r} now={now} onOpen={onOpen} />)
    const a = screen.getByRole('link', { name: '#9' })
    expect(a.getAttribute('href')).toBe('https://github.com/x/y/pull/9')
    fireEvent.click(a)
    expect(onOpen).not.toHaveBeenCalled()
  })

  it('renders a plain chip when the PR has no url, and PR when it has no number', () => {
    const { container, rerender } = render(<RunCard run={run({ pr: { number: '9' } })} now={now} />)
    expect(container.querySelector('span.run-chip.pr')?.textContent).toBe('#9')
    rerender(<RunCard run={run({ pr: { number: '', url: 'https://x/y' } })} now={now} />)
    expect(container.querySelector('a.run-chip.pr')?.textContent).toBe('PR')
  })
})
