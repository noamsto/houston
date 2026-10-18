import type { Register, EngineInterface } from 'claude-code'

type Rec = Record<string, unknown>

const boot = crypto.randomUUID()
let seq = 0
let sid = ''
let sock = ''
let pending = 0
let chain: Promise<unknown> = Promise.resolve()

async function socketPath($: EngineInterface): Promise<string> {
  const dir =
    (await $.env.get('HOUSTON_STATE_DIR')) ||
    (await $.env.get('HOUSTON_STATUS_DIR')) ||
    `${await $.env.get('HOME')}/.local/state/houston`
  return `${dir}/claude-mod.sock`
}

function fire($: EngineInterface, rec: Rec) {
  $.http
    .fetch('http://houston/v1/record', {
      method: 'POST',
      socketPath: sock,
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify(rec),
    })
    .catch(() => {})
}

async function settle($: EngineInterface, rec: Rec) {
  sock ||= await socketPath($)
  sid ||= await $.session.id()
  rec.sid ||= sid
  fire($, rec)
}

// seq is taken when the record is built, so it follows hook order even when
// the socket path or session id still has to be awaited; the chain keeps the
// sends in that order.
function post($: EngineInterface, ev: string, fields: Rec = {}) {
  const rec: Rec = { v: 1, boot, seq: ++seq, ts: Date.now(), ev, sid: sid || undefined, ...fields }
  if (sock && rec.sid && !pending) return fire($, rec)
  pending++
  chain = chain
    .then(() => settle($, rec))
    .catch(() => {})
    .then(() => pending--)
}

function parseTaskEnds(text: string) {
  const tasks: { id: string; status?: string }[] = []
  for (const part of text.split('<task-notification')) {
    const status = /<status>([^<]*)<\/status>/.exec(part)?.[1]
    for (const m of part.matchAll(/<task-id>([^<]*)<\/task-id>/g)) tasks.push({ id: m[1]!, status })
  }
  return tasks
}

export const register: Register = on => {
  on('session.start', ($, e, next) => {
    sid = ''
    post($, 'session.start')
    return next(e)
  })

  on('session.end', ($, e, next) => {
    post($, 'session.end', { sid: e.sessionId, reason: e.reason })
    sid = ''
    return next(e)
  })

  on('session.compact', ($, e, next) => {
    post($, 'compact', { trigger: e.trigger, agent: e.agentId })
    return next(e)
  })

  on('turn.start', ($, e, next) => {
    post($, 'turn.start', { turn: e.turnId, agent: e.agentId })
    return next(e)
  })

  on('turn.complete', ($, e, next) => {
    post($, 'turn.complete', { turn: e.turnId, agent: e.agentId, reason: e.reason })
    return next(e)
  })

  on('tool.call', async ($, e, next) => {
    const start: Rec = { tool: e.tool, id: e.tool_use_id, agent: e.agentId }
    if (e.tool === 'Bash') start.bg = e.run_in_background === true || undefined
    if (e.tool === 'AskUserQuestion')
      start.ask = e.questions.map(q => ({
        question: q.question,
        multi: q.multiSelect || undefined,
        options: q.options.map(o => o.label),
      }))
    post($, 'tool.start', start)

    const r = await next(e)
    const end: Rec = { tool: e.tool, id: e.tool_use_id, agent: e.agentId, err: r.isError === true || undefined }
    if ('result' in r && r.isError !== true) {
      const res = r.result as { backgroundTaskId?: string; backgroundedByUser?: boolean; taskId?: string } | undefined
      if (e.tool === 'Bash') {
        end.task = res?.backgroundTaskId
        end.user_bg = res?.backgroundedByUser === true || undefined
      } else if (e.tool === 'Monitor') end.task = res?.taskId
    }
    post($, 'tool.end', end)
    return r
  })

  on('tool.check', async ($, e, next) => {
    const r = await next(e)
    post($, 'tool.check', { tool: e.tool, id: e.tool_use_id, agent: e.agentId, decision: r.decision })
    return r
  })

  on('prompt.submit', ($, e, next) => {
    if (e.origin.kind === 'task-notification') {
      const tasks = parseTaskEnds(e.text)
      if (tasks.length) post($, 'task.end', { tasks })
    }
    return next(e)
  })

  on('classic.Notification', ($, e, next) => {
    post($, 'notification', { type: e.notification_type, message: e.message })
    return next(e)
  })

  on('classic.PermissionRequest', ($, e, next) => {
    post($, 'permission', { tool: e.tool_name, suggestions: e.permission_suggestions?.length ?? 0 })
    return next(e)
  })

  on('classic.Stop', ($, e, next) => {
    post($, 'stop', { tasks: e.background_tasks?.map(t => ({ id: t.id, type: t.type, status: t.status })) })
    return next(e)
  })
}
