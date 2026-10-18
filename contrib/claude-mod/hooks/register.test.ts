import type { On } from 'claude-code'
import { test, expect, mock } from 'claude-code/testing'

type Posted = { socketPath?: string; body: Record<string, unknown> }

function rig(on: On, sessionId: () => string | Promise<string> = () => 'sid-1') {
  const posted: Posted[] = []
  const waiters: { n: number; done: () => void }[] = []
  const arrived = (n: number) =>
    posted.length >= n ? Promise.resolve() : new Promise<void>(done => waiters.push({ n, done }))
  mock.env(on, { HOUSTON_STATE_DIR: '/tmp/hs' })
  on('session.id', async () => ({ value: await sessionId() }))
  on('http.fetch', (_$, e) => {
    posted.push({ socketPath: e.init?.socketPath, body: JSON.parse(e.init?.body ?? '{}') })
    for (const w of waiters) if (posted.length >= w.n) w.done()
    return { value: { status: 204, ok: true, headers: {}, text: '' } }
  })
  return { posted, arrived }
}

const BASH = { tool: 'Bash', command: 'echo hi' } as const
const RAN = { ref: 1, text: 'hi', result: { stdout: 'hi', stderr: '', interrupted: false } }

test('a tool call posts start and end in order and passes the call through untouched', async ($, on) => {
  const { posted, arrived } = rig(on)
  let seen: unknown
  on('tool.call', (_$, e) => {
    seen = e
    return RAN
  })

  const got = await $.tool.call(BASH)
  await arrived(2)

  expect(got).toMatchObject(RAN)
  expect(seen).toMatchObject(BASH)
  expect(posted.map(p => p.body.ev)).toEqual(['tool.start', 'tool.end'])
  expect(posted.every(p => p.socketPath === '/tmp/hs/claude-mod.sock')).toBe(true)
  const [start, end] = posted.map(p => p.body)
  expect(start).toMatchObject({ v: 1, sid: 'sid-1', seq: 1, tool: 'Bash' })
  expect((end!.seq as number) > (start!.seq as number)).toBe(true)
  expect(end!.boot).toBe(start!.boot)
})

test('a background Bash call carries its task id', async ($, on) => {
  const { posted, arrived } = rig(on)
  on('tool.call', () => ({ ...RAN, result: { ...RAN.result, backgroundTaskId: 'b1', backgroundedByUser: true } }))

  await $.tool.call({ ...BASH, run_in_background: true })
  await arrived(2)

  expect(posted[0]!.body.bg).toBe(true)
  expect(posted[1]!.body).toMatchObject({ ev: 'tool.end', task: 'b1', user_bg: true })
})

test('a task notification prompt posts task.end and reaches beneath unchanged', async ($, on) => {
  const { posted, arrived } = rig(on)
  let seen: unknown
  on('prompt.submit', (_$, e) => {
    seen = e
    return e
  })
  const text =
    '<task-notification><task-id>b1</task-id><status>completed</status></task-notification>' +
    '<task-notification><task-id>m2</task-id><status>killed</status></task-notification>'

  await $.prompt.submit({ text, wait: false, origin: { kind: 'task-notification' } })
  await arrived(1)

  expect(posted[0]!.body).toMatchObject({
    ev: 'task.end',
    tasks: [
      { id: 'b1', status: 'completed' },
      { id: 'm2', status: 'killed' },
    ],
  })
  expect(seen).toMatchObject({ text, origin: { kind: 'task-notification' } })
})

test('a failing fetch does not fail the tool call and later posts still go out', async ($, on) => {
  let attempts = 0
  const waiters: { n: number; done: () => void }[] = []
  const attempted = (n: number) =>
    attempts >= n ? Promise.resolve() : new Promise<void>(done => waiters.push({ n, done }))
  mock.env(on, { HOUSTON_STATE_DIR: '/tmp/hs' })
  on('session.id', () => ({ value: 'sid-1' }))
  on('http.fetch', () => {
    attempts++
    for (const w of waiters) if (attempts >= w.n) w.done()
    throw new Error('houston is down')
  })
  on('tool.call', () => RAN)

  const first = await $.tool.call(BASH)
  await attempted(2)
  const second = await $.tool.call(BASH)
  await attempted(4)

  expect(first).toMatchObject(RAN)
  expect(second).toMatchObject(RAN)
  expect(attempts).toBe(4)
})

test('records carry the id of the session they belong to across a session boundary', async ($, on) => {
  let current = 'sid-1'
  const { posted, arrived } = rig(on, () => current)
  on('session.start', () => ({ cwd: '/work' }))
  on('session.end', (_$, e) => ({ sessionId: e.sessionId }))
  on('tool.call', () => RAN)

  await $.session.start({ cwd: '/work', surface: null, isInteractive: false })
  await $.tool.call(BASH)
  await arrived(3)
  await $.session.end({ reason: 'clear', sessionId: 'sid-1', resume: { id: 'sid-1' } })
  current = 'sid-2'
  await $.session.start({ cwd: '/work', surface: null, isInteractive: false })
  await $.tool.call(BASH)
  await arrived(7)

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
  const { posted, arrived } = rig(on, async () => {
    const id = current
    if (++calls === 1) await gate
    return id
  })
  on('session.start', () => ({ cwd: '/work' }))
  on('session.end', (_$, e) => ({ sessionId: e.sessionId }))
  on('tool.call', () => RAN)

  await $.session.start({ cwd: '/work', surface: null, isInteractive: false })
  await $.session.end({ reason: 'clear', sessionId: 'sid-1', resume: { id: 'sid-1' } })
  current = 'sid-2'
  release()
  await $.tool.call(BASH)
  await arrived(4)

  expect(posted.map(p => [p.body.ev, p.body.sid])).toEqual([
    ['session.start', 'sid-1'],
    ['session.end', 'sid-1'],
    ['tool.start', 'sid-2'],
    ['tool.end', 'sid-2'],
  ])
})

test('a tool check returns the beneath decision unchanged and posts it', async ($, on) => {
  const { posted, arrived } = rig(on)
  on('tool.check', () => ({ decision: 'ask', reason: 'needs a human' }))

  const got = await $.tool.check({ tool: 'Bash', input: { command: 'echo hi' } })
  await arrived(1)

  expect(got).toMatchObject({ decision: 'ask', reason: 'needs a human' })
  expect(posted[0]!.body).toMatchObject({ ev: 'tool.check', tool: 'Bash', decision: 'ask' })
})
