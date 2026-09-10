import { afterEach, describe, expect, it } from 'vitest'
import { cleanup, render, screen } from '@testing-library/react'
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
