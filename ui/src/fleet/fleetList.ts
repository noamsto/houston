import type { Run } from '../api/runs'
import { isFresh, isHistory, needsYou } from './staleness'

export type Filter = 'active' | 'needs-you' | 'all'

export function filterRuns(runs: Run[], filter: Filter, now: number): Run[] {
  // Needs-you shows every blocked run, not just fresh ones — the badge
  // and the sort answer "how many need me now", this filter answers
  // "what asked for me at all".
  const keep = runs.filter((r) => {
    if (filter === 'needs-you') return r.state === 'blocked'
    if (filter === 'active') return !isHistory(r, now)
    return true
  })
  return keep.sort((a, b) => {
    if (filter === 'needs-you') {
      const af = isFresh(a, now) ? 1 : 0
      const bf = isFresh(b, now) ? 1 : 0
      if (af !== bf) return bf - af
      return b.updated_at - a.updated_at
    }
    // Anything asking for input first, then most recently active. Stale
    // blocked runs deliberately stay out of this bucket — see RunCard's
    // muted attention border for how they stay findable in place instead.
    const an = needsYou(a, now) ? 1 : 0
    const bn = needsYou(b, now) ? 1 : 0
    if (an !== bn) return bn - an
    return b.updated_at - a.updated_at
  })
}

export function groupByHost(runs: Run[]): [string, Run[]][] {
  const byHost = new Map<string, Run[]>()
  for (const r of runs) {
    const host = r.host || 'local'
    const list = byHost.get(host)
    if (list) list.push(r)
    else byHost.set(host, [r])
  }
  return Array.from(byHost.entries())
}

// The bus carries no crew-level title, only the id — shorten it for the
// header but keep the full id reachable as the element's `title`.
export function crewShortId(name: string): string {
  return name.length > 12 ? `${name.slice(0, 10)}…` : name
}
