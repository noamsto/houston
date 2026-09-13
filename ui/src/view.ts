export type View = 'agents' | 'panes' | 'fleet'

export function viewForHash(hash: string): View {
  if (hash === '#/agents') return 'agents'
  if (hash === '#/panes') return 'panes'
  return 'fleet'
}
