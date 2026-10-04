import { describe, expect, it } from 'vitest'
import type { ChatUpdate } from '../api/chat'
import {
  buildItems,
  mergeUpdates,
  provisionalTool,
  reconcileOptimistic,
  toolRowLabel,
} from './chatModel'

function textUpdate(id: string, seq: number, text: string, extra: Partial<ChatUpdate> = {}): ChatUpdate {
  return {
    id,
    seq,
    ts: seq * 1000,
    sessionUpdate: 'agent_message_chunk',
    content: [{ type: 'content', content: { type: 'text', text } }],
    ...extra,
  }
}

function userChunk(id: string, seq: number, text: string, extra: Partial<ChatUpdate> = {}): ChatUpdate {
  return { ...textUpdate(id, seq, text, extra), sessionUpdate: 'user_message_chunk' }
}

function toolCall(id: string, seq: number, extra: Partial<ChatUpdate> = {}): ChatUpdate {
  return { id, seq, ts: seq * 1000, sessionUpdate: 'tool_call', toolCallId: id, status: 'in_progress', ...extra }
}

function toolCallUpdate(id: string, seq: number, toolCallId: string, extra: Partial<ChatUpdate> = {}): ChatUpdate {
  return { id, seq, ts: seq * 1000, sessionUpdate: 'tool_call_update', toolCallId, ...extra }
}

describe('mergeUpdates', () => {
  it('sorts by seq and dedupes by id, incoming wins', () => {
    const existing = [textUpdate('a', 1, 'one'), textUpdate('c', 3, 'three')]
    const incoming = [textUpdate('b', 2, 'two'), textUpdate('a', 1, 'one-updated')]

    expect(mergeUpdates(existing, incoming)).toEqual([
      textUpdate('a', 1, 'one-updated'),
      textUpdate('b', 2, 'two'),
      textUpdate('c', 3, 'three'),
    ])
  })

  it('returns an empty list for two empty inputs', () => {
    expect(mergeUpdates([], [])).toEqual([])
  })
})

describe('commentary classification', () => {
  function commentaryOf(updates: ChatUpdate[], id: string): boolean | undefined {
    const item = buildItems(updates).find((i) => i.id === id)
    return item?.kind === 'assistant' ? item.commentary : undefined
  }

  it('is true when _meta.phase is explicitly commentary', () => {
    const u = textUpdate('a', 1, 'narrating', { _meta: { phase: 'commentary' } })
    expect(commentaryOf([u], 'a')).toBe(true)
  })

  it('is true when a later tool_call shares the messageId', () => {
    const u = textUpdate('a', 1, 'about to read', { _meta: { messageId: 'm1' } })
    const call = toolCall('t1', 2, { _meta: { messageId: 'm1' } })
    expect(commentaryOf([u, call], 'a')).toBe(true)
  })

  it('is false when the shared-messageId tool_call is earlier, not later', () => {
    const call = toolCall('t1', 1, { _meta: { messageId: 'm1' } })
    const u = textUpdate('a', 2, 'final answer', { _meta: { messageId: 'm1' } })
    expect(commentaryOf([call, u], 'a')).toBe(false)
  })

  it('is false with no messageId and no explicit phase', () => {
    const u = textUpdate('a', 1, 'plain text')
    expect(commentaryOf([u], 'a')).toBe(false)
  })

  it('is false when a later tool_call has a different messageId', () => {
    const u = textUpdate('a', 1, 'final answer', { _meta: { messageId: 'm1' } })
    const call = toolCall('t1', 2, { _meta: { messageId: 'm2' } })
    expect(commentaryOf([u, call], 'a')).toBe(false)
  })
})

describe('buildItems', () => {
  it('renders a human user chunk as a user bubble', () => {
    const items = buildItems([userChunk('u1', 1, 'hello')])
    expect(items).toEqual([{ kind: 'user', id: 'u1', text: 'hello', seq: 1 }])
  })

  it('renders a task-notification chunk as a centred divider, not a bubble', () => {
    const items = buildItems([userChunk('u1', 1, 'ignored', { _meta: { origin: 'task-notification' } })])
    expect(items).toEqual([{ kind: 'divider', id: 'u1', text: 'background task finished', seq: 1 }])
  })

  it('joins consecutive assistant chunks sharing a messageId into one item', () => {
    const items = buildItems([
      textUpdate('a1', 1, 'first part', { _meta: { messageId: 'm1' } }),
      textUpdate('a2', 2, 'second part', { _meta: { messageId: 'm1' } }),
    ])
    expect(items).toEqual([
      { kind: 'assistant', id: 'a1', text: 'first part\n\nsecond part', commentary: false, seq: 1 },
    ])
  })

  it('keeps assistant chunks with different messageIds as separate items', () => {
    const items = buildItems([
      textUpdate('a1', 1, 'first', { _meta: { messageId: 'm1' } }),
      textUpdate('a2', 2, 'second', { _meta: { messageId: 'm2' } }),
    ])
    expect(items.map((i) => i.id)).toEqual(['a1', 'a2'])
  })

  it('flags an assistant item commentary when its later tool_call shares the messageId', () => {
    const items = buildItems([
      textUpdate('a1', 1, 'about to read the file', { _meta: { messageId: 'm1' } }),
      toolCall('t1', 2, { _meta: { messageId: 'm1', tool: 'Read' } }),
    ])
    const assistant = items.find((i) => i.kind === 'assistant')
    expect(assistant?.commentary).toBe(true)
  })

  it('collapses a run of consecutive tool_calls into one tools item with per-tool rows', () => {
    const items = buildItems([
      toolCall('t1', 1, { kind: 'read', _meta: { tool: 'Read' } }),
      toolCallUpdate('u1', 2, 't1', { status: 'completed' }),
      toolCall('t2', 3, { kind: 'read', _meta: { tool: 'Read' } }),
      toolCallUpdate('u2', 4, 't2', { status: 'completed' }),
      toolCall('t3', 5, { kind: 'edit', _meta: { tool: 'Edit' } }),
      toolCallUpdate('u3', 6, 't3', { status: 'completed' }),
    ])
    expect(items).toHaveLength(1)
    const tools = items[0]
    if (tools.kind !== 'tools') throw new Error('expected a tools item')
    expect(tools.seq).toBe(1)
    expect(tools.calls.map((c) => c.status)).toEqual(['completed', 'completed', 'completed'])
    expect(tools.rows).toEqual([
      { tool: 'Read', kind: 'read', title: undefined, status: 'completed', count: 2, failed: false },
      { tool: 'Edit', kind: 'edit', title: undefined, status: 'completed', count: 1, failed: false },
    ])
    expect(tools.rows.map(toolRowLabel)).toEqual(['Read ×2', 'Edit'])
  })

  it('never merges a failed call into a non-failed row of the same tool', () => {
    const items = buildItems([
      toolCall('t1', 1, { _meta: { tool: 'Bash' } }),
      toolCallUpdate('u1', 2, 't1', { status: 'completed' }),
      toolCall('t2', 3, { _meta: { tool: 'Bash' } }),
      toolCallUpdate('u2', 4, 't2', { status: 'failed' }),
      toolCall('t3', 5, { _meta: { tool: 'Bash' } }),
      toolCallUpdate('u3', 6, 't3', { status: 'completed' }),
    ])
    const tools = items[0]
    if (tools.kind !== 'tools') throw new Error('expected a tools item')
    expect(tools.rows).toEqual([
      { tool: 'Bash', title: undefined, status: 'completed', count: 1, failed: false },
      { tool: 'Bash', title: undefined, status: 'failed', count: 1, failed: true },
      { tool: 'Bash', title: undefined, status: 'completed', count: 1, failed: false },
    ])
  })

  it('a user chunk between two tool_call runs starts a new tools item', () => {
    const items = buildItems([
      toolCall('t1', 1, { _meta: { tool: 'Read' } }),
      userChunk('u1', 2, 'thanks'),
      toolCall('t2', 3, { _meta: { tool: 'Edit' } }),
    ])
    expect(items.map((i) => i.kind)).toEqual(['tools', 'user', 'tools'])
  })

  it('carries a tool_call_update subagent onto its call', () => {
    const items = buildItems([
      toolCall('t1', 1, { _meta: { tool: 'Task' } }),
      toolCallUpdate('u1', 2, 't1', { status: 'completed', _meta: { subagent: 'agent-42' } }),
    ])
    const tools = items[0]
    if (tools.kind !== 'tools') throw new Error('expected a tools item')
    expect(tools.calls[0].subagent).toBe('agent-42')
  })

  it('returns an empty list for no updates', () => {
    expect(buildItems([])).toEqual([])
  })

  it('classifies commentary in linear time: each tool_call _meta is read a bounded number of times', () => {
    const n = 200
    let metaReads = 0
    const updates: ChatUpdate[] = []
    for (let i = 0; i < n; i++) {
      // Odd chunks never match a later tool_call, forcing a full scan per chunk.
      const messageId = i % 2 === 0 ? `m${i}` : `final${i}`
      updates.push(textUpdate(`a${i}`, 2 * i, `text ${i}`, { _meta: { messageId } }))
      const call = toolCall(`t${i}`, 2 * i + 1)
      const meta = { messageId: `m${i}`, tool: 'Read' }
      Object.defineProperty(call, '_meta', { get: () => { metaReads++; return meta } })
      updates.push(call)
    }

    const items = buildItems(updates)

    expect(items.filter((i) => i.kind === 'assistant' && i.commentary)).toHaveLength(n / 2)
    expect(metaReads).toBeLessThanOrEqual(n * 5)
  })
})

describe('reconcileOptimistic', () => {
  it('confirms a pending bubble matched by trimmed text within the window', () => {
    const pending = [{ localId: 'p1', text: '  hello  ', sentAt: 10_000 }]
    const updates = [userChunk('u1', 1, 'hello', { ts: 10_500 })]
    const result = reconcileOptimistic(pending, updates, 20_000)
    expect(result).toEqual({ confirmed: ['p1'], remaining: [] })
  })

  it('confirms at exactly the 30s late boundary', () => {
    const pending = [{ localId: 'p1', text: 'hello', sentAt: 10_000 }]
    const updates = [userChunk('u1', 1, 'hello', { ts: 40_000 })]
    const result = reconcileOptimistic(pending, updates, 41_000)
    expect(result.confirmed).toEqual(['p1'])
  })

  it('does not confirm just past the 30s late boundary', () => {
    const pending = [{ localId: 'p1', text: 'hello', sentAt: 10_000 }]
    const updates = [userChunk('u1', 1, 'hello', { ts: 40_001 })]
    const result = reconcileOptimistic(pending, updates, 39_000)
    expect(result.confirmed).toEqual([])
    expect(result.remaining).toEqual([{ localId: 'p1', text: 'hello', sentAt: 10_000, unconfirmed: false }])
  })

  it('does not confirm past the 5s early boundary', () => {
    const pending = [{ localId: 'p1', text: 'hello', sentAt: 10_000 }]
    const updates = [userChunk('u1', 1, 'hello', { ts: 4_999 })]
    const result = reconcileOptimistic(pending, updates, 11_000)
    expect(result.confirmed).toEqual([])
  })

  it('flags a pending bubble unconfirmed once 30s have elapsed with no match', () => {
    const pending = [{ localId: 'p1', text: 'hello', sentAt: 0 }]
    const result = reconcileOptimistic(pending, [], 30_001)
    expect(result.remaining).toEqual([{ localId: 'p1', text: 'hello', sentAt: 0, unconfirmed: true }])
  })

  it('does not flag unconfirmed before 30s have elapsed', () => {
    const pending = [{ localId: 'p1', text: 'hello', sentAt: 0 }]
    const result = reconcileOptimistic(pending, [], 29_000)
    expect(result.remaining).toEqual([{ localId: 'p1', text: 'hello', sentAt: 0, unconfirmed: false }])
  })

  it('matches each chunk to at most one pending, oldest pending first', () => {
    const pending = [
      { localId: 'p1', text: 'same', sentAt: 1_000 },
      { localId: 'p2', text: 'same', sentAt: 2_000 },
    ]
    const updates = [userChunk('u1', 1, 'same', { ts: 1_100 })]
    const result = reconcileOptimistic(pending, updates, 3_000)
    expect(result.confirmed).toEqual(['p1'])
    expect(result.remaining.map((r) => r.localId)).toEqual(['p2'])
  })
})

describe('provisionalTool', () => {
  it('is null when the run is idle', () => {
    expect(provisionalTool({ state: 'idle', activity: { tool: 'Read' } }, [], 0)).toBeNull()
  })

  it('is null when running with no activity.tool', () => {
    expect(provisionalTool({ state: 'running', activity: {} }, [], 0)).toBeNull()
  })

  it('shows the running activity tool and hint when nothing has matched it yet', () => {
    const result = provisionalTool({ state: 'running', activity: { tool: 'Bash', hint: 'npm test' } }, [], 0)
    expect(result).toEqual({ tool: 'Bash', hint: 'npm test' })
  })

  it('shows while thinking too', () => {
    const result = provisionalTool({ state: 'thinking', activity: { tool: 'Read' } }, [], 0)
    expect(result).toEqual({ tool: 'Read', hint: undefined })
  })

  it('disappears once a matching tool_call arrives at or after it was shown', () => {
    const updates = [toolCall('t1', 1, { ts: 5000, _meta: { tool: 'Bash' } })]
    const result = provisionalTool({ state: 'running', activity: { tool: 'Bash' } }, updates, 4000)
    expect(result).toBeNull()
  })

  it('matches the tool name case-insensitively', () => {
    const updates = [toolCall('t1', 1, { ts: 5000, _meta: { tool: 'bash' } })]
    const result = provisionalTool({ state: 'running', activity: { tool: 'Bash' } }, updates, 4000)
    expect(result).toBeNull()
  })

  it('still shows when the matching tool_call is older than shownSince', () => {
    const updates = [toolCall('t1', 1, { ts: 1000, _meta: { tool: 'Bash' } })]
    const result = provisionalTool({ state: 'running', activity: { tool: 'Bash' } }, updates, 4000)
    expect(result).toEqual({ tool: 'Bash', hint: undefined })
  })

  it('still shows when a tool_call for a different tool has arrived', () => {
    const updates = [toolCall('t1', 1, { ts: 5000, _meta: { tool: 'Read' } })]
    const result = provisionalTool({ state: 'running', activity: { tool: 'Bash' } }, updates, 4000)
    expect(result).toEqual({ tool: 'Bash', hint: undefined })
  })
})
