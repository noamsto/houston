import type { Run } from '../api/runs'
import { isFresh } from './staleness'

export interface CrewCounts {
  working: number
  needsYou: number
  stuck: number
  done: number
  idle: number
  ended: number
}

// Flags are gated on freshness like Fleet's badge; unflagged runs bucket by state.
export function bucket(run: Run, now: number): keyof CrewCounts {
  if (run.attention) {
    if (!isFresh(run, now)) return 'ended'
    return run.attention === 'needs-you' ? 'needsYou' : run.attention
  }
  if (run.state === 'done') return 'ended'
  return run.state === 'idle' ? 'idle' : 'working'
}

// Mirrors the server's restartBusy: a turn that ended on a question is the agent waiting.
export function isBusy(run: Run): boolean {
  switch (run.state) {
    case 'thinking':
    case 'running':
    case 'compacting':
      return true
    case 'blocked':
      return run.question?.kind !== 'turn'
    default:
      return false
  }
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

export function dispatchHref(repoPath: string | undefined, crew: string, host?: string): string {
  const params = host && host !== 'local' ? [`host=${encodeURIComponent(host)}`] : []
  if (repoPath !== undefined) params.push(`repo=${encodeURIComponent(repoPath)}`)
  params.push(`crew=${encodeURIComponent(crew)}`)
  return `#/dispatch?${params.join('&')}`
}
