import type { Register, EngineInterface, BuiltinToolResults } from 'claude-code'

type Rec = Record<string, unknown>
type Queued = { gen: number; body: Rec }

const boot = crypto.randomUUID()
const queueCap = 256
const queue: Queued[] = []
let seq = 0
// A generation is one session's span of records: bumped at session.start and
// after session.end, so a record queued before a reset keeps its own session.
let gen = 0
let sid = ''
const leftIds = new Map<number, string>()
let ticking = false
let draining = false
let sock = ''
let coordsRead = false
let pane = ''
let tmuxServer = ''

// Records that let houston join the session to its tmux pane: a session after
// /clear starts with turn.start, and classic events carry the transcript.
const withCoords = new Set(['session.start', 'turn.start', 'notification', 'permission', 'stop'])
const hintKeys = ['description', 'command', 'file_path', 'path', 'pattern', 'url', 'prompt']

async function socketPath($: EngineInterface): Promise<string> {
  const dir =
    (await $.env.get('HOUSTON_STATE_DIR')) ||
    (await $.env.get('HOUSTON_STATUS_DIR')) ||
    `${await $.env.get('HOME')}/.local/state/houston`
  return `${dir}/claude-mod.sock`
}

async function readCoords($: EngineInterface) {
  pane = (await $.env.get('TMUX_PANE')) ?? ''
  // $TMUX is "<socket>,<server-pid>,<session-id>" and the socket path may
  // itself hold a comma, so the pid is indexed from the right.
  const fields = ((await $.env.get('TMUX')) ?? '').split(',')
  tmuxServer = fields.length >= 2 ? fields[fields.length - 2]! : ''
  coordsRead = true
}

function bump(leftId: string) {
  if (leftId) leftIds.set(gen, leftId)
  gen++
  sid = ''
}

// The records' own session id: session.end names it, an earlier generation's
// was remembered when it was left, and only the current one may be asked for.
async function sessionOf($: EngineInterface, q: Queued): Promise<string | undefined> {
  if (typeof q.body.sid === 'string' && q.body.sid) return q.body.sid
  if (q.gen !== gen) return leftIds.get(q.gen)
  if (sid) return sid
  const id = await $.session.id()
  if (q.gen !== gen) return leftIds.get(q.gen)
  sid = id
  return id
}

function fire($: EngineInterface, body: Rec) {
  $.http
    .fetch('http://houston/v1/record', {
      method: 'POST',
      socketPath: sock,
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify(body),
    })
    .catch(() => {})
}

// Runs on the timer, never inside the hook that queued the record, so a record
// posted from a dispatch the engine abandoned is still sent.
async function drain($: EngineInterface) {
  if (draining) return
  draining = true
  try {
    sock ||= await socketPath($)
    if (!coordsRead) await readCoords($)
    while (queue.length) {
      const q = queue.shift()!
      const id = await sessionOf($, q)
      if (!id) continue
      const body: Rec = { ...q.body, sid: id }
      if (withCoords.has(body.ev as string)) {
        if (pane) body.pane = pane
        if (tmuxServer) body.tmux_server = tmuxServer
      }
      fire($, body)
    }
    const oldest = queue[0]?.gen ?? gen
    for (const g of leftIds.keys()) if (g < oldest) leftIds.delete(g)
  } finally {
    draining = false
  }
}

function tick($: EngineInterface) {
  if (ticking) return
  ticking = true
  $.clock.every(200, () => {
    drain($).catch(() => {})
  })
}

function evictOldest() {
  queue.shift()
}

// seq is taken when the record is built, so it follows hook order; the timer
// sends in queue order.
function post($: EngineInterface, ev: string, fields: Rec = {}) {
  if (queue.length >= queueCap) evictOldest()
  queue.push({ gen, body: { v: 1, boot, seq: ++seq, ts: Date.now(), ev, ...fields } })
  tick($)
}

function cut(s: string, n: number): string {
  return s.length > n ? Array.from(s).slice(0, n).join('') : s
}

function tail(s: string, n: number): string {
  return s.length > n ? Array.from(s).slice(-n).join('') : s
}

function field(x: unknown, key: string): unknown {
  return x && typeof x === 'object' ? (x as Rec)[key] : undefined
}

function text(x: unknown, key: string): string | undefined {
  const v = field(x, key)
  return typeof v === 'string' && v ? v : undefined
}

// The subset of a tool's input houston turns into an activity hint.
function inputHint(tool: string, x: unknown): Rec | undefined {
  const hint: Rec = {}
  for (const key of hintKeys) {
    const v = text(x, key)
    if (v) hint[key] = cut(v, 200)
  }
  const questions = field(x, 'questions')
  if (tool === 'AskUserQuestion' && Array.isArray(questions) && questions.length) {
    const header = text(questions[0], 'header')
    const question = text(questions[0], 'question')
    hint.questions = [{ header: header && cut(header, 200), question: question && cut(question, 200) }]
  }
  return Object.keys(hint).length ? hint : undefined
}

// Only a notification's header is ours: a monitor's event body embeds
// arbitrary output, and a part with no terminal status ends nothing.
function parseTaskEnds(notification: string) {
  const tasks: Rec[] = []
  for (const part of notification.split('<task-notification')) {
    const head = part.split('<summary>')[0]!.split('<event>')[0]!
    if (!/<status>(completed|failed|killed|stopped)<\/status>/.test(head)) continue
    for (const m of head.matchAll(/<task-id>([^<]*)<\/task-id>/g)) tasks.push({ id: m[1] })
    for (const m of head.matchAll(/<tool-use-id>([^<]*)<\/tool-use-id>/g)) tasks.push({ tool_use: m[1] })
  }
  return tasks
}

export const register: Register = on => {
  on('session.start', ($, e, next) => {
    bump(sid)
    tick($)
    post($, 'session.start', { cwd: e.cwd })
    return next(e)
  })

  on('session.end', ($, e, next) => {
    post($, 'session.end', { sid: e.sessionId, reason: e.reason })
    bump(e.sessionId)
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
    const tool: string = e.tool
    const start: Rec = { tool, id: e.tool_use_id, agent: e.agentId, input: inputHint(tool, e) }
    if (e.tool === 'Bash') start.bg = e.run_in_background === true || undefined
    if (e.tool === 'AskUserQuestion')
      start.ask = e.questions.map(q => ({
        question: cut(q.question, 500),
        multi: q.multiSelect || undefined,
        options: q.options.map(o => ({ label: cut(o.label, 200), preview: o.preview?.trim() ? true : undefined })),
      }))
    post($, 'tool.start', start)

    const r = await next(e)
    const end: Rec = { tool, id: e.tool_use_id, agent: e.agentId, err: r.isError === true || undefined }
    if ('result' in r && r.isError !== true) {
      if (e.tool === 'Bash') {
        const res = r.result as Partial<BuiltinToolResults['Bash']> | undefined
        end.task = res?.backgroundTaskId
        end.user_bg = res?.backgroundedByUser === true || undefined
      } else if (e.tool === 'Monitor') {
        const res = r.result as Partial<BuiltinToolResults['Monitor']> | undefined
        end.task = res?.taskId
        if (typeof res?.timeoutMs === 'number' && res.timeoutMs > 0) end.timeout_ms = res.timeoutMs
      } else if (tool === 'TaskStop' || tool === 'KillShell') {
        end.stop_task = text(r.result, 'task_id') ?? text(e, 'task_id') ?? text(e, 'shell_id')
      }
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
    post($, 'notification', {
      type: e.notification_type,
      message: e.message,
      transcript: e.transcript_path,
      cwd: e.cwd,
    })
    return next(e)
  })

  on('classic.PermissionRequest', ($, e, next) => {
    post($, 'permission', {
      tool: e.tool_name,
      agent: e.agent_id,
      input: inputHint(e.tool_name, e.tool_input),
      suggestions: e.permission_suggestions?.length ?? 0,
      transcript: e.transcript_path,
      cwd: e.cwd,
    })
    return next(e)
  })

  on('classic.Stop', ($, e, next) => {
    post($, 'stop', {
      last: e.last_assistant_message ? tail(e.last_assistant_message, 300) : undefined,
      tasks: e.background_tasks?.map(t => ({
        id: t.id,
        type: t.type,
        status: t.status,
        description: cut(t.description, 200),
      })),
      transcript: e.transcript_path,
      cwd: e.cwd,
    })
    return next(e)
  })
}
