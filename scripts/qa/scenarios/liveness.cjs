// Area 10: stream liveness. Pings, reopening after a hidden spell, and
// surviving a houston restart without a reload.
//
// ui/src/hooks/streamLiveness.ts reads exactly two things: document.visibilityState
// (and the `visibilitychange` event on document) with Date.now() deltas, and a
// 60 s silence timer. "Hidden" therefore means a visibilitychange event fired
// while visibilityState is 'hidden'; hiddenMs is the Date.now() distance to the
// next visibilitychange that reads 'visible'. The scenario drives exactly that
// (the property override plus the event), which is the whole input the code has.
//
// The restart check ends by leaving houston running in the mode it started in.
const path = require('node:path')
const { execFile } = require('node:child_process')
const { promisify } = require('node:util')

const AREA = 10
const run_ = promisify(execFile)
const houstonSh = path.join(__dirname, '..', 'houston.sh')

// Wraps EventSource so the page records every open, error and named event per URL.
const ES_LOG = () => {
  const Native = window.EventSource
  window.__es = []
  window.EventSource = class extends Native {
    constructor(url, init) {
      super(url, init)
      const u = String(url)
      const log = (type) => window.__es.push({ t: Date.now(), url: u, type })
      log('new')
      for (const t of ['open', 'error', 'ping', 'snapshot', 'update', 'updates', 'reset', 'entries']) this.addEventListener(t, () => log(t))
    }
  }
}

async function run(ctx) {
  const streamReqs = []
  const watchRequests = (page) => {
    page.on('request', (r) => {
      const p = new URL(r.url()).pathname
      if (p === '/api/runs/stream') streamReqs.push(Date.now())
    })
  }

  // ---- ping ---------------------------------------------------------------
  await ctx.expect(AREA, 'ping', async () => {
    const { context, page } = await ctx.newPage(ctx.browser, 'desktop')
    try {
      await page.goto(ctx.url('#/fleet'), { waitUntil: 'domcontentloaded' })
      const r = await page.evaluate(async () => {
        const ctl = new AbortController()
        const timer = setTimeout(() => ctl.abort(), 32000)
        const t0 = Date.now()
        let buf = ''
        let first = ''
        try {
          const res = await fetch('/api/runs/stream', { signal: ctl.signal })
          const reader = res.body.getReader()
          const dec = new TextDecoder()
          for (;;) {
            const { value, done } = await reader.read()
            if (done) break
            buf += dec.decode(value, { stream: true })
            if (!first && buf.includes('event: snapshot')) first = String(Date.now() - t0)
            if (/(^|\n)event: ping\n/.test(buf)) return { ms: Date.now() - t0, snapshotMs: first, status: res.status, tail: buf.slice(buf.lastIndexOf('event: ping') - 10, buf.lastIndexOf('event: ping') + 30) }
          }
          return { ms: -1, status: res.status, tail: buf.slice(-80) }
        } catch (e) {
          return { ms: -1, error: String(e), tail: buf.slice(-80) }
        } finally {
          clearTimeout(timer)
          ctl.abort()
        }
      })
      if (r.ms < 0) throw new Error(`no "event: ping" within 32 s on /api/runs/stream (${JSON.stringify(r)})`)
      if (r.ms > 30000) throw new Error(`first ping after ${r.ms} ms, over the 30 s bound`)
      return `event: ping (named, empty data) after ${r.ms} ms; snapshot arrived at ${r.snapshotMs} ms`
    } finally {
      await context.close()
    }
  })

  // ---- hidden-reopen ------------------------------------------------------
  await ctx.expect(AREA, 'hidden-reopen', async () => {
    const { context, page } = await ctx.newPage(ctx.browser, 'mobile')
    watchRequests(page)
    try {
      await page.addInitScript(ES_LOG)
      await page.goto(ctx.url('#/fleet'), { waitUntil: 'domcontentloaded' })
      await ctx.waitFor(() => page.evaluate(() => window.__es.some((e) => e.type === 'snapshot')), { timeout: 15000, message: 'no snapshot on the runs stream' })
      await page.evaluate(() => {
        let vis = 'visible'
        Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => vis })
        Object.defineProperty(document, 'hidden', { configurable: true, get: () => vis === 'hidden' })
        window.__setVis = (v) => {
          vis = v
          document.dispatchEvent(new Event('visibilitychange'))
        }
      })
      const hide = async (ms) => {
        await page.evaluate(() => window.__setVis('hidden'))
        await ctx.sleep(ms)
        await page.evaluate(() => window.__setVis('visible'))
      }

      // A short spell leaves the open connection alone.
      const n0 = streamReqs.length
      await hide(3000)
      await ctx.sleep(2000)
      if (streamReqs.length !== n0) throw new Error(`a 3 s hidden spell reopened the stream (${streamReqs.length - n0} new request(s)); CLAUDE.md says a shorter spell leaves it alone`)

      // An 11 s spell reopens it.
      const t0 = Date.now()
      await hide(11000)
      await ctx.waitFor(() => streamReqs.length > n0, { timeout: 8000, message: 'no new /api/runs/stream request within 8 s of returning visible after 11 s hidden' })
      const reopenMs = streamReqs[streamReqs.length - 1] - (t0 + 11000)
      await ctx.waitFor(() => page.evaluate((from) => window.__es.some((e) => e.type === 'snapshot' && e.t > from), t0 + 11000), { timeout: 8000, message: 'reopened stream delivered no snapshot' })
      const shot = await ctx.shot(page, 'liveness-hidden-reopen')
      return { note: `3 s hidden: no new request; 11 s hidden: new /api/runs/stream request ${reopenMs} ms after visible, then a fresh snapshot`, shot }
    } finally {
      await context.close()
    }
  })

  // ---- restart ------------------------------------------------------------
  const startMode = (await ctx.api('/api/mode')).json.mode
  await ctx.expect(AREA, 'restart', async () => {
    const runs = (await ctx.api('/api/runs')).json
    const sess = runs.find((r) => r.caps && r.caps.chat && r.id.startsWith('sess-'))
    if (!sess) throw new Error('no fixture session with a chat in /api/runs')
    const { context, page } = await ctx.newPage(ctx.browser, 'desktop')
    try {
      await page.addInitScript(ES_LOG)
      await page.goto(ctx.url(`#/fleet/${sess.id}/chat`), { waitUntil: 'domcontentloaded' })
      const seen = (type, urlPart, from = 0) => page.evaluate(([ty, u, f]) => window.__es.some((e) => e.type === ty && e.url.includes(u) && e.t > f), [type, urlPart, from])
      await ctx.waitFor(() => seen('snapshot', '/api/runs/stream'), { timeout: 15000, message: 'no runs snapshot before the restart' })
      await ctx.waitFor(() => seen('updates', '/chat/stream'), { timeout: 20000, message: 'chat stream delivered no updates batch before the restart' })
      await page.waitForSelector('.run-card', { timeout: 15000 })
      const cardsBefore = await page.locator('.run-card').count()
      await page.evaluate(() => {
        window.__noReload = true
      })

      const t0 = Date.now()
      let restartMs = 0
      await run_(houstonSh, [ctx.S, 'restart'], { timeout: 60000 })
      restartMs = Date.now() - t0
      const tUp = Date.now()

      const msSince = async (type, urlPart) => {
        await ctx.waitFor(() => seen(type, urlPart, t0), { timeout: 60000, message: `${type} on ${urlPart} never resumed within 60 s of the restart` })
        const times = await page.evaluate(([ty, u]) => window.__es.filter((e) => e.type === ty && e.url.includes(u)).map((e) => e.t), [type, urlPart])
        return times.filter((t) => t > t0).sort((a, b) => a - b)[0] - tUp
      }
      const listAfter = await msSince('snapshot', '/api/runs/stream')
      const chatOpenAfter = await msSince('open', '/chat/stream')
      const chatBatchAfter = await msSince('updates', '/chat/stream')

      if (!(await page.evaluate(() => window.__noReload === true))) throw new Error('the page reloaded during the restart')
      await ctx.waitFor(async () => (await page.locator('.run-card').count()) > 0 && (await page.locator('.fleet-badge', { hasText: /^offline$/ }).count()) === 0, { timeout: 15000, message: 'Fleet still shows offline or an empty list after reconnect' })
      const errors = await page.evaluate((from) => window.__es.filter((e) => e.type === 'error' && e.t > from).map((e) => (e.url.includes('/chat/') ? 'chat' : 'runs')), t0)
      const cardsAfter = await page.locator('.run-card').count()
      const shot = await ctx.shot(page, 'liveness-restart-after')
      return {
        note: `houston.sh restart took ${restartMs} ms; relative to it coming up: runs snapshot +${listAfter} ms, chat stream reopened +${chatOpenAfter} ms and delivered its catch-up batch +${chatBatchAfter} ms, no reload; EventSource errors during the outage: ${errors.length ? errors.join(',') : 'none'}; ${cardsBefore} cards before, ${cardsAfter} after`,
        shot,
      }
    } finally {
      await context.close()
      // houston.sh restart reuses the recorded mode; make sure it is the one we started in.
      if ((await ctx.api('/api/mode').catch(() => ({ json: {} }))).json?.mode !== startMode) await run_(houstonSh, [ctx.S, startMode], { timeout: 60000 })
    }
  })
}

module.exports = { run }
