import type { Register, EngineInterface, BuiltinToolResults, Timer } from 'claude-code'

type Rec = Record<string, unknown>
type Queued = { gen: number; body: Rec }

const boot = crypto.randomUUID()
const queueCap = 256
const tickMs = 200
// Ticks with nothing queued before a heartbeat: counted in ticks, not wall
// time, so houston sees one every 15 s of a running timer.
const heartbeatTicks = 15000 / tickMs
// A timer silent this long is dead (a refused period ends an interval), and a
// drain running this long is stuck on a host call.
const timerStaleMs = 2000
const drainStaleMs = 5000
// houston answers a body over 16 KiB with 413.
const maxBodyBytes = 15000
// Cut in this order until a record fits; ids, sequence and state stay.
const bulky = ['ask', 'tasks', 'last', 'input', 'message']
const encoder = new TextEncoder()
const queue: Queued[] = []
let seq = 0
// A generation is one session's span of records: bumped at session.start and
// after session.end, so a record queued before a reset keeps its own session.
let gen = 0
let sid = ''
const leftIds = new Map<number, string>()
let timer: Timer | undefined
let lastTick = 0
let quietTicks = 0
// Each drain gets a run number; one whose number is no longer current was
// given up on and sends nothing more.
let drainRun = 0
let draining: Promise<void> | undefined
let drainStarted = 0
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

function bodyBytes(body: Rec): number {
  return encoder.encode(JSON.stringify(body)).length
}

function fire($: EngineInterface, body: Rec) {
  $.http
    .fetch('http://houston.invalid/v1/record', {
      method: 'POST',
      socketPath: sock,
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify(body),
    })
    .catch(() => {})
}

// A record leaves the queue only once its session id is known, so a failed
// lookup leaves it for the next tick.
async function send($: EngineInterface, run: number) {
  try {
    sock ||= await socketPath($)
    if (!coordsRead) await readCoords($)
    while (queue.length && run === drainRun) {
      const q = queue[0]!
      const id = await sessionOf($, q)
      if (run !== drainRun) return
      // Evicted while the lookup ran.
      if (queue[0] !== q) continue
      queue.shift()
      if (!id) continue
      const body: Rec = { ...q.body, sid: id }
      if (withCoords.has(body.ev as string)) {
        if (pane) body.pane = pane
        if (tmuxServer) body.tmux_server = tmuxServer
      }
      for (const key of bulky) {
        if (bodyBytes(body) <= maxBodyBytes) break
        delete body[key]
      }
      fire($, body)
    }
  } finally {
    const oldest = queue[0]?.gen ?? gen
    for (const g of leftIds.keys()) if (g < oldest) leftIds.delete(g)
  }
}

// Joins the drain under way, or starts one when none runs or the one running
// is stuck.
function drain($: EngineInterface): Promise<void> {
  if (draining && Date.now() - drainStarted <= drainStaleMs) return draining
  const run = ++drainRun
  drainStarted = Date.now()
  const p = send($, run).finally(() => {
    if (run === drainRun) draining = undefined
  })
  draining = p
  return p
}

// Sends what is queued now, from inside a hook: waits out a drain already
// under way, which may have taken its records before the last one was queued.
async function flush($: EngineInterface) {
  await drain($).catch(() => {})
  await drain($).catch(() => {})
}

// Runs the sends on the timer, never inside the hook that queued the record,
// so a record posted from a dispatch the engine abandoned is still sent.
function startTimer($: EngineInterface) {
  timer?.cancel()
  lastTick = Date.now()
  quietTicks = 0
  timer = $.clock.every(tickMs, () => {
    lastTick = Date.now()
    if (++quietTicks >= heartbeatTicks) post($, 'heartbeat')
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
  quietTicks = 0
  if (!timer || Date.now() - lastTick > timerStaleMs) startTimer($)
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
// arbitrary output, and a part with no terminal status ends nothing. A
// notification opens a piece of the text split at its closing tags, so an
// opening tag inside an event body is never read as one.
function parseTaskEnds(notification: string) {
  const tasks: Rec[] = []
  for (const piece of notification.split('</task-notification>')) {
    const open = /^\s*<task-notification\b[^>]*>/.exec(piece)
    if (!open) continue
    const head = piece.slice(open[0].length).split('<summary>')[0]!.split('<event>')[0]!
    if (!/<status>(completed|failed|killed|stopped)<\/status>/.test(head)) continue
    for (const m of head.matchAll(/<task-id>([^<]*)<\/task-id>/g)) tasks.push({ id: m[1] })
    for (const m of head.matchAll(/<tool-use-id>([^<]*)<\/tool-use-id>/g)) tasks.push({ tool_use: m[1] })
  }
  return tasks
}

export const register: Register = on => {
  on('session.start', ($, e, next) => {
    bump(sid)
    post($, 'session.start', { cwd: e.cwd })
    return next(e)
  })

  // The timer may not outlive an exit, so the end and whatever is still
  // queued go out before the engine's end step.
  on('session.end', async ($, e, next) => {
    post($, 'session.end', { sid: e.sessionId, reason: e.reason })
    bump(e.sessionId)
    await flush($)
    return next(e)
  })

  on('session.compact', async ($, e, next) => {
    post($, 'compact', { trigger: e.trigger, agent: e.agentId })
    try {
      return await next(e)
    } finally {
      post($, 'compact.end', { agent: e.agentId })
    }
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
      message: cut(e.message, 500),
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
      tasks: e.background_tasks?.slice(0, 32).map(t => ({
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
