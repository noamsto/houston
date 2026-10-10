import { afterEach, describe, expect, it, vi } from 'vitest'
import { CrewFeedReset, CrewFeedUnavailable, crewFeedStreamURL, fetchCrewFeed } from './crewFeed'

function response(status: number, body: string): Response {
  return { ok: status >= 200 && status < 300, status, text: () => Promise.resolve(body), json: () => Promise.resolve(JSON.parse(body)) } as unknown as Response
}

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('fetchCrewFeed', () => {
  it('requests the newest page with no query by default', async () => {
    const fetchMock = vi.fn().mockResolvedValue(response(200, '{"epoch":"e1","entries":[],"more":false}'))
    vi.stubGlobal('fetch', fetchMock)

    const page = await fetchCrewFeed('run/1')

    expect(fetchMock).toHaveBeenCalledWith('/api/runs/run%2F1/crew/feed', { signal: undefined })
    expect(page).toEqual({ epoch: 'e1', entries: [], more: false })
  })

  it('passes before and limit as query parameters', async () => {
    const fetchMock = vi.fn().mockResolvedValue(response(200, '{"epoch":"e1","entries":[],"more":false}'))
    vi.stubGlobal('fetch', fetchMock)
    const controller = new AbortController()

    await fetchCrewFeed('r1', { before: 'e1.120', limit: 25, signal: controller.signal })

    expect(fetchMock).toHaveBeenCalledWith('/api/runs/r1/crew/feed?before=e1.120&limit=25', { signal: controller.signal })
  })

  it('maps 404 to CrewFeedUnavailable', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(response(404, 'no crew')))
    await expect(fetchCrewFeed('r1')).rejects.toBeInstanceOf(CrewFeedUnavailable)
  })

  it('maps 409 to CrewFeedReset', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(response(409, '{"reset":true}')))
    await expect(fetchCrewFeed('r1', { before: 'old.5' })).rejects.toBeInstanceOf(CrewFeedReset)
  })

  it('throws a plain Error carrying the status for other failures', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(response(503, 'no registry')))
    const err = await fetchCrewFeed('r1').catch((e: unknown) => e)
    expect(err).toBeInstanceOf(Error)
    expect(err).not.toBeInstanceOf(CrewFeedUnavailable)
    expect(err).not.toBeInstanceOf(CrewFeedReset)
    expect((err as Error).message).toContain('503')
  })
})

describe('crewFeedStreamURL', () => {
  it('encodes the run id and the cursor', () => {
    expect(crewFeedStreamURL('run/1', 'e1.42')).toBe('/api/runs/run%2F1/crew/feed/stream?after=e1.42')
  })
})
