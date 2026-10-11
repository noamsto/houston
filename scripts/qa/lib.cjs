// Shared helpers for the QA scenarios (scripts/qa/scenarios/<name>.cjs), driven
// by scripts/qa/qa.cjs against a harness made by scripts/qa/setup.sh.
//
// A scenario module exports `async run(ctx)`; ctx is this API plus `browser`
// (one shared headless Chromium). Nothing here talks to any server but the
// houston under test (127.0.0.1:$PORT) and the private tmux server ($S/tmux).
//
// Environment: S (scratch root, or the first non-flag argv) holds qa.env, the
// KEY=VALUE file setup.sh writes: S PORT TMUX_TMPDIR NODE_PATH CHROMIUM
// CLAUDE_PANE PI_PANE WT_PANE LESS_PANE CREW1 CREW2 (any may be empty). The file
// is authoritative: a process-environment value that differs from it is a
// refusal, except NODE_PATH and TMUX_TMPDIR, where the file silently wins
// (NODE_PATH holds playwright-core and qa.cjs prepends it; the caller's own
// TMUX_TMPDIR is expected to differ; ctx.tmux reads it only to refuse a default
// socket dir). S must be a marked
// /tmp/hqa.<random> dir and PORT within 17470-17499. SHOTS overrides the
// screenshot dir (default $S/shots).
//
// API (ctx.*):
//   env                   parsed qa.env (strings, '' when absent)
//   S, PORT, baseUrl      scratch root, houston port, 'http://127.0.0.1:$PORT'
//   violations            shared array of URLs the guard refused; qa.cjs fails the
//                         run if any remains. A scenario that provokes a refusal
//                         on purpose must splice its own entries out.
//   newContext(browser, kind)
//                         kind 'mobile' (390x844, touch, isMobile, DPR 3) or
//                         'desktop' (1280x800). Installs the guard: only
//                         http(s)/ws(s) to 127.0.0.1:$PORT passes, anything else is
//                         recorded in `violations` and aborted/closed, as is any
//                         POST to /api/dispatch* that no page.route fixture
//                         fulfilled first. Service workers are blocked. Returns
//                         the BrowserContext.
//   newPage(browser, kind) -> {context, page}   newContext plus one page
//   url(hash = '')        baseUrl + '/' + hash, e.g. url('#/fleet'). The first load
//                         of the SPA issues the auth cookie, so pages just goto it.
//   api(path, {method, body, headers})
//                         node-side request to houston with the Bearer token from
//                         $S/state/token. Resolves {status, headers, text, json}
//                         (json is null when the body is not JSON). path must start
//                         with '/'. Throws for any host/port other than
//                         127.0.0.1:$PORT, and for any method but GET on a path
//                         starting with /api/dispatch (the dispatch POSTs are
//                         browser-side page.route fixtures only). body: object ->
//                         JSON, string -> as is.
//   tmux(...args)         run `env -u TMUX -u TMUX_PANE TMUX_TMPDIR=$S/tmux tmux
//                         -f /dev/null ...args` on the private server; returns
//                         stdout (string), throws on non-zero exit, and refuses when
//                         $S/tmux is missing or the socket dir is a default one.
//   capture(pane, opts)   tmux capture-pane -p -t pane (opts.lines = scrollback
//                         lines to include; opts.escapes adds -e).
//   check(area, id, result, note, shot)
//                         record and print one JSON line
//                         {"area":N,"check":"id","result":"pass|fail|skip","note":"","shot":""}
//                         (note/shot omitted when empty). `skip` needs a reason in note.
//   expect(area, id, fn)  await fn(); the return value (a string note, or
//                         {note, shot}) becomes a pass line, a thrown error a fail
//                         line carrying its message. Returns true on pass.
//   shot(page, name)      full-viewport PNG to $SHOTS/<name>.png; returns the path.
//   waitFor(fn, {timeout=10000, interval=100, message})
//                         poll an (async) fn until it returns truthy; resolves its
//                         value, rejects with `message` on timeout. `message` is a
//                         string, or a function (sync or async) evaluated at the
//                         timeout so it can report the state then.
//   sleep(ms)
//   results               every check line recorded so far
//
// Result lines go to stdout; diagnostics should go to stderr.
const fs = require('node:fs')
const http = require('node:http')
const path = require('node:path')
const { execFileSync } = require('node:child_process')

function parseEnvFile(file) {
  const out = {}
  let text
  try {
    text = fs.readFileSync(file, 'utf8')
  } catch {
    return out
  }
  for (const raw of text.split('\n')) {
    const line = raw.trim()
    if (!line || line.startsWith('#')) continue
    const m = /^(?:export\s+)?([A-Za-z_][A-Za-z0-9_]*)=(.*)$/.exec(line)
    if (!m) continue
    let v = m[2].trim()
    if (v.length >= 2 && (v[0] === '"' || v[0] === "'") && v.endsWith(v[0])) v = v.slice(1, -1)
    out[m[1]] = v
  }
  return out
}

const PORT_MIN = 17470
const PORT_MAX = 17499
const FILE_WINS = new Set(['NODE_PATH', 'TMUX_TMPDIR'])

const KEYS = ['S', 'PORT', 'TMUX_TMPDIR', 'NODE_PATH', 'CHROMIUM', 'CLAUDE_PANE', 'PI_PANE', 'WT_PANE', 'LESS_PANE', 'CREW1', 'CREW2']

const VIEWPORTS = {
  mobile: { viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true, deviceScaleFactor: 3 },
  desktop: { viewport: { width: 1280, height: 800 }, deviceScaleFactor: 1 },
}

const sleep = (ms) => new Promise((r) => setTimeout(r, ms))

function load(argv = process.argv.slice(2)) {
  const S = process.env.S || argv.find((a) => !a.startsWith('-')) || ''
  if (!S) throw new Error('S not set: pass the scratch root as env S or the first argument')
  if (!/^\/tmp\/hqa\.[A-Za-z0-9]+$/.test(S)) throw new Error(`refusing: S must be a /tmp/hqa.<random> dir, got: ${S}`)
  if (!fs.existsSync(path.join(S, '.houstonqa'))) throw new Error(`refusing: ${S} is not a QA scratch dir (no .houstonqa marker)`)
  const file = parseEnvFile(path.join(S, 'qa.env'))
  const env = {}
  for (const k of KEYS) {
    const fromProc = process.env[k]
    if (!FILE_WINS.has(k) && fromProc && fromProc !== (file[k] ?? '')) {
      throw new Error(`refusing: ${k} in the process environment differs from ${S}/qa.env; unset it`)
    }
    env[k] = file[k] || ''
  }
  if (env.S !== S) throw new Error(`refusing: ${S}/qa.env names S=${env.S || '(none)'}`)
  if (!/^\d+$/.test(env.PORT)) throw new Error(`no PORT in ${S}/qa.env`)
  const port = Number(env.PORT)
  if (port < PORT_MIN || port > PORT_MAX) throw new Error(`refusing port ${port}; only ${PORT_MIN}-${PORT_MAX}`)
  return createLib(env)
}

function createLib(env) {
  const { S, PORT } = env
  const baseUrl = `http://127.0.0.1:${PORT}`
  const shotsDir = process.env.SHOTS || path.join(S, 'shots')
  const violations = []
  const results = []

  const sameOrigin = (u) => {
    try {
      const p = new URL(u)
      return ['http:', 'https:', 'ws:', 'wss:'].includes(p.protocol) && p.hostname === '127.0.0.1' && p.port === PORT
    } catch {
      return false
    }
  }

  async function newContext(browser, kind) {
    const opts = VIEWPORTS[kind]
    if (!opts) throw new Error(`newContext: unknown kind ${kind} (mobile|desktop)`)
    const context = await browser.newContext({ ...opts, serviceWorkers: 'block' })
    // Page-level page.route fixtures take precedence over this context route, so
    // a fulfilled dispatch POST never reaches it; one that does is a stray.
    await context.route('**/*', (route) => {
      const req = route.request()
      const u = req.url()
      if (!sameOrigin(u)) {
        violations.push(u)
        return route.abort()
      }
      if (req.method() === 'POST' && new URL(u).pathname.startsWith('/api/dispatch')) {
        violations.push(`POST ${u}`)
        return route.abort()
      }
      return route.continue()
    })
    await context.routeWebSocket(/.*/, (ws) => {
      const u = ws.url()
      if (sameOrigin(u)) return ws.connectToServer()
      violations.push(u)
      return ws.close()
    })
    return context
  }

  async function newPage(browser, kind) {
    const context = await newContext(browser, kind)
    return { context, page: await context.newPage() }
  }

  const url = (hash = '') => `${baseUrl}/${hash}`

  let tokenCache = null
  function token() {
    tokenCache ??= fs.readFileSync(path.join(S, 'state', 'token'), 'utf8').trim()
    return tokenCache
  }

  function api(p, { method = 'GET', body, headers = {} } = {}) {
    if (typeof p !== 'string' || !p.startsWith('/')) throw new Error(`api: path must start with '/': ${p}`)
    const target = new URL(p, baseUrl)
    if (target.hostname !== '127.0.0.1' || target.port !== PORT || target.protocol !== 'http:') {
      throw new Error(`api: refusing ${target.origin}; only ${baseUrl}`)
    }
    method = method.toUpperCase()
    if (target.pathname.startsWith('/api/dispatch') && method !== 'GET') {
      throw new Error(`api: refusing ${method} ${target.pathname}; dispatch routes are GET-only from node`)
    }
    const h = { Authorization: `Bearer ${token()}`, ...headers }
    let payload
    if (body !== undefined) {
      if (typeof body === 'string') payload = body
      else {
        payload = JSON.stringify(body)
        h['Content-Type'] ??= 'application/json'
      }
      h['Content-Length'] = Buffer.byteLength(payload)
    }
    return new Promise((resolve, reject) => {
      const req = http.request(
        { host: '127.0.0.1', port: Number(PORT), path: target.pathname + target.search, method, headers: h },
        (res) => {
          const chunks = []
          res.on('data', (c) => chunks.push(c))
          res.on('end', () => {
            const text = Buffer.concat(chunks).toString('utf8')
            let json = null
            try {
              json = JSON.parse(text)
            } catch {}
            resolve({ status: res.statusCode, headers: res.headers, text, json })
          })
          res.on('error', reject)
        },
      )
      req.setTimeout(30000, () => req.destroy(new Error(`api: ${method} ${p} timed out`)))
      req.on('error', reject)
      if (payload !== undefined) req.write(payload)
      req.end()
    })
  }

  const tmuxDir = path.join(S, 'tmux')
  function tmux(...args) {
    if (!fs.existsSync(tmuxDir)) throw new Error(`tmux: ${tmuxDir} missing; refusing to touch any other tmux server`)
    const sockDir = `tmux-${process.getuid()}`
    const want = path.resolve(tmuxDir, sockDir)
    for (const d of [path.join('/tmp', sockDir), path.join(process.env.TMUX_TMPDIR || '/tmp', sockDir)]) {
      if (path.resolve(d) === want) throw new Error(`tmux: refusing ${want}, a default tmux socket dir`)
    }
    return execFileSync(
      'env',
      ['-u', 'TMUX', '-u', 'TMUX_PANE', `TMUX_TMPDIR=${tmuxDir}`, 'tmux', '-f', '/dev/null', ...args.map(String)],
      { encoding: 'utf8', timeout: 15000, stdio: ['ignore', 'pipe', 'pipe'] },
    )
  }

  function capture(pane, { lines, escapes } = {}) {
    const a = ['capture-pane', '-p']
    if (escapes) a.push('-e')
    if (lines) a.push('-S', `-${lines}`)
    return tmux(...a, '-t', pane)
  }

  function check(area, id, result, note = '', shotPath = '') {
    if (!['pass', 'fail', 'skip'].includes(result)) throw new Error(`check: bad result ${result}`)
    const line = { area, check: id, result }
    if (note) line.note = String(note)
    if (shotPath) line.shot = shotPath
    results.push(line)
    console.log(JSON.stringify(line))
    return result === 'pass'
  }

  async function expect(area, id, fn) {
    try {
      const r = await fn()
      const note = r && typeof r === 'object' ? r.note : r
      const s = r && typeof r === 'object' ? r.shot : ''
      return check(area, id, 'pass', typeof note === 'string' ? note : '', s)
    } catch (e) {
      return check(area, id, 'fail', e instanceof Error ? e.message : String(e))
    }
  }

  async function shot(page, name) {
    fs.mkdirSync(shotsDir, { recursive: true })
    const file = path.join(shotsDir, `${name.replace(/[^A-Za-z0-9._-]/g, '_')}.png`)
    await page.screenshot({ path: file, type: 'png' })
    return file
  }

  async function waitFor(fn, { timeout = 10000, interval = 100, message = 'waitFor timed out' } = {}) {
    const end = Date.now() + timeout
    for (;;) {
      const v = await fn()
      if (v) return v
      if (Date.now() >= end) throw new Error(typeof message === 'function' ? await message() : message)
      await sleep(interval)
    }
  }

  return { env, S, PORT, baseUrl, violations, results, newContext, newPage, url, api, tmux, capture, check, expect, shot, waitFor, sleep }
}

module.exports = { load, createLib, parseEnvFile }
