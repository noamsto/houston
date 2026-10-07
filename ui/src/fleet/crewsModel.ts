import type { Attention, Run } from '../api/runs'
import { isFresh, needsYou } from './staleness'

export interface CrewCounts {
  working: number
  needsYou: number
  stuck: number
  done: number
  idle: number
  ended: number
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

// What a card is flagged as: the server's verdict, nothing inferred. A run with
// no attention is either working, idle, or ended (state done), and Fleet calls
// that last one history, not "done".
export function crewAttention(run: Run): Attention | undefined {
  return run.attention
}

// Flags are gated on freshness like Fleet's badge, so a stale question does not
// keep lighting the header of a crew that is already under Finished. Unflagged
// runs bucket by state alone, as Fleet does.
function bucket(run: Run, now: number): keyof CrewCounts {
  if (run.attention) {
    if (!isFresh(run, now)) return 'ended'
    return run.attention === 'needs-you' ? 'needsYou' : run.attention
  }
  if (run.state === 'done') return 'ended'
  return run.state === 'idle' ? 'idle' : 'working'
}

function rank(run: Run, now: number): number {
  if (needsYou(run, now)) return 2
  return isLiveMember(run, now) ? 1 : 0
}

// Only a project every member that reports one agrees on is safe to label
// the crew with — a crew id can mirror into several repos' bus dirs, so a
// crew's members don't all necessarily share one project.
function soleProject(members: Run[]): string | undefined {
  let project: string | undefined
  for (const m of members) {
    if (!m.project) continue
    if (project === undefined) project = m.project
    else if (project !== m.project) return undefined
  }
  return project
}

function buildGroup(name: string, runs: Run[], now: number): CrewGroup {
  const members = [...runs].sort((a, b) => {
    const dr = rank(b, now) - rank(a, now)
    return dr !== 0 ? dr : b.updated_at - a.updated_at
  })
  const counts: CrewCounts = { working: 0, needsYou: 0, stuck: 0, done: 0, idle: 0, ended: 0 }
  for (const m of members) counts[bucket(m, now)]++
  return {
    name,
    project: soleProject(members),
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
  ['working', 'working'],
  ['needsYou', 'needs you'],
  ['stuck', 'stuck'],
  ['done', 'done'],
  ['idle', 'idle'],
  ['ended', 'ended'],
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
