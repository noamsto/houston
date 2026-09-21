import type { Run } from '../api/runs'
import { isFresh, needsYou } from './staleness'

export interface CrewCounts {
  blocked: number
  running: number
  review: number
  done: number
  failed: number
  other: number
}

export interface CrewGroup {
  name: string
  project?: string // main repo name, from the members' runs
  members: Run[]
  counts: CrewCounts
  needsYou: number
  lastActive: number // seconds, like Run.updated_at
  live: boolean
}

function isLiveMember(run: Run, now: number): boolean {
  const working = run.state === 'running' || run.state === 'thinking' || run.state === 'compacting'
  return (working && run.stale !== true && isFresh(run, now)) || needsYou(run, now)
}

function bucket(run: Run): keyof CrewCounts {
  switch (run.state) {
    case 'blocked':
    case 'review':
    case 'done':
    case 'failed':
      return run.state
    case 'running':
    case 'thinking':
    case 'compacting':
      return 'running'
    default:
      return 'other'
  }
}

function rank(run: Run, now: number): number {
  if (needsYou(run, now)) return 2
  return isLiveMember(run, now) ? 1 : 0
}

function buildGroup(name: string, runs: Run[], now: number): CrewGroup {
  const members = [...runs].sort((a, b) => {
    const dr = rank(b, now) - rank(a, now)
    return dr !== 0 ? dr : b.updated_at - a.updated_at
  })
  const counts: CrewCounts = { blocked: 0, running: 0, review: 0, done: 0, failed: 0, other: 0 }
  for (const m of members) counts[bucket(m)]++
  return {
    name,
    project: members.find((m) => m.project)?.project,
    members,
    counts,
    needsYou: members.filter((m) => needsYou(m, now)).length,
    lastActive: members.reduce((max, m) => Math.max(max, m.updated_at), 0),
    live: members.some((m) => isFresh(m, now)),
  }
}

export function groupCrews(runs: Run[], now: number): { live: CrewGroup[]; finished: CrewGroup[] } {
  const byCrew = new Map<string, Run[]>()
  for (const r of runs) {
    // No crew, or a crew with no id, belongs to no group — the Fleet tab
    // already lists every run; this would just be a second fleet list.
    const name = r.crew?.name
    if (!name) continue
    const list = byCrew.get(name)
    if (list) list.push(r)
    else byCrew.set(name, [r])
  }

  const groups = Array.from(byCrew, ([name, members]) => buildGroup(name, members, now))
  const live = groups.filter((g) => g.live)
  const finished = groups.filter((g) => !g.live)

  live.sort((a, b) => {
    const an = a.needsYou > 0 ? 1 : 0
    const bn = b.needsYou > 0 ? 1 : 0
    return an !== bn ? bn - an : b.lastActive - a.lastActive
  })
  finished.sort((a, b) => b.lastActive - a.lastActive)
  return { live, finished }
}

const COUNT_LABELS: [keyof CrewCounts, string][] = [
  ['blocked', 'blocked'],
  ['running', 'running'],
  ['review', 'review'],
  ['done', 'done'],
  ['failed', 'failed'],
  ['other', 'other'],
]

export function countsLabel(counts: CrewCounts): string {
  return COUNT_LABELS.filter(([key]) => counts[key] > 0)
    .map(([key, label]) => `${counts[key]} ${label}`)
    .join(' · ')
}

export function dispatchHref(repoPath: string | undefined, crew: string): string {
  const params = repoPath === undefined ? [] : [`repo=${encodeURIComponent(repoPath)}`]
  params.push(`crew=${encodeURIComponent(crew)}`)
  return `#/dispatch?${params.join('&')}`
}
