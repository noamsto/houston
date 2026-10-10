export type FeedKind = 'dispatch' | 'resume' | 'status' | 'question' | 'follow-ups' | 'reply' | 'pr' | 'reap'

// Mirror of crewfeed.Entry (Go). `id` is the cursor `<epoch>.<offset>`; `ts` is ms.
export interface FeedEntry {
  id: string
  ts: number
  kind: FeedKind
  text: string
  branch?: string
  codename?: string
  state?: string
  pr?: { number: number; url: string }
}

export interface FeedPage {
  epoch: string
  entries: FeedEntry[]
  more: boolean
}

// Thrown when the run has no crew feed (404: unknown run, not a joined
// dispatcher, or tmux mode) — the caller's cue to hide the feed.
export class CrewFeedUnavailable extends Error {
  constructor(message = 'crew feed unavailable') {
    super(message)
    this.name = 'CrewFeedUnavailable'
  }
}

// Thrown when a `before` cursor belongs to another epoch (409 `{"reset":true}`):
// the caller's loaded history is stale and the feed must be reloaded.
export class CrewFeedReset extends Error {
  constructor(message = 'crew feed reset') {
    super(message)
    this.name = 'CrewFeedReset'
  }
}

export async function fetchCrewFeed(
  runId: string,
  opts?: { before?: string; limit?: number; signal?: AbortSignal },
): Promise<FeedPage> {
  const params = new URLSearchParams()
  if (opts?.before !== undefined) params.set('before', opts.before)
  if (opts?.limit !== undefined) params.set('limit', String(opts.limit))
  const qs = params.toString()
  const res = await fetch(`/api/runs/${encodeURIComponent(runId)}/crew/feed${qs ? `?${qs}` : ''}`, {
    signal: opts?.signal,
  })
  if (res.status === 404) throw new CrewFeedUnavailable()
  if (res.status === 409) throw new CrewFeedReset()
  if (!res.ok) throw new Error(`fetchCrewFeed: ${res.status} ${await res.text()}`)
  return (await res.json()) as FeedPage
}

export function crewFeedStreamURL(runId: string, after: string): string {
  return `/api/runs/${encodeURIComponent(runId)}/crew/feed/stream?after=${encodeURIComponent(after)}`
}
