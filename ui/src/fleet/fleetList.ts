import type { Run } from '../api/runs'
import { isDone, isFresh, isHistory, isStuck, needsYou } from './staleness'

export type Filter = 'active' | 'needs-you' | 'stuck' | 'done' | 'all'

// Fresh needs-you, then fresh stuck, then fresh done, then the rest. A stale
// run of any kind ranks as the rest: the verdict no longer describes it.
export function rank(r: Run, now: number): number {
  if (needsYou(r, now)) return 3
  if (isStuck(r, now)) return 2
  if (isDone(r, now)) return 1
  return 0
}

// Newest-started first, then id: neither moves while a run lives, so a run's
// output never reshuffles the list.
export function stableOrder(a: Run, b: Run): number {
  const bySince = (b.since ?? 0) - (a.since ?? 0)
  if (bySince !== 0) return bySince
  return a.id < b.id ? -1 : a.id > b.id ? 1 : 0
}

export function filterRuns(runs: Run[], filter: Filter, now: number): Run[] {
  // Needs-you shows every blocked run, not just fresh ones — the badge
  // and the sort answer "how many need me now", this filter answers
  // "what asked for me at all". Stuck and done work the same way.
  const keep = runs.filter((r) => {
    if (filter === 'needs-you') return r.state === 'blocked'
    if (filter === 'stuck') return r.attention === 'stuck'
    if (filter === 'done') return r.attention === 'done'
    if (filter === 'active') return !isHistory(r, now)
    return true
  })
  return keep.sort((a, b) => {
    if (filter === 'needs-you' || filter === 'stuck' || filter === 'done') {
      const af = isFresh(a, now) ? 1 : 0
      const bf = isFresh(b, now) ? 1 : 0
      if (af !== bf) return bf - af
      return stableOrder(a, b)
    }
    // Stale blocked runs deliberately stay out of the ranked buckets — see
    // RunCard's muted attention border for how they stay findable in place.
    const ar = rank(a, now)
    const br = rank(b, now)
    if (ar !== br) return br - ar
    return stableOrder(a, b)
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

export function projectOf(run: Run): string {
  return run.project || run.repo || 'unknown'
}

/** Projects in order of first appearance; a project's dispatcher leads its workers. */
export function groupByProject(runs: Run[]): [string, Run[]][] {
  const byProject = new Map<string, Run[]>()
  for (const r of runs) {
    const project = projectOf(r)
    const list = byProject.get(project)
    if (list) list.push(r)
    else byProject.set(project, [r])
  }
  return Array.from(byProject.entries()).map(([project, list]) => [
    project,
    [...list.filter((r) => r.role === 'dispatcher'), ...list.filter((r) => r.role !== 'dispatcher')],
  ])
}

// The bus carries no crew-level title, only the id — shorten it for the
// header but keep the full id reachable as the element's `title`.
export function crewShortId(name: string): string {
  return name.length > 12 ? `${name.slice(0, 10)}…` : name
}
