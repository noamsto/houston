import type { ChatUpdate } from '../api/chat'
import { buildItems } from '../fleet/chatModel'

export function isClaudeAgent(agent?: string): boolean {
  return agent === 'claude' || agent === 'claude-code'
}

// A hook 'waiting' is demoted to idle by the runs layer, and 'waiting' is not a
// RunState, so idle is the only state that means the prompt is ready.
export function commandsDisabledReason(state?: string): string | null {
  if (state === 'idle') return null
  if (state === 'blocked') return 'waiting on a prompt'
  if (state === 'thinking' || state === 'running' || state === 'compacting') return 'agent is working'
  return 'agent is not at its prompt'
}

// Single line only: a multi-line block or an absolute path is not a command.
const SLASH_COMMAND = /^\/[a-z][\w:-]*([ \t][^\n]*)?$/i

const FENCE = /```[^\n]*\n([\s\S]*?)\n?```/g

/**
 * A slash command the agent's latest reply proposes in a fenced block. Only
 * the very last chat item counts: once anything follows it, the suggestion is
 * stale.
 */
export function suggestedCommand(updates: ChatUpdate[]): string | null {
  const last = buildItems(updates).at(-1)
  if (!last || last.kind !== 'assistant' || last.commentary) return null
  for (const m of last.text.matchAll(FENCE)) {
    const body = m[1].trim()
    if (SLASH_COMMAND.test(body)) return body
  }
  return null
}
