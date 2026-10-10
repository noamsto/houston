// Client-side load loop for houston: a headless mobile Chromium against a
// houston, reporting time-to-interactive per scenario.
//
// The default scenarios WRITE into <status-dir>: it creates hook state files under
// claude/ and appends lines to the transcript that <chat-run-id>'s state file
// points at. <status-dir> must therefore be the state dir of a loadfixture
// (go run -tags tools ./cmd/loadfixture -dir D, then D/state); it refuses to
// start without the .loadfixture marker the generator writes there, and never
// appends to a transcript outside the projects dir the marker names. The
// target must be a houston you started by hand on that dir, for example
//   HOME=D/home houston -status-dir D/state -addr 127.0.0.1:PORT -mode tmux -no-opencode
// (startup.sh's private state dir is a copy and cannot be reused).
//
//   npm i --prefix /tmp/pw playwright-core
//   NODE_PATH=/tmp/pw/node_modules CHROMIUM=$(command -v chromium) \
//     node scripts/perf/client.cjs <houston-port> <status-dir> <run-id> <chat-run-id>
//
// SCENARIOS selects what runs: unset or containing "load" runs the scenarios
// below; "fleet" runs the Fleet list scenario alone and needs only
//   SCENARIOS=fleet node scripts/perf/client.cjs <houston-port>
// (it writes nothing, so any houston will do, fixture or not).
//
// <run-id> is the run whose Chat tab is timed (the largest transcript);
// <chat-run-id> is a second run whose Chat tab the page sits on through the
// resume scenarios.
//
// The browser reaches houston through a local TCP proxy, so the resume
// scenarios can cut its live connections the way a phone does when it sleeps.
// Each scenario runs on a 390x844 mobile viewport with a 4x CPU slowdown:
//   firstLoad  fresh context, navigate to #/fleet
//   reload     same context, page.reload()
//   chat       navigate to the run's Chat tab
//   resume-control  page (on <chat-run-id>'s Chat tab) frozen and hidden for
//                   GAP_MS, then visible again
//   resume-zombie   the same, while every open connection silently stops
//                   passing data (no FIN/RST), as a sleeping phone's do
//   resume-reset    the same, but the connections are reset
// "tti" = the awaited element is rendered and the main thread has had no long
// task for QUIET_MS. For the resume scenarios "liveMs" is how long a session
// started on the server right after the resume takes to reach the page's
// runs stream, and "chatLiveMs" how long a transcript line appended right
// after the resume takes to render in the open Chat tab.
//
// The fleet scenarios ("fleet" at 390x844 with the 4x CPU slowdown,
// "fleet-desktop" at 1280x800 with the same) each open a fresh context on
// #/fleet and wait until the list holds FLEET_EXPECT run cards plus worker
// rows (any count that has stopped changing for QUIET_MS when unset). They
// report tti, "nodes" (elements under the list container: .fleet, or
// .console-list .fleet on desktop), "cards" (.run-card) and "rows"
// (.worker-row; 0 on a build without it). SCREENSHOT_DIR saves
// fleet-mobile.png and fleet-desktop.png there.
const net = require('node:net')
const fs = require('node:fs')
const path = require('node:path')

const GAP_MS = 15000
const QUIET_MS = 500
const LIVE_TIMEOUT_MS = 60000
const SCENARIOS = new Set((process.env.SCENARIOS ?? 'load').split(','))

// proxy forwards a local port to houston. cut('zombie') silently stops the
// connections currently carrying a stream (SSE or WebSocket): what a phone is
// left with when the server's writes were lost while it slept, since nothing
// on a stream makes either side notice. Idle keep-alive sockets pass, as they
// recover once the network is back. cut('reset') resets every connection, as
// a server restart does. New connections always pass.
function proxy(target) {
  const live = new Set()
  const server = net.createServer((c) => {
    const u = net.connect(target, '127.0.0.1')
    const pair = { c, u, dead: false, stream: false }
    live.add(pair)
    c.on('data', (d) => {
      const line = d.toString('latin1', 0, 300).split('\r\n', 1)[0]
      if (/^[A-Z]+ /.test(line)) pair.stream = /^GET \/api\/runs\/(stream|[^ ]+\/(chat\/stream|terminal))[ ?]/.test(line)
      if (!pair.dead) u.write(d)
    })
    u.on('data', (d) => {
      if (!pair.dead) c.write(d)
    })
    const end = () => {
      live.delete(pair)
      c.destroy()
      u.destroy()
    }
    c.on('close', end).on('error', end)
    u.on('close', end).on('error', end)
  })
  return new Promise((resolve) =>
    server.listen(0, '127.0.0.1', () =>
      resolve({
        port: server.address().port,
        cut(mode) {
          for (const pair of live) {
            if (mode === 'reset') {
              pair.c.resetAndDestroy()
              pair.u.destroy()
              live.delete(pair)
            } else if (pair.stream) {
              pair.dead = true // half-open: both ends think it is fine
              live.delete(pair)
            }
          }
        },
        close: () => server.close(),
      }),
    ),
  )
}

// fixtureProjects returns the projects dir a loadfixture state dir names in
// its marker, or exits when statusDir is not a loadfixture state dir.
function fixtureProjects(statusDir) {
  try {
    return fs.realpathSync(fs.readFileSync(path.join(statusDir, '.loadfixture'), 'utf8').trim())
  } catch {
    console.error(`${statusDir} is not a loadfixture state dir (no readable .loadfixture marker); refusing to write into it`)
    process.exit(2)
  }
}

function inside(dir, file) {
  const rel = path.relative(dir, file)
  return rel !== '' && !rel.startsWith('..') && !path.isAbsolute(rel)
}

// say appends an assistant text line to a run's transcript, which the open
// Chat tab should render.
function say(statusDir, projects, runId, text) {
  const sid = runId.replace(/^sess-/, '')
  if (!/^[0-9A-Za-z-]+$/.test(sid)) throw new Error(`bad run id ${runId}`)
  const st = JSON.parse(fs.readFileSync(path.join(statusDir, 'claude', `${sid}.json`), 'utf8'))
  const transcript = fs.realpathSync(st.transcript_path)
  if (!inside(projects, transcript)) throw new Error(`transcript ${transcript} is outside the fixture projects dir ${projects}`)
  const rec = {
    type: 'assistant',
    uuid: `perf-${text}`,
    timestamp: new Date().toISOString(),
    message: { id: `msg-${text}`, role: 'assistant', content: [{ type: 'text', text }] },
  }
  fs.appendFileSync(transcript, JSON.stringify(rec) + '\n')
}

// spawn writes a hook state file for a brand-new session, which the hub
// picks up over fsnotify and every houston version publishes as a run update
// naming that session. Returns the file, for cleanup.
function spawn(statusDir, sid) {
  const file = path.join(statusDir, 'claude', `${sid}.json`)
  const now = Math.floor(Date.now() / 1000)
  fs.writeFileSync(file, JSON.stringify({ version: 1, session_id: sid, state: 'waiting', since: now, updated_at: now, agent: 'claude' }))
  return file
}

async function main() {
  const [port, statusDir, runId, chatRunId] = process.argv.slice(2)
  const load = SCENARIOS.has('load')
  if (!port || (load && (!statusDir || !chatRunId))) {
    console.error('usage: client.cjs <houston-port> <status-dir> <run-id> <chat-run-id>')
    console.error('       SCENARIOS=fleet client.cjs <houston-port>')
    process.exit(2)
  }
  const projects = load ? fixtureProjects(statusDir) : null
  const { chromium } = require('playwright-core')
  // Everything run() opens or creates, for the finally below.
  const held = { px: null, browser: null, spawned: [] }
  try {
    return await run({ chromium, port, statusDir, projects, runId, chatRunId, held, load })
  } finally {
    await held.browser?.close().catch(() => {})
    held.px?.close()
    for (const f of held.spawned) fs.rmSync(f, { force: true })
  }
}

// trackLongTasks runs in the page: window.__long collects the main thread's
// long tasks.
function trackLongTasks() {
  window.__long = []
  new PerformanceObserver((l) => {
    for (const e of l.getEntries()) window.__long.push({ start: e.startTime, dur: e.duration })
  }).observe({ type: 'longtask', buffered: true })
}

// quietAfter resolves once the page's main thread has had no long task for
// QUIET_MS; times are relative to t0, a performance.now() value in the page.
// seenAt is when the awaited content was rendered (now, when omitted).
function quietAfter(p, t0, seenAt) {
  return p.evaluate(
    async ({ t0, quiet, seenAt }) => {
      const seen = seenAt ?? performance.now()
      for (;;) {
        const last = window.__long.filter((l) => l.start + l.dur > t0).at(-1)
        const busyUntil = Math.max(seen, last ? last.start + last.dur : 0)
        if (performance.now() - busyUntil >= quiet) {
          const longs = window.__long.filter((l) => l.start >= t0 && l.start < busyUntil)
          return {
            tti: Math.round(busyUntil - t0),
            longTasks: longs.length,
            longMs: Math.round(longs.reduce((a, l) => a + l.dur, 0)),
          }
        }
        await new Promise((r) => setTimeout(r, 100))
      }
    },
    { t0, quiet: QUIET_MS, seenAt },
  )
}

// fleetScenario opens a fresh context on #/fleet and reports how long the
// list takes to render and how big it is.
async function fleetScenario(browser, base, { mobile, shot }) {
  const ctx = await browser.newContext(
    mobile
      ? { viewport: { width: 390, height: 844 }, deviceScaleFactor: 3, isMobile: true, hasTouch: true }
      : { viewport: { width: 1280, height: 800 } },
  )
  try {
    await ctx.addInitScript(trackLongTasks)
    const p = await ctx.newPage()
    const cdp = await ctx.newCDPSession(p)
    await cdp.send('Emulation.setCPUThrottlingRate', { rate: 4 })
    await p.goto(`${base}/#/fleet`)
    const expect = Number(process.env.FLEET_EXPECT ?? 0)
    const seen = await p
      .waitForFunction(
        ({ expect, quiet }) => {
          const n = document.querySelectorAll('.run-card, .worker-row').length
          const now = performance.now()
          const s = (window.__fleetCount ??= { n: -1, since: now })
          if (s.n !== n) Object.assign(s, { n, since: now })
          return n > 0 && n >= expect && now - s.since >= quiet ? s.since : false
        },
        { expect, quiet: QUIET_MS },
        { timeout: 120000, polling: 100 },
      )
      .then((h) => h.jsonValue())
    const out = await quietAfter(p, 0, seen)
    const counts = await p.evaluate((mobile) => {
      const root = document.querySelector(mobile ? '.fleet' : '.console-list .fleet')
      return {
        nodes: root ? root.querySelectorAll('*').length : 0,
        cards: document.querySelectorAll('.run-card').length,
        rows: document.querySelectorAll('.worker-row').length,
      }
    }, mobile)
    if (shot) await p.screenshot({ path: shot, fullPage: true })
    return { ...out, ...counts }
  } finally {
    await ctx.close()
  }
}

async function run({ chromium, port, statusDir, projects, runId, chatRunId, held, load }) {
  const px = (held.px = await proxy(Number(port)))
  const base = `http://127.0.0.1:${px.port}`
  const results = {}

  const browser = (held.browser = await chromium.launch({ executablePath: process.env.CHROMIUM, headless: true }))
  if (SCENARIOS.has('fleet')) {
    const dir = process.env.SCREENSHOT_DIR
    if (dir) fs.mkdirSync(dir, { recursive: true })
    const shot = (name) => (dir ? path.join(dir, name) : null)
    results.fleet = await fleetScenario(browser, base, { mobile: true, shot: shot('fleet-mobile.png') })
    results['fleet-desktop'] = await fleetScenario(browser, base, { mobile: false, shot: shot('fleet-desktop.png') })
  }
  if (!load) return results

  const ctx = await browser.newContext({
    viewport: { width: 390, height: 844 },
    deviceScaleFactor: 3,
    isMobile: true,
    hasTouch: true,
  })
  await ctx.addInitScript(trackLongTasks)
  await ctx.addInitScript(() => {
    // Lets the resume scenarios flip visibility the way a phone does.
    window.__vis = 'visible'
    Object.defineProperty(document, 'visibilityState', { get: () => window.__vis, configurable: true })
    Object.defineProperty(document, 'hidden', { get: () => window.__vis === 'hidden', configurable: true })
    // Records each EventSource's opens, errors and messages.
    window.__es = []
    const Native = window.EventSource
    window.EventSource = class extends Native {
      constructor(url, init) {
        super(url, init)
        const rec = { url: String(url), opens: 0, errors: 0, msgs: [] }
        window.__es.push(rec)
        this.addEventListener('open', () => rec.opens++)
        this.addEventListener('error', () => rec.errors++)
        for (const type of ['snapshot', 'update'])
          this.addEventListener(type, (e) => rec.msgs.push({ type, at: performance.now(), data: e.data }))
      }
    }
  })
  const p = await ctx.newPage()
  const cdp = await ctx.newCDPSession(p)
  await cdp.send('Emulation.setCPUThrottlingRate', { rate: 4 })
  await cdp.send('Network.enable')

  // Bytes per path, so the snapshot and chat payloads can be reported.
  const bytes = new Map()
  const urls = new Map()
  cdp.on('Network.requestWillBeSent', (e) => urls.set(e.requestId, e.request.url))
  cdp.on('Network.dataReceived', (e) => {
    const key = (urls.get(e.requestId) ?? '?').replace(base, '').replace(/\?.*$/, '')
    bytes.set(key, (bytes.get(key) ?? 0) + e.dataLength)
  })

  // Waits for sel, then for QUIET_MS without a long task; times are relative
  // to t0, a performance.now() value in the page.
  async function interactive(sel, t0) {
    await p.waitForSelector(sel, { timeout: 120000 })
    return quietAfter(p, t0)
  }

  const card = '.run-card'
  const chat = '.chat-bubble, .chat-tool-call'

  bytes.clear()
  await p.goto(`${base}/#/fleet`)
  results.firstLoad = { ...(await interactive(card, 0)), snapshotBytes: bytes.get('/api/runs/stream') ?? null }

  bytes.clear()
  await p.reload()
  results.reload = { ...(await interactive(card, 0)), snapshotBytes: bytes.get('/api/runs/stream') ?? null }

  if (runId) {
    bytes.clear()
    const t0 = await p.evaluate(() => performance.now())
    await p.evaluate((id) => (window.location.hash = `#/fleet/${id}/chat`), runId)
    results.chat = { ...(await interactive(chat, t0)), chatBytes: bytes.get(`/api/runs/${runId}/chat`) ?? null }
    await p.evaluate(() => (window.location.hash = '#/fleet'))
    await p.waitForSelector(card)
  }

  for (const mode of ['control', 'zombie', 'reset']) {
    // Fresh connections per scenario, so one cut cannot leak into the next.
    await p.goto(`${base}/#/fleet/${chatRunId}/chat`)
    await p.reload()
    await interactive(chat, 0)
    bytes.clear()
    await p.evaluate(() => {
      for (const r of window.__es) r.msgs.length = 0
      window.__vis = 'hidden'
      document.dispatchEvent(new Event('visibilitychange'))
    })
    await cdp.send('Page.setWebLifecycleState', { state: 'frozen' })
    if (mode !== 'control') px.cut(mode)
    await new Promise((r) => setTimeout(r, GAP_MS))
    await cdp.send('Page.setWebLifecycleState', { state: 'active' })
    const t0 = await p.evaluate(() => {
      window.__vis = 'visible'
      document.dispatchEvent(new Event('visibilitychange'))
      return performance.now()
    })
    const marker = `perf-${mode}-${Date.now()}`
    held.spawned.push(spawn(statusDir, marker))
    const said = `perf-marker-${mode}-${Date.now()}`
    say(statusDir, projects, chatRunId, said)
    const chatLiveAt = p
      .waitForFunction(
        ({ t0, said }) => {
          const hit = [...document.querySelectorAll('.chat-bubble')].some((b) => b.textContent.includes(said))
          return hit ? performance.now() - t0 : false
        },
        { t0, said },
        { timeout: LIVE_TIMEOUT_MS, polling: 50 },
      )
      .then(
        (h) => h.jsonValue(),
        () => null,
      )
    const liveAt = await p
      .waitForFunction(
        ({ t0, marker }) => {
          const m = window.__es
            .filter((r) => r.url.includes('/api/runs/stream'))
            .flatMap((r) => r.msgs)
            .find((m) => m.at > t0 && m.data.includes(marker))
          return m ? m.at : false
        },
        { t0, marker },
        { timeout: LIVE_TIMEOUT_MS, polling: 50 },
      )
      .then(
        (h) => h.jsonValue(),
        () => null,
      )
    // Main-thread cost of catching up: long tasks from the resume until the
    // page has been quiet for QUIET_MS.
    const chatMs = await chatLiveAt
    const quiet = await interactive(chat, t0)
    results[`resume-${mode}`] = {
      liveMs: liveAt === null ? `not within ${LIVE_TIMEOUT_MS / 1000}s` : Math.round(liveAt - t0),
      chatLiveMs: chatMs === null ? `not within ${LIVE_TIMEOUT_MS / 1000}s` : Math.round(chatMs),
      longTasks: quiet.longTasks,
      longMs: quiet.longMs,
      snapshotBytes: bytes.get('/api/runs/stream') ?? null,
    }
  }

  return results
}

main().then(
  (r) => console.log(JSON.stringify(r, null, 2)),
  (e) => {
    console.error(e)
    process.exit(1)
  },
)
