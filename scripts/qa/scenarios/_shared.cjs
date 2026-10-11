// Helpers shared by the fleet, nav and workspace scenarios. Not a scenario
// itself (qa.cjs only loads names from its AREAS list).
//
// ensureStates(ctx) adds the run states the loadfixture crews lack, inside the
// private tmux server and $S only:
//   qa-blocked  stamped pane `@claude_status waiting <now>`  -> blocked / needs-you
//   qa-stuck    stamped pane `@claude_status error <now>`    -> failed  / stuck
//   qa-bg       stamped pane plus a hook state file and a tiny transcript whose
//               Bash tool_use has run_in_background:true and a non-error result
//               naming task id qabg1                          -> background [1 task]
// All three sit in $S/repos/qa-repo, so Fleet's project is qa-repo. Idempotent.
//
// The remaining helpers re-implement the UI's own rules (ui/src/fleet/staleness.ts,
// fleetList.ts, fleetEntries.ts) so a scenario can compute what Fleet must show
// from /api/runs and compare it with the DOM.
const fs = require('node:fs')
const path = require('node:path')

const BG_SESSION = '11111111-1111-4111-8111-111111111111'
const FRESH_MS = 60 * 60 * 1000
const REVIEW_HISTORY_MS = 24 * 60 * 60 * 1000
const MEMBER_CAP = 5
const COLLAPSE_OVER = 6

async function ensureStates(ctx) {
  const repo = path.join(ctx.S, 'repos', 'qa-repo')
  const windows = () =>
    new Map(
      ctx
        .tmux('list-panes', '-a', '-F', '#{window_name} #{pane_id}')
        .split('\n')
        .filter(Boolean)
        .map((l) => l.split(' ')),
    )
  const open = (name) => {
    if (windows().has(name)) return windows().get(name)
    const ids = ctx.tmux('new-window', '-d', '-t', 'qa:', '-n', name, '-c', repo, '-P', '-F', '#{window_id} #{pane_id}', 'sleep', '100000').trim()
    const [win, pane] = ids.split(' ')
    ctx.tmux('set-option', '-w', '-t', win, '@git_root', repo)
    return pane
  }
  const now = Math.floor(Date.now() / 1000)
  const fresh = !windows().has('qa-bg')

  const blocked = open('qa-blocked')
  ctx.tmux('set-option', '-p', '-t', blocked, '@claude_status', `waiting ${now}`)
  const stuck = open('qa-stuck')
  ctx.tmux('set-option', '-p', '-t', stuck, '@claude_status', `error ${now}`)
  const bg = open('qa-bg')
  ctx.tmux('set-option', '-p', '-t', bg, '@claude_status', `idle ${now}`)

  if (fresh) {
    const server = ctx.tmux('display-message', '-p', '#{pid}').trim()
    const dir = path.join(ctx.S, 'home', '.claude', 'projects', '-tmp-qa-bg')
    fs.mkdirSync(dir, { recursive: true })
    const transcript = path.join(dir, `${BG_SESSION}.jsonl`)
    const ts = new Date().toISOString()
    const base = { cwd: repo, isSidechain: false, sessionId: BG_SESSION, timestamp: ts, version: '2.1.0' }
    const lines = [
      { ...base, type: 'user', uuid: 'u0', message: { role: 'user', content: 'run sleep in the background' } },
      {
        ...base,
        type: 'assistant',
        uuid: 'u1',
        parentUuid: 'u0',
        message: {
          id: 'msg_qa',
          role: 'assistant',
          type: 'message',
          model: 'claude-haiku-4-5',
          usage: { input_tokens: 10, output_tokens: 5 },
          content: [{ type: 'tool_use', id: 'toolu_qa_bg', name: 'Bash', input: { command: 'sleep 600', description: 'qa sleep', run_in_background: true } }],
        },
      },
      {
        ...base,
        type: 'user',
        uuid: 'u2',
        parentUuid: 'u1',
        message: {
          role: 'user',
          content: [{ type: 'tool_result', tool_use_id: 'toolu_qa_bg', is_error: false, content: 'Command running in background with ID: qabg1. Output is being written to: /tmp/qa' }],
        },
      },
    ]
    fs.writeFileSync(transcript, lines.map((l) => JSON.stringify(l)).join('\n') + '\n')
    const window = ctx.tmux('display-message', '-p', '-t', bg, '#{window_index}').trim()
    const state = {
      agent: 'claude',
      cwd: repo,
      session_id: BG_SESSION,
      since: now,
      state: 'waiting',
      tmux_pane: bg,
      tmux_server: server,
      tmux_session: 'qa',
      tmux_window: window,
      transcript_path: transcript,
      turn: 1,
      updated_at: now,
      version: 1,
    }
    fs.writeFileSync(path.join(ctx.S, 'state', 'claude', `${BG_SESSION}.json`), JSON.stringify(state) + '\n')
  }

  await ctx.waitFor(
    async () => {
      const runs = await runsOf(ctx)
      return (
        runs.some((r) => r.project === 'qa-repo' && r.state === 'blocked') &&
        runs.some((r) => r.project === 'qa-repo' && r.attention === 'stuck') &&
        runs.some((r) => r.background?.length)
      )
    },
    { timeout: 15000, interval: 500, message: 'qa-blocked / qa-stuck / qa-bg runs did not appear in /api/runs' },
  )
}

async function runsOf(ctx) {
  const r = await ctx.api('/api/runs')
  if (r.status !== 200 || !Array.isArray(r.json)) throw new Error(`GET /api/runs -> ${r.status}`)
  return r.json
}

const isFresh = (r, now) => Boolean(r.updated_at) && now - r.updated_at * 1000 <= FRESH_MS
const needsYou = (r, now) => r.state === 'blocked' && isFresh(r, now)
const isEnded = (r) => r.state === 'done' && !r.attention
function isHistory(r, now) {
  if (isEnded(r)) return true
  if (r.state === 'done' || r.state === 'failed') return !isFresh(r, now)
  if (r.state === 'review' && r.caps?.terminal !== true) return r.updated_at > 0 && now - r.updated_at * 1000 > REVIEW_HISTORY_MS
  return false
}
const projectOf = (r) => r.project || r.repo || 'unknown'
const nameLabel = (r) => (r.repo && r.branch ? `${r.repo}/${r.branch}` : r.repo || r.branch || r.id)
const hostOf = (r) => r.host || 'local'

function filterRuns(runs, filter, now) {
  return runs.filter((r) => {
    if (filter === 'needs-you') return r.state === 'blocked'
    if (filter === 'stuck') return r.attention === 'stuck'
    if (filter === 'done') return r.attention === 'done'
    if (filter === 'active') return !isHistory(r, now)
    return true
  })
}

// What Fleet renders for a filter in dispatcher mode: top-level cards (plain
// runs plus dispatcher heads) and worker rows (members of heads and of orphan
// crews), as run objects.
function fleetExpected(runs, filter, now) {
  const heads = new Map()
  const sorted = [...runs].sort((a, b) => (b.since ?? 0) - (a.since ?? 0) || (a.id < b.id ? -1 : 1))
  for (const r of sorted) {
    if (r.role !== 'dispatcher' || !r.crew?.name) continue
    const id = `${hostOf(r)}\0${r.crew.name}`
    if (!heads.has(id)) heads.set(id, r)
  }
  const members = new Map()
  const plain = []
  for (const r of runs) {
    if (r.role === 'worker' && r.crew?.name) {
      const id = `${hostOf(r)}\0${r.crew.name}`
      if (!members.has(id)) members.set(id, [])
      members.get(id).push(r)
    } else if (r.role !== 'dispatcher' || heads.get(`${hostOf(r)}\0${r.crew?.name ?? ''}`) !== r) {
      plain.push(r)
    }
  }
  const cards = filterRuns(plain, filter, now)
  const rows = []
  const groups = []
  const matching = new Set(filterRuns([...heads.values()], filter, now))
  for (const [id, head] of heads) {
    const all = members.get(id) ?? []
    const shown = filterRuns(all, filter, now)
    if (!matching.has(head) && shown.length === 0) continue
    cards.push(head)
    rows.push(...shown)
    groups.push({ kind: 'dispatcher', crew: head.crew.name, head, all, shown })
  }
  for (const [id, all] of members) {
    if (heads.has(id)) continue
    const shown = filterRuns(all, filter, now)
    if (shown.length === 0) continue
    rows.push(...shown)
    groups.push({ kind: 'crew', crew: all[0].crew.name, head: null, all, shown })
  }
  return { cards, rows, groups }
}

// Page-side readers. They run inside page.evaluate, so they close over nothing.
function readCards() {
  const dotState = (el) => /--state-([a-z]+)/.exec(el?.getAttribute('style') ?? '')?.[1] ?? ''
  return [...document.querySelectorAll('.fleet .run-card')].map((c) => ({
    project: c.querySelector('.run-project')?.textContent ?? '',
    name: c.querySelector('.run-name')?.textContent ?? '',
    state: dotState(c.querySelector('.run-dot')),
    flags: ['attention', 'muted', 'stuck', 'done', 'history'].filter((k) => c.classList.contains(k)).join(','),
    role: c.querySelector('.run-role')?.textContent ?? '',
    stuckChip: Boolean(c.querySelector('.run-chip.stuck')),
    doneChip: Boolean(c.querySelector('.run-chip.done')),
    bg: c.querySelector('.run-chip.bg')?.textContent ?? '',
    crewLine: c.querySelector('.run-crew')?.textContent ?? '',
  }))
}

function readRows() {
  const dotState = (el) => /--state-([a-z]+)/.exec(el?.getAttribute('style') ?? '')?.[1] ?? ''
  return [...document.querySelectorAll('.fleet .worker-row')].map((r) => ({
    codename: r.querySelector('.worker-row-codename')?.textContent ?? '',
    title: r.querySelector('.worker-row-title')?.textContent ?? '',
    state: dotState(r.querySelector('.run-dot')),
    attention: ['needs-you', 'stuck', 'done'].find((k) => r.classList.contains(k)) ?? '',
    chip: r.querySelector('.worker-row-attention, .worker-row-idle')?.textContent ?? '',
    swatch: getComputedStyle(r.querySelector('.worker-row-swatch')).backgroundColor,
  }))
}

// The same descriptors computed from a run, for a multiset comparison.
function cardOf(r, now) {
  const att = needsYou(r, now)
  const staleBlocked = r.state === 'blocked' && !isFresh(r, now)
  const stuck = r.attention === 'stuck' && isFresh(r, now)
  const done = r.attention === 'done' && isFresh(r, now)
  const flags = [
    att ? 'attention' : staleBlocked ? 'attention,muted' : '',
    stuck ? 'stuck' : r.attention === 'stuck' ? 'stuck,muted' : done ? 'done' : r.attention === 'done' ? 'done,muted' : '',
    isHistory(r, now) ? 'history' : '',
  ]
    .filter(Boolean)
    .join(',')
  return {
    project: projectOf(r),
    name: r.branch || nameLabel(r),
    state: r.state,
    flags: ['attention', 'muted', 'stuck', 'done', 'history'].filter((k) => flags.split(',').includes(k)).join(','),
    role: r.role ?? '',
    stuckChip: r.attention === 'stuck',
    doneChip: r.attention === 'done',
    bg: r.background?.length ? `${r.background.length} bg` : '',
  }
}

function rowOf(r) {
  const attention = r.attention ?? (r.state === 'blocked' ? 'needs-you' : '')
  const label = { 'needs-you': 'needs you', stuck: 'stuck', done: 'done' }
  return {
    codename: r.crew?.codename || 'worker',
    title: r.crew?.title || r.issue?.title || r.branch || '',
    state: r.state,
    attention,
    chip: attention ? label[attention] : r.state === 'idle' ? 'idle' : '',
  }
}

const hexToRgb = (hex) => {
  const m = /^#([0-9a-f]{2})([0-9a-f]{2})([0-9a-f]{2})$/i.exec(hex)
  return m ? `rgb(${parseInt(m[1], 16)}, ${parseInt(m[2], 16)}, ${parseInt(m[3], 16)})` : ''
}

// diffSets compares two lists of plain objects as multisets over the given keys
// and returns a human description of the first differences, or '' when equal.
function diffSets(got, want, keys, label) {
  const key = (o) => keys.map((k) => `${k}=${o[k]}`).join(' | ')
  const count = (list) => {
    const m = new Map()
    for (const o of list) m.set(key(o), (m.get(key(o)) ?? 0) + 1)
    return m
  }
  const g = count(got)
  const w = count(want)
  const missing = []
  const extra = []
  for (const [k, n] of w) if ((g.get(k) ?? 0) < n) missing.push(`${n - (g.get(k) ?? 0)}x ${k}`)
  for (const [k, n] of g) if ((w.get(k) ?? 0) < n) extra.push(`${n - (w.get(k) ?? 0)}x ${k}`)
  if (!missing.length && !extra.length) return ''
  return `${label}: missing [${missing.slice(0, 4).join('; ')}] unexpected [${extra.slice(0, 4).join('; ')}]`
}

module.exports = {
  BG_SESSION,
  MEMBER_CAP,
  COLLAPSE_OVER,
  ensureStates,
  runsOf,
  isFresh,
  needsYou,
  isHistory,
  projectOf,
  filterRuns,
  fleetExpected,
  readCards,
  readRows,
  cardOf,
  rowOf,
  hexToRgb,
  diffSets,
}
