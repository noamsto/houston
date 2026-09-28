// Mirror of chat.Update (Go, package chat) plus the server's page/tool wire
// shapes. `chat/` speaks ACP; `_meta` is its extension point (messageId,
// tool, phase, origin, subagent — see docs/ "Chat" section).
export type SessionUpdateKind =
  | 'user_message_chunk'
  | 'agent_message_chunk'
  | 'tool_call'
  | 'tool_call_update'
  | 'plan'

export interface ChatContentBlock {
  type: 'text'
  text: string
}

export type ChatContent =
  | { type: 'content'; content: ChatContentBlock }
  | { type: 'diff'; path: string; oldText: string; newText: string }

export interface ChatLocation {
  path: string
  line?: number
}

export interface ChatMeta {
  messageId?: string
  tool?: string
  phase?: string
  origin?: string
  subagent?: string
  [k: string]: unknown
}

export interface ChatUpdate {
  id: string
  seq: number
  ts: number
  sessionUpdate: SessionUpdateKind
  content?: ChatContent[]
  toolCallId?: string
  title?: string
  kind?: string
  status?: 'pending' | 'in_progress' | 'completed' | 'failed'
  locations?: ChatLocation[]
  _meta?: ChatMeta
}

export interface ChatPage {
  epoch: string
  updates: ChatUpdate[]
  more: boolean
}

export interface ChatToolDetail {
  toolCallId: string
  name: string
  title?: string
  kind?: string
  status?: string
  input?: unknown
  output?: string
  truncated?: boolean
  diff?: { path: string; oldText: string; newText: string }
}

// Thrown when the run has no chat (404: no Session, no reader for its
// engine, or the run itself is unknown) — the caller's cue to not offer the
// Chat tab, not a transient failure.
export class ChatUnavailable extends Error {
  constructor(message = 'chat unavailable') {
    super(message)
    this.name = 'ChatUnavailable'
  }
}

export async function fetchChatPage(
  runId: string,
  opts?: { before?: number; limit?: number; signal?: AbortSignal },
): Promise<ChatPage> {
  const params = new URLSearchParams()
  if (opts?.before !== undefined) params.set('before', String(opts.before))
  if (opts?.limit !== undefined) params.set('limit', String(opts.limit))
  const qs = params.toString()
  const res = await fetch(`/api/runs/${encodeURIComponent(runId)}/chat${qs ? `?${qs}` : ''}`, {
    signal: opts?.signal,
  })
  if (res.status === 404) throw new ChatUnavailable()
  if (!res.ok) throw new Error(`fetchChatPage: ${res.status} ${await res.text()}`)
  return (await res.json()) as ChatPage
}

export async function fetchTool(runId: string, callId: string): Promise<ChatToolDetail> {
  const res = await fetch(`/api/runs/${encodeURIComponent(runId)}/chat/tool/${encodeURIComponent(callId)}`)
  if (!res.ok) throw new Error(`fetchTool: ${res.status} ${await res.text()}`)
  return (await res.json()) as ChatToolDetail
}

export function formatCursor(epoch: string, seq: number): string {
  return `${epoch}.${seq}`
}

export function parseCursor(s: string): { epoch: string; seq: number } | null {
  const i = s.lastIndexOf('.')
  if (i <= 0 || i === s.length - 1) return null
  const epoch = s.slice(0, i)
  const seqPart = s.slice(i + 1)
  if (!/^\d+$/.test(seqPart)) return null
  return { epoch, seq: Number(seqPart) }
}

export function chatStreamURL(runId: string, epoch: string, seq: number): string {
  const after = encodeURIComponent(formatCursor(epoch, seq))
  return `/api/runs/${encodeURIComponent(runId)}/chat/stream?after=${after}`
}
