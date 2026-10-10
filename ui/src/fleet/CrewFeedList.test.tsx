import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { CrewFeedList } from './CrewFeedList'
import type { FeedEntry, FeedKind } from '../api/crewFeed'
import type { Run } from '../api/runs'
import type { UseCrewFeedResult } from '../hooks/useCrewFeed'

let feed: UseCrewFeedResult
const loadOlder = vi.fn()
const retry = vi.fn()
const useCrewFeed = vi.fn()
vi.mock('../hooks/useCrewFeed', () => ({
  useCrewFeed: (...args: unknown[]) => useCrewFeed(...args),
}))

const ts = new Date(2026, 9, 10, 9, 5).getTime()

function entry(p: Partial<FeedEntry> = {}): FeedEntry {
  return { id: 'e.1', ts, kind: 'status', text: 'x', ...p }
}

function worker(p: Partial<Run> = {}): Run {
  return {
    id: 'w1', agent: 'claude', state: 'running', role: 'worker', branch: 'feat/a', crew: { name: 'c1', codename: 'Nova' },
    activity: {}, tokens: { input: 0, output: 0 }, caps: { terminal: true, reply: true, kill: true }, updated_at: 0,
    ...p,
  } as Run
}

function setFeed(p: Partial<UseCrewFeedResult>) {
  feed = { status: 'ready', entries: [], more: false, loadOlder, retry, ...p }
}

function renderList(runs: Run[] = []) {
  return render(<CrewFeedList runId="d1" crew="c1" runs={runs} />)
}

beforeEach(() => {
  setFeed({})
  useCrewFeed.mockImplementation(() => feed)
})

afterEach(() => {
  cleanup()
  vi.clearAllMocks()
})

describe('CrewFeedList', () => {
  it('subscribes to the dispatcher run\'s feed', () => {
    renderList()
    expect(useCrewFeed).toHaveBeenCalledWith('d1', true)
  })

  it('renders one row per kind with its label', () => {
    const rows: [FeedKind, string, Partial<FeedEntry>][] = [
      ['dispatch', 'dispatched', {}],
      ['resume', 'resumed', {}],
      ['status', 'pr_open', { state: 'pr_open' }],
      ['question', 'question', {}],
      ['follow-ups', 'follow-ups', {}],
      ['reply', 'reply', {}],
      ['pr', 'PR', {}],
      ['reap', 'reaped', {}],
    ]
    setFeed({ entries: rows.map(([kind, , p], i) => entry({ id: `e.${i}`, kind, text: `row ${kind}`, ...p })) })
    const { container } = renderList()

    const items = Array.from(container.querySelectorAll('.crew-feed-row'))
    expect(items).toHaveLength(rows.length)
    // newest first: the hook order is oldest first
    const expected = [...rows].reverse()
    items.forEach((li, i) => {
      expect(li.querySelector('.crew-feed-kind')?.textContent).toBe(expected[i][1])
      expect(li.querySelector('.crew-feed-text')?.textContent).toBe(`row ${expected[i][0]}`)
    })
  })

  it('falls back to "status" for a status entry without a state', () => {
    setFeed({ entries: [entry({ kind: 'status' })] })
    const { container } = renderList()
    expect(container.querySelector('.crew-feed-kind')?.textContent).toBe('status')
  })

  it('shows HH:MM in local time with the full timestamp as the title', () => {
    setFeed({ entries: [entry()] })
    const { container } = renderList()
    const time = container.querySelector('.crew-feed-time') as HTMLElement
    expect(time.textContent).toBe('09:05')
    expect(time.title).toBe(new Date(ts).toLocaleString())
  })

  it('renders text literally: no markup, no links, no autolinking', () => {
    const texts = ['<b>x</b>', '[a](http://evil)', 'see https://evil.example/pull/1 now']
    setFeed({ entries: texts.map((text, i) => entry({ id: `e.${i}`, text })) })
    const { container } = renderList()

    expect(container.querySelector('b')).toBeNull()
    expect(container.querySelectorAll('a')).toHaveLength(0)
    const rendered = Array.from(container.querySelectorAll('.crew-feed-text')).map((n) => n.textContent)
    expect(rendered).toEqual([...texts].reverse())
  })

  describe('pull request chip', () => {
    const prEntry = (url: string) => entry({ kind: 'pr', pr: { number: 12, url } })

    it('links a valid GitHub PR URL in a new tab', () => {
      setFeed({ entries: [prEntry('https://github.com/acme/repo.x/pull/12')] })
      renderList()
      const a = screen.getByRole('link', { name: 'Pull request #12' })
      expect(a.getAttribute('href')).toBe('https://github.com/acme/repo.x/pull/12')
      expect(a.getAttribute('target')).toBe('_blank')
      expect(a.getAttribute('rel')).toBe('noreferrer')
      expect(a.textContent).toBe('#12')
    })

    it.each([
      'javascript:alert(1)',
      'https://evil.example/pull/12',
      'http://github.com/acme/repo/pull/12',
      'https://github.com/acme/repo/pull/12/files',
      'https://github.com/acme/repo/issues/12',
      'https://github.com.evil.example/acme/repo/pull/12',
      'https://github.com/acme/repo/pull/12\n',
      '',
    ])('renders no link for %j', (url) => {
      setFeed({ entries: [prEntry(url)] })
      const { container } = renderList()
      expect(container.querySelectorAll('a')).toHaveLength(0)
    })
  })

  describe('worker chip', () => {
    it('links to the worker run when a worker of this crew has the branch', () => {
      setFeed({ entries: [entry({ branch: 'feat/a', codename: 'Nova' })] })
      renderList([worker()])
      const a = screen.getByRole('link', { name: 'Nova' })
      expect(a.getAttribute('href')).toBe('#/fleet/w1')
    })

    it('labels with the worker codename when the entry has none, then the branch', () => {
      setFeed({ entries: [entry({ id: 'e.2', branch: 'feat/b' }), entry({ id: 'e.1', branch: 'feat/a' })] })
      renderList([worker(), worker({ id: 'w2', branch: 'feat/b', crew: { name: 'c1' } })])
      expect(screen.getByRole('link', { name: 'Nova' }).getAttribute('href')).toBe('#/fleet/w1')
      expect(screen.getByRole('link', { name: 'feat/b' }).getAttribute('href')).toBe('#/fleet/w2')
    })

    it('is plain text when no matching worker exists', () => {
      setFeed({ entries: [entry({ branch: 'feat/a', codename: 'Nova' }), entry({ id: 'e.2', branch: 'feat/z' })] })
      const { container } = renderList([
        worker({ crew: { name: 'other' } }),
        worker({ id: 'd', role: 'dispatcher', branch: 'feat/z' }),
      ])
      expect(container.querySelectorAll('a')).toHaveLength(0)
      const chips = Array.from(container.querySelectorAll('.crew-feed-chips')).map((n) => n.textContent)
      expect(chips).toEqual(['feat/z', 'Nova'])
    })

    it('renders no chip for an entry without branch or codename', () => {
      setFeed({ entries: [entry()] })
      const { container } = renderList([worker()])
      expect(container.querySelector('.crew-feed-chips')?.textContent).toBe('')
    })
  })

  it('offers Load older only when there is more, and calls loadOlder', () => {
    setFeed({ entries: [entry()], more: false })
    const { rerender } = renderList()
    expect(screen.queryByRole('button', { name: 'Load older' })).toBeNull()

    setFeed({ entries: [entry()], more: true })
    rerender(<CrewFeedList runId="d1" crew="c1" runs={[]} />)
    fireEvent.click(screen.getByRole('button', { name: 'Load older' }))
    expect(loadOlder).toHaveBeenCalledTimes(1)
  })

  it('shows loading, empty, unavailable and error-with-retry states', () => {
    setFeed({ status: 'loading' })
    const { rerender } = renderList()
    expect(screen.getByText(/loading crew activity/i)).toBeTruthy()

    setFeed({ status: 'ready' })
    rerender(<CrewFeedList runId="d1" crew="c1" runs={[]} />)
    expect(screen.getByText('No crew activity yet.')).toBeTruthy()

    setFeed({ status: 'unavailable' })
    rerender(<CrewFeedList runId="d1" crew="c1" runs={[]} />)
    expect(screen.getByText(/unavailable/i)).toBeTruthy()

    setFeed({ status: 'error' })
    rerender(<CrewFeedList runId="d1" crew="c1" runs={[]} />)
    fireEvent.click(screen.getByRole('button', { name: 'Retry' }))
    expect(retry).toHaveBeenCalledTimes(1)
  })
})
