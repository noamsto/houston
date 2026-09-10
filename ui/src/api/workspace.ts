// Mirror of server/workspace.go's Workspace contract (I2).
export interface WorkspacePane {
  id: string
  index: number
  active: boolean
  command: string
  agent: boolean
  run_id?: string
}

export interface WorkspaceWindow {
  index: number
  name: string
  active: boolean
  branch?: string
  task?: string
  crew_codename?: string
  panes: WorkspacePane[]
}

export interface WorkspaceSession {
  name: string
  window_count: number
  main_checkout?: WorkspaceWindow[]
  worktrees?: WorkspaceWindow[]
  other?: WorkspaceWindow[]
}

export interface Workspace {
  host: string
  sessions: WorkspaceSession[]
}

export async function fetchWorkspace(): Promise<Workspace> {
  const res = await fetch('/api/workspace')
  if (!res.ok) {
    throw new Error(`fetchWorkspace: ${res.status} ${await res.text()}`)
  }
  return (await res.json()) as Workspace
}
