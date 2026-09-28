import type { ChatUpdate } from '../api/chat'

/** Seq-ascending, deduped by id — a later update with the same id wins. */
export function mergeUpdates(existing: ChatUpdate[], incoming: ChatUpdate[]): ChatUpdate[] {
  const byId = new Map<string, ChatUpdate>()
  for (const u of existing) byId.set(u.id, u)
  for (const u of incoming) byId.set(u.id, u)
  return Array.from(byId.values()).sort((a, b) => a.seq - b.seq)
}

/**
 * An `agent_message_chunk` is commentary when explicitly marked so, or when
 * a later tool_call in the same stream shares its messageId (R1: the reader
 * emits text immediately, before the tool call that follows it is known).
 */
export function isCommentary(u: ChatUpdate, all: ChatUpdate[]): boolean {
  if (u._meta?.phase === 'commentary') return true
  const messageId = u._meta?.messageId
  if (!messageId) return false
  return all.some((o) => o.sessionUpdate === 'tool_call' && o.seq > u.seq && o._meta?.messageId === messageId)
}

function textOf(u: ChatUpdate): string {
  return (u.content ?? [])
    .filter((c) => c.type === 'content')
    .map((c) => (c.type === 'content' ? c.content.text : ''))
    .join('')
}

export interface UserItem {
  kind: 'user'
  id: string
  text: string
  seq: number
}

export interface DividerItem {
  kind: 'divider'
  id: string
  text: string
  seq: number
}

export interface AssistantItem {
  kind: 'assistant'
  id: string
  text: string
  commentary: boolean
  seq: number
}

export interface ToolCall {
  toolCallId: string
  tool: string
  title?: string
  kind?: string
  status?: string
  subagent?: string
  seq: number
}

export interface ToolRow {
  tool: string
  title?: string
  status?: string
  count: number
  failed: boolean
}

export interface ToolsItem {
  kind: 'tools'
  id: string
  calls: ToolCall[]
  rows: ToolRow[]
  seq: number
}

export type ChatItem = UserItem | DividerItem | AssistantItem | ToolsItem

function isFailed(call: ToolCall): boolean {
  return call.status === 'failed'
}

/** collapseTrail-style rows: consecutive calls of the same tool AND same failed-ness merge. */
function toolRows(calls: ToolCall[]): ToolRow[] {
  const rows: ToolRow[] = []
  for (const call of calls) {
    const failed = isFailed(call)
    const last = rows[rows.length - 1]
    if (last && last.tool === call.tool && last.failed === failed) {
      last.title = call.title
      last.status = call.status
      last.count++
    } else {
      rows.push({ tool: call.tool, title: call.title, status: call.status, count: 1, failed })
    }
  }
  return rows
}

export function toolsSummary(rows: ToolRow[]): string {
  return rows.map((r) => (r.count > 1 ? `${r.tool} ×${r.count}` : r.tool)).join(' · ')
}

/**
 * Turns the update list into render items in stream order: a user bubble, a
 * task-notification divider, assistant text (consecutive chunks sharing a
 * messageId joined), or a tools row collapsing a run of consecutive
 * tool_calls (tool_call_update never breaks the run — it folds into its
 * call by toolCallId). Each item's `seq` is the minimum seq of its members.
 */
export function buildItems(updates: ChatUpdate[]): ChatItem[] {
  const sorted = [...updates].sort((a, b) => a.seq - b.seq)
  const items: ChatItem[] = []
  const toolCallsById = new Map<string, ToolCall>()

  let currentTools: ToolsItem | null = null
  let currentAssistant: AssistantItem | null = null
  let currentAssistantMessageId: string | undefined

  for (const u of sorted) {
    if (u.sessionUpdate === 'user_message_chunk') {
      currentTools = null
      currentAssistant = null
      currentAssistantMessageId = undefined
      if (u._meta?.origin === 'task-notification') {
        items.push({ kind: 'divider', id: u.id, text: 'background task finished', seq: u.seq })
      } else {
        items.push({ kind: 'user', id: u.id, text: textOf(u), seq: u.seq })
      }
      continue
    }

    if (u.sessionUpdate === 'agent_message_chunk') {
      currentTools = null
      const messageId = u._meta?.messageId
      if (currentAssistant && messageId && messageId === currentAssistantMessageId) {
        currentAssistant.text += '\n\n' + textOf(u)
        currentAssistant.commentary = currentAssistant.commentary || isCommentary(u, sorted)
      } else {
        currentAssistant = { kind: 'assistant', id: u.id, text: textOf(u), commentary: isCommentary(u, sorted), seq: u.seq }
        currentAssistantMessageId = messageId
        items.push(currentAssistant)
      }
      continue
    }

    if (u.sessionUpdate === 'tool_call') {
      currentAssistant = null
      currentAssistantMessageId = undefined
      const call: ToolCall = {
        toolCallId: u.toolCallId ?? u.id,
        tool: u._meta?.tool ?? u.title ?? 'tool',
        title: u.title,
        kind: u.kind,
        status: u.status,
        subagent: u._meta?.subagent,
        seq: u.seq,
      }
      toolCallsById.set(call.toolCallId, call)
      if (!currentTools) {
        currentTools = { kind: 'tools', id: u.id, calls: [], rows: [], seq: u.seq }
        items.push(currentTools)
      }
      currentTools.calls.push(call)
      continue
    }

    if (u.sessionUpdate === 'tool_call_update') {
      const call = u.toolCallId ? toolCallsById.get(u.toolCallId) : undefined
      if (call) {
        if (u.status) call.status = u.status
        if (u.title) call.title = u.title
        if (u._meta?.subagent) call.subagent = u._meta.subagent
      }
      continue
    }
  }

  for (const item of items) {
    if (item.kind === 'tools') item.rows = toolRows(item.calls)
  }

  return items
}

export interface Optimistic {
  localId: string
  text: string
  sentAt: number
}

export interface ReconcileResult {
  confirmed: string[]
  remaining: (Optimistic & { unconfirmed: boolean })[]
}

const OPTIMISTIC_EARLY_MS = 5000
const OPTIMISTIC_LATE_MS = 30000

/**
 * Matches an optimistically-shown bubble to the `user_message_chunk` it
 * became, by trimmed text within [sentAt-5s, sentAt+30s]. Each chunk matches
 * at most one pending bubble; pending bubbles claim a chunk oldest-sent
 * first. Unmatched after 30s is flagged unconfirmed, not dropped.
 */
export function reconcileOptimistic(pending: Optimistic[], updates: ChatUpdate[], now: number): ReconcileResult {
  const chunks = updates
    .filter((u) => u.sessionUpdate === 'user_message_chunk')
    .map((u) => ({ text: textOf(u).trim(), ts: u.ts }))
    .sort((a, b) => a.ts - b.ts)
  const claimed = new Set<number>()

  const confirmed: string[] = []
  const remaining: (Optimistic & { unconfirmed: boolean })[] = []

  for (const p of [...pending].sort((a, b) => a.sentAt - b.sentAt)) {
    const trimmed = p.text.trim()
    const idx = chunks.findIndex(
      (c, i) => !claimed.has(i) && c.text === trimmed && c.ts >= p.sentAt - OPTIMISTIC_EARLY_MS && c.ts <= p.sentAt + OPTIMISTIC_LATE_MS,
    )
    if (idx >= 0) {
      claimed.add(idx)
      confirmed.push(p.localId)
    } else {
      remaining.push({ ...p, unconfirmed: now - p.sentAt > OPTIMISTIC_LATE_MS })
    }
  }

  return { confirmed, remaining }
}

export interface ProvisionalTool {
  tool: string
  hint?: string
}

/**
 * A provisional tool row from the run's live activity, shown only while the
 * run is actually working and no confirmed tool_call for that tool has
 * arrived since it was first shown.
 */
export function provisionalTool(
  run: { state: string; activity: { tool?: string; hint?: string } },
  updates: ChatUpdate[],
  shownSince: number,
): ProvisionalTool | null {
  if (run.state !== 'running' && run.state !== 'thinking') return null
  const tool = run.activity.tool
  if (!tool) return null
  const matched = updates.some((u) => u.sessionUpdate === 'tool_call' && u._meta?.tool === tool && u.ts >= shownSince)
  if (matched) return null
  return { tool, hint: run.activity.hint }
}
