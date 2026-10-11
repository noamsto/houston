// Area 2: navigation stack. Checks: open-tab-back worker-from-dispatcher
// deeplink-reload back-label long-press-root edge-swipe native-back
// desktop-select. Plan step 7.
//
// Runs come from the loadfixture crews: the joined dispatcher (crew 1) opens on
// its Crew tab and its workers are reachable from the roster, which gives the
// depth-2 stack (Fleet -> dispatcher -> worker). Touch gestures are real touch
// events sent through CDP Input.dispatchTouchEvent.
const sh = require('./_shared.cjs')

const AREA = 2

const hashOf = (page) => page.evaluate(() => location.hash)
const histLen = (page) => page.evaluate(() => history.length)
const depthOf = (page) => page.evaluate(() => history.state?.houston?.depth ?? 0)

async function waitHash(ctx, page, want, timeout = 5000) {
  await ctx
    .waitFor(async () => (await hashOf(page)) === want, { timeout, interval: 50, message: `hash did not become ${want}` })
    .catch(async (e) => {
      throw new Error(`${e.message} (is ${await hashOf(page)})`)
    })
}

async function fresh(ctx, kind, hash) {
  const { context, page } = await ctx.newPage(ctx.browser, kind)
  const errors = []
  page.on('pageerror', (e) => errors.push(e.message))
  await page.goto(ctx.url(hash))
  return { context, page, errors }
}

async function fleetReady(page) {
  await page.waitForSelector('.fleet .fleet-group-card', { timeout: 15000 })
}

const dispatcherCard = (page) => page.locator('.fleet .fleet-group-card:not(.crew) > .run-card').first()
const member = (page, codename) => page.locator('.crews-member-main', { hasText: codename }).first()

async function fixtureIds(ctx) {
  const runs = await sh.runsOf(ctx)
  const dispatcher = runs.find((r) => r.role === 'dispatcher' && r.crew?.name)
  if (!dispatcher) throw new Error('fixture has no joined dispatcher')
  const worker = runs.find((r) => r.role === 'worker' && r.crew?.name === dispatcher.crew.name && r.state === 'blocked') ?? runs.find((r) => r.role === 'worker' && r.crew?.name === dispatcher.crew.name)
  if (!worker) throw new Error('fixture dispatcher has no worker')
  return { dispatcher, worker, label: dispatcher.crew.codename || dispatcher.branch || dispatcher.id }
}

// Fleet -> dispatcher (Crew tab) [-> worker]
async function openDispatcher(ctx, page, ids) {
  await fleetReady(page)
  await dispatcherCard(page).click()
  await waitHash(ctx, page, `#/fleet/${ids.dispatcher.id}`)
  await page.waitForSelector('.crew-tab', { timeout: 10000 })
}

async function openWorker(ctx, page, ids) {
  await member(page, ids.worker.crew.codename).click()
  await waitHash(ctx, page, `#/fleet/${ids.worker.id}`)
}

async function touchSession(context, page) {
  return context.newCDPSession(page)
}

const touch = (cdp, type, x, y) =>
  cdp.send('Input.dispatchTouchEvent', { type, touchPoints: type === 'touchEnd' ? [] : [{ x, y, id: 1 }] })

// swipe starts a touch at (x0,y), moves it to (x1,y) over totalMs, and ends it
// unless `end` is false. Returns the cdp session.
async function swipe(ctx, cdp, { x0, x1, y = 420, totalMs = 300, steps = 12, end = true }) {
  await touch(cdp, 'touchStart', x0, y)
  for (let i = 1; i <= steps; i++) {
    await ctx.sleep(totalMs / steps)
    await touch(cdp, 'touchMove', x0 + ((x1 - x0) * i) / steps, y)
  }
  if (end) await touch(cdp, 'touchEnd')
}

async function hold(ctx, cdp, page, selector, ms) {
  const box = await page.locator(selector).boundingBox()
  if (!box) throw new Error(`${selector} has no box`)
  const x = box.x + box.width / 2
  const y = box.y + box.height / 2
  await touch(cdp, 'touchStart', x, y)
  await ctx.sleep(ms)
  await touch(cdp, 'touchEnd', x, y)
}

async function run(ctx) {
  const ids = await fixtureIds(ctx)
  const wLabel = ids.worker.crew.codename

  await ctx.expect(AREA, 'open-tab-back', async () => {
    const { context, page } = await fresh(ctx, 'mobile', '#/fleet')
    try {
      await fleetReady(page)
      const l0 = await histLen(page)
      await dispatcherCard(page).click()
      await waitHash(ctx, page, `#/fleet/${ids.dispatcher.id}`)
      const l1 = await histLen(page)
      if (l1 !== l0 + 1) throw new Error(`opening a run grew history ${l0} -> ${l1}, expected +1`)
      await page.waitForSelector('.run-tabs-list [role=tab]')
      const tabs = await page.locator('.run-tabs-list [role=tab]').allInnerTexts()
      await page.locator('.run-tabs-list [role=tab]', { hasText: 'Terminal' }).click()
      await waitHash(ctx, page, `#/fleet/${ids.dispatcher.id}/terminal`)
      await page.waitForSelector('.run-detail-body.terminal')
      const l2 = await histLen(page)
      if (l2 !== l1) throw new Error(`switching tab changed history ${l1} -> ${l2}, expected no entry`)
      const shot = await ctx.shot(page, 'nav-open-tab-back-mobile')
      await page.locator('.run-tabs-back').click()
      await waitHash(ctx, page, '#/fleet')
      const l3 = await histLen(page)
      if (l3 !== l1) throw new Error(`after Back history length is ${l3}, expected ${l1} (a pop, not a push)`)
      if ((await depthOf(page)) !== 0) throw new Error('nav depth is not 0 back on the shell root')
      if ((await page.locator('.run-detail').count()) !== 0) throw new Error('run detail overlay still mounted after Back')
      return { note: `history ${l0} -> ${l1} on open, unchanged on tab switch (${tabs.map((t) => t.replace(/^\W+/, '').trim()).join('/')}) and after Back to #/fleet`, shot }
    } finally {
      await context.close()
    }
  })

  await ctx.expect(AREA, 'worker-from-dispatcher', async () => {
    const { context, page } = await fresh(ctx, 'mobile', '#/fleet')
    try {
      await openDispatcher(ctx, page, ids)
      await openWorker(ctx, page, ids)
      if ((await depthOf(page)) !== 2) throw new Error(`depth is ${await depthOf(page)} on the worker, expected 2`)
      const aria = await page.locator('.run-tabs-back').getAttribute('aria-label')
      const label = (await page.locator('.run-tabs-back-label').innerText()).trim()
      if (aria !== `Back to ${ids.label}` || label !== ids.label) throw new Error(`Back reads "${label}" (aria "${aria}"), expected the dispatcher codename "${ids.label}"`)
      const shot = await ctx.shot(page, 'nav-worker-from-dispatcher-mobile')
      await page.locator('.run-tabs-back').click()
      await waitHash(ctx, page, `#/fleet/${ids.dispatcher.id}`)
      await page.waitForSelector('.crew-tab', { timeout: 5000 })
      const sel = (await page.locator('.run-tabs-list [role=tab][aria-selected=true]').innerText()).trim()
      if (!/Crew/.test(sel)) throw new Error(`landed on tab "${sel}", expected Crew`)
      return { note: `worker ${wLabel}: Back label "${label}", Back lands on the dispatcher's Crew tab`, shot }
    } finally {
      await context.close()
    }
  })

  await ctx.expect(AREA, 'deeplink-reload', async () => {
    const hash = `#/fleet/${ids.dispatcher.id}/terminal`
    const { context, page } = await fresh(ctx, 'mobile', hash)
    try {
      await page.waitForSelector('.run-detail-body.terminal', { timeout: 15000 })
      const tab = () => page.locator('.run-tabs-list [role=tab][aria-selected=true]').innerText().then((t) => t.trim())
      await ctx.waitFor(async () => /Terminal/.test(await tab().catch(() => '')), { message: 'Terminal tab not selected on deep link' })
      await page.reload()
      await page.waitForSelector('.run-detail-body.terminal', { timeout: 15000 })
      await ctx.waitFor(async () => /Terminal/.test(await tab().catch(() => '')), { message: 'Terminal tab not selected after reload' })
      if ((await hashOf(page)) !== hash) throw new Error(`hash after reload is ${await hashOf(page)}`)
      const shot = await ctx.shot(page, 'nav-deeplink-reload-mobile')
      const len = await histLen(page)
      await page.locator('.run-tabs-back').click()
      await waitHash(ctx, page, '#/fleet')
      await page.waitForSelector('.fleet .fleet-group-card', { timeout: 10000 })
      if (!page.url().startsWith(ctx.baseUrl)) throw new Error(`left the app: ${page.url()}`)
      if ((await histLen(page)) !== len) throw new Error(`Back on a deep link changed history length ${len} -> ${await histLen(page)} (it must replace, never push)`)
      if ((await page.locator('.run-detail').count()) !== 0) throw new Error('detail still mounted')
      return { note: `deep link and reload keep the Terminal view; Back replaces to #/fleet (history length stays ${len}), still in the app`, shot }
    } finally {
      await context.close()
    }
  })

  await ctx.expect(AREA, 'back-label', async () => {
    const out = []
    const { context, page } = await fresh(ctx, 'mobile', '#/fleet')
    let shot = ''
    try {
      const read = async () => ({
        bar: (await page.locator('.run-tabs-back-label').innerText()).trim(),
        aria: await page.locator('.run-tabs-back').getAttribute('aria-label'),
        header: (await page.locator('.run-detail-back').getAttribute('aria-label')) ?? '',
      })
      const expectLabel = async (where, want) => {
        const g = await read()
        if (g.bar !== want || g.aria !== `Back to ${want}` || g.header !== `Back to ${want}`) {
          throw new Error(`${where}: bar "${g.bar}", aria "${g.aria}", header "${g.header}"; expected "${want}"`)
        }
        out.push(`${where} -> "${want}"`)
      }
      await openDispatcher(ctx, page, ids)
      await expectLabel('from Fleet', 'Fleet')
      await openWorker(ctx, page, ids)
      await expectLabel('from the dispatcher', ids.label)
      shot = await ctx.shot(page, 'nav-back-label-mobile')
      await page.locator('.shell-tabs.run-tabs').waitFor()
      await page.goto(ctx.url('#/workspace'))
      await page.reload()
      await page.waitForSelector('.ws-pane.agent', { timeout: 15000 })
      await page.locator('.ws-pane.agent').first().click()
      await page.waitForSelector('.run-tabs-back')
      await expectLabel('from Workspace', 'Workspace')
    } finally {
      await context.close()
    }
    return { note: out.join('; '), shot }
  })

  await ctx.expect(AREA, 'long-press-root', async () => {
    const { context, page } = await fresh(ctx, 'mobile', '#/fleet')
    try {
      const cdp = await touchSession(context, page)
      await openDispatcher(ctx, page, ids)
      await openWorker(ctx, page, ids)
      const rootBtn = await page.locator('.run-detail-root').getAttribute('aria-label')
      if (rootBtn !== 'Back to Fleet') throw new Error(`header root button reads "${rootBtn}" at depth 2, expected "Back to Fleet"`)
      // A short press is an ordinary Back.
      await hold(ctx, cdp, page, '.run-tabs-back', 120)
      await waitHash(ctx, page, `#/fleet/${ids.dispatcher.id}`)
      await page.waitForSelector('.crew-tab')
      await openWorker(ctx, page, ids)
      const shotBefore = await ctx.shot(page, 'nav-long-press-before-mobile')
      await hold(ctx, cdp, page, '.run-tabs-back', 600)
      await waitHash(ctx, page, '#/fleet', 5000)
      await page.waitForSelector('.fleet .fleet-group-card')
      if ((await page.locator('.run-detail').count()) !== 0) throw new Error('detail still mounted after the long press')
      if ((await depthOf(page)) !== 0) throw new Error(`depth ${await depthOf(page)} after popping to the root`)
      return { note: 'a 120 ms press at depth 2 pops one level; a 600 ms hold pops to #/fleet', shot: shotBefore }
    } finally {
      await context.close()
    }
  })

  await ctx.expect(AREA, 'edge-swipe', async () => {
    const { context, page } = await fresh(ctx, 'mobile', '#/fleet')
    try {
      const cdp = await touchSession(context, page)
      await openDispatcher(ctx, page, ids)
      const notes = []

      // A short, slow swipe from the edge springs back.
      await swipe(ctx, cdp, { x0: 5, x1: 50, totalMs: 500 })
      await ctx.sleep(700)
      if ((await hashOf(page)) !== `#/fleet/${ids.dispatcher.id}`) throw new Error(`short swipe navigated to ${await hashOf(page)}`)
      const tr = await page.locator('.run-detail').evaluate((el) => el.style.transform)
      if (tr) throw new Error(`overlay left at transform "${tr}" after a short swipe`)
      notes.push('short slow swipe (45 px) springs back')

      // A swipe that does not start in the edge zone is not the back gesture.
      await swipe(ctx, cdp, { x0: 100, x1: 300, totalMs: 300 })
      await ctx.sleep(500)
      if ((await hashOf(page)) !== `#/fleet/${ids.dispatcher.id}`) throw new Error(`mid-screen swipe navigated to ${await hashOf(page)}`)
      notes.push('mid-screen swipe ignored')

      // A long swipe follows the finger, then commits like Back.
      const parent = await page.evaluate(() => history.state?.houston?.from)
      await swipe(ctx, cdp, { x0: 5, x1: 250, totalMs: 400, end: false })
      const mid = await page.locator('.run-detail').evaluate((el) => ({ transform: el.style.transform, swiping: el.classList.contains('edge-swiping') }))
      const shot = await ctx.shot(page, 'nav-edge-swipe-mid-mobile')
      if (!/translateX\(2\d\dpx\)|translateX\(24\dpx\)/.test(mid.transform) || !mid.swiping) {
        throw new Error(`mid-drag the overlay is transform "${mid.transform}", swiping=${mid.swiping}; expected it to follow the finger`)
      }
      await touch(cdp, 'touchEnd')
      await waitHash(ctx, page, parent, 5000)
      await page.waitForSelector('.fleet .fleet-group-card')
      notes.push(`depth-1 swipe followed the finger (${mid.transform}) and landed on ${parent}`)

      // Depth 2: the swipe lands where Back would, on the parent run.
      await openDispatcher(ctx, page, ids)
      await openWorker(ctx, page, ids)
      const parent2 = await page.evaluate(() => history.state?.houston?.from)
      await swipe(ctx, cdp, { x0: 5, x1: 250, totalMs: 300 })
      await waitHash(ctx, page, parent2, 5000)
      await page.waitForSelector('.crew-tab')
      if (parent2 !== `#/fleet/${ids.dispatcher.id}`) throw new Error(`depth-2 parent is ${parent2}`)
      notes.push(`depth-2 swipe landed on ${parent2}`)
      return { note: notes.join('; '), shot }
    } finally {
      await context.close()
    }
  })

  await ctx.expect(AREA, 'native-back', async () => {
    const { context, page } = await fresh(ctx, 'mobile', '#/fleet')
    try {
      await openDispatcher(ctx, page, ids)
      await openWorker(ctx, page, ids)
      const houstonLands = await page.evaluate(() => history.state?.houston?.from)
      await page.goBack()
      await waitHash(ctx, page, houstonLands)
      await page.waitForSelector('.crew-tab')
      if ((await depthOf(page)) !== 1) throw new Error(`depth ${await depthOf(page)} after a native back from depth 2, expected 1`)
      const label = (await page.locator('.run-tabs-back-label').innerText()).trim()
      if (label !== 'Fleet') throw new Error(`Back label after native back is "${label}", expected Fleet`)
      const shot = await ctx.shot(page, 'nav-native-back-mobile')
      await page.goBack()
      await waitHash(ctx, page, '#/fleet')
      await page.waitForSelector('.fleet .fleet-group-card')
      if ((await page.locator('.run-detail').count()) !== 0) throw new Error('detail still mounted after the second native back')
      return { note: `native back from depth 2 landed on ${houstonLands} (where houston's Back goes), then #/fleet`, shot }
    } finally {
      await context.close()
    }
  })

  await ctx.expect(AREA, 'desktop-select', async () => {
    const { context, page } = await fresh(ctx, 'desktop', '#/fleet')
    try {
      await page.waitForSelector('.console-list .fleet-group-card', { timeout: 15000 })
      const plain = page.locator('.console-list .run-card', { hasText: 'qa-repo' })
      const l0 = await histLen(page)
      await plain.nth(0).click()
      await ctx.waitFor(async () => /^#\/fleet\/pane-\d+/.test(await hashOf(page)), { message: 'first selection did not open a run' })
      const h1 = await hashOf(page)
      const l1 = await histLen(page)
      if (l1 !== l0 + 1) throw new Error(`first selection grew history ${l0} -> ${l1}, expected +1`)
      await plain.nth(1).click()
      await ctx.waitFor(async () => (await hashOf(page)) !== h1, { message: 'second selection did not change the run' })
      const h2 = await hashOf(page)
      if ((await histLen(page)) !== l1) throw new Error(`selecting a sibling pushed an entry (${l1} -> ${await histLen(page)})`)
      if ((await depthOf(page)) !== 1) throw new Error(`depth ${await depthOf(page)} after sibling select, expected 1`)

      await dispatcherCard(page).click()
      await waitHash(ctx, page, `#/fleet/${ids.dispatcher.id}`)
      if ((await histLen(page)) !== l1) throw new Error('selecting the dispatcher pushed an entry')
      await page.waitForSelector('.console-detail .crew-tab')
      await member(page, wLabel).click()
      await waitHash(ctx, page, `#/fleet/${ids.worker.id}`)
      const l3 = await histLen(page)
      if (l3 !== l1 + 1 || (await depthOf(page)) !== 2) throw new Error(`opening a worker from the roster: history ${l1} -> ${l3}, depth ${await depthOf(page)}; expected a push to depth 2`)
      const shotBefore = await ctx.shot(page, 'nav-desktop-select-worker')
      await dispatcherCard(page).click()
      await waitHash(ctx, page, `#/fleet/${ids.dispatcher.id}`)
      if ((await depthOf(page)) !== 1) throw new Error(`selecting the parent left depth ${await depthOf(page)}, expected 1 (collapse)`)
      if ((await histLen(page)) !== l3) throw new Error(`selecting the parent pushed an entry (${l3} -> ${await histLen(page)})`)
      return { note: `sibling select replaces (${h1} -> ${h2}, history +1 only for the first open); selecting the parent pops depth 2 -> 1 with no push`, shot: shotBefore }
    } finally {
      await context.close()
    }
  })
}

module.exports = { run }
