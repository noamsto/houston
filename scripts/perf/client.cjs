// Client-side load loop for houston: a headless mobile Chromium against a
// houston started by scripts/perf/startup.sh (or any houston on loopback),
// reporting time-to-interactive per scenario.
//
//   npm i --prefix /tmp/pw playwright-core
//   NODE_PATH=/tmp/pw/node_modules CHROMIUM=$(command -v chromium) \
//     node scripts/perf/client.cjs <houston-port> <status-dir> <chat-run-id>
//
// The browser reaches houston through a local TCP proxy, so the resume
// scenarios can cut its live connections the way a phone does when it sleeps.
// Each scenario runs on a 390x844 mobile viewport with a 4x CPU slowdown:
//   firstLoad  fresh context, navigate to #/fleet
//   reload     same context, page.reload()
//   chat       navigate to the run's Chat tab
//   resume-control  page frozen and hidden for GAP_MS, then visible again
//   resume-zombie   the same, while every open connection silently stops
//                   passing data (no FIN/RST), as a sleeping phone's do
//   resume-reset    the same, but the connections are reset
// "tti" = the awaited element is rendered and the main thread has had no long
// task for QUIET_MS. For the resume scenarios "liveMs" is how long a state
// change made on the server right after the resume takes to reach the page.
const net = require('node:net')
const fs = require('node:fs')
const path = require('node:path')
const { chromium } = require('playwright-core')

const GAP_MS = 5000
const QUIET_MS = 500
const LIVE_TIMEOUT_MS = 60000

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

// bump rewrites a session's hook state file with a new turn, which the hub
// picks up over fsnotify and publishes as a run update.
function bump(statusDir, runId, turn) {
  const file = path.join(statusDir, 'claude', `${runId.replace(/^sess-/, '')}.json`)
  const st = JSON.parse(fs.readFileSync(file, 'utf8'))
  st.turn = turn
  st.state = turn % 2 ? 'thinking' : 'waiting'
  st.updated_at = Math.floor(Date.now() / 1000)
  fs.writeFileSync(file, JSON.stringify(st))
}

async function main() {
  const [port, statusDir, runId] = process.argv.slice(2)
  const px = await proxy(Number(port))
  const base = `http://127.0.0.1:${px.port}`
  const results = {}

  const browser = await chromium.launch({ executablePath: process.env.CHROMIUM, headless: true })
  const ctx = await browser.newContext({
    viewport: { width: 390, height: 844 },
    deviceScaleFactor: 3,
    isMobile: true,
    hasTouch: true,
  })
  await ctx.addInitScript(() => {
    window.__long = []
    new PerformanceObserver((l) => {
      for (const e of l.getEntries()) window.__long.push({ start: e.startTime, dur: e.duration })
    }).observe({ type: 'longtask', buffered: true })
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
          this.addEventListener(type, (e) => rec.msgs.push({ type, at: performance.now(), data: e.data.length < 200000 ? e.data : '' }))
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
    return p.evaluate(
      async ({ t0, quiet }) => {
        const seen = performance.now()
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
      { t0, quiet: QUIET_MS },
    )
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

  let turn = 1000
  for (const mode of ['control', 'zombie', 'reset']) {
    // Fresh connections per scenario, so one cut cannot leak into the next.
    await p.reload()
    await interactive(card, 0)
    bytes.clear()
    await p.evaluate(() => {
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
    turn++
    bump(statusDir, runId, turn)
    const marker = `"turn":${turn}`
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
    const quiet = await interactive(card, t0)
    results[`resume-${mode}`] = {
      liveMs: liveAt === null ? `not within ${LIVE_TIMEOUT_MS / 1000}s` : Math.round(liveAt - t0),
      longTasks: quiet.longTasks,
      longMs: quiet.longMs,
      snapshotBytes: bytes.get('/api/runs/stream') ?? null,
    }
  }

  await browser.close()
  px.close()
  return results
}

main().then(
  (r) => console.log(JSON.stringify(r, null, 2)),
  (e) => {
    console.error(e)
    process.exit(1)
  },
)
