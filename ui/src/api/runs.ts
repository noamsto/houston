// Mirror of runs.State (Go). Exactly one state means "a human is required".
export type RunState =
  | 'thinking'
  | 'running'
  | 'blocked'
  | 'compacting'
  | 'review'
  | 'done'
  | 'failed'
  | 'idle'

export interface TmuxRef {
  session: string
  window: number
  pane_id: string
}

export interface IssueRef {
  id: string
  title?: string
  url?: string
  provider?: string
}

export interface PRRef {
  number: string
  state?: string
  check_state?: string
  mergeable?: string
  draft?: boolean
  url?: string
}

export interface CrewRef {
  name: string
  color?: string
  tier?: string
}

export interface TrailChip {
  tool: string
  hint?: string
  done: boolean
  error?: boolean
}

export interface Activity {
  tool?: string
  hint?: string
  message?: string
  task?: string
  trail?: TrailChip[]
  preview?: string
  turn?: number
}

export interface Question {
  text: string
  via: string // "pane" | "crew"
}

export interface Tokens {
  input: number
  output: number
}

export interface Caps {
  terminal: boolean
  reply: boolean
  kill: boolean
}

// Mirror of runs.Run.
//
// A removal arrives as an ordinary `update` event carrying only `id` and
// `removed: true` — every other field is empty, so consumers must check
// `removed` before reading anything else.
export interface Run {
  id: string
  host?: string
  agent: string
  state: RunState
  repo?: string
  branch?: string
  worktree?: string
  tmux?: TmuxRef
  issue?: IssueRef
  pr?: PRRef
  crew?: CrewRef
  activity: Activity
  question?: Question
  tokens: Tokens
  since?: number
  updated_at: number
  stale?: boolean
  caps: Caps
  removed?: boolean
}

// The pane WS route (`server/server.go:parsePaneTarget`) percent-decodes the
// path twice — once automatically via net/http, once again explicitly — so a
// literal `%`-prefixed pane id (e.g. tmux's `%307`) must be encoded twice here
// to survive both decodes and arrive at the server as the original string.
export function paneWsTarget(paneId: string): string {
  return encodeURIComponent(encodeURIComponent(paneId))
}
