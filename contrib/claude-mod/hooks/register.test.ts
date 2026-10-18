import type { On } from 'claude-code'
import { test, expect, mock } from 'claude-code/testing'

type Posted = { socketPath?: string; body: Record<string, unknown> }

const TICK = 200

function rig(
  on: On,
  sessionId: () => string | Promise<string> = () => 'sid-1',
  env: Record<string, string> = {},
) {
  const posted: Posted[] = []
  const clock = mock.clock(on)
  mock.env(on, { HOUSTON_STATE_DIR: '/tmp/hs', ...env })
  on('session.id', async () => ({ value: await sessionId() }))
  on('http.fetch', (_$, e) => {
    posted.push({ socketPath: e.init?.socketPath, body: JSON.parse(e.init?.body ?? '{}') })
    return { value: { status: 204, ok: true, headers: {}, text: '' } }
  })
  const bodies = (ev?: string) => posted.map(p => p.body).filter(b => !ev || b.ev === ev)
  return { posted, clock, bodies, flush: () => clock.advance(TICK) }
}

const BASH = { tool: 'Bash', command: 'echo hi' } as const
const RAN = { ref: 1, text: 'hi', result: { stdout: 'hi', stderr: '', interrupted: false } }
const START = { cwd: '/work', surface: null, isInteractive: false } as const

test('a tool call posts start and end in order and passes the call through untouched', async ($, on) => {
  const { posted, flush } = rig(on)
  let seen: unknown
  on('tool.call', (_$, e) => {
    seen = e
    return RAN
  })

  const got = await $.tool.call(BASH)
  await flush()

  expect(got).toMatchObject(RAN)
  expect(seen).toMatchObject(BASH)
  expect(posted.map(p => p.body.ev)).toEqual(['tool.start', 'tool.end'])
  expect(posted.every(p => p.socketPath === '/tmp/hs/claude-mod.sock')).toBe(true)
  const [start, end] = posted.map(p => p.body)
  expect(start).toMatchObject({ v: 1, sid: 'sid-1', seq: 1, tool: 'Bash', input: { command: 'echo hi' } })
  expect(start!.gen).toBeUndefined()
  expect((end!.seq as number) > (start!.seq as number)).toBe(true)
  expect(end!.boot).toBe(start!.boot)
})

test('records are delivered only once the clock advances', async ($, on) => {
  const { posted, clock } = rig(on)
  on('tool.call', () => RAN)

  await $.tool.call(BASH)
  await clock.settle()
  expect(posted).toHaveLength(0)
  await clock.advance(TICK - 1)
  expect(posted).toHaveLength(0)
  await clock.advance(1)
  expect(posted.map(p => p.body.ev)).toEqual(['tool.start', 'tool.end'])
})

test('a tool.end posted after the call resolves is fetched by the timer', async ($, on) => {
  const { clock, bodies, flush } = rig(on)
  on('tool.call', async () => {
    await clock.sleep(1100)
    return RAN
  })

  const call = $.tool.call(BASH)
  await flush()
  expect(bodies().map(b => b.ev)).toEqual(['tool.start'])
  await clock.advance(900)
  await call
  expect(bodies().map(b => b.ev)).toEqual(['tool.start'])
  await clock.advance(100)
  expect(bodies().map(b => b.ev)).toEqual(['tool.start', 'tool.end'])
})

test('a background Bash call carries its task id', async ($, on) => {
  const { bodies, flush } = rig(on)
  on('tool.call', () => ({ ...RAN, result: { ...RAN.result, backgroundTaskId: 'b1', backgroundedByUser: true } }))

  await $.tool.call({ ...BASH, description: 'Say hi', run_in_background: true })
  await flush()

  expect(bodies('tool.start')[0]).toMatchObject({ bg: true, input: { command: 'echo hi', description: 'Say hi' } })
  expect(bodies('tool.end')[0]).toMatchObject({ task: 'b1', user_bg: true })
})

test('a Monitor call carries its task id and timeout', async ($, on) => {
  const { bodies, flush } = rig(on)
  const monitor = { tool: 'Monitor', description: 'watch CI', timeout_ms: 60000, command: 'gh run watch' } as const
  on('tool.call', (_$, e) => ({
    ref: 1,
    text: 'started',
    result: e.tool === 'Monitor' && e.timeout_ms ? { taskId: 'm1', timeoutMs: e.timeout_ms } : { taskId: 'm2', timeoutMs: 0, persistent: true },
  }))

  await $.tool.call(monitor)
  await $.tool.call({ ...monitor, timeout_ms: 0 })
  await flush()

  const [timed, persistent] = bodies('tool.end')
  expect(bodies('tool.start')[0]).toMatchObject({ input: { description: 'watch CI', command: 'gh run watch' } })
  expect(timed).toMatchObject({ tool: 'Monitor', task: 'm1', timeout_ms: 60000 })
  expect(persistent).toMatchObject({ tool: 'Monitor', task: 'm2' })
  expect(persistent!.timeout_ms).toBeUndefined()
})

test('a TaskStop call names the task it stopped', async ($, on) => {
  const { bodies, flush } = rig(on)
  on('tool.call', (_$, e) => ({
    ref: 1,
    text: 'stopped',
    result: e.tool === 'TaskStop' && e.task_id === 'b1' ? { message: 'ok', task_id: 'b1', task_type: 'shell' } : { message: 'ok' },
  }))

  await $.tool.call({ tool: 'TaskStop', task_id: 'b1' })
  await $.tool.call({ tool: 'TaskStop', shell_id: 'b2' })
  await flush()

  expect(bodies('tool.end').map(b => b.stop_task)).toEqual(['b1', 'b2'])
})

test('an AskUserQuestion call carries its questions, labels and preview presence', async ($, on) => {
  const { bodies, flush } = rig(on)
  on('tool.call', () => ({ ref: 1, text: 'answered', result: {} }))
  const ask = {
    tool: 'AskUserQuestion' as const,
    questions: [
      {
        question: 'Which layout?',
        header: 'Layout',
        multiSelect: false,
        options: [
          { label: 'Grid', description: 'cards', preview: '[ ][ ]' },
          { label: 'List', description: 'rows', preview: '  ' },
        ],
      },
      { question: 'Which theme?', header: 'Theme', multiSelect: true, options: [{ label: 'Dark', description: 'd' }] },
    ],
  }

  await $.tool.call(ask)
  await flush()

  const start = bodies('tool.start')[0]!
  expect(start.input).toEqual({ questions: [{ header: 'Layout', question: 'Which layout?' }] })
  expect(start.ask).toEqual([
    { question: 'Which layout?', options: [{ label: 'Grid', preview: true }, { label: 'List' }] },
    { question: 'Which theme?', multi: true, options: [{ label: 'Dark' }] },
  ])
})

test('a task notification prompt posts only terminal task ends and reaches beneath unchanged', async ($, on) => {
  const { bodies, flush } = rig(on)
  let seen: unknown
  on('prompt.submit', (_$, e) => {
    seen = e
    return e
  })
  const text =
    '<task-notification><task-id>b1</task-id><tool-use-id>toolu_1</tool-use-id><status>completed</status>' +
    '<summary>done <task-id>x1</task-id></summary></task-notification>' +
    '<task-notification><task-id>m2</task-id><event>line <status>killed</status> <task-id>x2</task-id></event></task-notification>' +
    '<task-notification><task-id>m3</task-id><status>killed</status></task-notification>'
  const events = '<task-notification><task-id>m4</task-id><event><status>failed</status></event></task-notification>'

  await $.prompt.submit({ text, wait: false, origin: { kind: 'task-notification' } })
  await $.prompt.submit({ text: events, wait: false, origin: { kind: 'task-notification' } })
  await flush()

  expect(bodies().map(b => b.ev)).toEqual(['task.end'])
  expect(bodies('task.end')[0]!.tasks).toEqual([{ id: 'b1' }, { tool_use: 'toolu_1' }, { id: 'm3' }])
  expect(seen).toMatchObject({ text: events, origin: { kind: 'task-notification' } })
})

test('a failing fetch does not fail the tool call and later posts still go out', async ($, on) => {
  let attempts = 0
  const clock = mock.clock(on)
  mock.env(on, { HOUSTON_STATE_DIR: '/tmp/hs' })
  on('session.id', () => ({ value: 'sid-1' }))
  on('http.fetch', () => {
    attempts++
    throw new Error('houston is down')
  })
  on('tool.call', () => RAN)

  const first = await $.tool.call(BASH)
  await clock.advance(TICK)
  const second = await $.tool.call(BASH)
  await clock.advance(TICK)

  expect(first).toMatchObject(RAN)
  expect(second).toMatchObject(RAN)
  expect(attempts).toBe(4)
})

test('records carry the id of the session they belong to across a session boundary', async ($, on) => {
  let current = 'sid-1'
  const { posted, flush } = rig(on, () => current)
  on('session.start', () => ({ cwd: '/work' }))
  on('session.end', (_$, e) => ({ sessionId: e.sessionId }))
  on('tool.call', () => RAN)

  await $.session.start(START)
  await $.tool.call(BASH)
  await flush()
  await $.session.end({ reason: 'clear', sessionId: 'sid-1', resume: { id: 'sid-1' } })
  current = 'sid-2'
  await $.session.start(START)
  await $.tool.call(BASH)
  await flush()

  expect(posted.map(p => [p.body.ev, p.body.sid])).toEqual([
    ['session.start', 'sid-1'],
    ['tool.start', 'sid-1'],
    ['tool.end', 'sid-1'],
    ['session.end', 'sid-1'],
    ['session.start', 'sid-2'],
    ['tool.start', 'sid-2'],
    ['tool.end', 'sid-2'],
  ])
})

test('records queued before a reset keep their session when drained after the next one started', async ($, on) => {
  let current = 'sid-1'
  const { posted, flush } = rig(on, () => current)
  on('session.start', () => ({ cwd: '/work' }))
  on('session.end', (_$, e) => ({ sessionId: e.sessionId }))
  on('tool.call', () => RAN)

  await $.session.start(START)
  await $.tool.call(BASH)
  await $.session.end({ reason: 'clear', sessionId: 'sid-1', resume: { id: 'sid-1' } })
  current = 'sid-2'
  await $.session.start(START)
  await $.tool.call(BASH)
  await flush()

  expect(posted.map(p => [p.body.ev, p.body.sid])).toEqual([
    ['session.start', 'sid-1'],
    ['tool.start', 'sid-1'],
    ['tool.end', 'sid-1'],
    ['session.end', 'sid-1'],
    ['session.start', 'sid-2'],
    ['tool.start', 'sid-2'],
    ['tool.end', 'sid-2'],
  ])
})

test('a session id lookup that straddles a session end is not cached for the next session', async ($, on) => {
  let current = 'sid-1'
  let calls = 0
  let release = () => {}
  const gate = new Promise<void>(done => (release = done))
  const { posted, clock, flush } = rig(on, async () => {
    const id = current
    if (++calls === 1) await gate
    return id
  })
  on('session.start', () => ({ cwd: '/work' }))
  on('session.end', (_$, e) => ({ sessionId: e.sessionId }))
  on('tool.call', () => RAN)

  await $.session.start(START)
  await flush()
  await $.session.end({ reason: 'clear', sessionId: 'sid-1', resume: { id: 'sid-1' } })
  current = 'sid-2'
  release()
  await clock.settle()
  await $.tool.call(BASH)
  await flush()

  expect(posted.map(p => [p.body.ev, p.body.sid])).toEqual([
    ['session.start', 'sid-1'],
    ['session.end', 'sid-1'],
    ['tool.start', 'sid-2'],
    ['tool.end', 'sid-2'],
  ])
})

test('past 256 queued records the oldest are evicted and the rest keep their order', async ($, on) => {
  const { bodies, flush } = rig(on)
  on('tool.check', () => ({ decision: 'allow' }))

  for (let i = 0; i < 300; i++) await $.tool.check({ tool: 'Bash', input: { command: 'echo hi' } })
  await flush()

  const seqs = bodies().map(b => b.seq)
  expect(seqs).toHaveLength(256)
  expect(seqs[0]).toBe(45)
  expect(seqs[255]).toBe(300)
  expect(seqs.every((s, i) => i === 0 || (s as number) === (seqs[i - 1] as number) + 1)).toBe(true)
})

test('session and turn starts carry the cwd and tmux coordinates', async ($, on) => {
  const { bodies, flush } = rig(on, undefined, { TMUX_PANE: '%7', TMUX: '/tmp/tmux-1000/default,12345,0' })
  on('session.start', () => ({ cwd: '/work' }))
  on('turn.start', (_$, e) => ({ turnId: e.turnId }))
  on('tool.call', () => RAN)

  await $.session.start(START)
  await $.turn.start({ turnId: 't1', text: 'hi' })
  await $.tool.call(BASH)
  await flush()

  expect(bodies('session.start')[0]).toMatchObject({ cwd: '/work', pane: '%7', tmux_server: '12345' })
  expect(bodies('turn.start')[0]).toMatchObject({ turn: 't1', pane: '%7', tmux_server: '12345' })
  expect(bodies('tool.start')[0]!.pane).toBeUndefined()
})

test('a permission request carries its agent, input hint and coordinates and reaches beneath unchanged', async ($, on) => {
  const { bodies, flush } = rig(on, undefined, { TMUX_PANE: '%7', TMUX: '/tmp/tmux,1000/default,12345,0' })
  let seen: unknown
  on('classic.PermissionRequest', (_$, e) => {
    seen = e
    return {}
  })
  const request = {
    tool_name: 'Bash',
    tool_input: { command: 'rm -rf build', description: 'Remove build', timeout: 5000, other: 'x'.repeat(300) },
    agent_id: 'agent-1',
    transcript_path: '/home/u/.claude/projects/p/s.jsonl',
    cwd: '/work',
    permission_suggestions: [],
  }

  await $.classic.PermissionRequest(request)
  await flush()

  expect(bodies('permission')[0]).toEqual(
    expect.objectContaining({
      tool: 'Bash',
      agent: 'agent-1',
      input: { command: 'rm -rf build', description: 'Remove build' },
      suggestions: 0,
      transcript: '/home/u/.claude/projects/p/s.jsonl',
      cwd: '/work',
      pane: '%7',
      tmux_server: '12345',
    }),
  )
  expect(seen).toMatchObject(request)
})

test('input hints are cut to 200 characters', async ($, on) => {
  const { bodies, flush } = rig(on)
  on('tool.call', () => RAN)

  await $.tool.call({ tool: 'Bash', command: 'y'.repeat(500) })
  await flush()

  expect(bodies('tool.start')[0]!.input).toEqual({ command: 'y'.repeat(200) })
})

test('a stop carries the tail of the last message and each task description', async ($, on) => {
  const { bodies, flush } = rig(on)
  let seen: unknown
  on('classic.Stop', (_$, e) => {
    seen = e
    return {}
  })
  const last = 'a'.repeat(400) + '\nShall I go on?'
  const stop = {
    stop_hook_active: false,
    last_assistant_message: last,
    background_tasks: [{ id: 'b1', type: 'shell', status: 'running', description: 'npm test', command: 'npm test' }],
    transcript_path: '/t.jsonl',
    cwd: '/work',
  }

  await $.classic.Stop(stop)
  await $.classic.Stop({ stop_hook_active: false })
  await flush()

  const [first, second] = bodies('stop')
  expect(first).toMatchObject({
    last: last.slice(-300),
    tasks: [{ id: 'b1', type: 'shell', status: 'running', description: 'npm test' }],
    transcript: '/t.jsonl',
    cwd: '/work',
  })
  expect((first!.last as string).length).toBe(300)
  expect((first!.tasks as Record<string, unknown>[])[0]!.command).toBeUndefined()
  expect(second!.last).toBeUndefined()
  expect(seen).toMatchObject({ stop_hook_active: false })
})

test('a notification carries its type, message and transcript', async ($, on) => {
  const { bodies, flush } = rig(on)
  on('classic.Notification', () => ({}))

  await $.classic.Notification({
    message: 'Claude needs your permission to use Bash',
    notification_type: 'permission_prompt',
    transcript_path: '/t.jsonl',
    cwd: '/work',
  })
  await flush()

  expect(bodies('notification')[0]).toMatchObject({
    type: 'permission_prompt',
    message: 'Claude needs your permission to use Bash',
    transcript: '/t.jsonl',
    cwd: '/work',
  })
})

test('a tool check returns the beneath decision unchanged and posts it', async ($, on) => {
  const { bodies, flush } = rig(on)
  on('tool.check', () => ({ decision: 'ask', reason: 'needs a human' }))

  const got = await $.tool.check({ tool: 'Bash', input: { command: 'echo hi' } })
  await flush()

  expect(got).toMatchObject({ decision: 'ask', reason: 'needs a human' })
  expect(bodies()[0]).toMatchObject({ ev: 'tool.check', tool: 'Bash', decision: 'ask' })
})
