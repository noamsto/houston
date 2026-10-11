// Area 9: dispatcher vs tmux mode. Restarts the houston under test in tmux mode
// (scripts/qa/houston.sh S tmux), checks what the mode removes, then always
// restores dispatcher mode (finally) and checks `#/crews` there too.
//
// Dispatcher-only routes are probed with GET only. Go's method-specific mux
// answers a GET to a registered POST-only route (/api/runs/<id>/reply,
// /api/dispatch, /api/dispatch/dispatcher) with 405 and to an unregistered one
// with its own 404, so a GET tells registered from removed without a POST.
const path = require('node:path')
const { execFile } = require('node:child_process')
const { promisify } = require('node:util')

const AREA = 9
const run_ = promisify(execFile)
const houstonSh = path.join(__dirname, '..', 'houston.sh')

async function run(ctx) {
  const { S } = ctx
  const mode = async () => (await ctx.api('/api/mode')).json?.mode

  const restart = async (m) => {
    await run_(houstonSh, [S, m], { timeout: 60000 })
    await ctx.waitFor(async () => (await mode()) === m, { timeout: 15000, message: `houston did not report mode ${m}` })
    // The registry fills in a few seconds after start.
    await ctx.waitFor(async () => ((await ctx.api('/api/runs')).json || []).length > 0, { timeout: 30000, message: 'no runs after restart' })
  }

  // Control for routes-404: in dispatcher mode the POST-only routes answer a GET with 405.
  const control = {}
  const controlRuns = (await ctx.api('/api/runs')).json || []
  const controlId = controlRuns.find((r) => r.id.startsWith('pane-'))?.id ?? controlRuns[0]?.id
  if ((await mode()) === 'dispatcher' && controlId) {
    for (const p of ['/api/dispatch', '/api/dispatch/dispatcher', `/api/runs/${controlId}/reply`]) {
      control[p.replace(controlId, '<id>')] = (await ctx.api(p)).status
    }
  }

  try {
    await restart('tmux')

    // ---- no-dispatch-tab --------------------------------------------------
    await ctx.expect(AREA, 'no-dispatch-tab', async () => {
      const notes = []
      let shot
      for (const kind of ['mobile', 'desktop']) {
        const { context, page } = await ctx.newPage(ctx.browser, kind)
        try {
          const modeResp = page.waitForResponse((r) => new URL(r.url()).pathname === '/api/mode', { timeout: 15000 })
          await page.goto(ctx.url('#/fleet'), { waitUntil: 'domcontentloaded' })
          await modeResp
          await ctx.sleep(800)
          const sel = kind === 'mobile' ? 'nav[aria-label="sections"] button' : '.console-rail-item'
          await page.waitForSelector(sel, { timeout: 15000 })
          const labels = (await page.locator(sel).allInnerTexts()).map((t) => t.replace(/[^A-Za-z ]/g, '').trim())
          if (!labels.some((l) => /Workspace/.test(l))) throw new Error(`${kind}: no Workspace entry, so the shell did not render (${labels})`)
          if (labels.some((l) => /^Dispatch/.test(l))) throw new Error(`${kind}: a Dispatch tab is present in tmux mode (${labels})`)
          notes.push(`${kind}: ${labels.filter((l) => /^(Fleet|Workspace|Dispatch)/.test(l)).join('/')}`)
          shot = await ctx.shot(page, `modes-tmux-${kind}`)
        } finally {
          await context.close()
        }
      }
      return { note: notes.join('; '), shot }
    })

    // ---- rewrite-dispatch / rewrite-crews ---------------------------------
    for (const [id, hash] of [
      ['rewrite-dispatch', '#/dispatch?repo=x'],
      ['rewrite-crews', '#/crews?x=1'],
    ]) {
      await ctx.expect(AREA, id, async () => {
        const { context, page } = await ctx.newPage(ctx.browser, 'mobile')
        try {
          await page.goto(ctx.url(hash), { waitUntil: 'domcontentloaded' })
          let hashNow = ''
          await ctx.waitFor(async () => (hashNow = await page.evaluate(() => location.hash)) === '#/fleet', {
            timeout: 10000,
            message: () => `hash stayed ${hashNow} for ${hash}`,
          })
          const fleetShown = await page.locator('[data-shell-tab="fleet"]:not([hidden])').count()
          if (!fleetShown) throw new Error(`hash is #/fleet but the Fleet pane is not the visible one`)
          const shot = await ctx.shot(page, `modes-${id}`)
          return { note: `${hash} -> #/fleet in place, Fleet visible`, shot }
        } finally {
          await context.close()
        }
      })
    }

    // ---- routes-404 -------------------------------------------------------
    await ctx.expect(AREA, 'routes-404', async () => {
      const runs = (await ctx.api('/api/runs')).json
      const id = runs.find((r) => r.id.startsWith('pane-'))?.id ?? runs[0].id
      const out = []
      const bad = []
      const note = (label, r) => {
        out.push(`${label} ${r.status}`)
        // A handler's own 404 ("no such run", "no crew") would mean the route is registered.
        if (r.status !== 404 || /no such run|no crew|no registry/i.test(r.text)) bad.push(`${label}: ${r.status} ${r.text.trim().slice(0, 60)}`)
      }
      for (const p of ['/api/dispatch/options', '/api/repos', '/api/repos/candidates', `/api/runs/${id}/crew/feed`, `/api/runs/${id}/crew/feed/stream`]) {
        note(`GET ${p.replace(id, '<id>')}`, await ctx.api(p))
      }
      for (const p of ['/api/dispatch', '/api/dispatch/dispatcher', `/api/runs/${id}/reply`]) {
        note(`GET ${p.replace(id, '<id>')} (POST-only route)`, await ctx.api(p))
      }
      if (bad.length) throw new Error(`routes still answer in tmux mode: ${bad.join('; ')}`)
      const ctl = Object.entries(control)
      const ctlBad = ctl.filter(([, st]) => st !== 405)
      if (ctl.length !== 3 || ctlBad.length) {
        throw new Error(`control failed: in dispatcher mode a GET to the POST-only routes should be 405, got ${JSON.stringify(control)}; a 404 in tmux mode proves nothing`)
      }
      return `all unregistered (404, no handler message): ${out.join(', ')}; control in dispatcher mode (registered POST routes, GET): ${ctl.map(([p, st]) => `${p} ${st}`).join(', ')}`
    })

    // ---- no-crew-names ----------------------------------------------------
    await ctx.expect(AREA, 'no-crew-names', async () => {
      const runs = (await ctx.api('/api/runs')).json
      const withCrew = runs.filter((r) => r.role || (r.crew && (r.crew.name || r.crew.codename || r.crew.color)))
      if (withCrew.length) throw new Error(`${withCrew.length} runs carry role/crew in tmux mode, e.g. ${JSON.stringify({ id: withCrew[0].id, role: withCrew[0].role, crew: withCrew[0].crew })}`)
      const ws = await ctx.api('/api/workspace')
      if (/crew_codename/.test(ws.text)) throw new Error('/api/workspace carries crew_codename in tmux mode')
      const { context, page } = await ctx.newPage(ctx.browser, 'desktop')
      try {
        await page.goto(ctx.url('#/fleet'), { waitUntil: 'domcontentloaded' })
        await page.waitForSelector('.run-card', { timeout: 15000 })
        await ctx.sleep(500)
        const counts = {
          workerRows: await page.locator('.worker-row').count(),
          groups: await page.locator('.fleet-group-card').count(),
          roleBadges: await page.locator('.run-role').count(),
          codenameChips: await page.locator('.run-chip.codename').count(),
          crewRail: await page.locator('.console-rail h2', { hasText: 'Crews' }).count(),
        }
        const shot = await ctx.shot(page, 'modes-tmux-fleet')
        const nonzero = Object.entries(counts).filter(([, n]) => n)
        if (nonzero.length) throw new Error(`crew UI in tmux mode: ${JSON.stringify(Object.fromEntries(nonzero))} (shot ${shot})`)
        return { note: `${runs.length} runs, none with role/crew; no worker rows, groups, role badges, codename chips or Crews rail; workspace has no crew_codename`, shot }
      } finally {
        await context.close()
      }
    })
  } finally {
    await restart('dispatcher').catch((e) => {
      ctx.check(AREA, 'restore-dispatcher', 'fail', `could not restore dispatcher mode: ${e instanceof Error ? e.message : e}`)
    })
  }

  // ---- crews-rewrite-dispatcher -------------------------------------------
  await ctx.expect(AREA, 'crews-rewrite-dispatcher', async () => {
    if ((await mode()) !== 'dispatcher') throw new Error('houston is not in dispatcher mode')
    const { context, page } = await ctx.newPage(ctx.browser, 'mobile')
    try {
      const modeResp = page.waitForResponse((r) => new URL(r.url()).pathname === '/api/mode', { timeout: 15000 })
      await page.goto(ctx.url('#/crews?x=1'), { waitUntil: 'domcontentloaded' })
      await modeResp
      let hashNow = ''
      await ctx.waitFor(async () => (hashNow = await page.evaluate(() => location.hash)) === '#/fleet', {
        timeout: 10000,
        message: () => `hash stayed ${hashNow}`,
      })
      await page.waitForSelector('nav[aria-label="sections"] button', { timeout: 15000 })
      await ctx.sleep(500)
      const labels = (await page.locator('nav[aria-label="sections"] button').allInnerTexts()).map((t) => t.replace(/[^A-Za-z ]/g, '').trim())
      if (!labels.some((l) => /^Dispatch/.test(l))) throw new Error(`dispatcher mode shows no Dispatch tab (${labels})`)
      const shot = await ctx.shot(page, 'modes-crews-dispatcher')
      return { note: `#/crews?x=1 -> #/fleet in dispatcher mode; tabs ${labels.join('/')}`, shot }
    } finally {
      await context.close()
    }
  })
}

module.exports = { run }
