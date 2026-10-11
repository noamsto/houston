// Area 6: the dispatcher's Crew tab (roster, activity feed, worker questions,
// "Dispatch here"). Fixture data only (setup.sh --no-agents is enough): crew 1
// ($CREW1) has a live dispatcher, crew 2 is an orphan.
//
// Two checks mutate the fixture bus ($S/repo/.git/crew/events.jsonl):
//   question-composer appends one raw `status` blocked row for a crew-1 worker
//   whose ts is 20 min old (past crewDispatcherSilence), then answers it through
//   the UI, which runs the real `crew reply` inside $S/repo.
//   question-live posts a fresh `crew status … blocked` for another crew-1 worker
//   through the real crew CLI.
// The crew CLI runs with TMUX_TMPDIR pointing at the private server, so any
// nudge or pane-option write it makes can only reach the fixture panes.
const fs = require('node:fs')
const path = require('node:path')
const { execFileSync } = require('node:child_process')

const AREA = 6
const PR_URL = /^https:\/\/github\.com\/[A-Za-z0-9-]+\/[A-Za-z0-9._-]+\/pull\/[0-9]+$/

function busLines(file) {
  return fs
    .readFileSync(file, 'utf8')
    .split('\n')
    .filter(Boolean)
    .map((l) => {
      try {
        return JSON.parse(l)
      } catch {
        return null
      }
    })
    .filter(Boolean)
}

function branchOf(from) {
  const m = /^worker:(.*?)(?:#.*)?$/.exec(from || '')
  return m ? m[1] : ''
}

// Per crew-1 branch: the newest status row (file order, as the fold reads it)
// and the newest dispatcher reply ts.
function branchFacts(rows, crew) {
  const facts = new Map()
  for (const r of rows) {
    if (r.crew_id !== crew) continue
    if (r.kind === 'status' && typeof r.from === 'string' && r.from.startsWith('worker:')) {
      const b = r.branch || branchOf(r.from)
      const f = facts.get(b) ?? { replyTs: 0 }
      f.from = r.from
      f.state = r.body && r.body.state
      facts.set(b, f)
    } else if (r.kind === 'msg' && typeof r.from === 'string' && r.from.startsWith('dispatcher:')) {
      const b = branchOf(r.to)
      const f = facts.get(b) ?? { replyTs: 0 }
      f.replyTs = Math.max(f.replyTs, r.ts || 0)
      facts.set(b, f)
    }
  }
  return facts
}

async function run(ctx) {
  const { S, env } = ctx
  const crew1 = env.CREW1
  const repoDir = path.join(S, 'repo')
  const bus = path.join(repoDir, '.git', 'crew', 'events.jsonl')
  const getRuns = async () => (await ctx.api('/api/runs')).json
  const crewWorkers = (runs) => runs.filter((r) => r.role === 'worker' && r.crew && r.crew.name === crew1)

  const crewCli = (...args) => {
    // Without an existing TMUX_TMPDIR tmux falls back to /tmp and would reach any other server there.
    if (!fs.existsSync(path.join(S, 'tmux'))) throw new Error(`crew: ${path.join(S, 'tmux')} missing; refusing to run crew without the private tmux dir`)
    return execFileSync('env', ['-u', 'TMUX', '-u', 'TMUX_PANE', '-u', 'CREW_WORKER_ID', '-u', 'CREW_ROLE_ID', `TMUX_TMPDIR=${path.join(S, 'tmux')}`, `CREW_ID=${crew1}`, 'crew', ...args], {
      cwd: repoDir,
      encoding: 'utf8',
      timeout: 20000,
      stdio: ['ignore', 'pipe', 'pipe'],
    })
  }

  const runs0 = await getRuns()
  const disp = runs0.find((r) => r.role === 'dispatcher' && r.crew && r.crew.name === crew1)
  if (!disp) {
    for (const id of ['roster', 'feed', 'question-live', 'question-composer', 'dispatch-here']) {
      ctx.check(AREA, id, 'fail', `no dispatcher run carries crew.name ${crew1}; the crew-1 dispatcher join did not happen`)
    }
    return
  }
  const crewHash = `#/fleet/${disp.id}/crew`

  const { context, page } = await ctx.newPage(ctx.browser, 'desktop')
  const feedPages = []
  const feedPending = []
  const feedErrors = []
  page.on('response', (res) => {
    const u = new URL(res.url())
    if (!u.pathname.endsWith('/crew/feed') || res.request().method() !== 'GET') return
    const entry = { before: u.searchParams.get('before'), status: res.status(), body: null }
    feedPages.push(entry)
    feedPending.push(
      res.json().then(
        (b) => {
          entry.body = b
        },
        (e) => {
          feedErrors.push(`${u.pathname}${u.search} (${entry.status}): ${e instanceof Error ? e.message : e}`)
        },
      ),
    )
  })

  try {
    await page.goto(ctx.url(crewHash), { waitUntil: 'domcontentloaded' })
    const member = (codename) => page.locator('.crews-member').filter({ has: page.locator('.crews-codename', { hasText: new RegExp(`^${codename}$`) }) })

    // ---- roster -----------------------------------------------------------
    await ctx.expect(AREA, 'roster', async () => {
      await page.waitForSelector('.crew-tab .crews-member', { timeout: 15000 })
      const want = crewWorkers(await getRuns())
      let seen = 0
      await ctx.waitFor(async () => (seen = await page.locator('.crews-member').count()) === want.length, {
        timeout: 15000,
        message: () => `roster row count never reached ${want.length} (api workers); saw ${seen}`,
      })
      const bad = []
      for (const w of want) {
        const row = member(w.crew.codename)
        if ((await row.count()) !== 1) {
          bad.push(`${w.crew.codename}: ${await row.count()} rows`)
          continue
        }
        const expected = [w.crew.tier, w.agent, w.crew.model].filter(Boolean).join(' · ')
        const got = (await row.locator('.crews-meta').innerText()).trim()
        if (got !== expected) bad.push(`${w.crew.codename}: meta "${got}" != "${expected}"`)
      }
      const head = (await page.locator('.crews-head').innerText()).replace(/\s+/g, ' ')
      const shot = await ctx.shot(page, 'crew-roster')
      if (bad.length) throw new Error(`roster mismatches vs /api/runs: ${bad.join('; ')} (shot ${shot})`)
      return { note: `${want.length} rows match tier · engine · model; header: ${head}`, shot }
    })

    // ---- feed -------------------------------------------------------------
    await ctx.expect(AREA, 'feed', async () => {
      await page.waitForSelector('.crew-feed-row', { timeout: 15000 })
      const n0 = await page.locator('.crew-feed-row').count()
      const hadMore = (await page.locator('.crew-feed-more').count()) > 0
      let clicks = 0
      while ((await page.locator('.crew-feed-more').count()) > 0 && clicks < 6) {
        const before = await page.locator('.crew-feed-row').count()
        await page.locator('.crew-feed-more').click()
        await ctx.waitFor(async () => (await page.locator('.crew-feed-row').count()) > before, { timeout: 10000, message: 'Load older added no rows' })
        clicks++
      }
      const n1 = await page.locator('.crew-feed-row').count()
      if (!hadMore) throw new Error(`no "Load older" button with ${n0} rows; the fixture crew has more than one page of events`)
      if (n1 <= n0) throw new Error(`Load older did not grow the list (${n0} -> ${n1})`)

      await Promise.all(feedPending)
      if (feedErrors.length) throw new Error(`could not parse feed page(s): ${feedErrors.join('; ')}`)
      const ids = feedPages.filter((p) => p.status === 200).flatMap((p) => (p.body.entries || []).map((e) => e.id))
      const dup = ids.filter((id, i) => ids.indexOf(id) !== i)
      if (dup.length) throw new Error(`duplicate entry ids across feed pages: ${[...new Set(dup)].slice(0, 5).join(', ')}`)
      const olderPages = feedPages.filter((p) => p.before)
      if (olderPages.length === 0) throw new Error('Load older sent no ?before= request')
      if (n1 !== new Set(ids).size) throw new Error(`DOM has ${n1} rows but the pages delivered ${new Set(ids).size} distinct entries`)

      const prHrefs = await page.locator('.crew-feed-pr, .crews-pr').evaluateAll((els) => els.map((e) => e.getAttribute('href') || ''))
      if (prHrefs.length === 0) throw new Error('no PR chip rendered in the roster or feed')
      const badPr = prHrefs.filter((h) => !PR_URL.test(h))
      if (badPr.length) throw new Error(`PR chip hrefs outside the github pull-URL shape: ${badPr.slice(0, 3).join(', ')}`)

      const link = page.locator('a.crew-feed-link').first()
      if ((await link.count()) === 0) throw new Error('no worker chip link in the feed')
      const label = (await link.innerText()).trim()
      await link.click()
      await ctx.waitFor(async () => {
        const h = await page.evaluate(() => location.hash)
        return h.startsWith('#/fleet/') && !h.startsWith(`#/fleet/${disp.id}`)
      }, { timeout: 10000, message: 'worker chip did not open another run' })
      const opened = (await page.evaluate(() => location.hash)).split('/')[2]
      const w = crewWorkers(await getRuns()).find((r) => r.id === opened)
      if (!w) throw new Error(`chip "${label}" opened ${opened}, which is not a crew-1 worker`)
      const shot = await ctx.shot(page, 'crew-feed-worker-link')
      await page.goto(ctx.url(crewHash), { waitUntil: 'domcontentloaded' })
      await page.waitForSelector('.crews-member', { timeout: 15000 })
      return { note: `${n0} rows, +${n1 - n0} after ${clicks} Load older click(s), ${new Set(ids).size} distinct ids; ${prHrefs.length} PR chips match the github regex; chip "${label}" opened ${w.crew.codename}`, shot }
    })

    // ---- pick two workers for the question checks --------------------------
    const rows0 = busLines(bus)
    const facts = branchFacts(rows0, crew1)
    const askTs = Date.now() - 20 * 60 * 1000
    const cands = crewWorkers(await getRuns()).filter((w) => {
      const f = facts.get(w.branch)
      return f && f.from && f.state !== 'blocked' && f.replyTs < askTs && w.tmux
    })
    const [composerW, liveW] = cands
    // Both checks leave rows behind that a second run cannot reuse: a reply newer than any
    // 20-minute-old question retires it. Report that instead of a misleading failure.
    const used = rows0.some((r) => r.body === 'qa-reply' || (r.body && r.body.detail === 'qa live?'))
    if (used) {
      for (const id of ['question-live', 'question-composer']) {
        ctx.check(AREA, id, 'skip', 'harness: this $S bus already holds the rows an earlier crew run posted; run scripts/qa/setup.sh for a fresh one')
      }
    }

    // ---- question-live -----------------------------------------------------
    if (!used) await ctx.expect(AREA, 'question-live', async () => {
      if (!liveW) throw new Error('no second crew-1 worker free of a blocked episode / newer reply in the fixture bus')
      const f = facts.get(liveW.branch)
      crewCli('status', f.from, 'blocked', 'qa live?')
      let detail
      await ctx.waitFor(async () => {
        const r = crewWorkers(await getRuns()).find((x) => x.id === liveW.id)
        detail = r?.crew?.detail
        return detail === 'qa live?'
      }, { timeout: 15000, message: () => `houston never picked up the fresh blocked status (crew.detail is ${JSON.stringify(detail)}, not "qa live?")` })
      const r = crewWorkers(await getRuns()).find((x) => x.id === liveW.id)
      const row = member(r.crew.codename)
      await row.waitFor({ timeout: 10000 })
      await ctx.sleep(1500)
      const composer = await row.locator('.crew-reply').count()
      const attention = await row.locator('.crews-attention').allInnerTexts()
      const shot = await ctx.shot(page, 'crew-question-live')
      if (r.state === 'blocked' || r.question) throw new Error(`api state=${r.state} question=${JSON.stringify(r.question)}; a fresh crew question with a live dispatcher should read running with no Question (shot ${shot})`)
      if (composer) throw new Error(`ReplyComposer shown on a fresh question routed to a live dispatcher (shot ${shot})`)
      if (attention.some((t) => /needs you/.test(t))) throw new Error(`row carries a needs-you chip: ${attention.join(',')} (shot ${shot})`)
      return { note: `${r.crew.codename}: state=${r.state}, no question, no ReplyComposer, detail "qa live?" stays on the roster row`, shot }
    })

    // ---- question-composer -------------------------------------------------
    if (!used) await ctx.expect(AREA, 'question-composer', async () => {
      if (!composerW) throw new Error('no crew-1 worker free of a blocked episode / newer reply in the fixture bus')
      const f = facts.get(composerW.branch)
      const text = fs.readFileSync(bus, 'utf8')
      const row = {
        body: { detail: 'Which approach should qa take?', state: 'blocked' },
        branch: composerW.branch,
        crew_id: crew1,
        from: f.from,
        kind: 'status',
        to: `dispatcher:${crew1}`,
        ts: askTs,
      }
      const t0 = Date.now()
      fs.appendFileSync(bus, `${text.endsWith('\n') || text === '' ? '' : '\n'}${JSON.stringify(row)}\n`)
      const mrow = member(composerW.crew.codename)
      const input = mrow.locator('.crew-reply input')
      await input.waitFor({ timeout: 10000 }).catch(async () => {
        const r = crewWorkers(await getRuns()).find((x) => x.id === composerW.id)
        const shot = await ctx.shot(page, 'crew-question-composer-missing')
        throw new Error(`no ReplyComposer within 10 s of a 20-minute-old blocked status; api state=${r && r.state} question=${JSON.stringify(r && r.question)} (shot ${shot})`)
      })
      const appeared = Date.now() - t0
      const before = fs.readFileSync(bus, 'utf8').split('\n').filter(Boolean).length
      await input.fill('qa-reply')
      await mrow.locator('.crew-reply button', { hasText: 'Send' }).click()
      const status = mrow.locator('.crew-reply-status')
      await status.waitFor({ timeout: 20000 })
      const said = (await status.innerText()).trim()
      const shot = await ctx.shot(page, 'crew-question-composer')
      const added = busLines(bus).slice(before)
      const reply = added.find((r) => r.kind === 'msg' && typeof r.from === 'string' && r.from === `dispatcher:${crew1}` && r.body === 'qa-reply' && String(r.to).startsWith(`worker:${composerW.branch}`))
      if (!reply) throw new Error(`UI said "${said}" but no dispatcher->worker msg "qa-reply" for ${composerW.branch} in events.jsonl (new rows: ${JSON.stringify(added.map((r) => [r.kind, r.from, r.to, r.body]))}) (shot ${shot})`)
      return { note: `composer appeared after ${appeared} ms; status "${said}"; bus msg ${reply.from} -> ${reply.to} body "qa-reply"`, shot }
    })

    // ---- dispatch-here -----------------------------------------------------
    await ctx.expect(AREA, 'dispatch-here', async () => {
      await page.goto(ctx.url(crewHash), { waitUntil: 'domcontentloaded' })
      const a = page.locator('a.crews-dispatch')
      await a.waitFor({ timeout: 15000 })
      const href = await a.getAttribute('href')
      const m = /^#\/dispatch\?(.*)$/.exec(href || '')
      if (!m) throw new Error(`href is ${href}, not #/dispatch?…`)
      const q = new URLSearchParams(m[1])
      if (q.get('crew') !== crew1) throw new Error(`href crew=${q.get('crew')} != ${crew1}`)
      const repo = q.get('repo')
      const opts = (await ctx.api('/api/dispatch/options')).json
      if (!opts.repos.some((r) => r.path === repo)) throw new Error(`href repo=${repo} is not one of /api/dispatch/options repos`)
      await a.click()
      await page.waitForSelector('#dispatch-repo', { timeout: 15000 })
      await ctx.waitFor(async () => (await page.locator('#dispatch-crew').inputValue()) === crew1, { timeout: 10000, message: 'crew select never took the linked crew' })
      const repoVal = await page.locator('#dispatch-repo').inputValue()
      const shot = await ctx.shot(page, 'crew-dispatch-here')
      if (repoVal !== repo) throw new Error(`repo select is ${repoVal}, link said ${repo} (shot ${shot})`)
      return { note: `${href} -> repo ${repoVal}, crew ${crew1} selected`, shot }
    })
  } finally {
    await context.close()
  }
}

module.exports = { run }
