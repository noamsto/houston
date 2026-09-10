// Mirror of runs.State (Go). Exactly one state means "a human is required".
// Can also be '' on the wire: a crew layer that has nothing left to say (R5)
// publishes no state opinion, and runtime already tolerates the empty value.
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
  name: string // crew id; '' when only tmux knows this run — such a run belongs to no crew group
  codename?: string
  color?: string // always '#rrggbb' when present; never a tmux colour name
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

// Mirror of server/runs_reply.go's response contract. `refused` (409, the
// worker's own stderr) and `failed` (502/504/network, houston's fault) must
// stay distinct — collapsing them is what makes "surface the failure
// honestly" untestable.
export type ReplyOutcome =
  | { kind: 'delivered' }
  | { kind: 'refused'; reason: string }
  | { kind: 'rejected'; reason: string }
  | { kind: 'failed'; reason: string }

export async function replyRun(id: string, text: string): Promise<ReplyOutcome> {
  let res: Response
  try {
    res = await fetch(`/api/runs/${id}/reply`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ text }),
    })
  } catch {
    return { kind: 'failed', reason: 'network error' }
  }
  if (res.status === 204) return { kind: 'delivered' }
  // http.Error appends a trailing newline server-side; trim it, not the text itself.
  const reason = (await res.text()).trim()
  if (res.status === 409) return { kind: 'refused', reason }
  if (res.status === 400 || res.status === 404 || res.status === 413) return { kind: 'rejected', reason }
  return { kind: 'failed', reason }
}

// The pane WS route (`server/server.go:parsePaneTarget`) percent-decodes the
// path twice — once automatically via net/http, once again explicitly — so a
// literal `%`-prefixed pane id (e.g. tmux's `%307`) must be encoded twice here
// to survive both decodes and arrive at the server as the original string.
export function paneWsTarget(paneId: string): string {
  return encodeURIComponent(encodeURIComponent(paneId))
}
