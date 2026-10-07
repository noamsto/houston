import { describe, expect, it } from 'vitest'
import type { ChatUpdate } from '../api/chat'
import { commandsDisabledReason, isClaudeAgent, suggestedCommand } from './quickCommands'

function text(id: string, seq: number, body: string, extra: Partial<ChatUpdate> = {}): ChatUpdate {
  return {
    id,
    seq,
    ts: seq * 1000,
    sessionUpdate: 'agent_message_chunk',
    content: [{ type: 'content', content: { type: 'text', text: body } }],
    ...extra,
  }
}

const fence = (body: string, lang = '') => `Try this:\n\n\`\`\`${lang}\n${body}\n\`\`\`\n`

describe('isClaudeAgent', () => {
  it.each([['claude', true], ['claude-code', true], ['amp', false], ['pi', false], [undefined, false]])('%s -> %s', (a, want) => {
    expect(isClaudeAgent(a)).toBe(want)
  })
})

describe('commandsDisabledReason', () => {
  it.each([
    ['idle', null],
    ['blocked', 'waiting on a prompt'],
    ['thinking', 'agent is working'],
    ['running', 'agent is working'],
    ['compacting', 'agent is working'],
    ['done', 'agent is not at its prompt'],
    [undefined, 'agent is not at its prompt'],
  ])('%s -> %s', (state, want) => {
    expect(commandsDisabledReason(state)).toBe(want)
  })
})

describe('suggestedCommand', () => {
  it('finds a fenced slash command in the last assistant message', () => {
    expect(suggestedCommand([text('a', 1, fence('/compact keep the plan', 'text'))])).toBe('/compact keep the plan')
  })

  it('rejects a multi-line body', () => {
    expect(suggestedCommand([text('a', 1, fence('/compact keep\nthe plan'))])).toBeNull()
    expect(suggestedCommand([text('a', 1, fence('/compact\n/clear'))])).toBeNull()
  })

  it('rejects absolute paths', () => {
    expect(suggestedCommand([text('a', 1, fence('/home/u/x.sh'))])).toBeNull()
    expect(suggestedCommand([text('a', 1, fence('/usr/bin/env foo'))])).toBeNull()
  })

  it('accepts namespaced commands', () => {
    expect(suggestedCommand([text('a', 1, fence('/plugin:cmd arg'))])).toBe('/plugin:cmd arg')
  })

  it('skips non-slash blocks and takes the first slash one', () => {
    const body = `${fence('ls -la')}\n${fence('/clear')}`
    expect(suggestedCommand([text('a', 1, body)])).toBe('/clear')
  })

  it('is null without a block or with only a non-slash block', () => {
    expect(suggestedCommand([text('a', 1, 'run /compact please')])).toBeNull()
    expect(suggestedCommand([text('a', 1, fence('echo hi'))])).toBeNull()
    expect(suggestedCommand([])).toBeNull()
  })

  it('is null when anything follows the assistant message', () => {
    const a = text('a', 1, fence('/compact'))
    const user: ChatUpdate = { ...text('u', 2, 'ok'), sessionUpdate: 'user_message_chunk' }
    const tool: ChatUpdate = { id: 't', seq: 2, ts: 2000, sessionUpdate: 'tool_call', toolCallId: 't', _meta: { tool: 'Bash' } }
    expect(suggestedCommand([a, user])).toBeNull()
    expect(suggestedCommand([a, tool])).toBeNull()
  })

  it('is null for commentary', () => {
    expect(suggestedCommand([text('a', 1, fence('/compact'), { _meta: { phase: 'commentary' } })])).toBeNull()
  })
})
