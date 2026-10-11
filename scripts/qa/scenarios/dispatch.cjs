// Area 8: the Dispatch tab (link prefill, options, validation, request bodies).
//
// The two dispatch POSTs are never sent to houston: a page.route installed
// before any interaction fulfils POST /api/dispatch and
// POST /api/dispatch/dispatcher with canned bodies and records what the UI
// sent. GET /api/dispatch/options is real. Any other POST under /api/dispatch/
// is fulfilled with a 418 and fails the run.
const AREA = 8

const sameSet = (a, b) => a.length === b.length && a.every((x, i) => x === b[i])

async function run(ctx) {
  const { S, env } = ctx
  const crew1 = env.CREW1
  const repoPath = `${S}/repo`

  const opts = (await ctx.api('/api/dispatch/options')).json
  const optsRepo = opts && opts.repos && opts.repos.find((r) => r.path === repoPath)

  const { context, page } = await ctx.newPage(ctx.browser, 'desktop')
  const worker = []
  const dispatcher = []
  const stray = []
  await page.route(
    (u) => u.pathname.startsWith('/api/dispatch') && u.pathname !== '/api/dispatch/options',
    async (route) => {
      const req = route.request()
      if (req.method() !== 'POST') return route.fallback()
      const p = new URL(req.url()).pathname
      const body = req.postData() || ''
      if (p === '/api/dispatch') {
        worker.push(body)
        return route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify({ worker_id: 'qa-worker-7', branch: 'qa/branch', crew: crew1, output: '' }),
        })
      }
      if (p === '/api/dispatch/dispatcher') {
        dispatcher.push(body)
        return route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify({ crew: crew1, session: 'fx', window: '@qa', pane: '%qa', run_id: 'pane-qa' }),
        })
      }
      stray.push(`${req.method()} ${p}`)
      return route.fulfill({ status: 418, contentType: 'text/plain', body: 'qa: unexpected dispatch route' })
    },
  )

  const optionValues = (sel) => page.locator(`${sel} option`).evaluateAll((os) => os.map((o) => o.value))

  try {
    // ---- prefill ----------------------------------------------------------
    await ctx.expect(AREA, 'prefill', async () => {
      if (!optsRepo) throw new Error(`${repoPath} is not in /api/dispatch/options repos: ${opts.repos.map((r) => r.path).join(', ')}`)
      if (!optsRepo.crews.includes(crew1)) throw new Error(`options list no crew ${crew1} for the repo`)
      if (optsRepo.crews[0] === crew1) throw new Error('crew 1 is already the default crew; the prefill would prove nothing')
      const hash = `#/dispatch?repo=${encodeURIComponent(repoPath)}&crew=${encodeURIComponent(crew1)}`
      await page.goto(ctx.url(hash), { waitUntil: 'domcontentloaded' })
      await page.waitForSelector('#dispatch-repo', { timeout: 15000 })
      let crewNow = ''
      await ctx.waitFor(async () => (crewNow = await page.locator('#dispatch-crew').inputValue()) === crew1, {
        timeout: 10000,
        message: () => `crew select is "${crewNow}", expected ${crew1}`,
      })
      const repo = await page.locator('#dispatch-repo').inputValue()
      if (repo !== repoPath) throw new Error(`repo select is ${repo}, expected ${repoPath}`)
      let hashNow = ''
      await ctx.waitFor(async () => (hashNow = await page.evaluate(() => location.hash)) === '#/dispatch', {
        timeout: 5000,
        message: () => `hash is ${hashNow}, expected it rewritten to #/dispatch`,
      })
      const shot = await ctx.shot(page, 'dispatch-prefill')
      return { note: `repo ${repo} and crew ${crew1} selected (default would be ${optsRepo.crews[0]}); hash rewritten to #/dispatch`, shot }
    })

    // ---- options ----------------------------------------------------------
    await ctx.expect(AREA, 'options', async () => {
      await page.waitForSelector('#dispatch-tier', { timeout: 15000 })
      const tiers = await optionValues('#dispatch-tier')
      if (!sameSet(tiers, opts.tiers)) throw new Error(`tier options ${tiers} != api ${opts.tiers}`)
      const engines = await optionValues('#dispatch-engine')
      if (!sameSet(engines, opts.engine_order)) throw new Error(`engine options ${engines} != engine_order ${opts.engine_order}`)
      const efforts = await optionValues('#dispatch-effort')
      if (!sameSet(efforts, opts.efforts)) throw new Error(`effort options ${efforts} != api ${opts.efforts}`)

      const engine = await page.locator('#dispatch-engine').inputValue()
      const models = await optionValues('#dispatch-model')
      if (!sameSet(models, opts.engines[engine] || [])) throw new Error(`model options ${models} != api engines[${engine}] ${opts.engines[engine]}`)

      const seen = []
      for (const t of opts.tiers) {
        await page.selectOption('#dispatch-tier', t)
        const want = (opts.tier_models[engine] && opts.tier_models[engine][t]) || models[0]
        let modelNow = ''
        await ctx.waitFor(async () => (modelNow = await page.locator('#dispatch-model').inputValue()) === want, {
          timeout: 5000,
          message: () => `tier ${t}: model is ${modelNow}, tier_models says ${want}`,
        })
        seen.push(`${t}=${want}`)
      }
      const shot = await ctx.shot(page, 'dispatch-options')
      const empty = opts.engine_order.filter((e) => !(opts.engines[e] || []).length)
      const extra = empty.length ? `; engine_order also lists ${empty.join(', ')} with no allowlisted models (selecting one leaves the model select empty)` : ''
      return { note: `tiers/engines/efforts/models match the options; default model per tier ${seen.join(', ')}${extra}`, shot }
    })

    // ---- validation -------------------------------------------------------
    await ctx.expect(AREA, 'validation', async () => {
      const submit = page.locator('button.dispatch-submit:visible')
      const before = worker.length
      await page.fill('#dispatch-title', '')
      if (!(await submit.isDisabled())) throw new Error('Dispatch is enabled with an empty title')
      const hint = (await page.locator('.dispatch-hint', { hasText: 'Title is required.' }).count()) > 0
      if (!hint) throw new Error('no "Title is required." hint for an empty title')
      await page.fill('#dispatch-title', '   ')
      if (!(await submit.isDisabled())) throw new Error('Dispatch is enabled with a blank title')
      await page.fill('#dispatch-title', 'qa title')
      await page.fill('#dispatch-issue', 'not an issue')
      if (!(await submit.isDisabled())) throw new Error('Dispatch is enabled with a malformed issue id')
      const issueHint = (await page.locator('.dispatch-error', { hasText: /GitHub issue number/ }).count()) > 0
      if (!issueHint) throw new Error('no issue-id error text for a malformed issue')
      await page.fill('#dispatch-issue', '')
      await page.fill('#dispatch-title', '-qa')
      const dashEnabled = !(await submit.isDisabled())
      const shot = await ctx.shot(page, 'dispatch-validation')

      // The dispatcher form mirrors the server's "task may not start with -" rule.
      await page.getByRole('button', { name: 'New dispatcher' }).click()
      await page.fill('#dispatcher-task-0', '-qa task')
      const dsub = page.getByRole('button', { name: 'Start dispatcher' })
      if (!(await dsub.isDisabled())) throw new Error('Start dispatcher is enabled for a task starting with "-"')
      const dmsg = await page.locator('.dispatch-error:visible').first().innerText()
      if (!/cannot start with "-"/.test(dmsg)) throw new Error(`dispatcher error text is "${dmsg}"`)
      await page.fill('#dispatcher-task-0', '')
      await page.getByRole('button', { name: 'Add worker' }).click()
      await page.fill('#dispatch-title', '')
      if (worker.length !== before || dispatcher.length) throw new Error('a dispatch request was sent during validation')
      return {
        note: `empty/blank title and bad issue disable Dispatch with a reason, a dispatcher task starting with "-" is refused client-side, no request sent; worker form with title "-qa" ${dashEnabled ? 'is NOT blocked client-side (the server must refuse it; not exercised, dispatch POSTs are never sent)' : 'is blocked'}`,
        shot,
      }
    })

    // ---- body -------------------------------------------------------------
    await ctx.expect(AREA, 'body', async () => {
      await page.fill('#dispatch-title', 'qa title')
      await page.fill('#dispatch-spec', 'qa spec body')
      await page.selectOption('#dispatch-tier', 'deep')
      await page.selectOption('#dispatch-engine', 'claude')
      const models = await optionValues('#dispatch-model')
      const model = models[models.length - 1]
      await page.selectOption('#dispatch-model', model)
      await page.selectOption('#dispatch-effort', 'high')
      await page.fill('#dispatch-issue', '123')
      const n = worker.length
      await page.locator('button.dispatch-submit:visible').click()
      await ctx.waitFor(() => worker.length > n, { timeout: 10000, message: 'no POST /api/dispatch reached the page.route fixture' })
      const body = JSON.parse(worker[worker.length - 1])
      const want = { repo: repoPath, title: 'qa title', spec: 'qa spec body', tier: 'deep', engine: 'claude', model, effort: 'high', crew: crew1, issue: '123' }
      const wrong = Object.entries(want).filter(([k, v]) => body[k] !== v).map(([k, v]) => `${k}: sent ${JSON.stringify(body[k])}, expected ${JSON.stringify(v)}`)
      const extra = Object.keys(body).filter((k) => !(k in want))
      if (wrong.length || extra.length) throw new Error(`request body mismatch: ${wrong.join('; ')}${extra.length ? ` unexpected keys ${extra}` : ''}`)
      await page.waitForSelector('.dispatch-result.success', { timeout: 10000 })
      const text = (await page.locator('.dispatch-result.success').innerText()).replace(/\s+/g, ' ')
      const strong = (await page.locator('.dispatch-result.success strong').first().innerText()).trim()
      if (strong !== 'qa-worker-7' || !text.includes('qa/branch') || !text.includes(crew1)) throw new Error(`success panel shows "${text}", want worker qa-worker-7 on qa/branch in crew ${crew1}`)
      const shot = await ctx.shot(page, 'dispatch-body')
      return { note: `POST /api/dispatch body ${JSON.stringify(body)}; success panel "${text}"`, shot }
    })

    // ---- dispatcher-body --------------------------------------------------
    await ctx.expect(AREA, 'dispatcher-body', async () => {
      const engines = opts.dispatcher_engines || []
      if (engines.length === 0) {
        throw new Error(`options carry no dispatcher_engines (${opts.dispatcher_engines_error || 'no error text'}); the form cannot submit`)
      }
      await page.getByRole('button', { name: 'New dispatcher' }).click()
      await page.fill('#dispatcher-task-0', 'qa task one')
      await page.getByRole('button', { name: 'Add task' }).click()
      await page.fill('#dispatcher-task-1', 'qa   task\ttwo')
      const engine = engines[engines.length - 1]
      await page.selectOption('#dispatcher-engine', engine)
      const models = await optionValues('#dispatcher-model')
      const model = models.find((m) => m) || ''
      if (model) await page.selectOption('#dispatcher-model', model)
      await page.selectOption('#dispatcher-effort', 'low')
      const n = dispatcher.length
      await page.getByRole('button', { name: 'Start dispatcher' }).click()
      await ctx.waitFor(() => dispatcher.length > n, { timeout: 10000, message: 'no POST /api/dispatch/dispatcher reached the page.route fixture' })
      const body = JSON.parse(dispatcher[dispatcher.length - 1])
      const want = { repo: repoPath, tasks: ['qa task one', 'qa task two'], engine, effort: 'low' }
      if (model) want.model = model
      const wrong = Object.entries(want).filter(([k, v]) => JSON.stringify(body[k]) !== JSON.stringify(v)).map(([k, v]) => `${k}: sent ${JSON.stringify(body[k])}, expected ${JSON.stringify(v)}`)
      const extra = Object.keys(body).filter((k) => !(k in want))
      if (wrong.length || extra.length) throw new Error(`request body mismatch: ${wrong.join('; ')}${extra.length ? ` unexpected keys ${extra}` : ''}`)
      await page.waitForSelector('.dispatch-result.success', { timeout: 10000 })
      const shot = await ctx.shot(page, 'dispatch-dispatcher-body')
      return { note: `POST /api/dispatch/dispatcher body ${JSON.stringify(body)} (whitespace in a task collapsed to one space)`, shot }
    })

    if (stray.length) ctx.check(AREA, 'dispatch-stray-routes', 'fail', `unexpected dispatch requests: ${stray.join(', ')}`)
  } finally {
    await context.close()
  }
}

module.exports = { run }
