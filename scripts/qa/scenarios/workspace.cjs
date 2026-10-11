// Area 7: Workspace tab. Checks: group-by-repo needs-you tap-pane deleted-wt.
//
// deleted-wt is destructive to the harness (it removes the qa-wt worktree whose
// directory the `wt` window sits in), so it runs last.
const fs = require('node:fs')
const { execFileSync } = require('node:child_process')
const path = require('node:path')
const sh = require('./_shared.cjs')

const AREA = 7
const BUCKETS = ['main_checkout', 'worktrees', 'other']

const windowsOf = (p) => BUCKETS.flatMap((b) => (p[b] ?? []).map((w) => ({ ...w, bucket: b })))
const agentsOf = (p) => windowsOf(p).flatMap((w) => w.panes.filter((x) => x.agent))
const title = (w) => w.branch || w.name
const sectionName = (p) => (p.name === '' ? 'Other sessions' : p.name)

async function workspaceOf(ctx) {
  const r = await ctx.api('/api/workspace')
  if (r.status !== 200 || !r.json) throw new Error(`GET /api/workspace -> ${r.status}`)
  return r.json
}

async function open(ctx, kind = 'mobile') {
  const { context, page } = await ctx.newPage(ctx.browser, kind)
  const errors = []
  page.on('pageerror', (e) => errors.push(e.message))
  await page.goto(ctx.url('#/workspace'))
  await page.waitForSelector('.workspace section', { timeout: 15000 })
  return { context, page, errors }
}

const readSections = (page) =>
  page.evaluate(() =>
    [...document.querySelectorAll('.workspace section')].map((s) => ({
      name: s.querySelector('.fleet-group > span')?.textContent ?? '',
      need: s.querySelector('.ws-need')?.textContent ?? '',
      agents: s.querySelector('.ws-session-meta > span:last-child')?.textContent ?? '',
      buckets: [...s.querySelectorAll('.ws-bucket')].map((b) => b.textContent ?? ''),
      windows: [...s.querySelectorAll('.ws-window-title')].map((t) => t.textContent ?? ''),
      agentPanes: s.querySelectorAll('.ws-pane.agent').length,
    })),
  )

// settled retries a comparison until the 3 s workspace poll agrees with the API.
async function settled(ctx, fn) {
  let last = ''
  await ctx.waitFor(
    async () => {
      last = (await fn()) || ''
      return last === ''
    },
    { timeout: 9000, interval: 400, message: () => `workspace view differs from the API: ${last}` },
  )
}

async function run(ctx) {
  await sh.ensureStates(ctx)
  // An earlier run of deleted-wt removed the worktree for good; group-by-repo
  // and deleted-wt both start from it.
  const wtPath = path.join(ctx.S, 'repos', 'qa-wt')
  const wtGone = !fs.existsSync(wtPath)
  const wtReason = `harness: ${wtPath} is already gone (an earlier deleted-wt run removed it); run scripts/qa/setup.sh for a fresh harness`
  const mobile = await open(ctx)
  const { page } = mobile
  try {
    if (wtGone) ctx.check(AREA, 'group-by-repo', 'skip', wtReason)
    else await ctx.expect(AREA, 'group-by-repo', async () => {
      let names = []
      await settled(ctx, async () => {
        const ws = await workspaceOf(ctx)
        const secs = await readSections(page)
        names = ws.projects.map(sectionName)
        if (JSON.stringify(secs.map((s) => s.name)) !== JSON.stringify(names)) return `sections ${JSON.stringify(secs.map((s) => s.name))}, API projects ${JSON.stringify(names)}`
        for (const p of ws.projects) {
          const s = secs.find((x) => x.name === sectionName(p))
          const wantWins = windowsOf(p).map(title).sort()
          if (JSON.stringify([...s.windows].sort()) !== JSON.stringify(wantWins)) return `${s.name}: windows ${JSON.stringify(s.windows)}, expected ${JSON.stringify(wantWins)}`
          const n = agentsOf(p).length
          const wantAgents = `${n} ${n === 1 ? 'agent' : 'agents'}`
          if (s.agents !== wantAgents) return `${s.name}: "${s.agents}", expected "${wantAgents}"`
          if (s.agentPanes !== n) return `${s.name}: ${s.agentPanes} agent pane rows, expected ${n}`
          const wantBuckets = [
            ...(p.main_checkout?.length ? ['Main checkout'] : []),
            ...(p.worktrees?.length ? [`Worktrees (${p.worktrees.length})`] : []),
            ...(p.other?.length ? ['Other'] : []),
          ]
          if (JSON.stringify(s.buckets) !== JSON.stringify(wantBuckets)) return `${s.name}: buckets ${JSON.stringify(s.buckets)}, expected ${JSON.stringify(wantBuckets)}`
        }
        return ''
      })
      for (const want of ['qa-repo', 'repo']) if (!names.includes(want)) throw new Error(`project ${want} is not listed (have ${names})`)
      const ws = await workspaceOf(ctx)
      const qa = ws.projects.find((p) => p.name === 'qa-repo')
      const fx = ws.projects.find((p) => p.name === 'repo')
      const wtBucket = (qa.worktrees ?? []).some((w) => w.name === 'wt')
      if (!wtBucket) throw new Error('the wt window (a linked worktree of qa-repo) is not in qa-repo\'s Worktrees bucket')
      return {
        note: `projects ${names.join(', ')}; qa-repo: ${windowsOf(qa).length} windows (wt under Worktrees), repo: ${windowsOf(fx).length} windows; counts and buckets match /api/workspace`,
        shot: await ctx.shot(page, 'workspace-group-by-repo-mobile'),
      }
    })

    await ctx.expect(AREA, 'needs-you', async () => {
      const notes = []
      await settled(ctx, async () => {
        notes.length = 0
        const ws = await workspaceOf(ctx)
        const runs = await sh.runsOf(ctx)
        const now = Date.now()
        const secs = await readSections(page)
        for (const p of ws.projects) {
          const s = secs.find((x) => x.name === sectionName(p))
          const fromPanes = agentsOf(p).filter((a) => sh.needsYou({ state: a.state ?? '', updated_at: a.updated_at ?? 0 }, now)).length
          const fromRuns = p.name === '' ? fromPanes : runs.filter((r) => r.project === p.name && sh.needsYou(r, now)).length
          const shown = s.need ? Number(/^(\d+) need you$/.exec(s.need)?.[1] ?? NaN) : 0
          if (shown !== fromRuns || fromPanes !== fromRuns) return `${s.name}: badge ${shown}, blocked panes ${fromPanes}, blocked runs ${fromRuns}`
          notes.push(`${s.name}=${shown}`)
        }
        return ''
      })
      if (!notes.some((n) => /=[1-9]/.test(n))) throw new Error('no project has a needs-you run, the badge was not exercised')
      return { note: `need-you badges equal blocked runs per project (${notes.join(', ')})`, shot: await ctx.shot(page, 'workspace-needs-you-mobile') }
    })

    await ctx.expect(AREA, 'tap-pane', async () => {
      const section = page.locator('.workspace section', { has: page.locator('.fleet-group > span', { hasText: /^qa-repo$/ }) })
      const pane = section.locator('.ws-pane.agent').first()
      await pane.waitFor()
      const label = (await pane.locator('.ws-pane-agent').innerText()).trim()
      await pane.click()
      await ctx.waitFor(async () => /^#\/fleet\/pane-\d+/.test(await page.evaluate(() => location.hash)), { message: 'tapping an agent pane did not open a run' })
      const hash = await page.evaluate(() => location.hash)
      const id = hash.split('/')[2]
      const run = (await sh.runsOf(ctx)).find((r) => r.id === id)
      if (!run) throw new Error(`no run ${id}`)
      if (run.project !== 'qa-repo') throw new Error(`opened ${id} of project ${run.project}, expected a qa-repo run`)
      await page.waitForSelector('.run-detail .run-detail-project')
      const head = (await page.locator('.run-detail-project').innerText()).trim()
      if (head !== 'qa-repo') throw new Error(`detail header project "${head}"`)
      const back = (await page.locator('.run-tabs-back-label').innerText()).trim()
      if (back !== 'Workspace') throw new Error(`Back label "${back}", expected Workspace`)
      const shot = await ctx.shot(page, 'workspace-tap-pane-mobile')
      await page.locator('.run-tabs-back').click()
      await ctx.waitFor(async () => (await page.evaluate(() => location.hash)) === '#/workspace', { message: 'Back did not return to #/workspace' })
      return { note: `tapped the "${label}" pane -> ${hash}; detail shows qa-repo, Back returns to Workspace`, shot }
    })

    if (wtGone) ctx.check(AREA, 'deleted-wt', 'skip', wtReason)
    else await ctx.expect(AREA, 'deleted-wt', async () => {
      const before = await workspaceOf(ctx)
      const find = (ws) => {
        for (const p of ws.projects) for (const w of windowsOf(p)) if (w.name === 'wt') return { project: sectionName(p), bucket: w.bucket, branch: w.branch ?? '', panes: w.panes.length, agents: w.panes.filter((x) => x.agent).length }
        return null
      }
      const was = find(before)
      if (!was) throw new Error('the wt window is not in /api/workspace before removal')
      const repo = path.join(ctx.S, 'repos', 'qa-repo')
      const wt = path.join(ctx.S, 'repos', 'qa-wt')
      try {
        execFileSync('git', ['-C', repo, 'worktree', 'remove', '--force', wt], { stdio: 'pipe', timeout: 15000 })
      } catch (e) {
        if (!/is not a working tree|No such file/.test(String(e.stderr))) throw new Error(`git worktree remove failed: ${String(e.stderr).slice(0, 200)}`)
      }
      const { context, page: p2, errors } = await open(ctx)
      try {
        await p2.reload()
        await p2.waitForSelector('.workspace section', { timeout: 15000 })
        const status = (await ctx.api('/api/workspace')).status
        if (status !== 200) throw new Error(`GET /api/workspace -> ${status} after removing the worktree`)
        const after = find(await workspaceOf(ctx))
        const secs = await readSections(p2)
        if (secs.length === 0 || (await p2.locator('.workspace .fleet-empty').count()) > 0) throw new Error('Workspace is blank after the worktree was removed')
        const shown = secs.some((s) => s.windows.includes(after?.branch || 'wt'))
        const fleet = await ctx.api('/api/runs')
        const run = (fleet.json ?? []).find((r) => r.tmux?.pane_id === ctx.env.WT_PANE)
        const shot = await ctx.shot(p2, 'workspace-deleted-wt-mobile')
        if (errors.length) throw new Error(`uncaught page errors: ${errors.slice(0, 2).join(' | ')}`)
        if (!after) {
          return { note: `no crash or blank view; the wt window no longer appears in /api/workspace (before: ${was.project}/${was.bucket}); its run is ${run ? `still listed as ${run.state}, project ${run.project}` : 'gone from /api/runs'}`, shot }
        }
        return {
          note: `no crash or blank view; wt window before: ${was.project}/${was.bucket} branch "${was.branch}", after: ${after.project}/${after.bucket} branch "${after.branch}" (${after.agents} agent pane, ${shown ? 'rendered' : 'NOT rendered'}); run of pane ${ctx.env.WT_PANE} ${run ? `${run.state}, project ${run.project}` : 'gone'}`,
          shot,
        }
      } finally {
        await context.close()
      }
    })
  } finally {
    await mobile.context.close()
  }
}

module.exports = { run }
