import type { Run } from '../api/runs'
import type { Mode } from '../api/mode'
import type { DetailTab } from './routes'

export const DETAIL_TAB_LABEL: Record<DetailTab, string> = { crew: 'Crew', chat: 'Chat', terminal: 'Terminal' }

/** Whether a run's terminal can be attached here: a local pane, or a peer that forwards it. */
export function terminalTarget(run: Run): boolean {
  return Boolean(run.tmux) || Boolean(run.host)
}

/** The tabs a run detail offers, in display order. */
export function offeredTabs(run: Run, mode: Mode | null): DetailTab[] {
  const tabs: DetailTab[] = []
  if (mode === 'dispatcher' && run.role === 'dispatcher' && run.crew?.name) tabs.push('crew')
  if (run.caps.chat) tabs.push('chat')
  if (run.caps.terminal) tabs.push('terminal')
  return tabs
}

/** The tab shown for a route, ignoring whether the terminal has ever been live. */
export function activeTab(run: Run, mode: Mode | null, tab: DetailTab | undefined): DetailTab {
  if (offeredTabs(run, mode).includes('crew') && (tab === undefined || tab === 'crew')) return 'crew'
  return tab === 'terminal' && run.caps.terminal ? 'terminal' : 'chat'
}
