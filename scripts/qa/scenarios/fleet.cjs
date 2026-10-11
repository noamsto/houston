// Area 1: Fleet list. Checks: states badge filters nesting orphan-group
// group-by-project codenames show-more bg-chip. Plan step 6.
//
// The expected view is computed from /api/runs with the UI's own rules (see
// _shared.cjs) and compared with the DOM; the run states the loadfixture lacks
// (blocked, stuck, background task) come from _shared.ensureStates.
const sh = require('./_shared.cjs')

const AREA = 1
const FILTERS = [
  ['active', 'Active'],
  ['needs-you', 'Needs you'],
  ['stuck', 'Stuck'],
  ['done', 'Done'],
  ['all', 'All'],
]

const BUCKET_LABELS = [
  ['working', 'working'],
  ['needsYou', 'needs you'],
  ['stuck', 'stuck'],
  ['done', 'done'],
  ['idle', 'idle'],
  ['ended', 'ended'],
]

function bucket(r, now) {
  if (r.attention) {
    if (!sh.isFresh(r, now)) return 'ended'
    return r.attention === 'needs-you' ? 'needsYou' : r.attention
  }
  if (r.state === 'done') return 'ended'
  return r.state === 'idle' ? 'idle' : 'working'
}

function countsLabel(members, now) {
  const c = {}
  for (const m of members) c[bucket(m, now)] = (c[bucket(m, now)] ?? 0) + 1
  return BUCKET_LABELS.filter(([k]) => c[k] > 0)
    .map(([k, l]) => `${c[k]} ${l}`)
    .join(' · ')
}

async function openFleet(ctx, kind) {
  const { context, page } = await ctx.newPage(ctx.browser, kind)
  const errors = []
  page.on('pageerror', (e) => errors.push(e.message))
  await page.goto(ctx.url('#/fleet'))
  // The group card only exists once the runs and the mode (dispatcher) are known.
  await page.waitForSelector('.fleet .fleet-group-card', { timeout: 15000 })
  return { context, page, errors }
}

async function chooseFilter(page, kind, label) {
  if (kind === 'desktop') await page.locator('.console-rail-item', { hasText: new RegExp(`^${label}`) }).first().click()
  else await page.locator('nav[aria-label="filter runs"] button', { hasText: new RegExp(`^${label}$`) }).click()
}

async function expandAll(page) {
  for (let i = 0; i < 20; i++) {
    const more = page.locator('.fleet .fleet-group-more[aria-expanded="false"]')
    if ((await more.count()) === 0) return
    await more.first().click()
  }
  throw new Error('expandAll: collapsed groups remained after 20 taps of "Show more"')
}

// viewDiff is '' when the cards and worker rows on screen are exactly what the
// rules select for the filter.
async function viewDiff(ctx, page, filter) {
  await expandAll(page)
  const runs = await sh.runsOf(ctx)
  const now = Date.now()
  const want = sh.fleetExpected(runs, filter, now)
  const cards = await page.evaluate(sh.readCards)
  const rows = await page.evaluate(sh.readRows)
  const cardKeys = ['project', 'name', 'state', 'flags', 'role', 'stuckChip', 'doneChip', 'bg']
  const rowKeys = ['codename', 'title', 'state', 'attention', 'chip']
  return [
    sh.diffSets(
      cards,
      want.cards.map((r) => sh.cardOf(r, now)),
      cardKeys,
      `${filter} cards (${cards.length} on screen, ${want.cards.length} expected)`,
    ),
    sh.diffSets(
      rows,
      want.rows.map(sh.rowOf),
      rowKeys,
      `${filter} rows (${rows.length} on screen, ${want.rows.length} expected)`,
    ),
  ]
    .filter(Boolean)
    .join(' ; ')
}

async function expectView(ctx, page, filter) {
  let last = ''
  await ctx.waitFor(
    async () => {
      last = await viewDiff(ctx, page, filter)
      return last === ''
    },
    { timeout: 8000, interval: 300, message: () => `view differs from /api/runs rules: ${last}` },
  )
}

async function run(ctx) {
  await sh.ensureStates(ctx)
  const runs = await sh.runsOf(ctx)

  const mobile = await openFleet(ctx, 'mobile')
  const { page } = mobile
  try {
    await ctx.expect(AREA, 'states', async () => {
      const states = new Set(runs.map((r) => r.state))
      for (const s of ['running', 'idle', 'blocked', 'review', 'done', 'failed']) {
        if (!states.has(s)) throw new Error(`fixture lacks a ${s} run (have ${[...states].join(',')})`)
      }
      await chooseFilter(page, 'mobile', 'All')
      await expectView(ctx, page, 'all')
      const cards = await page.evaluate(sh.readCards)
      const want = (f) => cards.some((c) => f(c))
      if (!want((c) => c.flags.includes('attention'))) throw new Error('no needs-you card styling on screen')
      if (!want((c) => c.stuckChip)) throw new Error('no stuck chip on screen')
      if (!want((c) => c.flags.includes('history'))) throw new Error('no ended (history) card on screen')
      const shot = await ctx.shot(page, 'fleet-states-mobile')

      const desk = await openFleet(ctx, 'desktop')
      try {
        await chooseFilter(desk.page, 'desktop', 'All')
        await expectView(ctx, desk.page, 'all')
        const dshot = await ctx.shot(desk.page, 'fleet-states-desktop')
        return { note: `All filter: ${cards.length} cards, ${(await page.evaluate(sh.readRows)).length} rows match /api/runs on mobile and desktop (desktop shot ${dshot})`, shot }
      } finally {
        await desk.context.close()
      }
    })

    await ctx.expect(AREA, 'badge', async () => {
      await chooseFilter(page, 'mobile', 'Active')
      let want = 0
      let text = ''
      await ctx.waitFor(async () => {
        want = (await sh.runsOf(ctx)).filter((r) => sh.needsYou(r, Date.now())).length
        text = want ? (await page.locator('.fleet-badge').innerText().catch(() => '')).trim() : ''
        return want > 0 && text === `${want} needs you`
      }, { timeout: 8000, interval: 300, message: () => (want === 0 ? 'fixture has no fresh blocked run, the badge cannot be checked' : `badge reads "${text}", /api/runs has ${want} fresh blocked`) })
      await page.locator('.fleet-badge').click()
      const pressed = await page.locator('nav[aria-label="filter runs"] button[aria-pressed="true"]').innerText()
      if (pressed.trim() !== 'Needs you') throw new Error(`tapping the badge selected "${pressed}"`)
      await chooseFilter(page, 'mobile', 'Active')
      return { note: `badge "${text}"; tapping it selects Needs you`, shot: await ctx.shot(page, 'fleet-badge-mobile') }
    })

    await ctx.expect(AREA, 'filters', async () => {
      const seen = []
      for (const [f, label] of FILTERS) {
        await chooseFilter(page, 'mobile', label)
        const pressed = await page.locator(`nav[aria-label="filter runs"] button[aria-pressed="true"]`).allInnerTexts()
        if (pressed.length !== 1 || pressed[0].trim() !== label) throw new Error(`${label}: pressed buttons are ${JSON.stringify(pressed)}`)
        await expectView(ctx, page, f)
        const n = (await page.evaluate(sh.readCards)).length + (await page.evaluate(sh.readRows)).length
        seen.push(`${label}=${n}`)
      }
      await chooseFilter(page, 'mobile', 'Stuck')
      return { note: `each filter shows what its rule selects (cards+rows: ${seen.join(', ')})`, shot: await ctx.shot(page, 'fleet-filters-stuck-mobile') }
    })

    await chooseFilter(page, 'mobile', 'Active')
    await page.reload()
    await page.waitForSelector('.fleet .fleet-group-card', { timeout: 15000 })

    await ctx.expect(AREA, 'nesting', async () => {
      const live = (await sh.runsOf(ctx)).filter((r) => r.role === 'dispatcher' && r.crew?.name)
      if (live.length !== 1) throw new Error(`expected exactly one joined dispatcher in the fixture, got ${live.length}`)
      const crew = live[0].crew.name
      const t = Date.now()
      const all = (await sh.runsOf(ctx)).filter((r) => r.role === 'worker' && r.crew?.name === crew)
      const liveWorkers = all.filter((r) => !sh.isHistory(r, t))
      const ny = all.filter((r) => sh.needsYou(r, t)).length
      const wantLine = `${liveWorkers.length} ${liveWorkers.length === 1 ? 'worker' : 'workers'}${ny > 0 ? ` · ${ny} need you` : ''}`
      await expandAll(page)
      const got = await page.evaluate(() => {
        const g = [...document.querySelectorAll('.fleet .fleet-group-card:not(.crew)')]
        return g.map((card) => ({
          role: card.querySelector('.run-card .run-role')?.textContent ?? '',
          line: card.querySelector('.run-crew')?.textContent ?? '',
          badge: card.querySelector('.fleet-group-badge')?.textContent ?? '',
          rows: card.querySelectorAll('.worker-row').length,
          needs: card.classList.contains('needs-you'),
          // The member rows are siblings of the head <button>, never inside it.
          nestedInButton: Boolean(card.querySelector('button .worker-row')),
        }))
      })
      if (got.length !== 1) throw new Error(`expected one dispatcher group on screen, got ${got.length}`)
      const g = got[0]
      if (g.role !== 'dispatcher') throw new Error(`group head role chip is "${g.role}"`)
      // The Active filter narrows the nested members; the crew line counts live workers whatever the filter.
      const shownMembers = sh.filterRuns(all, 'active', t)
      if (g.rows !== shownMembers.length) throw new Error(`dispatcher group holds ${g.rows} worker rows, ${shownMembers.length} of its ${all.length} workers match the Active filter`)
      if (g.line !== wantLine) throw new Error(`crew line "${g.line}", expected "${wantLine}"`)
      if (ny > 0 && (g.badge !== `${ny} need you` || !g.needs)) throw new Error(`needs-you badge "${g.badge}" / class ${g.needs}, expected "${ny} need you"`)
      if (g.nestedInButton) throw new Error('worker rows are inside the head <button>')
      return { note: `crew ${crew}: ${g.rows} nested rows, line "${g.line}", badge "${g.badge}"`, shot: await ctx.shot(page, 'fleet-nesting-mobile') }
    })

    await ctx.expect(AREA, 'orphan-group', async () => {
      const t = Date.now()
      const all = await sh.runsOf(ctx)
      const joined = new Set(all.filter((r) => r.role === 'dispatcher' && r.crew?.name).map((r) => r.crew.name))
      const orphanCrews = [...new Set(all.filter((r) => r.role === 'worker' && r.crew?.name && !joined.has(r.crew.name)).map((r) => r.crew.name))]
      if (orphanCrews.length !== 1) throw new Error(`expected one orphan crew, got ${JSON.stringify(orphanCrews)}`)
      const crew = orphanCrews[0]
      const members = all.filter((r) => r.role === 'worker' && r.crew?.name === crew)
      const home = members.find((m) => m.crew.home)?.crew.home
      if (!home) throw new Error('orphan workers carry no crew.home to compare against')
      const groups = await page.evaluate(() =>
        [...document.querySelectorAll('.fleet .fleet-group-card.crew')].map((g) => ({
          repo: g.querySelector('.fleet-group-repo')?.textContent ?? '',
          idTitle: g.querySelector('.fleet-group-id')?.getAttribute('title') ?? '',
          counts: g.querySelector('.fleet-group-counts')?.textContent ?? '',
          link: g.querySelector('a.fleet-group-dispatch')?.getAttribute('href') ?? '',
          hasHeadCard: Boolean(g.querySelector('.run-card')),
          rows: g.querySelectorAll('.worker-row').length,
        })),
      )
      if (groups.length !== 1) throw new Error(`expected one orphan CrewGroup, got ${groups.length}`)
      const g = groups[0]
      if (g.idTitle !== crew) throw new Error(`group id title "${g.idTitle}", expected ${crew}`)
      if (g.repo !== home) throw new Error(`header repo "${g.repo}", crew.home is "${home}"`)
      const wantCounts = countsLabel(members, t)
      if (g.counts !== wantCounts) throw new Error(`counts "${g.counts}", expected "${wantCounts}"`)
      if (g.hasHeadCard) throw new Error('an orphan crew group renders a head run card')
      if (!g.link.startsWith('#/dispatch?') || !g.link.includes(`crew=${encodeURIComponent(crew)}`)) throw new Error(`Dispatch here link is "${g.link}"`)
      return { note: `${crew}: header "${g.repo}" = crew.home, counts "${g.counts}", ${g.rows} rows shown, link ${g.link.slice(0, 40)}…`, shot: await ctx.shot(page, 'fleet-orphan-group-mobile') }
    })

    await ctx.expect(AREA, 'show-more', async () => {
      await page.reload()
      await page.waitForSelector('.fleet .fleet-group-card', { timeout: 15000 })
      const groups = await ctx.waitFor(
        async () => {
          const g = await page.evaluate(() =>
            [...document.querySelectorAll('.fleet .fleet-group-card')].map((card) => ({
              crew: card.classList.contains('crew'),
              idTitle: card.querySelector('.fleet-group-id')?.getAttribute('title') ?? '',
              codename: card.querySelector('.worker-row-codename')?.textContent ?? '',
              rows: card.querySelectorAll('.worker-row').length,
              more: card.querySelector('.fleet-group-more')?.textContent ?? '',
              expanded: card.querySelector('.fleet-group-more')?.getAttribute('aria-expanded') ?? '',
            })),
          )
          return g.length >= 2 && g.every((x) => x.rows > 0) ? g : null
        },
        { message: 'groups did not render rows' },
      )
      const notes = []
      const apiRuns = await sh.runsOf(ctx)
      for (const g of groups) {
        // A group is named by its crew id (a crew group's header, else the crew of its first row's codename).
        const crewId = g.idTitle || apiRuns.find((r) => r.crew?.codename === g.codename)?.crew?.name || g.codename
        const shownTotal = g.rows + (/^Show (\d+) more$/.exec(g.more)?.[1] ? Number(/(\d+)/.exec(g.more)[1]) : 0)
        if (shownTotal <= sh.COLLAPSE_OVER) continue
        if (g.rows !== sh.MEMBER_CAP) throw new Error(`crew ${crewId}: ${shownTotal} members but ${g.rows} rows before expanding (cap ${sh.MEMBER_CAP})`)
        const want = `Show ${shownTotal - sh.MEMBER_CAP} more`
        if (g.more !== want || g.expanded !== 'false') throw new Error(`crew ${crewId}: toggle "${g.more}" expanded=${g.expanded}, expected "${want}"`)
        const card = page.locator('.fleet .fleet-group-card').filter({ has: page.locator('.worker-row-codename', { hasText: new RegExp(`^${g.codename}$`) }) })
        if ((await card.count()) !== 1) throw new Error(`crew ${crewId}: ${await card.count()} group cards hold the codename ${g.codename}`)
        await card.locator('.fleet-group-more').click()
        const after = await card.evaluate((el) => ({
          rows: el.querySelectorAll('.worker-row').length,
          more: el.querySelector('.fleet-group-more')?.textContent ?? '',
          expanded: el.querySelector('.fleet-group-more')?.getAttribute('aria-expanded') ?? '',
        }))
        if (after.rows !== shownTotal || after.expanded !== 'true' || after.more !== 'Show less') {
          throw new Error(`crew ${crewId} after tap: ${after.rows} rows, "${after.more}", expanded=${after.expanded}; expected ${shownTotal} rows, "Show less"`)
        }
        await card.locator('.fleet-group-more').click()
        if ((await card.locator('.worker-row').count()) !== sh.MEMBER_CAP) throw new Error(`crew ${crewId}: Show less did not collapse back to ${sh.MEMBER_CAP} rows`)
        notes.push(`crew ${crewId}: ${shownTotal} members -> ${g.rows} rows + "${g.more}" -> ${after.rows} -> collapsed`)
      }
      if (notes.length === 0) throw new Error('no group has more than 6 members, Show N more not exercised')
      return { note: notes.join('; '), shot: await ctx.shot(page, 'fleet-show-more-mobile') }
    })

    await ctx.expect(AREA, 'codenames', async () => {
      await expandAll(page)
      const rows = await page.evaluate(() =>
        [...document.querySelectorAll('.fleet .worker-row')].map((r) => ({
          codename: r.querySelector('.worker-row-codename')?.textContent ?? '',
          swatch: getComputedStyle(r.querySelector('.worker-row-swatch')).backgroundColor,
        })),
      )
      const workers = (await sh.runsOf(ctx)).filter((r) => r.role === 'worker' && r.crew?.name)
      const byCodename = new Map(rows.map((r) => [r.codename, r.swatch]))
      const bad = []
      for (const w of workers) {
        const cn = w.crew.codename
        if (!cn || !w.crew.color) {
          bad.push(`${w.id}: API has no codename/colour`)
          continue
        }
        const got = byCodename.get(cn)
        if (got === undefined) bad.push(`${cn}: no row`)
        else if (got !== sh.hexToRgb(w.crew.color)) bad.push(`${cn}: swatch ${got}, crew.color ${w.crew.color}`)
      }
      if (bad.length) throw new Error(bad.slice(0, 5).join('; '))
      const distinct = new Set(rows.map((r) => r.swatch)).size
      return { note: `${workers.length} worker rows show their codename and a swatch equal to crew.color (${distinct} distinct colours)`, shot: await ctx.shot(page, 'fleet-codenames-mobile') }
    })

    await ctx.expect(AREA, 'group-by-project', async () => {
      await chooseFilter(page, 'mobile', 'Active')
      await expandAll(page)
      const toggle = page.locator('.fleet-group-toggle')
      if ((await toggle.getAttribute('aria-pressed')) !== 'false') throw new Error('Group by project is on by default')
      await toggle.click()
      const readSections = () =>
        page.evaluate(() =>
          [...document.querySelectorAll('.fleet .fleet-project-section')].map((s) => ({
            name: s.querySelector('.fleet-project-name')?.textContent ?? '',
            count: Number(s.querySelector('.fleet-project-count > span')?.textContent ?? NaN),
            attention: s.querySelector('.fleet-project-attention')?.textContent ?? '',
            // Top-level entries in order: dispatcher group, crew group or plain card.
            order: [...s.children]
              .filter((c) => c.classList.contains('fleet-group-card') || c.classList.contains('run-card'))
              .map((c) => (c.classList.contains('run-card') ? 'run' : c.classList.contains('crew') ? 'crew' : 'dispatcher')),
          })),
        )
      const t = Date.now()
      const rs = await sh.runsOf(ctx)
      const want = sh.fleetExpected(rs, 'active', t)
      const perProject = new Map()
      const add = (project) => perProject.set(project, (perProject.get(project) ?? 0) + 1)
      for (const c of want.cards) add(sh.projectOf(c))
      for (const grp of want.groups) {
        const soleProject = (list) => [...new Set(list.map((m) => m.project).filter(Boolean))]
        const project = grp.head ? sh.projectOf(grp.head) : soleProject(grp.shown).length === 1 ? soleProject(grp.shown)[0] : (grp.shown.find((m) => m.crew?.home)?.crew.home ?? 'unknown')
        for (let i = 0; i < grp.shown.length; i++) add(project)
      }
      let sections = []
      await ctx.waitFor(
        async () => {
          sections = await readSections()
          return sections.length > 0
        },
        { message: 'no project sections after switching Group by project on' },
      )
      const names = sections.map((s) => s.name)
      if (new Set(names).size !== names.length) throw new Error(`duplicate sections ${names}`)
      for (const [project, n] of perProject) {
        const s = sections.find((x) => x.name === project)
        if (!s) throw new Error(`no section for project ${project} (have ${names})`)
        if (s.count !== n) throw new Error(`section ${project} counts ${s.count}, ${n} runs expected`)
      }
      if (sections.length !== perProject.size) throw new Error(`${sections.length} sections (${names}), expected ${perProject.size} (${[...perProject.keys()]})`)
      for (const s of sections) {
        const lastDispatcher = s.order.lastIndexOf('dispatcher')
        const firstOther = s.order.findIndex((k) => k !== 'dispatcher')
        if (lastDispatcher !== -1 && firstOther !== -1 && firstOther < lastDispatcher) throw new Error(`section ${s.name}: dispatcher group does not lead (${s.order})`)
      }
      if (!sections.some((s) => s.order.includes('dispatcher'))) throw new Error('no dispatcher group in any section')
      const shotOn = await ctx.shot(page, 'fleet-group-by-project-mobile')

      await page.reload()
      await page.waitForSelector('.fleet .fleet-project-section', { timeout: 15000 })
      if ((await page.locator('.fleet-group-toggle').getAttribute('aria-pressed')) !== 'true') throw new Error('toggle did not persist across reload')
      const stored = await page.evaluate(() => localStorage.getItem('houston-fleet-group-by-project'))
      if (stored !== 'true') throw new Error(`localStorage houston-fleet-group-by-project is ${stored}`)
      const after = await readSections()
      if (after.length !== sections.length) throw new Error(`after reload ${after.length} sections, before ${sections.length}`)

      await page.locator('.fleet-group-toggle').click()
      if ((await page.locator('.fleet .fleet-project-section').count()) !== 0) throw new Error('sections remain after switching the toggle off')
      return { note: `sections ${sections.map((s) => `${s.name}(${s.count})`).join(', ')}; dispatchers lead; persists across reload`, shot: shotOn }
    })

    await ctx.expect(AREA, 'bg-chip', async () => {
      await chooseFilter(page, 'mobile', 'Active')
      const bgRuns = (await sh.runsOf(ctx)).filter((r) => r.background?.length)
      if (bgRuns.length === 0) throw new Error('no run carries background tasks even with the fixture transcript')
      await expandAll(page)
      const chips = await page.evaluate(() =>
        [...document.querySelectorAll('.fleet .run-card')]
          .map((c) => ({ name: c.querySelector('.run-name')?.textContent ?? '', text: c.querySelector('.run-chip.bg')?.textContent ?? '', title: c.querySelector('.run-chip.bg')?.getAttribute('title') ?? '' }))
          .filter((c) => c.text),
      )
      if (chips.length !== bgRuns.length) throw new Error(`${chips.length} bg chips on screen, ${bgRuns.length} runs with background`)
      for (const r of bgRuns) {
        const want = `${r.background.length} bg`
        const chip = chips.find((c) => c.text === want && c.title === r.background.map((t) => t.hint || t.kind).join('\n'))
        if (!chip) throw new Error(`run ${r.id}: expected chip "${want}" titled ${JSON.stringify(r.background.map((t) => t.hint || t.kind))}, got ${JSON.stringify(chips)}`)
      }
      const shell = bgRuns[0].background[0]
      if (shell.kind !== 'shell') throw new Error(`background task kind ${shell.kind}`)
      return { note: `${bgRuns[0].id} shows "${chips[0].text}" (title "${chips[0].title}"); source: fixture transcript with a run_in_background Bash`, shot: await ctx.shot(page, 'fleet-bg-chip-mobile') }
    })

    if (mobile.errors.length) console.error(`fleet: uncaught page errors: ${mobile.errors.slice(0, 3).join(' | ')}`)
  } finally {
    await mobile.context.close()
  }
}

module.exports = { run }
