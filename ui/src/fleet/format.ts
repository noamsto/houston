import type { Run } from '../api/runs'

export function agoLabel(updatedAt: number, now: number): string {
  if (!updatedAt) return '—'
  const s = Math.max(0, Math.floor(now / 1000 - updatedAt))
  if (s < 60) return `${s}s`
  const m = Math.floor(s / 60)
  if (m < 60) return `${m}m`
  const h = Math.floor(m / 60)
  if (h < 24) return `${h}h`
  return `${Math.floor(h / 24)}d`
}

/** The one line describing what this run is doing right now. */
export function subtitle(run: Run): string {
  const a = run.activity
  if (run.state === 'blocked') return a.message || 'waiting on you'
  if (a.tool) return a.hint ? `${a.tool} · ${a.hint}` : a.tool
  if (a.task) return a.task
  if (run.pr) return `PR #${run.pr.number}${run.pr.check_state ? ` · ${run.pr.check_state}` : ''}`
  return run.state
}

/** repo/branch when both are present, otherwise whichever half exists. */
export function nameLabel(run: Run): string {
  if (run.repo && run.branch) return `${run.repo}/${run.branch}`
  return run.repo || run.branch || run.id
}
