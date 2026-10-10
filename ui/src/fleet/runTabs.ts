import type { Run } from '../api/runs'
import type { Mode } from '../api/mode'
import type { DetailTab } from './routes'

export const DETAIL_TAB_LABEL: Record<DetailTab, string> = { crew: 'Crew', chat: 'Chat', terminal: 'Terminal' }

/** The tabs a run detail offers, in display order. */
export function offeredTabs(run: Run, mode: Mode | null): DetailTab[] {
  const tabs: DetailTab[] = []
  if (mode === 'dispatcher' && run.role === 'dispatcher' && run.crew?.name) tabs.push('crew')
  if (run.caps.chat) tabs.push('chat')
  if (run.caps.terminal) tabs.push('terminal')
  return tabs
}
