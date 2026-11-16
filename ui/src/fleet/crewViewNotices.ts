import type { Run } from '../api/runs'

export function crewViewNotices(runs: Run[]): string[] {
  return runs.flatMap((r) => (r.role === 'dispatcher' && r.crew?.view_notice ? [r.crew.view_notice] : []))
}
