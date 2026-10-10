import type { Run } from '../api/runs'
import type { Mode } from '../api/mode'
import { bucket, type CrewCounts } from './crewsModel'
import { filterRuns, projectOf, rank, stableOrder, type Filter } from './fleetList'
import { isFresh, needsYou } from './staleness'

export type FleetEntry =
  | { kind: 'run'; key: string; run: Run }
  | { kind: 'dispatcher'; key: string; head: Run; shown: Run[]; members: Run[]; needsYou: number; counts: CrewCounts }
  | {
      kind: 'crew'
      key: string
      crew: string
      host: string
      project?: string
      shown: Run[]
      members: Run[]
      needsYou: number
      counts: CrewCounts
    }

/** Member rows a group renders when it has more than COLLAPSE_OVER shown members. */
export const MEMBER_CAP = 5
export const COLLAPSE_OVER = 6

const hostOf = (r: Run) => r.host || 'local'
const groupId = (host: string, crew: string) => `${host}\0${crew}`

// A crew id is "<unix>-<pid>"; its leading seconds order crews that have no
// run of their own to carry a start time.
function crewSeconds(crew: string): number {
  const n = parseInt(crew, 10)
  return Number.isFinite(n) ? n : 0
}

function soleProject(members: Run[]): string | undefined {
  let project: string | undefined
  for (const m of members) {
    if (!m.project) continue
    if (project === undefined) project = m.project
    else if (project !== m.project) return undefined
  }
  return project
}

function summarize(members: Run[], now: number) {
  const counts: CrewCounts = { working: 0, needsYou: 0, stuck: 0, done: 0, idle: 0, ended: 0 }
  for (const m of members) counts[bucket(m, now)]++
  return { counts, needsYou: members.filter((m) => needsYou(m, now)).length }
}

// What the top-level ordering reads: the entry's own rank (max over its head
// and shown members), whether any of them is fresh, and a start key that only
// changes when the entry's identity does.
interface Sort {
  rank: number
  fresh: boolean
  since: number
  id: string
  key: string
}

function sortOf(e: FleetEntry, now: number): Sort {
  if (e.kind === 'run') {
    return { rank: rank(e.run, now), fresh: isFresh(e.run, now), since: e.run.since ?? 0, id: e.run.id, key: e.key }
  }
  const parts = e.kind === 'dispatcher' ? [e.head, ...e.shown] : e.shown
  const rk = Math.max(...parts.map((r) => rank(r, now)))
  const fresh = parts.some((r) => isFresh(r, now))
  if (e.kind === 'dispatcher') return { rank: rk, fresh, since: e.head.since ?? 0, id: e.head.id, key: e.key }
  return { rank: rk, fresh, since: crewSeconds(e.crew), id: e.crew, key: e.key }
}

function entryCompare(filter: Filter, now: number) {
  const byFresh = filter === 'needs-you' || filter === 'stuck' || filter === 'done'
  return (a: FleetEntry, b: FleetEntry): number => {
    const sa = sortOf(a, now)
    const sb = sortOf(b, now)
    if (byFresh) {
      if (sa.fresh !== sb.fresh) return sa.fresh ? -1 : 1
    } else if (sa.rank !== sb.rank) {
      return sb.rank - sa.rank
    }
    if (sa.since !== sb.since) return sb.since - sa.since
    if (sa.id !== sb.id) return sa.id < sb.id ? -1 : 1
    return sa.key < sb.key ? -1 : sa.key > sb.key ? 1 : 0
  }
}

const runEntry = (run: Run): FleetEntry => ({ kind: 'run', key: run.id, run })

export function fleetEntries(runs: Run[], filter: Filter, now: number, mode: Mode | null): FleetEntry[] {
  if (mode !== 'dispatcher') return filterRuns(runs, filter, now).map(runEntry)

  // Sorted so that, should two dispatchers claim one crew, the same one wins every time.
  const heads = new Map<string, Run>()
  for (const r of [...runs].sort(stableOrder)) {
    if (r.role !== 'dispatcher' || !r.crew?.name) continue
    const id = groupId(hostOf(r), r.crew.name)
    if (!heads.has(id)) heads.set(id, r)
  }

  const members = new Map<string, Run[]>()
  const plain: Run[] = []
  for (const r of runs) {
    if (r.role === 'worker' && r.crew?.name) {
      const id = groupId(hostOf(r), r.crew.name)
      const list = members.get(id)
      if (list) list.push(r)
      else members.set(id, [r])
    } else if (r.role !== 'dispatcher' || heads.get(groupId(hostOf(r), r.crew?.name ?? '')) !== r) {
      plain.push(r)
    }
  }

  const out: FleetEntry[] = filterRuns(plain, filter, now).map(runEntry)
  const headMatches = new Set(filterRuns([...heads.values()], filter, now))

  for (const [id, head] of heads) {
    const all = members.get(id) ?? []
    const shown = filterRuns(all, filter, now)
    if (!headMatches.has(head) && shown.length === 0) continue
    out.push({ kind: 'dispatcher', key: head.id, head, shown, members: all, ...summarize(all, now) })
  }
  for (const [id, all] of members) {
    if (heads.has(id)) continue
    const shown = filterRuns(all, filter, now)
    if (shown.length === 0) continue
    const crew = all[0].crew?.name ?? ''
    const host = hostOf(all[0])
    out.push({
      kind: 'crew',
      key: `crew:${host}:${crew}`,
      crew,
      host,
      project: soleProject(all),
      shown,
      members: all,
      ...summarize(all, now),
    })
  }
  return out.sort(entryCompare(filter, now))
}

export function entryHost(e: FleetEntry): string {
  if (e.kind === 'run') return hostOf(e.run)
  return e.kind === 'dispatcher' ? hostOf(e.head) : e.host
}

export function entryProject(e: FleetEntry): string {
  if (e.kind === 'run') return projectOf(e.run)
  if (e.kind === 'dispatcher') return projectOf(e.head)
  return e.project ?? 'unknown'
}

function groupBy(es: FleetEntry[], by: (e: FleetEntry) => string): [string, FleetEntry[]][] {
  const groups = new Map<string, FleetEntry[]>()
  for (const e of es) {
    const k = by(e)
    const list = groups.get(k)
    if (list) list.push(e)
    else groups.set(k, [e])
  }
  return Array.from(groups.entries())
}

export function groupEntriesByHost(es: FleetEntry[]): [string, FleetEntry[]][] {
  return groupBy(es, entryHost)
}

/** Projects in order of first appearance; a project's dispatchers lead. */
export function groupEntriesByProject(es: FleetEntry[]): [string, FleetEntry[]][] {
  return groupBy(es, entryProject).map(([project, list]) => [
    project,
    [...list.filter((e) => e.kind === 'dispatcher'), ...list.filter((e) => e.kind !== 'dispatcher')],
  ])
}

/** Keeps a crew's dispatcher (or crew header) as the container for its members. */
export function narrowToCrew(es: FleetEntry[], crew: string): FleetEntry[] {
  return es.filter((e) => {
    if (e.kind === 'run') return e.run.crew?.name === crew
    return (e.kind === 'dispatcher' ? e.head.crew?.name : e.crew) === crew
  })
}
